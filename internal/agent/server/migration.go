package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/maburvm/panel/internal/agent/hwprofile"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/storage"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// migrations are the moves this agent runs: live moves out (as the source)
// and offline copies in (as the destination).
var migrations migration.Registry

func migrationLayout() migration.Layout { return migration.DefaultLayout(agentDataDir()) }

// forcedCommand is what a peer's key runs here: this binary's migration shell.
func forcedCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe + " " + migration.ShellCommand, nil
}

func socketExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

func ncHasQ(path string) bool {
	out, _ := exec.Command(path, "-h").CombinedOutput()
	return strings.Contains(string(out), "-q")
}

func migErr(code pb.ErrorCode, err error) *pb.ErrorResponse {
	return &pb.ErrorResponse{Code: code, Message: err.Error()}
}

func requireAuth(ctx context.Context) error {
	if _, ok := GetNodeIDFromContext(ctx); !ok {
		return status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	return nil
}

// GetMigrationInfo describes this node, and optionally a VM and the link to a peer.
func (s *NodeAgentService) GetMigrationInfo(ctx context.Context, req *pb.GetMigrationInfoRequest) (*pb.GetMigrationInfoResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	l := migrationLayout()
	host, _ := os.Hostname()
	pub, err := migration.EnsureKey(l, "maburvm-migration@"+host)
	if err != nil {
		return &pb.GetMigrationInfoResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
	}
	resp := &pb.GetMigrationInfoResponse{
		Success:            true,
		MigrationPublicKey: pub,
		SshHostKeys:        migration.HostKeys(l.HostKeyGlob),
		LibvirtVersion:     libvirt.LibvirtVersion(),
	}
	_, resp.SshProxy = migration.ProxyCommand(exec.LookPath, socketExists, ncHasQ)
	resp.CpuVendor, resp.CpuModel, resp.CpuFeatures, _ = libvirt.HostCPU()

	if req.VmId != "" {
		info, err := libvirt.MigrationInfo(req.VmId, req.IncludePayload)
		switch {
		case err == nil:
			resp.VmFound = true
			resp.VmState, resp.VmCpuMode, resp.VmCpuXml = info.State, info.CPUMode, info.CPUXML
			resp.SnapshotCount, resp.BackupRunning, resp.Bridges = int32(info.Snapshots), info.BackupRunning, info.Bridges
			resp.SeedImage, resp.DomainXml = info.SeedImage, info.DomainXML
			for _, d := range info.Drives {
				resp.Disks = append(resp.Disks, &pb.MigrationDiskInfo{
					Target: d.Target, Device: d.Device, Path: d.Path, Format: d.Format, SourceType: d.SourceType,
					VirtualSizeBytes: d.VirtualSize, FileSizeBytes: d.FileSize, BackingFile: d.BackingFile,
					Readonly: d.ReadOnly, Seed: d.Seed,
				})
			}
		case libvirt.IsNoDomain(err):
		default:
			return &pb.GetMigrationInfoResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
		}
	}

	if req.PeerAddress != "" {
		err := testLink(l, req.PeerAddress)
		resp.LinkOk = err == nil
		if err != nil {
			resp.LinkError = err.Error()
		}
	}
	return resp, nil
}

// testLink opens a libvirt connection to a peer over the migration link.
func testLink(l migration.Layout, address string) error {
	uri, err := migration.DestURI(l, address)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- libvirt.TestRemote(uri) }()
	select {
	case err := <-done:
		return err
	case <-time.After(45 * time.Second):
		return fmt.Errorf("no answer from %s over the migration link in 45 seconds", address)
	}
}

// SetMigrationPeers replaces the nodes this node trusts for migration.
func (s *NodeAgentService) SetMigrationPeers(ctx context.Context, req *pb.SetMigrationPeersRequest) (*pb.SetMigrationPeersResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	cmd, err := forcedCommand()
	if err != nil {
		return &pb.SetMigrationPeersResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
	}
	peers := make([]migration.Peer, 0, len(req.Peers))
	for _, p := range req.Peers {
		peers = append(peers, migration.Peer{NodeID: p.NodeId, Address: p.Address, PublicKey: p.PublicKey, HostKeys: p.HostKeys})
	}
	if err := migration.ApplyPeers(migrationLayout(), peers, cmd); err != nil {
		return &pb.SetMigrationPeersResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)}, nil
	}
	log.Printf("[Migration] trusted peers set: %d", len(peers))
	return &pb.SetMigrationPeersResponse{Success: true}, nil
}

