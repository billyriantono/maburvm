package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

// Recovering a failed node's VMs on other nodes. Staff start it, per VM, for a
// node the panel has marked offline; nothing here runs on its own.
//
// A VM whose disks are all on a shared pool is defined on another member, on
// the same files. Any other VM is restored from its latest backup. Either way
// it stays the same VM: same record, owner and, where the destination carries
// its address pools, the same addresses.
//
// Two copies of a VM must never run at once. Staff confirm the node is powered
// off or cut off, the panel refuses while it has heard from the node lately,
// and when the node returns it is told which VMs live elsewhere (FenceNode):
// its agent then never starts them.

var (
	// ErrRecoveryRefused is a recovery that was refused; the text says why.
	ErrRecoveryRefused = errors.New("recovery refused")
	// ErrNoLeftover is a clean-up of a copy the node does not hold.
	ErrNoLeftover = errors.New("this node holds no old copy of that VM")
)

// NodeCheckInterval is how often the collector checks every node.
const NodeCheckInterval = 60 * time.Second

// Phases a recovery reports.
const (
	recChecking  = "Checking"
	recAddresses = "Giving the VM new addresses"
	recDefining  = "Defining the VM on the destination"
	recRestoring = "Restoring the backup on the destination"
	recRecords   = "Moving the VM's records"
	recApplying  = "Applying network, console and limits on the destination"
	recStarting  = "Starting the VM"
)

// restoreSourcer says what a node needs to restore a backup (BackupService).
type restoreSourcer interface {
	RestoreSourceFor(ctx context.Context, backup *models.Backup, node *models.Node) (*RestoreSource, error)
}

// SetBackups gives the service the backups a VM without shared disks is
// recovered from.
func (m *MigrationService) SetBackups(b restoreSourcer) { m.backups = b }

// agentPortAnswers reports a node whose agent port accepts a connection now.
func agentPortAnswers(ctx context.Context, node *models.Node) bool {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(hostOnlyIP(node.IPAddress), "50051"))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// oldCopyOn reports a node that still holds an old copy of a VM recovered away
// from it. Its agent refuses to run that VM, so the VM must not be brought
// back there, by a recovery or a move, until the copy is removed.
func oldCopyOn(ctx context.Context, db *gorm.DB, vmID, nodeID string) bool {
	var n int64
	// Quiet: an older test schema has no recoveries table.
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Model(&models.VMRecovery{}).
		Where("vm_id = ? AND source_node_id = ? AND moved AND old_copy IN ?", vmID, nodeID,
			[]string{models.OldCopyPending, models.OldCopyFenced, models.OldCopyRunning}).Count(&n).Error
	return err == nil && n > 0
}

func refuseRecovery(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrRecoveryRefused, fmt.Sprintf(format, args...))
}

// nodeDownRefusal says why the panel does not take a node for down, "" when
// it does: marked offline by the node checks, silent for as long as those
// take to mark a node offline, and not answering right now.
func (m *MigrationService) nodeDownRefusal(ctx context.Context, node *models.Node) string {
	after := m.alerts.NodeOfflineAfter(ctx)
	switch {
	case node.Status != models.NodeStatusOffline:
		return fmt.Sprintf("node %s is %s; only the VMs of a node the panel has marked offline are recovered", node.Name, node.Status)
	case node.FailedPolls < after:
		return fmt.Sprintf("node %s has missed %d checks, and a node is down after %d in a row", node.Name, node.FailedPolls, after)
	case node.LastReportAt != nil && time.Since(*node.LastReportAt) < time.Duration(after)*NodeCheckInterval:
		return fmt.Sprintf("the panel heard from node %s's agent at %s, too recently to take it for down", node.Name, node.LastReportAt.UTC().Format("15:04:05 UTC"))
	case m.nodeAnswers != nil && m.nodeAnswers(ctx, node):
		return fmt.Sprintf("node %s's agent port answers right now, so the node is not down", node.Name)
	}
	return ""
}

// RecoveryBackup is the backup a VM would be restored from.
type RecoveryBackup struct {
	ID          string     `json:"id"`
	CompletedAt *time.Time `json:"completed_at"`
	Size        int64      `json:"size"`
	Mode        string     `json:"mode"`
}

// RecoveryOption is what recovering a VM onto one node would take.
type RecoveryOption struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	// Method is shared, backup, or "" when the node offers neither.
	Method string `json:"method"`
	// CanRecover: the VM can go there with its addresses. CanWithNewAddress:
	// only when staff choose to give it new ones from this node's pools.
	CanRecover        bool `json:"can_recover"`
	CanWithNewAddress bool `json:"can_with_new_address"`
	// Blockers stop it whatever is chosen; AddressBlockers are what new
	// addresses get around.
	Blockers        []string `json:"blockers"`
	AddressBlockers []string `json:"address_blockers"`
	Warnings        []string `json:"warnings"`
}

