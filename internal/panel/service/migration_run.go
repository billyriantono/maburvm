package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

// Phases a move reports. Those from phaseCopying on mean the node is (or was)
// copying, so a resumed job follows the node rather than starting again.
const (
	phaseChecking  = "Checking"
	phaseLink      = "Setting up the node link"
	phasePreparing = "Preparing the destination"
	phaseCopying   = "Copying"
	phaseConfirm   = "Confirming the VM on the destination"
	phaseApplying  = "Applying network, console and limits on the destination"
	phaseCleanup   = "Removing the old copy"
)

// errInterrupted ends a job try without ending the move: the panel is
// stopping, or the job ran out of time, and the next try picks it up.
var errInterrupted = errors.New("interrupted")

// errCancelled is a move an operator stopped.
var errCancelled = errors.New("cancelled")

// RunMigrationJob runs a queued move; the queue calls it.
func (m *MigrationService) RunMigrationJob(ctx context.Context, migrationID string) error {
	var row models.VMMigration
	if err := m.db.WithContext(ctx).Where("id = ?", migrationID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return river.JobCancel(fmt.Errorf("migration %s no longer exists", migrationID))
		}
		return err
	}
	if !row.Active() {
		return nil
	}
	if err := m.run(ctx, &row); errors.Is(err, errInterrupted) {
		return river.JobSnooze(15 * time.Second)
	}
	return nil
}

// run takes a move to its end and records it. It returns errInterrupted when
// it stopped before the end (the move goes on, or waits, on its nodes);
// otherwise the outcome is in the record and alerts.
func (m *MigrationService) run(ctx context.Context, row *models.VMMigration) error {
	err := m.runSteps(ctx, row)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errInterrupted) || ctx.Err() != nil:
		return errInterrupted
	case errors.Is(err, errCancelled):
		m.finish(ctx, row, models.MigrationCancelled, "", "")
		return nil
	default:
		m.finish(ctx, row, models.MigrationFailed, err.Error(), "")
		m.alertFailed(ctx, row, err)
		return nil
	}
}