func bridgeExists(name string) bool {
	if name == "" || strings.ContainsAny(name, "/.") {
		return false
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", name, "bridge"))
	return err == nil
}

func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// PrepareMigrationTarget checks, or prepares, this node to receive a VM.
func (s *NodeAgentService) PrepareMigrationTarget(ctx context.Context, req *pb.PrepareMigrationTargetRequest) (*pb.PrepareMigrationTargetResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(err error) (*pb.PrepareMigrationTargetResponse, error) {
		return &pb.PrepareMigrationTargetResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)}, nil
	}
	if !migration.ValidUUID(req.VmId) || (!req.DryRun && req.MigrationId == "") {
		return fail(fmt.Errorf("a VM id and a migration id are required"))
	}
	l := migrationLayout()
	prev, havePrev := migration.LoadPrepared(l, req.VmId)
	ours := map[string]bool{} // files this same migration prepared on an earlier try
	if havePrev && prev.MigrationID == req.MigrationId {
		for _, f := range prev.Files {
			ours[f] = true
		}
	}

	lvPool, onLVM, err := requestLVMPool(req.PoolType, req.PoolPath)
	if err != nil {
		return fail(err)
	}
	var problems []string
	paths := map[string]string{}
	var planned []string
	var needLV int64
	for _, d := range req.Disks {
		if !migration.ValidTarget(d.Target) {
			return fail(fmt.Errorf("unusable drive %q", d.Target))
		}
		if onLVM {
			// A volume of the VM's own, named and tagged as MaburVM names and
			// tags the volumes it creates, so it is provably the VM's to remove.
			if d.VirtualSizeBytes <= 0 {
				return fail(fmt.Errorf("drive %s has no size to create its volume at", d.Target))
			}
			p := "/dev/" + lvPool.VG + "/" + models.LVMDiskName(req.VmId, d.Target)
			paths[d.Target] = p
			planned = append(planned, p)
			needLV += d.VirtualSizeBytes
			continue
		}
		p, err := migration.TargetPath(req.PoolPath, d.FileName)
		if err != nil {
			return fail(err)
		}
		paths[d.Target] = p
		planned = append(planned, p)
	}
	seedPath := ""
	if len(req.SeedImage) > 0 || req.SeedFileName != "" {
		// On an LVM pool the setup disc stays in the default directory, where
		// a create puts it too.
		seedDir := req.PoolPath
		if onLVM {
			seedDir = defaultImageDir
		}
		p, err := migration.TargetPath(seedDir, req.SeedFileName)
		if err != nil {
			return fail(err)
		}
		seedPath = p
		planned = append(planned, p)
	}

	if _, found, err := libvirt.DomainState(req.VmId); err != nil {
		problems = append(problems, "libvirt could not be asked about the VM: "+err.Error())
	} else if found {
		problems = append(problems, "the VM is already defined on this node")
	}
	if onLVM {
		problems = append(problems, lvmTargetProblems(ctx, lvPool, needLV, req.ScratchBytesNeeded)...)
	} else if fi, err := os.Stat(req.PoolPath); err != nil || !fi.IsDir() {
		problems = append(problems, fmt.Sprintf("the pool directory %s does not exist on this node", req.PoolPath))
	} else if req.FreeBytesNeeded > 0 {
		if free, err := freeBytes(req.PoolPath); err == nil && free < req.FreeBytesNeeded {
			problems = append(problems, fmt.Sprintf("%s has %d GB free, and the VM's disks need %d GB", req.PoolPath, free>>30, req.FreeBytesNeeded>>30))
		}
	}
	for _, p := range planned {
		if _, err := os.Lstat(p); err == nil && !ours[p] {
			problems = append(problems, p+" already exists on this node")
		}
	}
	for _, b := range req.Bridges {
		if !bridgeExists(b) {
			problems = append(problems, fmt.Sprintf("bridge %s does not exist on this node", b))
		}
	}
	if req.CpuXml != "" {
		if ok, err := libvirt.CPUCompatible(req.CpuXml); err != nil {
			problems = append(problems, "the VM's CPU model could not be checked against this node: "+err.Error())
		} else if !ok {
			problems = append(problems, "this node's CPU cannot run the VM's CPU model")
		}
	}
	if len(problems) > 0 {
		return &pb.PrepareMigrationTargetResponse{Problems: problems,
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_CONFLICT, Message: strings.Join(problems, "; ")}}, nil
	}
	resp := &pb.PrepareMigrationTargetResponse{Success: true, Paths: paths, SeedPath: seedPath}
	if req.DryRun {
		return resp, nil
	}

	// A record of an earlier migration that never cleaned up: its files are
	// ours to remove, as long as nothing uses them.
	if havePrev && prev.MigrationID != req.MigrationId {
		if inUse, err := libvirt.DiskSourceFiles(); err == nil {
			if old, err := migration.Removable(prev, prev.MigrationID, inUse); err == nil {
				for _, f := range old {
					_ = os.Remove(f)
				}
				if removePreparedVolumes(ctx, prev) == nil {
					migration.ClearPrepared(l, req.VmId)
				}
			}
		}
	}
	// Recorded before anything is created, so a crash halfway still leaves
	// the files traceable to this migration.
	rec := migration.Prepared{VMID: req.VmId, MigrationID: req.MigrationId, Files: planned}
	if onLVM {
		rec.PoolType, rec.PoolPath = req.PoolType, req.PoolPath
	}
	if err := migration.SavePrepared(l, rec); err != nil {
		return &pb.PrepareMigrationTargetResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
	}
	if onLVM {
		// An offline copy writes into the volumes too, so both kinds of move
		// have them made here.
		for _, d := range req.Disks {
			if _, err := os.Stat(paths[d.Target]); err == nil && ours[paths[d.Target]] {
				continue
			}
			if _, err := createDiskLV(ctx, lvPool, models.LVMDiskName(req.VmId, d.Target), d.VirtualSizeBytes); err != nil {
				return &pb.PrepareMigrationTargetResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, fmt.Errorf("could not create the volume for %s: %w", d.Target, err))}, nil
			}
		}
	} else if req.CreateDisks {
		for _, d := range req.Disks {
			// An earlier try may have recorded a file and stopped before making it.
			if _, err := os.Lstat(paths[d.Target]); err == nil && ours[paths[d.Target]] {
				continue
			}
			if err := migration.CreateImage(ctx, paths[d.Target], d.Format, d.VirtualSizeBytes); err != nil {
				return &pb.PrepareMigrationTargetResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
			}
		}
	}
	if seedPath != "" {
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if ours[seedPath] {
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC // this migration's own, maybe half written
		}
		f, err := os.OpenFile(seedPath, flags, 0o644)
		if err == nil {
			_, err = f.Write(req.SeedImage)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			return &pb.PrepareMigrationTargetResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, fmt.Errorf("failed to write the seed image: %w", err))}, nil
		}
	}
	log.Printf("[Migration] prepared %s for VM %s (migration %s)", req.PoolPath, req.VmId, req.MigrationId)
	return resp, nil
}