// RecoveryPlanItem is one VM of a failed node and what can be done with it.
type RecoveryPlanItem struct {
	VMID     string `json:"vm_id"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
	DiskGB   int    `json:"disk_gb"`
	// SharedPool names the shared pool all its disks are on, "" when they
	// are not all on shared storage.
	SharedPool string           `json:"shared_pool"`
	Backup     *RecoveryBackup  `json:"backup"`
	Options    []RecoveryOption `json:"options"`
	// Reason is why the VM cannot be recovered anywhere, "" when it can.
	Reason string `json:"reason"`
	// Active is the VM's recovery under way.
	Active *models.VMRecovery `json:"active,omitempty"`
}

// RecoveryRun is one recovery from a node, for its report.
type RecoveryRun struct {
	models.VMRecovery
	Hostname     string `json:"hostname"`
	DestNodeName string `json:"dest_node_name"`
}

// RecoveryPlan is a node's VMs and where each could be recovered.
type RecoveryPlan struct {
	NodeID       string     `json:"node_id"`
	NodeName     string     `json:"node_name"`
	NodeStatus   string     `json:"node_status"`
	LastReportAt *time.Time `json:"last_report_at"`
	// Refusal is why nothing can be recovered from this node now, "" when the
	// panel takes it for down.
	Refusal string             `json:"refusal"`
	Items   []RecoveryPlanItem `json:"items"`
	// Runs are the recoveries from this node, newest first.
	Runs []RecoveryRun `json:"runs"`
}

// recoveryFacts is what the panel's records say about a VM on a failed node,
// whatever node it goes to.
type recoveryFacts struct {
	vm       *models.VM
	blocked  []string
	warnings []string
	diskGB   int
	cards    int
	// disks are the VM's disk files when every one is in a shared pool (vda
	// first); shared are those pools' ids, by the directory they are at.
	disks      []*pb.AdoptDisk
	shared     map[string]string
	sharedName string
	// onShared are the shared pools any of its disks is in, all on shared
	// storage or not.
	onShared map[string]bool
	// localWhy says why the VM is not recovered from shared storage.
	localWhy string
	backup   *models.Backup
}

// recoveryEnv carries what one plan has asked the nodes and promised them.
type recoveryEnv struct {
	*checkEnv
	features map[string]map[string]bool
	members  map[string]string
}

func newRecoveryEnv() *recoveryEnv {
	return &recoveryEnv{checkEnv: newCheckEnv(), features: map[string]map[string]bool{}, members: map[string]string{}}
}

func (m *MigrationService) nodeFeatureSet(ctx context.Context, env *recoveryEnv, nodeID string) map[string]bool {
	if f, ok := env.features[nodeID]; ok {
		return f
	}
	f := m.vms.NodeFeatureSet(ctx, nodeID)
	env.features[nodeID] = f
	return f
}

// quiet is the database without logging, for tables an older test schema lacks.
func (m *MigrationService) quiet(ctx context.Context) *gorm.DB {
	return m.db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard})
}

// recoveryFactsOf reads what the records say about a VM of node src.
func (m *MigrationService) recoveryFactsOf(ctx context.Context, vm *models.VM, src *models.Node) *recoveryFacts {
	f := &recoveryFacts{vm: vm, diskGB: vm.Resources.Disk, shared: map[string]string{}, onShared: map[string]bool{}}
	block := func(format string, args ...interface{}) { f.blocked = append(f.blocked, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...interface{}) {
		f.warnings = append(f.warnings, fmt.Sprintf(format, args...))
	}

	switch vm.Status {
	case models.VMStatusCreating, models.VMStatusDeleting, models.VMStatusError:
		block("the VM is %s", vm.Status)
	}
	if active, _ := m.activeMigration(ctx, vm.ID); active != nil {
		block("a move of the VM was under way when its node went down; cancel the move on the VM's page first")
	}
	if diskMoveActive(ctx, m.db, vm.ID) {
		block("a disk of the VM was moving to another pool when its node went down, so where its data is cannot be told from here")
	}
	if queue.RestoreActive(ctx, m.db, vm.ID) {
		block("a backup or snapshot is being restored to the VM")
	}
	if nets := vmPrivateNetworks(ctx, m.db, vm.ID); len(nets) > 0 {
		block("the VM is in private network %s, which exists on its own node only; restore its backup as a new VM instead", strings.Join(nets, ", "))
	}

	var extra []models.VMDisk
	m.db.WithContext(ctx).Where("vm_id = ?", vm.ID).Order("device").Find(&extra)
	for _, d := range extra {
		f.diskGB += d.SizeGB
	}
	var cards int64
	m.quiet(ctx).Model(&models.VMNIC{}).Where("vm_id = ?", vm.ID).Count(&cards)
	f.cards = int(cards)

	// Its disks, as the records have them: the system disk is named after the
	// VM in its pool, the data disks are listed with their paths.
	var pools []models.StoragePool
	m.quiet(ctx).Where("node_id = ? AND shared_pool_id IS NOT NULL", src.ID).Find(&pools)
	holder := func(p string) *models.StoragePool {
		for i := range pools {
			if !models.IsLVMPoolType(pools[i].Type) && PoolHoldsDisk(pools[i], p) {
				return &pools[i]
			}
		}
		return nil
	}
	drives := []*pb.AdoptDisk{}
	// Only the pool on record counts: guessing the node's primary pool for a VM
	// without one could name a disk that is somewhere else.
	var own *string
	m.db.WithContext(ctx).Raw("SELECT storage_pool_id FROM vms WHERE id = ?", vm.ID).Scan(&own)
	switch p := poolByID(pools, ptrString(own)); {
	case p != nil && !models.IsLVMPoolType(p.Type):
		drives = append(drives, &pb.AdoptDisk{Target: systemDiskTarget, Path: path.Join(p.Path, vm.ID+".qcow2")})
	case ptrString(own) == "":
		f.localWhy = "the panel has no record of the pool its system disk is in"
	default:
		f.localWhy = "its system disk is on the failed node's own storage"
	}
	for _, d := range extra {
		drives = append(drives, &pb.AdoptDisk{Target: d.Device, Path: d.Path})
	}
	for _, d := range drives {
		p := holder(d.Path)
		if p == nil {
			if f.localWhy == "" {
				f.localWhy = fmt.Sprintf("its disk %s is on the failed node's own storage", d.Target)
			}
			continue
		}
		f.onShared[*p.SharedPoolID] = true
		f.shared[path.Clean(p.Path)] = *p.SharedPoolID
		if f.sharedName == "" {
			f.sharedName = p.Name
		}
	}
	if f.localWhy == "" {
		f.disks = drives
	} else {
		f.shared, f.sharedName = map[string]string{}, ""
	}

	// The newest finished backup, one kept outside the failed node first.
	var backups []models.Backup
	m.db.WithContext(ctx).Where("vm_id = ? AND status = ? AND COALESCE(checksum, '') <> ''", vm.ID, models.BackupStatusCompleted).
		Order("completed_at DESC NULLS LAST, created_at DESC").Limit(20).Find(&backups)
	for i := range backups {
		if backups[i].StorageProvider != models.BackupStoreNode {
			f.backup = &backups[i]
			break
		}
	}
	if f.backup == nil && len(backups) > 0 {
		f.backup = &backups[0]
	}

	// What a definition made from the records does not carry over.
	if vm.Hardware.DedicatedCPU {
		warn("its dedicated CPU cores are not carried over; turn dedicated CPUs on again from its page")
	}
	if vm.Hardware.TPM {
		warn("its virtual TPM starts empty: the keys were on the failed node")
	}
	if vm.Hardware.Firmware == models.FirmwareUEFI {
		warn("it boots with UEFI: its firmware settings start fresh, so it boots its disk's default boot loader")
	}
	var snaps int64
	if m.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ?", vm.ID).Count(&snaps).Error == nil && snaps > 0 {
		warn("its %d snapshots cannot be used on another node; their records are removed", snaps)
	}
	if vm.RescueMode {
		warn("it was in rescue mode; it is recovered booting its own disk")
	}
	return f
}

// memberOn is dst's member row of every shared pool the VM's disks are in,
// nil with the reason when dst cannot use one of them right now.
func (m *MigrationService) memberOn(ctx context.Context, f *recoveryFacts, dst *models.Node, env *recoveryEnv) (*models.StoragePool, string) {
	var first *models.StoragePool
	dirs := make([]string, 0, len(f.shared))
	for dir := range f.shared {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		id := f.shared[dir]
		var rows []models.StoragePool
		m.quiet(ctx).Where("node_id = ? AND shared_pool_id = ?", dst.ID, id).Find(&rows)
		if len(rows) == 0 || path.Clean(rows[0].Path) != dir {
			return nil, fmt.Sprintf("node %s is not a member of the shared pool the VM's disks are in", dst.Name)
		}
		if rows[0].Status != "online" {
			return nil, fmt.Sprintf("shared pool %s is %s on node %s", rows[0].Name, rows[0].Status, dst.Name)
		}
		key := id + "|" + dst.ID
		problem, asked := env.members[key]
		if !asked {
			problem = m.shared.MemberProblem(ctx, id, dst)
			env.members[key] = problem
		}
		if problem != "" {
			return nil, problem
		}
		if first == nil || strings.HasPrefix(f.disks[0].Path, dir+"/") {
			first = &rows[0]
		}
	}
	return first, ""
}

// recoveryOption works out what recovering the VM onto dst takes. pool is
// where its disks are (shared) or would go (backup) there.
func (m *MigrationService) recoveryOption(ctx context.Context, f *recoveryFacts, src, dst *models.Node, env *recoveryEnv) (RecoveryOption, *models.StoragePool) {
	opt := RecoveryOption{NodeID: dst.ID, NodeName: dst.Name, Blockers: append([]string{}, f.blocked...), AddressBlockers: []string{}, Warnings: append([]string{}, f.warnings...)}
	block := func(format string, args ...interface{}) {
		opt.Blockers = append(opt.Blockers, fmt.Sprintf(format, args...))
	}
	warn := func(format string, args ...interface{}) {
		opt.Warnings = append(opt.Warnings, fmt.Sprintf(format, args...))
	}
	vm := f.vm

	if dst.Status != models.NodeStatusActive {
		block("node %s is %s", dst.Name, dst.Status)
	}
	if dst.CreationPaused {
		block("node %s is paused for new VMs; resume it to recover VMs onto it", dst.Name)
	}
	if oldCopyOn(ctx, m.db, vm.ID, dst.ID) {
		block("node %s still holds an old copy of this VM from an earlier recovery; remove it from that node's recovery leftovers first", dst.Name)
	}
	if vm.PlanID != nil {
		if in, err := inNodeGroup([]models.Node{*dst}, planNodeGroup(ctx, m.db, *vm.PlanID)); err != nil || len(in) == 0 {
			block("the VM's plan is tied to a node group that node %s is not in", dst.Name)
		}
	}
	features := m.nodeFeatureSet(ctx, env, dst.ID)
	// Both ways need it: the job asks the node whether the VM is there
	// (GetMigrationInfo), which older agents cannot answer.
	if !features[models.FeatureVMRecovery] {
		block("node %s needs an agent update before VMs can be recovered onto it", dst.Name)
	}

	// How: on its own disks where dst shares them, else from its backup.
	var pool *models.StoragePool
	sharedWhy := f.localWhy
	if f.disks != nil {
		pool, sharedWhy = m.memberOn(ctx, f, dst, env)
	}
	switch {
	case pool != nil:
		opt.Method = models.RecoveryShared
		for _, b := range m.capacityBlockers(ctx, vm, dst, pool, 0, env.checkEnv) {
			block("%s", b)
		}
	case f.backup != nil:
		opt.Method = models.RecoveryBackup
		if f.disks != nil {
			warn("the VM's disks are on shared storage, but %s: this restores its backup instead, without what was written since", sharedWhy)
		}
		when := "an unknown time"
		if f.backup.CompletedAt != nil {
			when = f.backup.CompletedAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		warn("it is restored from the backup finished at %s and started; what was written since is not in it. The disk on node %s is left as it is until you remove it there", when, src.Name)
		if f.backup.StorageProvider == models.BackupStoreNode {
			warn("that backup was written to node %s's own storage; it can be restored only if that is the backup destination's storage", src.Name)
		}
		var err error
		if pool, err = NodeDiskPool(ctx, m.db, dst.ID, planStorageTier(ctx, m.db, ptrString(vm.PlanID))); err != nil {
			block("node %s: %v", dst.Name, err)
		}
		if pool != nil {
			if err := lvmPoolUsable(pool); err != nil {
				block("storage pool %s on node %s: %v", pool.Name, dst.Name, err)
			}
			// Restored there, the backup would land on the VM's own disk files
			// and replace them, not leave them as they are.
			if pool.SharedPoolID != nil && f.onShared[*pool.SharedPoolID] {
				block("node %s would restore the backup into shared pool %s, where the VM's disks are: that replaces them. Recover the VM onto a node that restores it elsewhere", dst.Name, pool.Name)
			}
			for _, b := range m.capacityBlockers(ctx, vm, dst, pool, f.diskGB, env.checkEnv) {
				block("%s", b)
			}
		} else {
			for _, b := range m.capacityBlockers(ctx, vm, dst, nil, 0, env.checkEnv) {
				block("%s", b)
			}
		}
		if m.backups == nil {
			block("backups cannot be restored from here")
		}
		if vm.Suspended {
			block("the VM is suspended, and restoring a backup starts it; lift the suspension first")
		}
	default:
		block("%s, and it has no finished backup", sharedWhy)
	}

	if vm.Hardware.Firmware == models.FirmwareUEFI && !features[models.FeatureUEFI] {
		block("the VM boots with UEFI and node %s has no UEFI firmware", dst.Name)
	}
	if vm.Hardware.Hugepages && !features[models.FeatureHugepages] {
		block("the VM's memory is on huge pages and node %s has none reserved", dst.Name)
	}

	// Its public ports: a NAT VM's go into a free block on dst, any other
	// VM's forwards must be free there.
	scratch := &MigrationCheck{}
	if b, err := natBlock(ctx, m.db, vm.ID); err == nil && b != nil {
		m.natMoveChecks(ctx, vm, dst, scratch)
	} else if clash, err := forwardsBlockedOn(ctx, m.db, vm.ID, dst.ID); err != nil {
		scratch.block("port forwards could not be checked: %v", err)
	} else if len(clash) > 0 {
		scratch.block("node %s already uses %d of the ports this VM forwards (%s); remove those port forwards first", dst.Name, len(clash), forwardList(clash))
	}
	opt.Blockers = append(opt.Blockers, scratch.Blockers...)
	opt.Warnings = append(opt.Warnings, scratch.Warnings...)

	// Its addresses reach it only where their pools do.
	missing, err := vmPoolsNotOn(ctx, m.db, vm.ID, dst.ID, true)
	if err != nil {
		block("the VM's address pools could not be checked: %v", err)
	}
	replaceable := len(missing) > 0
	for _, p := range missing {
		opt.AddressBlockers = append(opt.AddressBlockers, fmt.Sprintf("address pool %s is not available on node %s, so the VM's address would not reach it there", p.Name, dst.Name))
		if p.Family != models.IPFamilyIPv6 && len(m.replacementPools(ctx, dst.ID, p.NAT)) == 0 {
			replaceable = false
			opt.AddressBlockers = append(opt.AddressBlockers, fmt.Sprintf("node %s has no address pool of the same kind to give the VM a new address from", dst.Name))
		}
	}
	if len(missing) > 0 && f.cards > 1 {
		replaceable = false
		opt.AddressBlockers = append(opt.AddressBlockers, fmt.Sprintf("the VM has %d network cards; new addresses are only given to a VM with one", f.cards))
	}
	opt.CanRecover = opt.Method != "" && len(opt.Blockers) == 0 && len(opt.AddressBlockers) == 0
	opt.CanWithNewAddress = opt.Method != "" && len(opt.Blockers) == 0 && replaceable
	return opt, pool
}

// replacementPools are the pools on a node a VM's address can be replaced
// from: IPv4, public or NAT like the one it loses, with a bridge and gateway,
// and no tenant's private network.
func (m *MigrationService) replacementPools(ctx context.Context, nodeID string, nat bool) []models.IPPool {
	if m.vms.ipamService == nil {
		return nil
	}
	pools, err := m.vms.ipamService.ListPoolsForNode(ctx, nodeID)
	if err != nil {
		return nil
	}
	var private []string
	m.db.WithContext(ctx).Table("managed_networks").Where("deleted_at IS NULL AND bridge <> ''").Pluck("bridge", &private)
	out := pools[:0:0]
	for _, p := range pools {
		if p.Family == models.IPFamilyIPv6 || p.NAT != nat || p.Bridge == "" || p.Gateway == "" || containsString(private, p.Bridge) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// RecoveryPlanFor lists a node's VMs and, for each, the nodes it could be
// recovered onto. It changes nothing.
func (m *MigrationService) RecoveryPlanFor(ctx context.Context, nodeID string) (*RecoveryPlan, error) {
	src, err := m.vms.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	plan := &RecoveryPlan{NodeID: src.ID, NodeName: src.Name, NodeStatus: string(src.Status), LastReportAt: src.LastReportAt,
		Refusal: m.nodeDownRefusal(ctx, src), Items: []RecoveryPlanItem{}, Runs: m.recoveryRuns(ctx, src.ID)}
	if src.Status != models.NodeStatusOffline {
		return plan, nil // nothing to plan for a node that is up
	}
	vms, err := m.vms.vmRepo.ListByNodeID(ctx, nodeID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list the node's VMs: %w", err)
	}
	sort.Slice(vms, func(i, j int) bool {
		if vms[i].Hostname != vms[j].Hostname {
			return vms[i].Hostname < vms[j].Hostname
		}
		return vms[i].ID < vms[j].ID
	})
	others, err := m.recoveryTargets(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	env := newRecoveryEnv()
	for i := range vms {
		plan.Items = append(plan.Items, m.planRecovery(ctx, &vms[i], src, others, env))
	}
	return plan, nil
}

// recoveryTargets are the nodes VMs could be recovered onto: every active one
// but the failed node.
func (m *MigrationService) recoveryTargets(ctx context.Context, srcID string) ([]models.Node, error) {
	nodes, err := m.vms.nodeRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}
	out := nodes[:0:0]
	for _, n := range nodes {
		if n.ID != srcID {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MigrationService) planRecovery(ctx context.Context, vm *models.VM, src *models.Node, targets []models.Node, env *recoveryEnv) RecoveryPlanItem {
	f := m.recoveryFactsOf(ctx, vm, src)
	item := RecoveryPlanItem{VMID: vm.ID, Hostname: vm.Hostname, Status: string(vm.Status), DiskGB: f.diskGB, SharedPool: f.sharedName, Options: []RecoveryOption{}}
	if f.backup != nil {
		item.Backup = &RecoveryBackup{ID: f.backup.ID, CompletedAt: f.backup.CompletedAt, Size: f.backup.Size, Mode: f.backup.Mode}
	}
	if active := m.activeRecovery(ctx, vm.ID); active != nil {
		item.Active = active
	}
	possible := false
	for i := range targets {
		opt, _ := m.recoveryOption(ctx, f, src, &targets[i], env)
		possible = possible || opt.CanRecover || opt.CanWithNewAddress
		item.Options = append(item.Options, opt)
	}
	switch {
	case possible:
	case len(f.blocked) > 0:
		item.Reason = strings.Join(f.blocked, "; ")
	case f.disks == nil && f.backup == nil:
		item.Reason = f.localWhy + ", and it has no finished backup"
	case len(targets) == 0:
		item.Reason = "no other node is active"
	default:
		item.Reason = "no other node can take it; each node's entry says why"
	}
	return item
}

// activeRecovery is the VM's recovery under way, nil when there is none. One
// whose job is gone (cancelled from the job queue, or lost) is ended here, so
// it does not lock the VM for good.
func (m *MigrationService) activeRecovery(ctx context.Context, vmID string) *models.VMRecovery {
	var rows []models.VMRecovery
	if m.db.WithContext(ctx).Where("vm_id = ? AND status IN ?", vmID, []string{models.RecoveryQueued, models.RecoveryRunning}).Limit(1).Find(&rows).Error != nil || len(rows) == 0 {
		return nil
	}
	row := &rows[0]
	alive := time.Since(row.CreatedAt) < time.Minute
	if row.JobID != nil {
		var st string
		if m.db.WithContext(ctx).Raw("SELECT state FROM river_job WHERE id = ?", *row.JobID).Scan(&st).Error != nil {
			return row // cannot tell: leave it
		}
		alive = st == "available" || st == "running" || st == "retryable" || st == "scheduled" || st == "pending"
	}
	if !alive {
		m.finishRecovery(ctx, row, models.RecoveryFailed, "the panel lost track of this recovery; check on which node the VM is defined")
		return nil
	}
	return row
}

func (m *MigrationService) recoveryRuns(ctx context.Context, nodeID string) []RecoveryRun {
	var rows []models.VMRecovery
	m.db.WithContext(ctx).Where("source_node_id = ?", nodeID).Order("created_at DESC").Limit(500).Find(&rows)
	out := make([]RecoveryRun, 0, len(rows))
	names := map[string]string{}
	for _, r := range rows {
		run := RecoveryRun{VMRecovery: r}
		m.db.WithContext(ctx).Raw("SELECT hostname FROM vms WHERE id = ?", r.VMID).Scan(&run.Hostname)
		name, ok := names[r.DestNodeID]
		if !ok {
			m.db.WithContext(ctx).Raw("SELECT name FROM nodes WHERE id = ?", r.DestNodeID).Scan(&name)
			names[r.DestNodeID] = name
		}
		run.DestNodeName = name
		out = append(out, run)
	}
	return out
}

// RecoveryChoice is what staff chose for one VM.
type RecoveryChoice struct {
	VMID       string `json:"vm_id"`
	DestNodeID string `json:"dest_node_id"`
	// NewAddress gives the VM addresses from the destination's pools in place
	// of those that do not reach it there.
	NewAddress bool `json:"new_address"`
	// Start starts a VM recovered on shared storage; nil starts it when the
	// panel last saw it running. A VM restored from a backup is always started.
	Start *bool `json:"start"`
}

// RecoveryRequest starts the recovery of some of a node's VMs.
type RecoveryRequest struct {
	// NodeIsDown is the operator's word that the node is powered off, or cut
	// off from its storage and network. Without it nothing is recovered.
	NodeIsDown bool             `json:"node_is_down"`
	VMs        []RecoveryChoice `json:"vms"`

	RequestedBy string `json:"-"`
	IP          string `json:"-"`
	UserAgent   string `json:"-"`
}

// RecoveryRefusal is a VM that was not queued, and why.
type RecoveryRefusal struct {
	VMID     string `json:"vm_id"`
	Hostname string `json:"hostname"`
	Reason   string `json:"reason"`
}

// RecoveryStarted is what a request queued and what it refused.
type RecoveryStarted struct {
	Queued  []*models.VMRecovery `json:"queued"`
	Refused []RecoveryRefusal    `json:"refused"`
}

// StartRecovery queues one job per chosen VM. The whole request is refused
// unless the operator says the node is down and the panel has not heard from
// it; a VM that cannot be recovered as chosen is refused alone.
func (m *MigrationService) StartRecovery(ctx context.Context, nodeID string, req RecoveryRequest) (*RecoveryStarted, error) {
	src, err := m.vms.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	if !req.NodeIsDown {
		return nil, refuseRecovery("confirm that node %s is powered off, or cut off from its storage and network: a VM running in two places destroys its disk", src.Name)
	}
	if why := m.nodeDownRefusal(ctx, src); why != "" {
		return nil, refuseRecovery("%s", why)
	}
	if len(req.VMs) == 0 {
		return nil, refuseRecovery("choose at least one VM")
	}
	enqueue := m.enqueueRecovery
	if enqueue == nil {
		if m.river == nil {
			return nil, fmt.Errorf("the job queue is not running")
		}
		enqueue = func(ctx context.Context, row *models.VMRecovery) (int64, error) {
			res, err := m.river.Insert(ctx, queue.RecoveryJob{RecoveryID: row.ID, VMID: row.VMID}, nil)
			if err != nil {
				return 0, err
			}
			return res.Job.ID, nil
		}
	}
	out := &RecoveryStarted{Queued: []*models.VMRecovery{}, Refused: []RecoveryRefusal{}}
	env := newRecoveryEnv()
	seen := map[string]bool{}
	for _, c := range req.VMs {
		refuse := func(host, format string, args ...interface{}) {
			out.Refused = append(out.Refused, RecoveryRefusal{VMID: c.VMID, Hostname: host, Reason: fmt.Sprintf(format, args...)})
		}
		if seen[c.VMID] {
			continue
		}
		seen[c.VMID] = true
		vm, err := m.vms.vmRepo.GetByID(ctx, c.VMID)
		if err != nil || vm.NodeID != src.ID {
			refuse("", "this VM is not on node %s", src.Name)
			continue
		}
		dst, err := m.vms.nodeRepo.GetByID(ctx, c.DestNodeID)
		if err != nil || dst.ID == src.ID {
			refuse(vm.Hostname, "choose another node to recover the VM onto")
			continue
		}
		if m.activeRecovery(ctx, vm.ID) != nil {
			refuse(vm.Hostname, "the VM is already being recovered")
			continue
		}
		f := m.recoveryFactsOf(ctx, vm, src)
		opt, pool := m.recoveryOption(ctx, f, src, dst, env)
		if !opt.CanRecover && !(c.NewAddress && opt.CanWithNewAddress) {
			refuse(vm.Hostname, "%s", strings.Join(append(append([]string{}, opt.Blockers...), opt.AddressBlockers...), "; "))
			continue
		}
		start := vm.Status == models.VMStatusRunning
		if c.Start != nil {
			start = *c.Start
		}
		row := &models.VMRecovery{VMID: vm.ID, SourceNodeID: src.ID, DestNodeID: dst.ID, Method: opt.Method, Status: models.RecoveryQueued,
			StartVM: start, NewAddress: c.NewAddress && !opt.CanRecover, Phase: "Waiting to start"}
		if opt.Method == models.RecoveryBackup {
			row.BackupID, row.StartVM = &f.backup.ID, true
		}
		if req.RequestedBy != "" {
			row.RequestedBy = &req.RequestedBy
		}
		if err := m.db.WithContext(ctx).Create(row).Error; err != nil {
			refuse(vm.Hostname, "the recovery could not be recorded: %v", err)
			continue
		}
		jobID, err := enqueue(ctx, row)
		if err != nil {
			m.finishRecovery(ctx, row, models.RecoveryFailed, "could not be queued: "+err.Error())
			refuse(vm.Hostname, "the recovery could not be queued: %v", err)
			continue
		}
		if jobID != 0 {
			row.JobID = &jobID
			m.db.WithContext(ctx).Model(row).Update("job_id", jobID)
		}
		// Later VMs of this request see what this one takes on its node.
		if pool != nil && opt.Method == models.RecoveryBackup {
			env.add(dst.ID, pool.ID, vm, f.diskGB)
		} else {
			env.add(dst.ID, "", vm, 0)
		}
		out.Queued = append(out.Queued, row)
	}
	if len(out.Queued) > 0 {
		ids := make([]string, len(out.Queued))
		for i, r := range out.Queued {
			ids[i] = r.VMID
		}
		m.audit(ctx, req.RequestedBy, req.IP, req.UserAgent, "node.recover_vms", "node", src.ID, map[string]any{
			"node": src.Name, "confirmed_node_is_down": true, "vm_ids": ids, "last_report_at": src.LastReportAt, "missed_checks": src.FailedPolls,
		})
	}
	return out, nil
}

// audit writes one audit row; a lost row is logged, never an error.
func (m *MigrationService) audit(ctx context.Context, actorID, ip, userAgent, action, resourceType, resourceID string, details map[string]any) {
	entry := &models.AuditLog{Action: action, ResourceType: resourceType, ResourceID: &resourceID, IPAddress: ip, UserAgent: userAgent, Details: details}
	if actorID != "" {
		entry.UserID = &actorID
	}
	tx := m.db.WithContext(context.WithoutCancel(ctx))
	if net.ParseIP(ip) == nil {
		tx = tx.Omit("ip_address") // an inet column: "" is not an address
	}
	if err := tx.Create(entry).Error; err != nil {
		m.logger.WarnContext(ctx, "could not write an audit row", "action", action, "resource", resourceID, "error", err)
	}
}

func (m *MigrationService) setRecoveryPhase(ctx context.Context, row *models.VMRecovery, phase string) error {
	row.Phase = phase
	return m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMRecovery{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{"phase": phase, "updated_at": time.Now()}).Error
}

func (m *MigrationService) noteRecovery(ctx context.Context, row *models.VMRecovery, note string) {
	row.Note = strings.TrimSpace(row.Note + " " + note)
	m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMRecovery{}).Where("id = ?", row.ID).Update("note", row.Note)
}

func (m *MigrationService) finishRecovery(ctx context.Context, row *models.VMRecovery, st, errMsg string) {
	now := time.Now()
	row.Status, row.Error, row.FinishedAt = st, errMsg, &now
	row.Phase = map[string]string{models.RecoveryCompleted: "Recovered", models.RecoveryFailed: "Failed"}[st]
	if err := m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMRecovery{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
		"status": st, "error": errMsg, "phase": row.Phase, "finished_at": now, "updated_at": now,
	}).Error; err != nil {
		m.logger.ErrorContext(ctx, "could not record the end of a recovery", "recovery_id", row.ID, "error", err)
	}
}

// RunRecoveryJob runs a queued recovery; the queue calls it.
func (m *MigrationService) RunRecoveryJob(ctx context.Context, recoveryID string) error {
	var row models.VMRecovery
	if err := m.db.WithContext(ctx).Where("id = ?", recoveryID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return river.JobCancel(fmt.Errorf("recovery %s no longer exists", recoveryID))
		}
		return err
	}
	if !row.Active() {
		return nil
	}
	err := m.recover(ctx, &row)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errInterrupted) || ctx.Err() != nil:
		return river.JobSnooze(30 * time.Second)
	}
	m.finishRecovery(ctx, &row, models.RecoveryFailed, err.Error())
	name := row.VMID
	m.db.WithContext(ctx).Raw("SELECT hostname FROM vms WHERE id = ?", row.VMID).Scan(&name)
	detail := truncate(err.Error(), 500)
	switch {
	case row.Moved:
		detail = "Its records already point at the new node. " + detail
	case row.Note != "":
		detail += " The VM is still recorded on the failed node and can be recovered again. " + row.Note
	default:
		detail += " Nothing was changed for the VM; it can be recovered again."
	}
	m.alerts.Event(context.WithoutCancel(ctx), AlertInput{
		Key: "recovery_failed:" + row.VMID, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning,
		Title: "Recovering " + name + " from its failed node did not work", Detail: detail,
		ResourceType: "vm", ResourceID: row.VMID, ResourceName: name,
	})
	return nil
}

// recover takes one recovery to its end. An error fails it, except
// errInterrupted, which has the job try again later.
func (m *MigrationService) recover(ctx context.Context, row *models.VMRecovery) error {
	vm, err := m.vms.vmRepo.GetByID(ctx, row.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("the VM no longer exists")
		}
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	src, err := m.vms.nodeRepo.GetByID(ctx, row.SourceNodeID)
	if err != nil {
		return fmt.Errorf("the failed node cannot be read: %w", err)
	}
	dst, err := m.vms.nodeRepo.GetByID(ctx, row.DestNodeID)
	if err != nil {
		return fmt.Errorf("the destination node cannot be read: %w", err)
	}
	if row.Moved || vm.NodeID == dst.ID {
		return m.afterRecovered(ctx, row, vm, src, dst) // an earlier try got this far
	}
	if vm.NodeID != src.ID {
		return errors.New("the VM is no longer on the node it was to be recovered from")
	}
	if row.Status == models.RecoveryQueued {
		now := time.Now()
		if err := m.db.WithContext(ctx).Model(&models.VMRecovery{}).Where("id = ? AND status = ?", row.ID, models.RecoveryQueued).
			Updates(map[string]interface{}{"status": models.RecoveryRunning, "started_at": now, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("%w: %v", errInterrupted, err)
		}
		row.Status, row.StartedAt = models.RecoveryRunning, &now
	}

	// asked: an earlier try already told the destination to bring the VM
	// back. From there the recovery is only ever finished, never called off:
	// the VM may exist on the destination by now.
	asked := row.Phase == recDefining || row.Phase == recRestoring || row.Phase == recRecords
	f := m.recoveryFactsOf(ctx, vm, src)
	opt, pool := m.recoveryOption(ctx, f, src, dst, newRecoveryEnv())
	if !asked {
		// The node may have come back since staff confirmed: look again, last
		// thing before the VM is defined anywhere.
		m.setRecoveryPhase(ctx, row, recChecking)
		if why := m.nodeDownRefusal(ctx, src); why != "" {
			return errors.New(why)
		}
		if opt.Method != row.Method {
			return fmt.Errorf("the VM can no longer be recovered onto node %s the way it was planned: %s", dst.Name, strings.Join(append(opt.Blockers, opt.AddressBlockers...), "; "))
		}
		if !opt.CanRecover && !(row.NewAddress && opt.CanWithNewAddress) {
			return fmt.Errorf("the VM can no longer be recovered onto node %s: %s", dst.Name, strings.Join(append(opt.Blockers, opt.AddressBlockers...), "; "))
		}
		// A definition there already is left from an earlier move: the agent
		// would take it for this one and bring up that copy, on its old disks.
		info, err := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
		if err != nil {
			return fmt.Errorf("%w: node %s did not answer: %v", errInterrupted, dst.Name, err)
		}
		if info.VmFound {
			return fmt.Errorf("node %s already has a definition of this VM, left from an earlier move; undefine it there, keeping its disks, then recover the VM again", dst.Name)
		}
	}
	var restore *RestoreSource
	var backup *models.Backup
	if row.Method == models.RecoveryBackup {
		// Before anything changes: the node must be able to read the backup.
		backup = &models.Backup{}
		if row.BackupID == nil || m.db.WithContext(ctx).Where("id = ?", *row.BackupID).First(backup).Error != nil {
			return errors.New("the backup the VM was to be restored from is gone")
		}
		if restore, err = m.backups.RestoreSourceFor(ctx, backup, dst); err != nil {
			if asked {
				return fmt.Errorf("%w: %v", errInterrupted, err)
			}
			return err
		}
	}

	if asked {
		// Asked before, and the answer never came. Finish when the VM is there.
		// When it is not and the failed node has come back meanwhile, nothing
		// new is started: only a download begun earlier is waited for.
		info, ierr := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
		if ierr != nil {
			return fmt.Errorf("%w: node %s did not answer: %v", errInterrupted, dst.Name, ierr)
		}
		if info.VmFound {
			return m.finishMove(ctx, row, vm, src, dst, pool)
		}
		if why := m.nodeDownRefusal(ctx, src); why != "" {
			if row.Method == models.RecoveryShared || row.StartedAt == nil || time.Since(*row.StartedAt) > queue.DiskTransferTimeout {
				return fmt.Errorf("%s, and the VM was not brought back on node %s", why, dst.Name)
			}
			return errInterrupted
		}
	}

	if row.NewAddress {
		m.setRecoveryPhase(ctx, row, recAddresses)
		notes, err := m.giveNewAddresses(ctx, vm, dst)
		if err != nil {
			return fmt.Errorf("the VM could not be given new addresses on node %s: %w", dst.Name, err)
		}
		for _, n := range notes {
			m.noteRecovery(ctx, row, n)
		}
	}
	params, err := m.recoveryParams(ctx, vm, pool, backup, restore)
	if err != nil {
		return err
	}
	target := *vm
	target.NodeID = dst.ID
	var adopt []*pb.AdoptDisk
	timeout, phase := 5*time.Minute, recDefining
	if row.Method == models.RecoveryShared {
		adopt = f.disks
	} else {
		timeout, phase = queue.DiskTransferTimeout, recRestoring
	}
	// Recorded before the node is asked: a retry must know it was.
	if err := m.setRecoveryPhase(ctx, row, phase); err != nil {
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	cmd, err := queue.RecoveryCommand(ctx, &target, params, adopt)
	if err != nil {
		return err
	}
	client, actx, err := m.agent(ctx, dst)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(actx, timeout)
	resp, err := client.ExecuteVMCommand(cctx, cmd)
	cancel()
	switch {
	case err != nil && row.Method == models.RecoveryBackup:
		// The node goes on downloading; ask again, which finds the VM made or
		// the download still running.
		return fmt.Errorf("%w: node %s did not answer: %v", errInterrupted, dst.Name, err)
	case err != nil:
		// It may have defined the VM before the answer was lost. A definition
		// the panel does not know of would start with that node one day.
		info, ierr := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
		if ierr != nil {
			return fmt.Errorf("%w: node %s did not answer: %v", errInterrupted, dst.Name, err)
		}
		if !info.VmFound {
			return fmt.Errorf("node %s did not define the VM: %w", dst.Name, err)
		}
	case !resp.GetSuccess():
		detail := agentMessage(resp.GetError(), resp.GetMessage())
		if row.Method == models.RecoveryBackup && queue.CreateStillRunning(detail) {
			return errInterrupted
		}
		return fmt.Errorf("node %s did not bring the VM back: %s", dst.Name, detail)
	}

	return m.finishMove(ctx, row, vm, src, dst, pool)
}

// finishMove moves the records of a VM that is defined on the destination,
// then finishes there.
func (m *MigrationService) finishMove(ctx context.Context, row *models.VMRecovery, vm *models.VM, src, dst *models.Node, pool *models.StoragePool) error {
	m.setRecoveryPhase(ctx, row, recRecords)
	if err := m.moveRecovered(ctx, row, vm, src, dst, pool); err != nil {
		// The VM is defined on the destination and the records say the failed
		// node: try again rather than leave it there unknown.
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	vm.NodeID = dst.ID
	return m.afterRecovered(ctx, row, vm, src, dst)
}

// recoveryParams is the VM's definition as a create job carries it, built
// from the panel's records: the same VM, on its existing disks or on a
// backup's.
func (m *MigrationService) recoveryParams(ctx context.Context, vm *models.VM, pool *models.StoragePool, backup *models.Backup, restore *RestoreSource) (json.RawMessage, error) {
	// The kind of system, from the template's record even when it was removed.
	var osType string
	if err := m.db.WithContext(ctx).Raw("SELECT COALESCE(os_type, '') FROM os_templates WHERE id = ?", vm.OSTemplateID).Scan(&osType).Error; err != nil {
		return nil, fmt.Errorf("the VM's template record cannot be read, so its kind of system is not known: %w", err)
	}
	ip, gateway, bridge, prefix, vlan := m.vms.primaryNetworkConfig(ctx, vm.ID)
	// Dedicated cores are the failed node's own.
	hw := vm.Hardware
	hw.DedicatedCPU, hw.PinnedCPUs = false, ""
	image := models.AdoptImageRef
	if backup != nil {
		image = "s3://" + backup.StoragePath
	}
	params := map[string]interface{}{
		"hostname":     vm.Hostname,
		"image_path":   models.GuestImageRef(osType, image),
		"vnc_password": vm.VNCPassword,
		"vlan_id":      vlan,
		"cpu_model":    hw.CPUModel,
		"ip_address":   ip,
		"gateway":      gateway,
		"netmask":      prefix,
		"bridge":       bridge,
	}
	if !hw.IsZero() {
		params["hardware"] = hw
	}
	var first models.Network
	if m.db.WithContext(ctx).Where("vm_id = ?", vm.ID).Order("created_at ASC").First(&first).Error == nil {
		params["bandwidth_mbps"] = first.BandwidthLimit
	}
	var nic models.VMNIC
	if m.quiet(ctx).Where("vm_id = ? AND position = 0", vm.ID).First(&nic).Error == nil {
		params["mac_address"] = nic.MACAddress
		if nic.Bridge != "" && bridge == "" {
			params["bridge"] = nic.Bridge
		}
	}
	if p, perr := vmIPv6Prefix(ctx, m.db, vm.ID); perr == nil && p != nil {
		params["ipv6_prefix"], params["ipv6_address"], params["ipv6_gateway"] = p.Prefix, p.Address(), models.IPv6LinkLocalGateway
	}
	if hw.Firmware == models.FirmwareUEFI {
		params["metadata"] = map[string]string{models.FirmwareKey: models.FirmwareUEFI}
	}
	if pool != nil && backup != nil {
		params["pool_path"] = pool.Path
		if models.IsLVMPoolType(pool.Type) {
			params["pool_type"] = pool.Type
		}
	}
	if restore != nil {
		restore.apply(params)
	}
	return json.Marshal(params)
}

// giveNewAddresses replaces, in one transaction, every address of the VM whose
// pool does not reach dst with one from dst's pools; the network card keeps
// its record, limits and forwards. A floating IP from such a pool is detached
// and stays its owner's; IPv6 from one is released. The guest is given the new
// addresses when the VM's network config is applied on dst.
func (m *MigrationService) giveNewAddresses(ctx context.Context, vm *models.VM, dst *models.Node) ([]string, error) {
	missing, err := vmPoolsNotOn(ctx, m.db, vm.ID, dst.ID, true)
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	if m.vms.ipamService == nil {
		return nil, errors.New("address allocation is not configured")
	}
	gone := map[string]missingPool{}
	for _, p := range missing {
		gone[p.ID] = p
	}
	var notes []string
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var addrs []models.IPAddress
		if err := tx.Where("vm_id = ?", vm.ID).Order("created_at, id").Find(&addrs).Error; err != nil {
			return err
		}
		var primary string
		tx.Raw("SELECT host(ip_address) FROM networks WHERE vm_id = ? AND deleted_at IS NULL ORDER BY created_at ASC LIMIT 1", vm.ID).Scan(&primary)
		for _, a := range addrs {
			lost, ok := gone[a.PoolID]
			if !ok {
				continue
			}
			old := hostOnlyIP(a.Address)
			switch {
			case a.DeliveryMode == models.IPDeliveryFloating:
				if err := tx.Model(&models.IPAddress{}).Where("id = ?", a.ID).Updates(map[string]interface{}{
					"vm_id": nil, "nat_mode": "", "status": models.IPAddressStatusReserved}).Error; err != nil {
					return err
				}
				notes = append(notes, fmt.Sprintf("Floating IP %s was detached: its pool does not reach node %s. It is still its owner's.", old, dst.Name))
				continue
			case a.Family == models.IPFamilyIPv6:
				if err := tx.Model(&models.IPAddress{}).Where("id = ?", a.ID).Updates(map[string]interface{}{
					"vm_id": nil, "status": models.IPAddressStatusAvailable}).Error; err != nil {
					return err
				}
				notes = append(notes, fmt.Sprintf("IPv6 address %s was released: its pool does not reach node %s.", old, dst.Name))
				continue
			}
			var got *models.IPAddress
			var from models.IPPool
			for _, p := range m.replacementPools(ctx, dst.ID, lost.NAT) {
				x, aerr := m.vms.ipamService.AllocateAddressInTx(ctx, tx, &AllocateIPAddressRequest{PoolID: p.ID, NodeID: &dst.ID, VMID: &vm.ID})
				if aerr == nil {
					got, from = x, p
					break
				}
				if !errors.Is(aerr, ErrNoAvailableIPAddress) && !errors.Is(aerr, ErrPoolNotAvailableOnNode) {
					return aerr
				}
			}
			if got == nil {
				return fmt.Errorf("no pool on node %s has a free address to replace %s", dst.Name, old)
			}
			if err := tx.Exec("UPDATE networks SET ip_address = ?, updated_at = NOW() WHERE vm_id = ? AND host(ip_address) = ? AND deleted_at IS NULL",
				got.Address, vm.ID, old).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.IPAddress{}).Where("id = ?", a.ID).Updates(map[string]interface{}{
				"vm_id": nil, "status": models.IPAddressStatusAvailable}).Error; err != nil {
				return err
			}
			if old == primary {
				// The card sits on the new pool's bridge.
				if err := tx.Session(&gorm.Session{Logger: gormlogger.Discard}).Model(&models.VMNIC{}).
					Where("vm_id = ? AND position = 0", vm.ID).Update("bridge", from.Bridge).Error; err != nil {
					m.logger.WarnContext(ctx, "could not record the recovered VM's new bridge", "vm_id", vm.ID, "error", err)
				}
			}
			notes = append(notes, fmt.Sprintf("Address %s was replaced by %s from pool %s.", old, hostOnlyIP(got.Address), from.Name))
		}
		if p, perr := vmIPv6Prefix(ctx, tx, vm.ID); perr == nil && p != nil {
			if _, ok := gone[p.PoolID]; ok {
				if err := m.vms.ipamService.ReleaseIPv6PrefixByVMInTx(ctx, tx, vm.ID); err != nil {
					return err
				}
				notes = append(notes, fmt.Sprintf("IPv6 prefix %s was released: its pool does not reach node %s.", p.Prefix, dst.Name))
			}
		}
		return nil
	})
	return notes, err
}

// moveRecovered points the VM's records at dst once it is defined there, and
// from then on the failed node's copy is one to fence.
func (m *MigrationService) moveRecovered(ctx context.Context, row *models.VMRecovery, vm *models.VM, src, dst *models.Node, pool *models.StoragePool) error {
	ctx = context.WithoutCancel(ctx)
	info, err := m.migrationInfo(ctx, dst, &pb.GetMigrationInfoRequest{VmId: vm.ID})
	if err != nil {
		return fmt.Errorf("node %s could not list the VM's disks: %w", dst.Name, err)
	}
	if !info.VmFound {
		return fmt.Errorf("node %s does not list the VM", dst.Name)
	}
	paths := map[string]string{}
	for _, d := range info.Disks {
		if d.Device != "cdrom" {
			paths[d.Target] = d.Path
		}
	}
	status := models.VMStatusStopped
	if info.VmState == "running" {
		status = models.VMStatusRunning
	}
	if vm.Suspended {
		status = models.VMStatusSuspended
	}
	poolID := ""
	if pool != nil {
		poolID = pool.ID
	}
	note, nat, err := m.moveVMRecords(ctx, recordsMove{vm: vm, src: src, dst: dst, poolID: poolID, status: status, paths: paths, note: row.Note,
		record: func(tx *gorm.DB, note string) error {
			// What the new definition does not have: dedicated cores, a rescue
			// disk, a disc in the drive, the old node's snapshots, and data
			// disks that were not in the backup.
			if vm.Hardware.DedicatedCPU {
				hw := vm.Hardware
				hw.DedicatedCPU, hw.PinnedCPUs = false, ""
				if err := writeVMHardware(tx, vm.ID, hw); err != nil {
					return err
				}
			}
			if err := tx.Table("vms").Where("id = ?", vm.ID).Updates(map[string]interface{}{"rescue_mode": false, "rescue_kind": "", "attached_iso": ""}).Error; err != nil {
				return err
			}
			if err := tx.Where("vm_id = ?", vm.ID).Delete(&models.Snapshot{}).Error; err != nil {
				return err
			}
			var disks []models.VMDisk
			if err := tx.Where("vm_id = ?", vm.ID).Find(&disks).Error; err != nil {
				return err
			}
			for _, d := range disks {
				if paths[d.Device] == "" {
					if err := tx.Unscoped().Where("id = ?", d.ID).Delete(&models.VMDisk{}).Error; err != nil {
						return err
					}
					note = strings.TrimSpace(note + " " + fmt.Sprintf("Data disk %s was not in the backup, so the VM does not have it on node %s; it is still on node %s.", d.Device, dst.Name, src.Name))
				}
			}
			return tx.Model(&models.VMRecovery{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
				"moved": true, "old_copy": models.OldCopyPending, "phase": recApplying, "note": note, "updated_at": time.Now()}).Error
		}})
	if err != nil {
		return err
	}
	row.Moved, row.OldCopy = true, models.OldCopyPending
	// The record callback's note is the one stored; read it back.
	m.db.WithContext(ctx).Table("vm_recoveries").Select("note").Where("id = ?", row.ID).Scan(&note)
	row.Note = note
	if nat != nil {
		m.tellNATMove(ctx, vm, dst, nat)
	}
	return nil
}

// afterRecovered puts what lives on a node back for the VM on its new node and
// starts it when asked. The VM is recovered whatever happens here; what did
// not work is noted.
func (m *MigrationService) afterRecovered(ctx context.Context, row *models.VMRecovery, vm *models.VM, src, dst *models.Node) error {
	ctx = context.WithoutCancel(ctx)
	if !row.Moved {
		// The records moved but the try that moved them did not get to say so.
		m.db.WithContext(ctx).Model(&models.VMRecovery{}).Where("id = ?", row.ID).Updates(map[string]interface{}{"moved": true, "old_copy": models.OldCopyPending})
		row.Moved, row.OldCopy = true, models.OldCopyPending
	}
	m.setRecoveryPhase(ctx, row, recApplying)
	fresh, err := m.vms.vmRepo.GetByID(ctx, vm.ID)
	if err != nil {
		return fmt.Errorf("%w: %v", errInterrupted, err)
	}
	var problems []string
	client, actx, aerr := m.agent(ctx, dst)

	m.replayCards(ctx, fresh, &problems)

	// Host rules, and for a VM with new addresses the guest's own settings.
	var params []byte
	ok := false
	if row.NewAddress {
		if primary, pool, perr := m.network.primaryNetworkWithPool(ctx, fresh.ID); perr == nil {
			params, err = m.network.networkConfigParams(ctx, fresh, primary, pool)
			ok = err == nil
		}
	}
	if !ok {
		params, ok, err = m.network.NetworkConfigParams(ctx, fresh)
	}
	switch {
	case err != nil:
		problems = append(problems, "network config could not be built: "+err.Error())
	case ok:
		if aerr == nil {
			cctx, cancel := context.WithTimeout(actx, 5*time.Minute)
			aerr = queue.ApplyVMNetworkConfig(cctx, client, dst, fresh, params)
			cancel()
		}
		if aerr != nil {
			problems = append(problems, "network rules not applied yet ("+aerr.Error()+"); queued again")
			_ = m.network.enqueueNetworkResync(ctx, fresh)
		}
	}
	m.vms.ReconcileFloatingIPs(ctx, dst.ID)
	m.vms.applyConsoleAccess(ctx, fresh, fresh.ConsoleEnabled)
	if res, err := m.vms.ApplyVMLimits(ctx, fresh.ID); err == nil && res.Note != nil {
		problems = append(problems, "limits: "+*res.Note)
	}

	if row.Method == models.RecoveryShared && row.StartVM && !fresh.Suspended {
		m.setRecoveryPhase(ctx, row, recStarting)
		if err := m.startRecovered(ctx, fresh, dst); err != nil {
			problems = append(problems, "the VM is defined but did not start: "+err.Error())
		} else if err := m.vms.vmRepo.UpdateStatus(ctx, fresh.ID, models.VMStatusRunning); err != nil {
			m.logger.WarnContext(ctx, "could not record a recovered VM as running", "vm_id", fresh.ID, "error", err)
		}
	}
	if len(problems) > 0 {
		m.noteRecovery(ctx, row, strings.Join(problems, "; ")+".")
	}
	m.finishRecovery(ctx, row, models.RecoveryCompleted, "")
	m.alerts.Clear(ctx, "recovery_failed:"+vm.ID)
	details := map[string]any{"hostname": fresh.Hostname, "from_node": src.Name, "to_node": dst.Name, "method": row.Method, "new_address": row.NewAddress}
	if row.BackupID != nil {
		details["backup_id"] = *row.BackupID
	}
	if row.Note != "" {
		details["note"] = row.Note
	}
	m.audit(ctx, ptrString(row.RequestedBy), "", "", "vm.recover", "vm", vm.ID, details)
	m.logger.InfoContext(ctx, "VM recovered from a failed node", "vm_id", vm.ID, "from", src.Name, "to", dst.Name, "method", row.Method)
	return nil
}

// replayCards gives the new definition the VM's further network cards: it was
// made with the first one only.
func (m *MigrationService) replayCards(ctx context.Context, vm *models.VM, problems *[]string) {
	var cards []models.VMNIC
	if m.quiet(ctx).Where("vm_id = ? AND position > 0", vm.ID).Order("position").Find(&cards).Error != nil || len(cards) == 0 {
		return
	}
	client, actx, err := m.vms.AgentFor(ctx, vm.NodeID)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("its %d further network cards were not added (%v); add them from the VM's page", len(cards), err))
		return
	}
	bridges := m.network.vmIPBridges(ctx, vm.ID)
	for _, c := range cards {
		req := &pb.ConfigureNICRequest{VmId: vm.ID, Action: pb.NICAction_NIC_ACTION_ATTACH, MacAddress: c.MACAddress, Bridge: c.Bridge}
		for ip, br := range bridges {
			if br == c.Bridge {
				req.IpAddress, req.AntiSpoofing = ip, true
				break
			}
		}
		cctx, cancel := context.WithTimeout(actx, 90*time.Second)
		resp, err := client.ConfigureNIC(cctx, req)
		cancel()
		if err == nil && !resp.GetSuccess() {
			err = errors.New(agentMessage(resp.GetError(), "refused"))
		}
		if err != nil && !strings.Contains(err.Error(), "already") {
			*problems = append(*problems, fmt.Sprintf("network card %s was not added (%v); add it from the VM's page", c.MACAddress, err))
		}
	}
}

func (m *MigrationService) startRecovered(ctx context.Context, vm *models.VM, dst *models.Node) error {
	client, actx, err := m.agent(ctx, dst)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(actx, 2*time.Minute)
	defer cancel()
	resp, err := client.ExecuteVMCommand(cctx, &pb.VMCommandRequest{VmId: vm.ID, Command: pb.VMCommandType_VM_COMMAND_TYPE_START})
	if err != nil {
		return err
	}
	if !resp.GetSuccess() {
		return errors.New(agentMessage(resp.GetError(), resp.GetMessage()))
	}
	return nil
}

// RecoveryLeftover is what a node still holds of a VM recovered elsewhere.
type RecoveryLeftover struct {
	models.VMRecovery
	Hostname     string           `json:"hostname"`
	DestNodeName string           `json:"dest_node_name"`
	Defined      bool             `json:"defined"`
	State        string           `json:"state"`
	Disks        []*pb.FencedDisk `json:"disks"`
}

// fenceRows are the newest recovery of each VM recovered from a node whose
// copy there is not known to be gone, and that the panel has on another node.
func (m *MigrationService) fenceRows(ctx context.Context, nodeID string) ([]models.VMRecovery, error) {
	var rows []models.VMRecovery
	if err := m.db.WithContext(ctx).Where("source_node_id = ? AND moved AND old_copy IN ?", nodeID,
		[]string{models.OldCopyPending, models.OldCopyFenced, models.OldCopyRunning}).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := rows[:0:0]
	seen := map[string]bool{}
	for _, r := range rows {
		var now string
		m.db.WithContext(ctx).Raw("SELECT node_id FROM vms WHERE id = ? AND deleted_at IS NULL", r.VMID).Scan(&now)
		if seen[r.VMID] || now == nodeID {
			// An older recovery of the same VM, or the VM lives here again:
			// there is no copy to fence under this row.
			m.db.WithContext(ctx).Model(&models.VMRecovery{}).Where("id = ?", r.ID).Update("old_copy", models.OldCopyRemoved)
			continue
		}
		seen[r.VMID] = true
		out = append(out, r)
	}
	return out, nil
}

// FenceNode tells a node which VMs were recovered onto other nodes while it
// was down: its agent turns their autostart off and never starts them. It
// records what the node still holds of each, and raises a critical alert for
// one found running; it stops nothing. The node must be reachable.
func (m *MigrationService) FenceNode(ctx context.Context, nodeID string) ([]RecoveryLeftover, error) {
	node, err := m.vms.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	rows, err := m.fenceRows(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := []RecoveryLeftover{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.VMID
	}
	unfenced := "recovery_unfenced:" + node.ID
	client, actx, err := m.agent(ctx, node)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, time.Minute)
	resp, err := client.SetFencedVMs(cctx, &pb.SetFencedVMsRequest{VmIds: ids})
	cancel()
	if err == nil && !resp.GetSuccess() {
		err = errors.New(agentMessage(resp.GetError(), "refused"))
	}
	if err != nil {
		if unimplemented(err) {
			err = fmt.Errorf("node %s needs an agent update before it can be told not to run the VMs recovered from it", node.Name)
			m.alerts.Raise(ctx, AlertInput{Key: unfenced, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityCritical,
				Title:        "Node " + node.Name + " is back and may run VMs that were recovered elsewhere",
				Detail:       fmt.Sprintf("%d VMs were recovered onto other nodes while it was down. Its agent is too old to be told, so update the agent now and check that none of them runs there: a VM running twice destroys its disk or its address.", len(ids)),
				ResourceType: "node", ResourceID: node.ID, ResourceName: node.Name})
		}
		return nil, err
	}
	m.alerts.Clear(ctx, unfenced)
	byVM := map[string]*pb.FencedVM{}
	for _, v := range resp.GetVms() {
		byVM[v.GetVmId()] = v
	}
	for _, r := range rows {
		v := byVM[r.VMID]
		if v == nil {
			continue
		}
		left := RecoveryLeftover{VMRecovery: r, Defined: v.GetDefined(), State: v.GetState(), Disks: v.GetDisks()}
		m.db.WithContext(ctx).Raw("SELECT hostname FROM vms WHERE id = ?", r.VMID).Scan(&left.Hostname)
		m.db.WithContext(ctx).Raw("SELECT name FROM nodes WHERE id = ?", r.DestNodeID).Scan(&left.DestNodeName)
		name := left.Hostname
		if name == "" {
			name = r.VMID
		}
		key := "recovered_vm_running:" + r.VMID
		state := models.OldCopyFenced
		switch {
		case !v.GetDefined():
			state = models.OldCopyRemoved
			m.alerts.Clear(ctx, key)
		case v.GetState() != "shutoff":
			state = models.OldCopyRunning
			m.alerts.Raise(ctx, AlertInput{Key: key, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityCritical,
				Title: name + " runs on " + node.Name + " although it was recovered onto " + left.DestNodeName,
				Detail: fmt.Sprintf("Node %s is back and its old copy of the VM is %s there. The panel did not stop it. Two copies with the same address, or on the same disk, must not run: "+
					"decide which one to keep, and power the old copy off or remove it under Recovery leftovers on node %s's page.", node.Name, v.GetState(), node.Name),
				ResourceType: "vm", ResourceID: r.VMID, ResourceName: name})
		default:
			m.alerts.Clear(ctx, key)
		}
		if r.OldCopy == models.OldCopyPending {
			// Told for the first time: its floating IPs answer on its new node,
			// so this one stops holding them.
			m.vms.detachFloatingIPsOnAgentForVM(ctx, r.VMID, node.ID)
		}
		if state != r.OldCopy {
			m.db.WithContext(ctx).Model(&models.VMRecovery{}).Where("id = ?", r.ID).Updates(map[string]interface{}{"old_copy": state, "updated_at": time.Now()})
			left.OldCopy = state
		}
		if state != models.OldCopyRemoved {
			out = append(out, left)
		}
	}
	return out, nil
}

// NodeReported is called when a node answers a check. While the node holds
// copies it has not been told about, or one that was found running, it is
// told again: this is how a node that comes back learns what moved away.
func (m *MigrationService) NodeReported(ctx context.Context, nodeID string) {
	var n int64
	if m.quiet(ctx).Model(&models.VMRecovery{}).Where("source_node_id = ? AND moved AND old_copy IN ?", nodeID,
		[]string{models.OldCopyPending, models.OldCopyRunning}).Count(&n).Error != nil || n == 0 {
		return
	}
	if _, busy := m.fencing.LoadOrStore(nodeID, true); busy {
		return
	}
	go func() {
		defer m.fencing.Delete(nodeID)
		if _, err := m.FenceNode(context.WithoutCancel(ctx), nodeID); err != nil {
			m.logger.WarnContext(ctx, "a returned node could not be told which VMs were recovered elsewhere; trying again at its next check", "node_id", nodeID, "error", err)
		}
	}()
}

// RemoveLeftover removes a node's old copy of a VM that was recovered onto
// another node: its definition there, and its disks unless they are on shared
// storage. A copy that runs is removed only with forceStop.
func (m *MigrationService) RemoveLeftover(ctx context.Context, nodeID, vmID string, forceStop bool, actorID, ip, userAgent string) (*pb.RemoveFencedVMResponse, error) {
	node, err := m.vms.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	rows, err := m.fenceRows(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	var row *models.VMRecovery
	for i := range rows {
		if rows[i].VMID == vmID {
			row = &rows[i]
		}
	}
	if row == nil {
		return nil, ErrNoLeftover
	}
	// The node must hold the list before it removes by it.
	if _, err := m.FenceNode(ctx, nodeID); err != nil {
		return nil, err
	}
	client, actx, err := m.agent(ctx, node)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, 10*time.Minute)
	defer cancel()
	resp, err := client.RemoveFencedVM(cctx, &pb.RemoveFencedVMRequest{VmId: vmID, ForceStop: forceStop, LvmPools: queue.NodeLVMPools(ctx, m.db, nodeID)})
	if err != nil {
		return nil, fmt.Errorf("node %s did not answer: %w", node.Name, err)
	}
	if !resp.GetSuccess() {
		return resp, refuseRecovery("node %s: %s", node.Name, agentMessage(resp.GetError(), "refused"))
	}
	m.db.WithContext(ctx).Model(&models.VMRecovery{}).Where("id = ?", row.ID).Updates(map[string]interface{}{"old_copy": models.OldCopyRemoved, "updated_at": time.Now()})
	m.alerts.Clear(ctx, "recovered_vm_running:"+vmID)
	m.audit(ctx, actorID, ip, userAgent, "vm.recovery_leftover_removed", "vm", vmID, map[string]any{
		"node": node.Name, "force_stop": forceStop, "removed": resp.GetRemoved(), "kept": resp.GetKept(), "method": row.Method})
	return resp, nil
}