func (m *MigrationService) alertFailed(ctx context.Context, row *models.VMMigration, err error) {
	name := row.VMID
	m.db.WithContext(ctx).Raw("SELECT hostname FROM vms WHERE id = ?", row.VMID).Scan(&name)
	m.alerts.Event(context.WithoutCancel(ctx), AlertInput{
		Key: "migration_failed:" + row.VMID, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning,
		Title: "Moving " + name + " to another node failed", Detail: truncate(err.Error(), 500),
		ResourceType: "vm", ResourceID: row.VMID, ResourceName: name,
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

func (m *MigrationService) runSteps(ctx context.Context, row *models.VMMigration) error {
	if row.DestNodeID == nil || row.DestPoolID == nil {
		return errors.New("the move has no destination")
	}
	vm, err := m.vms.vmRepo.GetByID(ctx, row.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("the VM no longer exists")
		}
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	src, err := m.vms.nodeRepo.GetByID(ctx, row.SourceNodeID)
	if err != nil {
		return fmt.Errorf("the source node cannot be read: %w", err)
	}
	dst, err := m.vms.nodeRepo.GetByID(ctx, *row.DestNodeID)
	if err != nil {
		return fmt.Errorf("the destination node cannot be read: %w", err)
	}
	if vm.NodeID == dst.ID {
		// The record already moved; what is left is the tidy-up after it.
		return m.afterMove(ctx, row, vm, src, dst)
	}
	if vm.NodeID != src.ID {
		return fmt.Errorf("the VM is on another node than the one it was to be moved from")
	}
	if row.Status == models.MigrationQueued {
		now := time.Now()
		res := m.db.WithContext(ctx).Model(&models.VMMigration{}).Where("id = ? AND status = ?", row.ID, models.MigrationQueued).
			Updates(map[string]interface{}{"status": models.MigrationRunning, "started_at": now, "updated_at": now})
		if res.Error != nil {
			return fmt.Errorf("%w: %v", errInterrupted, res.Error)
		}
		if res.RowsAffected == 0 {
			// Cancelled since it was read: never start a move whose record is final.
			if err := m.db.WithContext(ctx).Where("id = ?", row.ID).First(row).Error; err != nil {
				return fmt.Errorf("%w: %v", errInterrupted, err)
			}
			if !row.Active() {
				return nil
			}
		} else {
			row.Status, row.StartedAt = models.MigrationRunning, &now
		}
	}

	worker := src // where the copy runs: the source for a live move, the destination for an offline one
	if row.Mode == models.MigrationOffline {
		worker = dst
	}
	if !strings.HasPrefix(row.Phase, phaseCopying) && row.Phase != phaseConfirm {
		if m.cancelRequested(ctx, row) {
			if row.Phase == phasePreparing {
				m.rollback(ctx, row, dst) // an earlier try stopped while files were being made
			}
			return errCancelled
		}
		if err := m.launch(ctx, row, vm, src, dst); err != nil {
			return err
		}
	}
	outcome := m.follow(ctx, row, vm, worker, src, dst)
	if outcome == nil {
		return m.confirmAndMove(ctx, row, vm, src, dst)
	}
	if errors.Is(outcome, errInterrupted) {
		return outcome
	}
	if row.Mode == models.MigrationLive {
		// A live move that did not end cleanly may still have switched over;
		// the copy on the destination is then the only current one.
		landed, known := m.liveLanded(ctx, vm, src, dst)
		switch {
		case landed:
			m.addNote(ctx, row, "The node reported the move as unfinished ("+outcome.Error()+"), but the VM runs on the destination.")
			return m.confirmAndMove(ctx, row, vm, src, dst)
		case !known:
			m.addNote(ctx, row, fmt.Sprintf("Files prepared on %s were kept: where the VM runs could not be confirmed.", dst.Name))
			return outcome
		}
	}
	m.rollback(ctx, row, dst)
	return outcome
}

// liveLanded reports whether a live move's VM is on the destination: not
// running on the source, and defined on the destination. known is false when
// a node could not be asked.
func (m *MigrationService) liveLanded(ctx context.Context, vm *models.VM, src, dst *models.Node) (landed, known bool) {
	srcRuns, dstHas, known := m.liveWhere(ctx, vm, src, dst)
	return known && !srcRuns && dstHas, known
}

// liveWhere reports whether a live move's VM runs on the source and is
// defined on the destination; known is false when a node could not be asked.
func (m *MigrationService) liveWhere(ctx context.Context, vm *models.VM, src, dst *models.Node) (srcRuns, dstHas, known bool) {
	si, serr := m.migrationInfo(ctx, src, &pb.GetMigrationInfoRequest{VmId: vm.ID})
	di, derr := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
	if serr != nil || derr != nil {
		return false, false, false
	}
	return si.VmFound && (si.VmState == "running" || si.VmState == "paused"), di.VmFound, true
}

// lostMoveWait is how long a live move whose agent lost track of it is
// followed from where the VM is before it is given up as failed.
const lostMoveWait = 6 * time.Hour

// launch checks the move again, sets up the link, prepares the destination
// and starts the copy.
func (m *MigrationService) launch(ctx context.Context, row *models.VMMigration, vm *models.VM, src, dst *models.Node) error {
	m.setPhase(ctx, row, phaseChecking)
	chk, err := m.check(ctx, MigrationRequest{VMID: vm.ID, DestNodeID: dst.ID, DestPoolID: *row.DestPoolID, Mode: row.Mode}, newCheckEnv(), row.ID)
	if err != nil {
		return err
	}
	if !chk.CanMove {
		return fmt.Errorf("the VM can no longer be moved: %s", strings.Join(chk.Blockers, "; "))
	}
	plan := chk.plan

	m.setPhase(ctx, row, phaseLink)
	if err := m.EnsureLink(ctx, src.ID, dst.ID); err != nil {
		return err
	}
	linkFrom, linkTo := src, dst // the side that opens the connection, and the side it reaches
	if plan.mode == models.MigrationOffline {
		linkFrom, linkTo = dst, src
	}
	if err := m.testLink(ctx, linkFrom, linkTo); err != nil {
		return err
	}

	m.setPhase(ctx, row, phasePreparing)
	srcInfo, err := m.migrationInfo(ctx, src, &pb.GetMigrationInfoRequest{VmId: vm.ID, IncludePayload: true})
	if err != nil {
		return fmt.Errorf("node %s could not describe the VM: %w", src.Name, err)
	}
	f := &srcFacts{vm: vm, src: src, info: srcInfo, mode: plan.mode, disks: plan.disks, seed: plan.seed}
	prep := m.targetRequest(f, plan.pool, row.ID, false)
	prep.SeedImage = srcInfo.SeedImage
	dclient, dctx, err := m.agent(ctx, dst)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(dctx, 10*time.Minute)
	presp, err := dclient.PrepareMigrationTarget(pctx, prep)
	cancel()
	if err != nil || !presp.Success {
		// Some files may exist already; the node recorded them before creating any.
		m.rollback(ctx, row, dst)
		if err != nil {
			return fmt.Errorf("node %s could not prepare for the VM: %w", dst.Name, err)
		}
		return fmt.Errorf("node %s could not prepare for the VM: %s", dst.Name, agentMessage(presp.Error, strings.Join(presp.Problems, "; ")))
	}
	maps := diskMaps(plan, presp)
	if m.cancelRequested(ctx, row) {
		m.rollback(ctx, row, dst)
		return errCancelled
	}

	if plan.mode == models.MigrationLive {
		m.setPhase(ctx, row, phaseCopying+" memory and disks")
		sclient, sctx, err := m.agent(ctx, src)
		if err != nil {
			m.rollback(ctx, row, dst)
			return err
		}
		bw := mibPerSecond(row.BandwidthMbps)
		mctx, cancel := context.WithTimeout(sctx, 2*time.Minute)
		resp, err := sclient.MigrateVM(mctx, &pb.MigrateVMRequest{
			VmId: vm.ID, MigrationId: row.ID, DestAddress: hostOnlyIP(dst.IPAddress), Disks: maps, BandwidthMibS: bw,
			Live: true, CopyStorage: true,
		})
		cancel()
		if err != nil {
			// The node may have started it before the answer was lost: the next
			// try follows the node, and settles it from where the VM is.
			return fmt.Errorf("%w: node %s did not answer the start of the move: %v", errInterrupted, src.Name, err)
		}
		if !resp.Success {
			m.rollback(ctx, row, dst)
			return fmt.Errorf("node %s did not start the move: %s", src.Name, agentMessage(resp.Error, resp.Message))
		}
		return nil
	}

	m.setPhase(ctx, row, phaseCopying+" disks")
	pctx, cancel = context.WithTimeout(dctx, 2*time.Minute)
	resp, err := dclient.PullMigratedVM(pctx, &pb.PullMigratedVMRequest{
		VmId: vm.ID, MigrationId: row.ID, SourceAddress: hostOnlyIP(src.IPAddress), Disks: maps, DomainXml: srcInfo.DomainXml,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("%w: node %s did not answer the start of the copy: %v", errInterrupted, dst.Name, err)
	}
	if !resp.Success {
		m.rollback(ctx, row, dst)
		return fmt.Errorf("node %s did not start the copy: %s", dst.Name, agentMessage(resp.Error, "it refused"))
	}
	return nil
}

// mibPerSecond turns a cap in Mbps into libvirt's MiB/s, never 0 for a set cap.
func mibPerSecond(mbps int) int64 {
	if mbps <= 0 {
		return 0
	}
	return max(1, int64(mbps)*125000/(1<<20))
}

// diskMaps says where each drive goes: the disks are copied, the seed image
// is already there.
func diskMaps(plan *migrationPlan, prep *pb.PrepareMigrationTargetResponse) []*pb.MigrationDiskMap {
	var out []*pb.MigrationDiskMap
	for _, d := range plan.disks {
		dm := &pb.MigrationDiskMap{Target: d.Target, DestPath: prep.Paths[d.Target], Copy: true, ExpectedBytes: d.FileSize}
		// Only a disk changing between a file and a volume says so: an older
		// agent never sees the fields it would not know.
		if f := d.destFormat(plan.pool, plan.mode); d.Block || f != d.Format {
			dm.SourceFormat, dm.DestFormat = d.Format, f
		}
		out = append(out, dm)
	}
	if plan.seed != nil && prep.SeedPath != "" {
		out = append(out, &pb.MigrationDiskMap{Target: plan.seed.Target, DestPath: prep.SeedPath})
	}
	return out
}

// testLink tries the migration link from one node to another.
func (m *MigrationService) testLink(ctx context.Context, from, to *models.Node) error {
	r, err := m.migrationInfo(ctx, from, &pb.GetMigrationInfoRequest{PeerAddress: hostOnlyIP(to.IPAddress)})
	if err != nil {
		return fmt.Errorf("node %s could not test its link to %s: %w", from.Name, to.Name, err)
	}
	if !r.LinkOk {
		return fmt.Errorf("node %s cannot reach node %s over the migration link (SSH as root with the panel's key must be allowed from %s): %s",
			from.Name, to.Name, hostOnlyIP(from.IPAddress), r.LinkError)
	}
	return nil
}

// follow waits for the copy on worker to end, keeping the record's progress
// and sending an operator's cancel to the node. nil means the copy finished.
func (m *MigrationService) follow(ctx context.Context, row *models.VMMigration, vm *models.VM, worker, src, dst *models.Node) error {
	abortSent := false
	misses := 0
	var cancelAt time.Time
	for {
		if ctx.Err() != nil {
			return errInterrupted
		}
		if !abortSent && m.cancelRequested(ctx, row) {
			if cancelAt.IsZero() {
				cancelAt = time.Now()
			}
			if client, actx, err := m.agent(ctx, worker); err == nil {
				cctx, cancel := context.WithTimeout(actx, 30*time.Second)
				_, err = client.AbortMigration(cctx, &pb.AbortMigrationRequest{VmId: vm.ID, MigrationId: row.ID})
				cancel()
				abortSent = err == nil
			}
		}
		prog, err := m.progress(ctx, worker, vm.ID, row.ID)
		if err != nil {
			// A node that does not answer is waited for: the copy may well be
			// going on. The job's own deadline bounds the wait.
			misses++
			if misses%20 == 1 {
				m.logger.WarnContext(ctx, "a moving VM's node did not answer", "vm_id", vm.ID, "node", worker.Name, "error", err)
			}
			// A cancel is not held up for ever by a node that is gone.
			if !cancelAt.IsZero() && time.Since(cancelAt) > 5*time.Minute {
				return fmt.Errorf("node %s stopped answering and the move was cancelled; the panel still has the VM on node %s, so check where it runs", worker.Name, src.Name)
			}
		} else {
			misses = 0
			switch prog.State {
			case "running":
				m.recordProgress(ctx, row, prog)
			case "completed":
				return nil
			case "cancelled":
				return errCancelled
			case "failed":
				return fmt.Errorf("%s", prog.Error)
			default:
				// The node has no record (its agent restarted): see where the VM is.
				return m.lostTrack(ctx, row, vm, src, dst)
			}
		}
		select {
		case <-ctx.Done():
			return errInterrupted
		case <-time.After(m.poll):
		}
	}
}

func (m *MigrationService) progress(ctx context.Context, node *models.Node, vmID, migrationID string) (*pb.GetMigrationProgressResponse, error) {
	client, actx, err := m.agent(ctx, node)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, 20*time.Second)
	defer cancel()
	return client.GetMigrationProgress(cctx, &pb.GetMigrationProgressRequest{VmId: vmID, MigrationId: migrationID})
}

func (m *MigrationService) recordProgress(ctx context.Context, row *models.VMMigration, prog *pb.GetMigrationProgressResponse) {
	updates := map[string]interface{}{"updated_at": time.Now()}
	if prog.TotalBytes > 0 {
		row.BytesTotal, row.BytesDone = prog.TotalBytes, prog.ProcessedBytes
		updates["bytes_total"], updates["bytes_done"] = prog.TotalBytes, prog.ProcessedBytes
	}
	// Only copying phases are kept: a resumed job reads "Copying" as "follow the node".
	if strings.HasPrefix(prog.Phase, phaseCopying) {
		row.Phase = prog.Phase
		updates["phase"] = prog.Phase
	}
	m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMMigration{}).Where("id = ?", row.ID).Updates(updates)
}

// lostTrack decides a move its node no longer knows about from where the VM is.
func (m *MigrationService) lostTrack(ctx context.Context, row *models.VMMigration, vm *models.VM, src, dst *models.Node) error {
	if row.Mode == models.MigrationOffline {
		// The destination defines the VM only once every disk has arrived whole.
		dstInfo, err := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
		if err != nil {
			return errInterrupted // ask again later
		}
		if dstInfo.VmFound {
			return nil
		}
		return fmt.Errorf("node %s has no record of the copy (it never started, or its agent restarted); the VM stayed on node %s", dst.Name, src.Name)
	}
	srcRuns, dstHas, known := m.liveWhere(ctx, vm, src, dst)
	switch {
	case !known:
		return errInterrupted
	case !srcRuns && dstHas:
		return nil
	case srcRuns && dstHas && time.Since(row.UpdatedAt) < lostMoveWait:
		// libvirt carries a peer-to-peer move on without the agent that started
		// it; the incoming copy exists only while it does.
		return errInterrupted
	}
	return fmt.Errorf("node %s has no record of the move (it never started, or its agent restarted); the VM stayed there", src.Name)
}

// rollback removes what the move prepared on the destination. The node keeps
// anything a VM there uses.
func (m *MigrationService) rollback(ctx context.Context, row *models.VMMigration, dst *models.Node) {
	ctx = context.WithoutCancel(ctx)
	client, actx, err := m.agent(ctx, dst)
	if err != nil {
		m.addNote(ctx, row, fmt.Sprintf("Files prepared on %s were not removed: %v.", dst.Name, err))
		return
	}
	cctx, cancel := context.WithTimeout(actx, 2*time.Minute)
	defer cancel()
	resp, err := client.FinishMigrationTarget(cctx, &pb.FinishMigrationTargetRequest{VmId: row.VMID, MigrationId: row.ID})
	if err != nil || !resp.Success {
		msg := ""
		if err != nil {
			msg = err.Error()
		} else {
			msg = agentMessage(resp.Error, "refused")
		}
		m.addNote(ctx, row, fmt.Sprintf("Files prepared on %s were not removed: %s.", dst.Name, msg))
	}
}

func (m *MigrationService) addNote(ctx context.Context, row *models.VMMigration, note string) {
	row.Note = strings.TrimSpace(row.Note + " " + note)
	m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMMigration{}).Where("id = ?", row.ID).Update("note", row.Note)
}

// confirmAndMove makes sure the VM is on the destination, then moves its
// records there and finishes on the new node.
func (m *MigrationService) confirmAndMove(ctx context.Context, row *models.VMMigration, vm *models.VM, src, dst *models.Node) error {
	m.setPhase(ctx, row, phaseConfirm)
	if row.Mode == models.MigrationOffline {
		// The disks were copied while the VM was off. Started since, the copy is stale.
		info, err := m.migrationInfo(ctx, src, &pb.GetMigrationInfoRequest{VmId: vm.ID})
		if err != nil {
			return fmt.Errorf("%w: %v", errInterrupted, err)
		}
		if info.VmFound && info.VmState != "shutoff" {
			m.rollback(ctx, row, dst)
			return fmt.Errorf("the VM was started on node %s while its disks were copied, so the copy was discarded", src.Name)
		}
	}
	client, actx, err := m.agent(ctx, dst)
	if err != nil {
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	cctx, cancel := context.WithTimeout(actx, time.Minute)
	resp, err := client.FinishMigrationTarget(cctx, &pb.FinishMigrationTargetRequest{VmId: vm.ID, MigrationId: row.ID, Commit: true})
	cancel()
	state := ""
	switch {
	case err != nil:
		return fmt.Errorf("%w: %v", errInterrupted, err)
	case resp.Success:
		state = resp.VmState
	default:
		// Not confirmed: move the records only if the VM is plainly there.
		info, ierr := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
		if ierr != nil || !info.VmFound {
			m.rollback(ctx, row, dst)
			return fmt.Errorf("node %s does not have the VM after the copy (%s); it stayed on node %s", dst.Name, agentMessage(resp.Error, "not confirmed"), src.Name)
		}
		if row.Mode == models.MigrationLive {
			// A finished live move always leaves a saved definition there; one
			// without may be a copy libvirt is still tearing down. Remove nothing.
			return fmt.Errorf("node %s has the VM but did not confirm it (%s); nothing was removed on either node, so check where it runs",
				dst.Name, agentMessage(resp.Error, "not confirmed"))
		}
		state = info.VmState
		m.addNote(ctx, row, fmt.Sprintf("Node %s did not confirm the VM (%s); check that it starts with the node.", dst.Name, agentMessage(resp.Error, "")))
	}
	if err := m.moveRecords(ctx, row, vm, src, dst, state); err != nil {
		// The VM runs on the destination and the records say the source: try again.
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	vm.NodeID = dst.ID
	return m.afterMove(ctx, row, vm, src, dst)
}

// moveRecords points the VM, its disks, port forwards and addresses at the
// destination, in one transaction.
func (m *MigrationService) moveRecords(ctx context.Context, row *models.VMMigration, vm *models.VM, src, dst *models.Node, state string) error {
	ctx = context.WithoutCancel(ctx)
	// The disk rows must point at the new files, or later disk and backup work
	// on the new node looks for the old ones: no paths, no move yet.
	info, err := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
	if err != nil {
		return fmt.Errorf("node %s could not list the VM's disks: %w", dst.Name, err)
	}
	if !info.VmFound {
		return fmt.Errorf("node %s does not list the VM", dst.Name)
	}
	paths := map[string]string{}
	for _, d := range info.Disks {
		paths[d.Target] = d.Path
	}
	newStatus := models.VMStatusStopped
	switch state {
	case "running":
		newStatus = models.VMStatusRunning
	case "paused":
		newStatus = models.VMStatusSuspended
	}
	if vm.Suspended && newStatus == models.VMStatusStopped {
		newStatus = models.VMStatusSuspended // as the status sync reads a suspended VM that is off
	}
	note := row.Note
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Table("vms").Where("id = ? AND node_id = ?", vm.ID, src.ID).Updates(map[string]interface{}{
			"node_id": dst.ID, "storage_pool_id": *row.DestPoolID, "vnc_port": nil, "status": newStatus, "updated_at": time.Now(),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // already moved by an earlier try
		}
		// Checkpoints do not move: the next scheduled backup starts a new chain.
		if err := tx.Model(&models.BackupSchedule{}).Where("vm_id = ?", vm.ID).Update("chain_reset_at", time.Now()).Error; err != nil {
			return err
		}
		// A port taken on the destination since the check cannot be refused
		// any more (the VM runs there): drop that forward rather than fail every try.
		if err := lockNodePorts(ctx, tx, dst.ID); err != nil {
			return err
		}
		clash, err := forwardsBlockedOn(ctx, tx, vm.ID, dst.ID)
		if err != nil {
			return err
		}
		if len(clash) > 0 {
			ids := make([]string, len(clash))
			for i := range clash {
				ids[i] = clash[i].ID
			}
			if err := tx.Where("id IN ?", ids).Delete(&models.PortForward{}).Error; err != nil {
				return err
			}
			note = strings.TrimSpace(note + " " + fmt.Sprintf("Port forwards %s were removed: node %s took those ports while the VM moved.", forwardList(clash), dst.Name))
		}
		var disks []models.VMDisk
		if err := tx.Where("vm_id = ?", vm.ID).Find(&disks).Error; err != nil {
			return err
		}
		for _, d := range disks {
			if p := paths[d.Device]; p != "" && p != d.Path {
				if err := tx.Model(&models.VMDisk{}).Where("id = ?", d.ID).Update("path", p).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&models.PortForward{}).Where("vm_id = ?", vm.ID).Update("node_id", dst.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.IPAddress{}).Where("vm_id = ? AND node_id = ?", vm.ID, src.ID).Update("node_id", dst.ID).Error; err != nil {
			return err
		}
		return tx.Model(&models.VMMigration{}).Where("id = ?", row.ID).Updates(map[string]interface{}{"phase": phaseApplying, "note": note, "updated_at": time.Now()}).Error
	})
	if err == nil {
		row.Note = note
	}
	return err
}

// afterMove puts everything that lives on a node back for the VM on its new
// node, then removes the old copy. The VM is moved whatever happens here;
// what did not work is noted and raised.
func (m *MigrationService) afterMove(ctx context.Context, row *models.VMMigration, vm *models.VM, src, dst *models.Node) error {
	ctx = context.WithoutCancel(ctx)
	m.setPhase(ctx, row, phaseApplying)
	var problems []string

	if params, ok, err := m.network.NetworkConfigParams(ctx, vm); err != nil {
		problems = append(problems, "network config could not be built: "+err.Error())
	} else if ok {
		client, actx, err := m.agent(ctx, dst)
		if err == nil {
			cctx, cancel := context.WithTimeout(actx, 2*time.Minute)
			err = queue.ApplyVMNetworkConfig(cctx, client, dst, vm, params)
			cancel()
		}
		if err != nil {
			problems = append(problems, "network rules not applied yet ("+err.Error()+"); queued again")
			_ = m.network.enqueueNetworkResync(ctx, vm)
		}
	}
	if fresh, err := m.vms.vmRepo.GetByID(ctx, vm.ID); err == nil {
		m.vms.applyConsoleAccess(ctx, fresh, fresh.ConsoleEnabled)
		if res, err := m.vms.ApplyVMLimits(ctx, vm.ID); err == nil && res.Note != nil {
			problems = append(problems, "limits: "+*res.Note)
		}
	}

	m.setPhase(ctx, row, phaseCleanup)
	if removed, err := m.removeOldCopy(ctx, vm, src, row); err != nil {
		problems = append(problems, fmt.Sprintf("the old copy on node %s was not removed: %v", src.Name, err))
		m.alerts.Event(ctx, AlertInput{
			Key: "migration_leftover:" + vm.ID, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning,
			Title: vm.Hostname + " moved, but its old copy on " + src.Name + " was left", Detail: truncate(err.Error(), 500),
			ResourceType: "vm", ResourceID: vm.ID, ResourceName: vm.Hostname,
		})
	} else if len(removed) > 0 {
		sort.Strings(removed)
		m.logger.InfoContext(ctx, "removed a moved VM's old copy", "vm_id", vm.ID, "node", src.Name, "files", removed)
	}
	note := ""
	if len(problems) > 0 {
		note = strings.Join(problems, "; ") + "."
	}
	m.finish(ctx, row, models.MigrationCompleted, "", note)
	return nil
}

func (m *MigrationService) removeOldCopy(ctx context.Context, vm *models.VM, src *models.Node, row *models.VMMigration) ([]string, error) {
	client, actx, err := m.agent(ctx, src)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, 5*time.Minute)
	defer cancel()
	resp, err := client.FinishMigrationSource(cctx, &pb.FinishMigrationSourceRequest{VmId: vm.ID, MigrationId: row.ID,
		LvmPools: queue.NodeLVMPools(ctx, m.db, src.ID)})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return resp.Removed, errors.New(agentMessage(resp.Error, "refused"))
	}
	return resp.Removed, nil
}