func diskMaps(in []*pb.MigrationDiskMap) (dests map[string]migration.Dest, err error) {
	dests = map[string]migration.Dest{}
	for _, d := range in {
		if !migration.ValidTarget(d.Target) || !filepath.IsAbs(d.DestPath) || filepath.Clean(d.DestPath) != d.DestPath {
			return nil, fmt.Errorf("unusable drive mapping %q -> %q", d.Target, d.DestPath)
		}
		if f := d.DestFormat; f != "" && f != "raw" && f != "qcow2" {
			return nil, fmt.Errorf("unusable format %q for drive %s", f, d.Target)
		}
		dests[d.Target] = migration.Dest{Path: d.DestPath, Format: d.DestFormat}
	}
	return dests, nil
}

// MigrateVM starts a live move of a VM from this node over the migration link.
func (s *NodeAgentService) MigrateVM(ctx context.Context, req *pb.MigrateVMRequest) (*pb.MigrateVMResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(code pb.ErrorCode, err error) (*pb.MigrateVMResponse, error) {
		return &pb.MigrateVMResponse{Message: err.Error(), Error: migErr(code, err)}, nil
	}
	if req.DestAddress == "" {
		// The old form, root SSH with host keys unchecked, is gone.
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, errors.New("this agent moves VMs only over the migration link the panel sets up; update the panel"))
	}
	if !migration.ValidUUID(req.VmId) || req.MigrationId == "" || len(req.MigrationId) > 64 || net.ParseIP(req.DestAddress) == nil {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, errors.New("vm_id, migration_id and an IP dest_address are required"))
	}
	paths, err := diskMaps(req.Disks)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)
	}
	if migrations.Running(req.VmId, req.MigrationId) {
		return &pb.MigrateVMResponse{Success: true, Message: "already running"}, nil
	}
	l := migrationLayout()
	uri, err := migration.DestURI(l, req.DestAddress)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)
	}
	info, err := libvirt.MigrationInfo(req.VmId, false)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_NOT_FOUND, err)
	}
	if info.State != "running" && info.State != "paused" {
		return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, fmt.Errorf("the VM is %s here; a stopped VM is moved offline", info.State))
	}
	var copyDrives []migration.Drive
	for _, d := range req.Disks {
		if d.Copy {
			copyDrives = append(copyDrives, migration.Drive{Target: d.Target})
		}
	}
	for _, d := range info.Drives {
		if _, mapped := paths[d.Target]; d.Copied() && !mapped {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, fmt.Errorf("drive %s has no place on the destination", d.Target))
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	if !migrations.Start(req.VmId, req.MigrationId, "Copying memory and disks", cancel) {
		cancel()
		return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, errors.New("another migration of this VM is running"))
	}
	bw := uint64(0)
	if req.BandwidthMibS > 0 {
		bw = uint64(req.BandwidthMibS)
	}
	vmID, migID, dest := req.VmId, req.MigrationId, req.DestAddress
	go func() {
		defer cancel()
		log.Printf("[Migration] live move of %s to %s started (migration %s)", vmID, dest, migID)
		err := libvirt.LiveMigrate(runCtx, vmID, uri, migration.DataURI(dest), copyDrives, paths, bw, func(total, done int64) {
			migrations.Update(vmID, migID, "", total, done)
		})
		migrations.Finish(vmID, migID, err)
		log.Printf("[Migration] live move of %s to %s ended: %v", vmID, dest, err)
	}()
	return &pb.MigrateVMResponse{Success: true, Message: "started"}, nil
}

// PullMigratedVM starts copying a stopped VM's disks here from its node.
func (s *NodeAgentService) PullMigratedVM(ctx context.Context, req *pb.PullMigratedVMRequest) (*pb.PullMigratedVMResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(err error) (*pb.PullMigratedVMResponse, error) {
		return &pb.PullMigratedVMResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)}, nil
	}
	if !migration.ValidUUID(req.VmId) || req.MigrationId == "" || net.ParseIP(req.SourceAddress) == nil || req.DomainXml == "" {
		return fail(errors.New("vm_id, migration_id, an IP source_address and the VM's definition are required"))
	}
	paths, err := diskMaps(req.Disks)
	if err != nil {
		return fail(err)
	}
	// Only files this migration prepared are written to.
	prep, ok := migration.LoadPrepared(migrationLayout(), req.VmId)
	if !ok || prep.MigrationID != req.MigrationId {
		return fail(errors.New("this node was not prepared for this migration"))
	}
	allowed := map[string]bool{}
	for _, f := range prep.Files {
		allowed[f] = true
	}
	for _, p := range paths {
		if !allowed[p.Path] {
			return fail(fmt.Errorf("%s was not prepared for this migration", p.Path))
		}
	}
	if migrations.Running(req.VmId, req.MigrationId) {
		return &pb.PullMigratedVMResponse{Success: true}, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	if !migrations.Start(req.VmId, req.MigrationId, "Copying disks", cancel) {
		cancel()
		return fail(errors.New("another migration of this VM is running here"))
	}
	go func() {
		defer cancel()
		err := s.pullVM(runCtx, req, paths)
		migrations.Finish(req.VmId, req.MigrationId, err)
		log.Printf("[Migration] offline copy of %s from %s ended: %v", req.VmId, req.SourceAddress, err)
	}()
	return &pb.PullMigratedVMResponse{Success: true}, nil
}

func (s *NodeAgentService) pullVM(ctx context.Context, req *pb.PullMigratedVMRequest, dests map[string]migration.Dest) error {
	l := migrationLayout()
	prep, _ := migration.LoadPrepared(l, req.VmId)
	thin := prep.PoolType == "lvmthin"
	var total, done int64
	for _, d := range req.Disks {
		if d.Copy {
			total += d.ExpectedBytes
		}
	}
	for _, d := range req.Disks {
		if !d.Copy {
			continue
		}
		phase := "Copying disk " + d.Target
		migrations.Update(req.VmId, req.MigrationId, phase, total, done)
		progress := func(v int64) { migrations.Update(req.VmId, req.MigrationId, phase, total, done+v) }
		n, err := pullOneDisk(ctx, l, req.SourceAddress, req.VmId, d, thin, progress)
		if err != nil {
			return err
		}
		if d.ExpectedBytes > 0 && n != d.ExpectedBytes {
			return fmt.Errorf("disk %s: copied %d of %d bytes", d.Target, n, d.ExpectedBytes)
		}
		done += n
	}
	migrations.Update(req.VmId, req.MigrationId, "Defining the VM", total, done)
	domXML, err := migration.RewriteDisks(req.DomainXml, dests)
	if err != nil {
		return err
	}
	if domXML, err = hwprofile.LocalFirmware(domXML, hwprofile.Exists); err != nil {
		return err
	}
	return libvirt.DefineMigrated(domXML)
}

// pullOneDisk brings one drive of a stopped VM here: into a file as it is,
// or into a logical volume made ready for it. A volume holds raw data, so a
// qcow2 image is fetched beside it first and written onto it.
func pullOneDisk(ctx context.Context, l migration.Layout, address, vmID string, d *pb.MigrationDiskMap, thin bool, progress func(int64)) (int64, error) {
	if !models.IsBlockDevicePath(d.DestPath) {
		n, err := pullDisk(ctx, l, address, vmID, d.Target, d.DestPath, false, progress)
		if err != nil {
			return n, err
		}
		if d.DestFormat != "raw" {
			if _, err := storage.NewQCOW2Manager().ImageInfo(d.DestPath); err != nil {
				return n, fmt.Errorf("disk %s arrived unreadable: %w", d.Target, err)
			}
		}
		return n, nil
	}
	if d.SourceFormat == "" || d.SourceFormat == "raw" {
		// Raw onto the volume. A new thin volume reads as zeroes, so its holes
		// may be skipped; a thick one may hold an earlier volume's data.
		return pullDisk(ctx, l, address, vmID, d.Target, d.DestPath, !thin, progress)
	}
	if err := os.MkdirAll(blockScratchDir(), 0o755); err != nil {
		return 0, err
	}
	scratch := filepath.Join(blockScratchDir(), vmID+"-"+d.Target+".migrate.part")
	defer os.Remove(scratch)
	n, err := pullDisk(ctx, l, address, vmID, d.Target, scratch, false, progress)
	if err != nil {
		return n, err
	}
	if err := lvm.WriteImage(ctx, scratch, d.SourceFormat, d.DestPath, thin); err != nil {
		return n, fmt.Errorf("disk %s could not be written onto %s: %w", d.Target, d.DestPath, err)
	}
	return n, nil
}

// pullDisk copies one drive of a stopped VM on a peer into dest over the
// migration link and returns the bytes copied. A file is created and keeps
// its holes; a device (a volume made ready for it) is written in place, every
// block of it when dense.
func pullDisk(ctx context.Context, l migration.Layout, address, vmID, target, dest string, dense bool, progress func(int64)) (int64, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if models.IsBlockDevicePath(dest) {
		flags = os.O_WRONLY
	}
	f, err := os.OpenFile(dest, flags, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "ssh", migration.SSHArgs(l, address, migration.ReadDiskRemote(vmID, target))...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	copyFn := migration.CopySparse
	if dense {
		copyFn = migration.CopyAll
	}
	n, cerr := copyFn(f, out, 1<<20, progress)
	if cerr != nil {
		_, _ = io.Copy(io.Discard, out)
	}
	werr := cmd.Wait()
	if cerr == nil {
		cerr = f.Sync()
	}
	if werr != nil || cerr != nil {
		return n, fmt.Errorf("copying disk %s from %s: %v %v (%s)", target, address, werr, cerr, strings.TrimSpace(stderr.String()))
	}
	return n, nil
}

// GetMigrationProgress reports a migration this node runs.
func (s *NodeAgentService) GetMigrationProgress(ctx context.Context, req *pb.GetMigrationProgressRequest) (*pb.GetMigrationProgressResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	p := migrations.Get(req.VmId, req.MigrationId)
	return &pb.GetMigrationProgressResponse{State: p.State, Phase: p.Phase, TotalBytes: p.Total, ProcessedBytes: p.Processed, Error: p.Error}, nil
}

// AbortMigration stops a migration this node runs.
func (s *NodeAgentService) AbortMigration(ctx context.Context, req *pb.AbortMigrationRequest) (*pb.AbortMigrationResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	if migrations.Abort(req.VmId, req.MigrationId) {
		log.Printf("[Migration] abort requested for %s (migration %s)", req.VmId, req.MigrationId)
	}
	return &pb.AbortMigrationResponse{Success: true}, nil
}

// FinishMigrationTarget confirms an arrived VM, or rolls back what a failed
// migration prepared here.
func (s *NodeAgentService) FinishMigrationTarget(ctx context.Context, req *pb.FinishMigrationTargetRequest) (*pb.FinishMigrationTargetResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(err error) (*pb.FinishMigrationTargetResponse, error) {
		return &pb.FinishMigrationTargetResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_CONFLICT, err)}, nil
	}
	if !migration.ValidUUID(req.VmId) {
		return fail(errors.New("a VM id is required"))
	}
	l := migrationLayout()
	if req.Commit {
		state, err := libvirt.ConfirmArrived(req.VmId)
		if err != nil {
			return fail(fmt.Errorf("the VM is not here: %w", err))
		}
		migration.ClearPrepared(l, req.VmId)
		return &pb.FinishMigrationTargetResponse{Success: true, VmState: state}, nil
	}
	if migrations.Running(req.VmId, req.MigrationId) {
		return fail(errors.New("the copy is still running; abort it first"))
	}
	prep, ok := migration.LoadPrepared(l, req.VmId)
	mine := ok && prep.MigrationID == req.MigrationId
	if state, found, err := libvirt.DomainState(req.VmId); err != nil {
		return fail(err)
	} else if found {
		// Only a stopped VM this very migration defined here (an offline copy
		// whose source changed meanwhile) is taken back.
		if !mine || state != "shutoff" {
			return fail(fmt.Errorf("the VM is defined on this node (%s), so nothing was removed", state))
		}
		if err := libvirt.UndefineStopped(req.VmId); err != nil {
			return fail(fmt.Errorf("the copy defined here could not be removed: %w", err))
		}
	}
	if !mine {
		return &pb.FinishMigrationTargetResponse{Success: true}, nil
	}
	inUse, err := libvirt.DiskSourceFiles()
	if err != nil {
		return fail(err)
	}
	files, err := migration.Removable(prep, req.MigrationId, inUse)
	if err != nil {
		return fail(err)
	}
	var removed []string
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return &pb.FinishMigrationTargetResponse{Removed: removed, Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
		}
		removed = append(removed, f)
	}
	if err := removePreparedVolumes(ctx, prep); err != nil {
		return &pb.FinishMigrationTargetResponse{Removed: removed, Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
	}
	removed = append(removed, migration.Devices(prep)...)
	migration.ClearPrepared(l, req.VmId)
	log.Printf("[Migration] rolled back migration %s of %s: removed %v", req.MigrationId, req.VmId, removed)
	return &pb.FinishMigrationTargetResponse{Success: true, Removed: removed}, nil
}

// FinishMigrationSource removes a VM that has moved to another node.
func (s *NodeAgentService) FinishMigrationSource(ctx context.Context, req *pb.FinishMigrationSourceRequest) (*pb.FinishMigrationSourceResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	if !migration.ValidUUID(req.VmId) {
		return &pb.FinishMigrationSourceResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, errors.New("a VM id is required"))}, nil
	}
	// Block devices outlive the definition, which owns only files: those
	// MaburVM created for the VM go after it, others are kept for an operator.
	blockDisks, _ := libvirt.BackupDisks(req.VmId)
	removed, err := libvirt.RemoveMovedSource(req.VmId)
	if err != nil && !libvirt.IsNoDomain(err) {
		return &pb.FinishMigrationSourceResponse{Removed: removed, Error: migErr(pb.ErrorCode_ERROR_CODE_CONFLICT, err)}, nil
	}
	if err == nil {
		for _, k := range removeVMVolumes(ctx, req.VmId, blockDisks, lvmPoolsOf(req.GetLvmPools()), false) {
			log.Printf("[Migration] kept %s of moved VM %s: %s", k.Path, req.VmId, k.Reason)
		}
		for _, d := range blockDisks {
			if d.Block {
				if _, serr := os.Stat(d.Path); errors.Is(serr, os.ErrNotExist) {
					removed = append(removed, d.Path)
				}
			}
		}
	}
	if s.networkMgr != nil {
		if cerr := s.networkMgr.CleanupVMNetwork(req.VmId); cerr != nil {
			log.Printf("[Migration] WARNING: network cleanup of moved VM %s: %v", req.VmId, cerr)
		}
		s.networkMgr.ForgetVM(req.VmId)
		updateNetworkConfig(netConfigPath(), currentBootID(), req.VmId, nil)
		clearPendingGuest(req.VmId)
		_ = s.hostProxy.Set(req.VmId, nil)
	}
	log.Printf("[Migration] removed the old copy of %s (migration %s): %v", req.VmId, req.MigrationId, removed)
	return &pb.FinishMigrationSourceResponse{Success: true, Removed: removed}, nil
}

// RunMigrationShell is the forced command of a peer's key: a libvirt
// connection, or one disk of a stopped VM. It returns the exit code.
func RunMigrationShell(original string, stdout, stderr io.Writer) int {
	req, err := migration.ParseShellCommand(original)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if req.ReadDisk {
		path, err := libvirt.StoppedDrivePath(req.VMID, req.Target)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer f.Close()
		if fi, err := f.Stat(); err != nil || !(fi.Mode().IsRegular() || fi.Mode()&os.ModeDevice != 0) {
			fmt.Fprintln(stderr, path, "is not a plain file or a disk device")
			return 1
		}
		if _, err := io.Copy(stdout, f); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	cmd, _ := migration.ProxyCommand(exec.LookPath, socketExists, ncHasQ)
	if cmd == nil {
		fmt.Fprintln(stderr, "no libvirt proxy (virt-ssh-helper, socat or nc) is installed on this node")
		return 1
	}
	err = syscall.Exec(cmd[0], cmd, os.Environ())
	fmt.Fprintln(stderr, err)
	return 1
}

// lvmTargetProblems says why an LVM pool cannot take a VM's volumes of need
// bytes in all, and an offline copy its scratch room.
func lvmTargetProblems(ctx context.Context, pool storage.LVMPool, need, scratch int64) []string {
	var out []string
	if err := lvm.Check(ctx, pool); err != nil {
		return []string{fmt.Sprintf("the LVM pool %s cannot hold VM disks: %v", pool, err)}
	}
	if c, err := lvm.Capacity(ctx, pool); err == nil && c.Available < need {
		out = append(out, fmt.Sprintf("the LVM pool %s has %d GB free, and the VM's disks need %d GB", pool, c.Available>>30, need>>30))
	}
	if scratch > 0 {
		if err := os.MkdirAll(blockScratchDir(), 0o755); err == nil {
			if free, err := freeBytes(blockScratchDir()); err == nil && free < scratch+volumeReserveBytes {
				out = append(out, fmt.Sprintf("%s has %d GB free; an offline copy onto logical volumes fetches the images there first (%d GB, and %d GB kept free)",
					blockScratchDir(), free>>30, scratch>>30, volumeReserveBytes>>30))
			}
		}
	}
	return out
}

// removePreparedVolumes removes the logical volumes a migration made here,
// each only when it is provably the VM's: named and tagged for it, unused.
func removePreparedVolumes(ctx context.Context, p migration.Prepared) error {
	devs := migration.Devices(p)
	if len(devs) == 0 {
		return nil
	}
	pool, err := storage.ParseLVMPool(p.PoolType, p.PoolPath)
	if err != nil {
		return fmt.Errorf("the volumes this migration made were kept: %w", err)
	}
	for _, dev := range devs {
		if _, err := os.Stat(dev); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := removeLV(ctx, p.VMID, dev, []storage.LVMPool{pool}, false); err != nil {
			return err
		}
	}
	return nil
}
