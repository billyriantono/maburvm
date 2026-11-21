package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// ErrMigrationBlocked is a move the checks refused; the check says why.
	ErrMigrationBlocked = errors.New("this VM cannot be moved")
	// ErrMigrationActive is a second move of a VM that is already moving.
	ErrMigrationActive = errors.New("this VM is already being moved")
	// ErrNoActiveMigration is a cancel with nothing to cancel.
	ErrNoActiveMigration = errors.New("this VM is not being moved")
	// ErrVMMigrating refuses a change to a VM while it moves.
	ErrVMMigrating = errors.New("the VM is being moved to another node; try again when the move has finished")
)

// maxMovedSeed is the largest seed image carried through the panel.
const maxMovedSeed = 8 << 20

// MigrationService moves VMs between nodes and drains nodes. Every move runs
// as a background job with its record in vm_migrations; nothing is removed
// from the node a VM leaves until the VM has been seen on its new node.
type MigrationService struct {
	db      *gorm.DB
	vms     *VMService
	network *NetworkService
	alerts  *AlertService
	river   *river.Client[pgx.Tx]
	logger  *slog.Logger

	// poll is how often a running move is asked for progress.
	poll time.Duration
}

// NewMigrationService builds the service.
func NewMigrationService(db *gorm.DB, vms *VMService, network *NetworkService, alerts *AlertService, riverClient *river.Client[pgx.Tx], logger *slog.Logger) *MigrationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &MigrationService{db: db, vms: vms, network: network, alerts: alerts, river: riverClient, logger: logger, poll: 3 * time.Second}
}

// agent returns a client for a node's agent and a context carrying its credentials.
func (m *MigrationService) agent(ctx context.Context, node *models.Node) (pb.NodeAgentClient, context.Context, error) {
	client, err := m.vms.getAgentClient(ctx, node.ID)
	if err != nil {
		return nil, ctx, fmt.Errorf("node %s cannot be reached: %w", node.Name, err)
	}
	return client, metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+node.Token, "x-node-id", node.ID), nil
}

func unimplemented(err error) bool { return status.Code(err) == codes.Unimplemented }

func agentMessage(e *pb.ErrorResponse, fallback string) string {
	if e != nil && e.Message != "" {
		return e.Message
	}
	return fallback
}

// MigrationRequest asks to move a VM.
type MigrationRequest struct {
	VMID string `json:"-"`
	// DestNodeID empty picks the node the VM fits best.
	DestNodeID string `json:"dest_node_id"`
	// DestPoolID empty uses the destination's primary pool.
	DestPoolID string `json:"dest_pool_id"`
	// Mode is live, offline, or empty to follow the VM's state.
	Mode string `json:"mode"`
	// BandwidthMbps caps the copy; 0 is no cap.
	BandwidthMbps int    `json:"bandwidth_mbps"`
	RequestedBy   string `json:"-"`
}

// MigrationCheck is what a move would do, and what stops it.
type MigrationCheck struct {
	VMID           string   `json:"vm_id"`
	Hostname       string   `json:"hostname"`
	SourceNodeID   string   `json:"source_node_id"`
	SourceNodeName string   `json:"source_node_name"`
	DestNodeID     string   `json:"dest_node_id"`
	DestNodeName   string   `json:"dest_node_name"`
	DestPoolID     string   `json:"dest_pool_id"`
	DestPoolName   string   `json:"dest_pool_name"`
	DestPoolPath   string   `json:"dest_pool_path"`
	Mode           string   `json:"mode"`
	CPUMode        string   `json:"cpu_mode"`
	DiskGB         int      `json:"disk_gb"`
	CopyBytes      int64    `json:"copy_bytes"`
	Blockers       []string `json:"blockers"`
	Warnings       []string `json:"warnings"`
	CanMove        bool     `json:"can_move"`

	plan *migrationPlan
}

func (c *MigrationCheck) block(format string, args ...interface{}) {
	c.Blockers = append(c.Blockers, fmt.Sprintf(format, args...))
}

func (c *MigrationCheck) warn(format string, args ...interface{}) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

// planDisk is one disk or the seed image of a move.
type planDisk struct {
	Target   string
	Path     string
	FileName string
	Format   string
	Virtual  int64
	FileSize int64
}

type migrationPlan struct {
	vm      *models.VM
	src     *models.Node
	dst     *models.Node
	pool    *models.StoragePool
	mode    string
	cpuMode string
	cpuXML  string
	bridges []string
	disks   []planDisk
	seed    *planDisk
}

// checkEnv carries what a drain plan already promised to each node, and the
// node descriptions asked for so far, between the checks of one plan.
type checkEnv struct {
	info  map[string]*pb.GetMigrationInfoResponse
	infoE map[string]error
	extra map[string]*extraLoad
}

type extraLoad struct {
	cpu    int
	ramMB  int64
	poolGB map[string]int
}

func newCheckEnv() *checkEnv {
	return &checkEnv{info: map[string]*pb.GetMigrationInfoResponse{}, infoE: map[string]error{}, extra: map[string]*extraLoad{}}
}

func (e *checkEnv) add(nodeID, poolID string, vm *models.VM, diskGB int) {
	x := e.extra[nodeID]
	if x == nil {
		x = &extraLoad{poolGB: map[string]int{}}
		e.extra[nodeID] = x
	}
	x.cpu += vm.Resources.CPU
	x.ramMB += int64(vm.Resources.RAM)
	x.poolGB[poolID] += diskGB
}

// nodeInfo describes a node (no VM), once per check environment.
func (m *MigrationService) nodeInfo(ctx context.Context, node *models.Node, env *checkEnv) (*pb.GetMigrationInfoResponse, error) {
	if r, ok := env.info[node.ID]; ok {
		return r, env.infoE[node.ID]
	}
	r, err := m.migrationInfo(ctx, node, &pb.GetMigrationInfoRequest{})
	env.info[node.ID], env.infoE[node.ID] = r, err
	return r, err
}

func (m *MigrationService) migrationInfo(ctx context.Context, node *models.Node, req *pb.GetMigrationInfoRequest) (*pb.GetMigrationInfoResponse, error) {
	client, actx, err := m.agent(ctx, node)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, 90*time.Second)
	defer cancel()
	resp, err := client.GetMigrationInfo(cctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(agentMessage(resp.Error, "the node could not describe itself"))
	}
	m.recordIdentity(ctx, node.ID, resp)
	return resp, nil
}

// recordIdentity keeps a node's migration key and host keys, for the link and
// for the node's page.
func (m *MigrationService) recordIdentity(ctx context.Context, nodeID string, r *pb.GetMigrationInfoResponse) {
	if r.MigrationPublicKey == "" {
		return
	}
	if err := m.db.WithContext(ctx).Table("nodes").Where("id = ?", nodeID).Updates(map[string]interface{}{
		"migration_public_key":  r.MigrationPublicKey,
		"ssh_host_keys":         strings.Join(r.SshHostKeys, "\n"),
		"migration_identity_at": time.Now(),
	}).Error; err != nil {
		m.logger.WarnContext(ctx, "could not record a node's migration identity", "node_id", nodeID, "error", err)
	}
}

// activeMigration is the VM's move in progress, nil when there is none.
func (m *MigrationService) activeMigration(ctx context.Context, vmID string) (*models.VMMigration, error) {
	var rows []models.VMMigration
	if err := m.db.WithContext(ctx).Where("vm_id = ? AND status IN ?", vmID,
		[]string{models.MigrationQueued, models.MigrationRunning, models.MigrationCancelling}).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// VMMigrating reports that a VM is being moved, for the guards on everything
// else that changes a VM.
func VMMigrating(ctx context.Context, db *gorm.DB, vmID string) bool {
	if db == nil {
		return false
	}
	var n int64
	// Quiet: a test schema without the table has no migrations, and says so on every call.
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("vm_migrations").Where("vm_id = ? AND status IN ?", vmID,
		[]string{models.MigrationQueued, models.MigrationRunning, models.MigrationCancelling}).Count(&n).Error
	return err == nil && n > 0
}

// Check reports what moving a VM would do and what stops it, changing nothing.
func (m *MigrationService) Check(ctx context.Context, req MigrationRequest) (*MigrationCheck, error) {
	return m.check(ctx, req, newCheckEnv(), "")
}

// srcFacts is what the source side of a check found, shared by every
// destination tried.
type srcFacts struct {
	vm      *models.VM
	src     *models.Node
	info    *pb.GetMigrationInfoResponse
	mode    string
	diskGB  int
	disks   []planDisk
	seed    *planDisk
	blocked []string
	// ownMigration is the move doing this check, whose own prepared files
	// on the destination are not in the way.
	ownMigration string
}

func (m *MigrationService) check(ctx context.Context, req MigrationRequest, env *checkEnv, ownMigrationID string) (*MigrationCheck, error) {
	f, err := m.sourceFacts(ctx, req, ownMigrationID)
	if err != nil {
		return nil, err
	}
	base := MigrationCheck{
		VMID: f.vm.ID, Hostname: f.vm.Hostname, SourceNodeID: f.src.ID, SourceNodeName: f.src.Name,
		Mode: f.mode, DiskGB: f.diskGB, Blockers: []string{}, Warnings: []string{},
	}
	if f.info != nil {
		base.CPUMode = f.info.VmCpuMode
	}
	for _, d := range f.disks {
		base.CopyBytes += d.FileSize
	}

	var candidates []models.Node
	if req.DestNodeID != "" {
		dst, err := m.vms.nodeRepo.GetByID(ctx, req.DestNodeID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrNodeNotFound
			}
			return nil, err
		}
		candidates = []models.Node{*dst}
	} else if candidates, err = m.candidates(ctx, f, env); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		chk := base
		chk.Blockers = append(append([]string{}, f.blocked...), "no other node can take this VM: each is offline, paused for new VMs, or without room under its RAM, vCPU and storage limits")
		return &chk, nil
	}

	var first *MigrationCheck
	for i := range candidates {
		chk := base
		chk.Blockers = append([]string{}, f.blocked...)
		chk.Warnings = []string{}
		m.destinationChecks(ctx, f, &candidates[i], req, env, &chk)
		chk.CanMove = len(chk.Blockers) == 0
		if chk.CanMove {
			return &chk, nil
		}
		if first == nil {
			first = &chk
		}
		if req.DestNodeID != "" || len(f.blocked) > 0 {
			break // nothing a different destination changes
		}
	}
	return first, nil
}

// sourceFacts checks the VM and its own node.
func (m *MigrationService) sourceFacts(ctx context.Context, req MigrationRequest, ownMigrationID string) (*srcFacts, error) {
	vm, err := m.vms.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	src, err := m.vms.nodeRepo.GetByID(ctx, vm.NodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get the VM's node: %w", err)
	}
	f := &srcFacts{vm: vm, src: src, diskGB: vm.Resources.Disk, ownMigration: ownMigrationID}
	block := func(format string, args ...interface{}) { f.blocked = append(f.blocked, fmt.Sprintf(format, args...)) }

	switch vm.Status {
	case models.VMStatusCreating, models.VMStatusDeleting, models.VMStatusError:
		block("the VM is %s", vm.Status)
	}
	if active, err := m.activeMigration(ctx, vm.ID); err != nil {
		return nil, err
	} else if active != nil && active.ID != ownMigrationID {
		block("the VM is already being moved")
	}
	if src.Status == models.NodeStatusOffline {
		block("the VM's node %s is offline", src.Name)
	}
	if req.Mode != "" && req.Mode != models.MigrationLive && req.Mode != models.MigrationOffline {
		return nil, fmt.Errorf("%w: mode must be live or offline", ErrMigrationBlocked)
	}
	m.recordBlockers(ctx, vm, block)
	if b, err := natBlock(ctx, m.db, vm.ID); err != nil {
		return nil, fmt.Errorf("failed to read the VM's NAT ports: %w", err)
	} else if b != nil {
		block("the VM is a NAT VM: its private address and public ports %d-%d belong to node %s", b.StartPort, b.EndPort(), src.Name)
	}

	var extra int
	if err := m.db.WithContext(ctx).Model(&models.VMDisk{}).Where("vm_id = ?", vm.ID).
		Select("COALESCE(SUM(size_gb), 0)").Scan(&extra).Error; err != nil {
		return nil, fmt.Errorf("failed to read the VM's disks: %w", err)
	}
	f.diskGB += extra

	info, err := m.migrationInfo(ctx, src, &pb.GetMigrationInfoRequest{VmId: vm.ID})
	switch {
	case unimplemented(err):
		block("node %s needs an agent update before VMs can move off it", src.Name)
	case err != nil:
		block("node %s could not describe the VM: %v", src.Name, err)
	case !info.VmFound:
		block("the VM is not defined on its node %s", src.Name)
	default:
		f.info = info
	}

	f.mode = req.Mode
	state := ""
	if f.info != nil {
		state = f.info.VmState
	} else if vm.Status == models.VMStatusRunning || vm.Status == models.VMStatusSuspended {
		state = "running"
	} else if vm.Status == models.VMStatusStopped {
		state = "shutoff"
	}
	switch {
	case state == "running" || state == "paused":
		if f.mode == "" {
			f.mode = models.MigrationLive
		} else if f.mode == models.MigrationOffline {
			block("the VM is running; stop it to move it offline, or move it live")
		}
	case state == "shutoff":
		if f.mode == "" {
			f.mode = models.MigrationOffline
		} else if f.mode == models.MigrationLive {
			block("the VM is stopped, so it moves offline")
		}
	case state != "":
		block("the VM is %s on its node; only a running or stopped VM can move", state)
	}
	if f.info == nil {
		return f, nil
	}

	if f.info.BackupRunning {
		block("a backup is copying the VM's disks; try again when it has finished")
	}
	if f.info.SnapshotCount > 0 {
		block("the VM has %d snapshots on its node; snapshots cannot move with it, so delete them first", f.info.SnapshotCount)
	}
	names := map[string]bool{}
	for _, d := range f.info.Disks {
		if d.Device == "cdrom" {
			if d.Seed {
				if d.FileSizeBytes > maxMovedSeed {
					block("the VM's setup image %s is too large to carry", d.Path)
				}
				f.seed = &planDisk{Target: d.Target, Path: d.Path, FileName: filepath.Base(d.Path), Format: d.Format, FileSize: d.FileSizeBytes}
			} else if d.Path != "" {
				block("an ISO is in the VM's drive %s; eject it first", d.Target)
			}
			continue
		}
		if d.SourceType != "file" {
			block("disk %s is a %s volume; only disks kept as files can move", d.Target, d.SourceType)
			continue
		}
		if d.Format != "qcow2" && d.Format != "raw" {
			block("disk %s is in %q format; only qcow2 and raw disks can move", d.Target, d.Format)
			continue
		}
		if d.Readonly {
			block("disk %s is read-only; detach it first", d.Target)
			continue
		}
		if f.mode == models.MigrationOffline && d.BackingFile != "" {
			block("disk %s sits on a backing image; start the VM and move it live, which copies the whole chain", d.Target)
		}
		name := filepath.Base(d.Path)
		if names[name] {
			block("two of the VM's disks are both named %s", name)
		}
		names[name] = true
		f.disks = append(f.disks, planDisk{Target: d.Target, Path: d.Path, FileName: name, Format: d.Format, Virtual: d.VirtualSizeBytes, FileSize: d.FileSizeBytes})
	}
	if f.seed != nil && names[f.seed.FileName] {
		block("the VM's setup image has the same name as one of its disks")
	}
	if f.mode == models.MigrationOffline && len(f.info.SshHostKeys) == 0 {
		block("no SSH host keys were found on node %s, so its disks cannot be fetched over a checked connection", f.src.Name)
	}
	return f, nil
}

// recordBlockers adds what the panel's own records say stops a move.
func (m *MigrationService) recordBlockers(ctx context.Context, vm *models.VM, block func(string, ...interface{})) {
	var floating []string
	m.db.WithContext(ctx).Model(&models.IPAddress{}).Where("vm_id = ? AND delivery_mode = ?", vm.ID, models.IPDeliveryFloating).Pluck("address", &floating)
	if len(floating) > 0 {
		block("floating IP %s is attached; floating IPs live on one node, so detach it first", strings.Join(hostAddrs(floating), ", "))
	}
	var networks []string
	m.db.WithContext(ctx).Table("ip_addresses a").Select("DISTINCT m.name").
		Joins("JOIN ip_pools p ON p.id = a.pool_id").
		Joins("JOIN managed_networks m ON m.bridge = p.bridge AND m.deleted_at IS NULL").
		Where("a.vm_id = ? AND a.deleted_at IS NULL AND p.bridge <> ''", vm.ID).Pluck("m.name", &networks)
	if len(networks) > 0 {
		block("the VM is in private network %s, which exists on one node only", strings.Join(networks, ", "))
	}
	if vm.RescueMode {
		block("the VM is in rescue mode; take it out first")
	}
	if vm.AttachedISO != nil && *vm.AttachedISO != "" {
		block("an ISO is mounted in the VM; unmount it first")
	}
	var snaps int64
	if m.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ?", vm.ID).Count(&snaps).Error == nil && snaps > 0 {
		block("the VM has %d snapshots; snapshots cannot move with it, so delete them first", snaps)
	}
}

func hostAddrs(in []string) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = hostOnlyIP(a)
	}
	return out
}

// movePool is where a VM lands on nodeID when no pool is chosen: its best
// online directory pool, since VMs move only into directory pools.
func movePool(ctx context.Context, db *gorm.DB, nodeID string) *models.StoragePool {
	var pools []models.StoragePool
	if err := db.WithContext(ctx).Where("node_id = ? AND status = ? AND COALESCE(type, '') IN ?", nodeID, "online", []string{"", "dir", "file"}).
		Find(&pools).Error; err != nil {
		return nil
	}
	return pickPool(pools)
}

// forwardsBlockedOn are the VM's port forwards nodeID cannot publish: the
// same rules addPortForwardTx applies to a new forward there.
func forwardsBlockedOn(ctx context.Context, db *gorm.DB, vmID, nodeID string) ([]models.PortForward, error) {
	q := db.WithContext(ctx)
	var mine []models.PortForward
	if err := q.Where("vm_id = ?", vmID).Order("external_port, protocol").Find(&mine).Error; err != nil {
		return nil, err
	}
	if len(mine) == 0 {
		return nil, nil
	}
	var routed int64
	if err := q.Table("vm_hostnames h").Joins("JOIN vms m ON m.id = h.vm_id AND m.deleted_at IS NULL").
		Where("m.node_id = ? AND m.id <> ? AND h.verified_at IS NOT NULL", nodeID, vmID).Count(&routed).Error; err != nil {
		return nil, err
	}
	var out []models.PortForward
	for _, f := range mine {
		var n int64
		if err := q.Model(&models.PortForward{}).Where("node_id = ? AND vm_id <> ? AND external_port = ? AND protocol = ?",
			nodeID, vmID, f.ExternalPort, f.Protocol).Count(&n).Error; err != nil {
			return nil, err
		}
		if n == 0 {
			if err := q.Model(&models.NATPortBlock{}).Where("node_id = ? AND vm_id <> ? AND start_port <= ? AND start_port + port_count - 1 >= ?",
				nodeID, vmID, f.ExternalPort, f.ExternalPort).Count(&n).Error; err != nil {
				return nil, err
			}
		}
		if n > 0 || (routed > 0 && (f.ExternalPort == 80 || f.ExternalPort == 443)) {
			out = append(out, f)
		}
	}
	return out, nil
}

func forwardList(fs []models.PortForward) string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = fmt.Sprintf("%d/%s", f.ExternalPort, f.Protocol)
	}
	return strings.Join(out, ", ")
}

// candidates are the nodes a VM could go to, best first: active, taking VMs,
// not its own, and with room under every limit.
func (m *MigrationService) candidates(ctx context.Context, f *srcFacts, env *checkEnv) ([]models.Node, error) {
	nodes, err := m.vms.nodeRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}
	var others []models.Node
	for _, n := range nodesTakingVMs(nodes) {
		if n.ID != f.src.ID {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		return nil, nil
	}
	loads, err := loadNodes(ctx, m.db, nodesReporting(others))
	if err != nil {
		return nil, err
	}
	var fit []nodeLoad
	for _, l := range loads {
		pool := movePool(ctx, m.db, l.Node.ID)
		if pool == nil {
			continue
		}
		if len(m.capacityBlockers(ctx, f.vm, &l.Node, pool, f.diskGB, env)) == 0 {
			fit = append(fit, l)
		}
	}
	ranked := rankNodes(withExtra(fit, env), f.vm.Resources.RAM)
	out := make([]models.Node, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].Node
	}
	return out, nil
}

func withExtra(loads []nodeLoad, env *checkEnv) []nodeLoad {
	out := append([]nodeLoad(nil), loads...)
	for i := range out {
		if x := env.extra[out[i].Node.ID]; x != nil {
			out[i].AllocatedVCPU += x.cpu
			out[i].AllocatedRAMMB += x.ramMB
		}
	}
	return out
}

// capacityBlockers is admission for a VM arriving on a node: RAM and vCPU
// under the node's ratios, the disks in the pool's free space and allocation
// ratio, counting what a drain plan already sends there.
func (m *MigrationService) capacityBlockers(ctx context.Context, vm *models.VM, dst *models.Node, pool *models.StoragePool, diskGB int, env *checkEnv) []string {
	var out []string
	loads, err := loadNodes(ctx, m.db, []models.Node{*dst})
	if err != nil {
		return []string{"node " + dst.Name + "'s allocation could not be read: " + err.Error()}
	}
	l := withExtra(loads, env)[0]
	if !l.ramFits(vm.Resources.RAM) {
		out = append(out, fmt.Sprintf("node %s would pass its RAM overcommit limit", dst.Name))
	}
	if !l.vcpuFits(vm.Resources.CPU) {
		out = append(out, fmt.Sprintf("node %s would pass its vCPU overcommit limit", dst.Name))
	}
	extraGB := 0
	if x := env.extra[dst.ID]; x != nil {
		extraGB = x.poolGB[pool.ID]
	}
	if !PoolFits(pool, diskGB+extraGB) {
		out = append(out, fmt.Sprintf("storage pool %s on %s has %d GB free, and the VM's %d GB of disk needs that much plus %d GB kept free",
			pool.Name, dst.Name, pool.AvailableSpace/bytesPerGB-int64(extraGB), diskGB, diskHeadroomBytes/bytesPerGB))
	} else if ratio := DiskAllocationRatio(ctx, m.db); ratio > 0 {
		alloc, err := PoolAllocations(ctx, m.db, pool.NodeID)
		if err != nil {
			out = append(out, "storage allocation could not be read: "+err.Error())
		} else if !PoolWithinRatio(pool, alloc[pool.ID]+int64(extraGB)*bytesPerGB, diskGB, ratio) {
			out = append(out, fmt.Sprintf("storage pool %s on %s would pass its allocation limit (%.2fx its capacity)", pool.Name, dst.Name, ratio))
		}
	}
	return out
}

// destinationChecks adds what stops the VM going to dst.
func (m *MigrationService) destinationChecks(ctx context.Context, f *srcFacts, dst *models.Node, req MigrationRequest, env *checkEnv, chk *MigrationCheck) {
	chk.DestNodeID, chk.DestNodeName = dst.ID, dst.Name
	if dst.ID == f.src.ID {
		chk.block("the VM is already on node %s", dst.Name)
		return
	}
	if dst.Status != models.NodeStatusActive {
		chk.block("node %s is %s", dst.Name, dst.Status)
	}
	if dst.CreationPaused {
		chk.block("node %s is paused for new VMs; resume it to move VMs onto it", dst.Name)
	}

	var pool *models.StoragePool
	if req.DestPoolID != "" {
		var p models.StoragePool
		if err := m.db.WithContext(ctx).Where("id = ? AND node_id = ?", req.DestPoolID, dst.ID).First(&p).Error; err != nil {
			chk.block("storage pool %s is not on node %s", req.DestPoolID, dst.Name)
		} else {
			pool = &p
		}
	} else if pool = movePool(ctx, m.db, dst.ID); pool == nil {
		chk.block("the panel knows no online directory storage pool on node %s, and VMs move only into directory pools", dst.Name)
	}
	if pool != nil {
		chk.DestPoolID, chk.DestPoolName, chk.DestPoolPath = pool.ID, pool.Name, pool.Path
		if pool.Type != "" && pool.Type != "dir" && pool.Type != "file" {
			chk.block("storage pool %s is %s storage; VMs move only into directory pools", pool.Name, pool.Type)
		}
		if pool.Status != "online" {
			chk.block("storage pool %s is %s", pool.Name, pool.Status)
		}
		for _, b := range m.capacityBlockers(ctx, f.vm, dst, pool, f.diskGB, env) {
			chk.block("%s", b)
		}
	}

	if clash, err := forwardsBlockedOn(ctx, m.db, f.vm.ID, dst.ID); err != nil {
		chk.block("port forwards could not be checked: %v", err)
	} else if len(clash) > 0 {
		chk.block("node %s already uses %d of the ports this VM forwards (%s): another VM's forward, a NAT VM's ports or its hostname router; remove those port forwards first",
			dst.Name, len(clash), forwardList(clash))
	}
	var forwards int64
	m.db.WithContext(ctx).Model(&models.PortForward{}).Where("vm_id = ?", f.vm.ID).Count(&forwards)
	if forwards > 0 {
		chk.warn("the VM's %d port forwards move with it and are published on node %s's address", forwards, dst.Name)
	}
	var nodeKept int64
	m.db.WithContext(ctx).Model(&models.Backup{}).Where("vm_id = ? AND storage_provider = ? AND node_id = ? AND status = ?",
		f.vm.ID, models.BackupStoreNode, f.src.ID, models.BackupStatusCompleted).Count(&nodeKept)
	if nodeKept > 0 {
		chk.warn("%d of the VM's backups are in node %s's own storage; node %s can restore them only if it uses the same storage", nodeKept, f.src.Name, dst.Name)
	}

	var pools []string
	if err := m.db.WithContext(ctx).Raw(`SELECT DISTINCT p.name FROM ip_pools p WHERE p.id IN (
			SELECT pool_id FROM ip_addresses WHERE vm_id = ? AND deleted_at IS NULL AND delivery_mode <> ?
			UNION SELECT pool_id FROM vm_ipv6_prefixes WHERE vm_id = ?)
		AND NOT (EXISTS (SELECT 1 FROM ip_pool_nodes j WHERE j.pool_id = p.id AND j.node_id = ?) OR COALESCE(p.node_id = ?, false)
			OR (p.node_id IS NULL AND NOT EXISTS (SELECT 1 FROM ip_pool_nodes j WHERE j.pool_id = p.id)))`,
		f.vm.ID, models.IPDeliveryFloating, f.vm.ID, dst.ID, dst.ID).Scan(&pools).Error; err != nil {
		chk.block("the VM's address pools could not be checked: %v", err)
	} else {
		for _, p := range pools {
			chk.block("address pool %s is not available on node %s, so the VM's address would not reach it there", p, dst.Name)
		}
	}
	var unmanaged int64
	m.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM networks n WHERE n.vm_id = ? AND n.deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM ip_addresses a WHERE a.vm_id = n.vm_id AND host(a.address) = host(n.ip_address) AND a.deleted_at IS NULL)`, f.vm.ID).Scan(&unmanaged)
	if unmanaged > 0 {
		chk.warn("%d of the VM's addresses are not in an address pool; make sure they reach node %s", unmanaged, dst.Name)
	}

	dstInfo, err := m.nodeInfo(ctx, dst, env)
	switch {
	case unimplemented(err):
		chk.block("node %s needs an agent update before it can receive VMs", dst.Name)
		return
	case err != nil:
		chk.block("node %s could not be asked about itself: %v", dst.Name, err)
		return
	}
	if f.info == nil {
		return
	}
	if f.mode == models.MigrationLive {
		chk.warn("the memory and disk copy goes from node %s to node %s on TCP ports 49152-49215 without encryption; between sites, use a private network or move the VM offline", f.src.Name, dst.Name)
		if dstInfo.SshProxy == "" {
			chk.block("node %s cannot accept the migration connection: install virt-ssh-helper (libvirt), socat or netcat-openbsd there", dst.Name)
		}
		if len(dstInfo.SshHostKeys) == 0 {
			chk.block("no SSH host keys were found on node %s, so it cannot be connected to over a checked connection", dst.Name)
		}
		if f.info.VmCpuMode == "host-passthrough" && !sameCPU(f.info, dstInfo) {
			chk.block("the VM uses its host's own CPU (host-passthrough) and node %s's CPU (%s) differs from node %s's (%s); stop the VM and move it offline, or choose a node with the same CPU",
				dst.Name, cpuName(dstInfo), f.src.Name, cpuName(f.info))
		}
	} else if f.info.VmCpuMode == "host-passthrough" && !sameCPU(f.info, dstInfo) {
		chk.warn("the VM uses its host's own CPU and will see node %s's CPU (%s) when it next starts", dst.Name, cpuName(dstInfo))
	}
	if pool == nil {
		return
	}

	client, actx, err := m.agent(ctx, dst)
	if err != nil {
		chk.block("%v", err)
		return
	}
	dry := m.targetRequest(f, pool, f.ownMigration, true)
	if f.mode == models.MigrationLive && f.info.VmCpuMode != "host-passthrough" {
		dry.CpuXml = f.info.VmCpuXml
	}
	cctx, cancel := context.WithTimeout(actx, 60*time.Second)
	defer cancel()
	resp, err := client.PrepareMigrationTarget(cctx, dry)
	switch {
	case unimplemented(err):
		chk.block("node %s needs an agent update before it can receive VMs", dst.Name)
	case err != nil:
		chk.block("node %s could not check itself for the VM: %v", dst.Name, err)
	case !resp.Success && len(resp.Problems) == 0:
		chk.block("node %s: %s", dst.Name, agentMessage(resp.Error, "it cannot receive the VM"))
	default:
		for _, p := range resp.Problems {
			chk.block("node %s: %s", dst.Name, p)
		}
	}
	chk.plan = &migrationPlan{vm: f.vm, src: f.src, dst: dst, pool: pool, mode: f.mode, cpuMode: f.info.VmCpuMode,
		cpuXML: dry.CpuXml, bridges: f.info.Bridges, disks: f.disks, seed: f.seed}
}

// targetRequest asks the destination to check or prepare for the VM.
func (m *MigrationService) targetRequest(f *srcFacts, pool *models.StoragePool, migrationID string, dryRun bool) *pb.PrepareMigrationTargetRequest {
	req := &pb.PrepareMigrationTargetRequest{
		VmId: f.vm.ID, MigrationId: migrationID, PoolPath: pool.Path, DryRun: dryRun,
		CreateDisks: f.mode == models.MigrationLive,
	}
	if f.info != nil {
		req.Bridges = f.info.Bridges
	}
	for _, d := range f.disks {
		req.Disks = append(req.Disks, &pb.MigrationTargetDisk{Target: d.Target, FileName: d.FileName, VirtualSizeBytes: d.Virtual, Format: d.Format})
		req.FreeBytesNeeded += d.FileSize
	}
	if f.seed != nil {
		req.SeedFileName = f.seed.FileName
	}
	return req
}

// sameCPU reports two hosts with the same CPU, as far as a guest given the
// host's own CPU can tell: vendor, model and every feature.
func sameCPU(a, b *pb.GetMigrationInfoResponse) bool {
	return a.CpuModel != "" && a.CpuVendor == b.CpuVendor && a.CpuModel == b.CpuModel && slices.Equal(a.CpuFeatures, b.CpuFeatures)
}

func cpuName(r *pb.GetMigrationInfoResponse) string {
	if r.CpuModel == "" {
		return "unknown"
	}
	return strings.TrimSpace(r.CpuVendor + " " + r.CpuModel)
}

// Start checks a move and queues it. The returned check says why a refused
// move was refused.
func (m *MigrationService) Start(ctx context.Context, req MigrationRequest) (*models.VMMigration, *MigrationCheck, error) {
	if req.BandwidthMbps < 0 || req.BandwidthMbps > 100000 {
		return nil, nil, fmt.Errorf("%w: the bandwidth cap must be between 0 and 100000 Mbps", ErrMigrationBlocked)
	}
	m.recoverOrphan(ctx, req.VMID)
	chk, err := m.Check(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if !chk.CanMove {
		return nil, chk, ErrMigrationBlocked
	}
	row, err := m.queue(ctx, chk, req, nil)
	return row, chk, err
}

// queue records a checked move and puts it on the job queue.
func (m *MigrationService) queue(ctx context.Context, chk *MigrationCheck, req MigrationRequest, drainID *string) (*models.VMMigration, error) {
	row := &models.VMMigration{
		VMID: chk.VMID, SourceNodeID: chk.SourceNodeID, Mode: chk.Mode, Status: models.MigrationQueued,
		BandwidthMbps: req.BandwidthMbps, BytesTotal: chk.CopyBytes, DrainID: drainID, Phase: "Waiting to start",
	}
	dst, pool := chk.DestNodeID, chk.DestPoolID
	row.DestNodeID, row.DestPoolID = &dst, &pool
	if req.RequestedBy != "" {
		row.RequestedBy = &req.RequestedBy
	}
	if err := m.db.WithContext(ctx).Create(row).Error; err != nil {
		if VMMigrating(ctx, m.db, chk.VMID) {
			return nil, ErrMigrationActive
		}
		return nil, fmt.Errorf("failed to record the move: %w", err)
	}
	if drainID != nil {
		return row, nil // the drain runs it
	}
	if m.river == nil {
		m.finish(ctx, row, models.MigrationFailed, "the job queue is not running", "")
		return nil, fmt.Errorf("the job queue is not running")
	}
	res, err := m.river.Insert(ctx, queue.MigrationJob{MigrationID: row.ID, VMID: row.VMID}, nil)
	if err != nil {
		m.finish(ctx, row, models.MigrationFailed, "could not be queued: "+err.Error(), "")
		return nil, fmt.Errorf("failed to queue the move: %w", err)
	}
	row.JobID = &res.Job.ID
	m.db.WithContext(ctx).Model(row).Update("job_id", res.Job.ID)
	return row, nil
}

// recoverOrphan ends a VM's move whose job is gone (it was discarded, or the
// panel lost it), so it does not block every later move.
func (m *MigrationService) recoverOrphan(ctx context.Context, vmID string) {
	row, err := m.activeMigration(ctx, vmID)
	if err != nil || row == nil {
		return
	}
	alive := false
	if row.DrainID != nil {
		var st string
		if m.db.WithContext(ctx).Table("node_drains").Select("status").Where("id = ?", *row.DrainID).Scan(&st).Error == nil {
			alive = st == models.DrainRunning || st == models.DrainAborting
		}
	} else if row.JobID != nil {
		var st string
		if m.db.WithContext(ctx).Raw("SELECT state FROM river_job WHERE id = ?", *row.JobID).Scan(&st).Error != nil {
			return // cannot tell: leave it
		}
		alive = st == "available" || st == "running" || st == "retryable" || st == "scheduled" || st == "pending"
	} else {
		alive = time.Since(row.CreatedAt) < time.Minute
	}
	if !alive {
		m.finish(ctx, row, models.MigrationFailed, "the panel lost track of this move; check on which node the VM runs", "")
	}
}

// Latest is the VM's newest move, nil when it never moved.
func (m *MigrationService) Latest(ctx context.Context, vmID string) (*models.VMMigration, error) {
	var rows []models.VMMigration
	if err := m.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// Cancel stops a VM's move. A move not started yet ends at once; a running one
// is aborted on its node, and the VM stays where it was.
func (m *MigrationService) Cancel(ctx context.Context, vmID string) (*models.VMMigration, error) {
	// A move whose job is gone (cancelled from the job queue) would otherwise
	// stay "cancelling" with nothing to finish it, and lock the VM.
	m.recoverOrphan(ctx, vmID)
	row, err := m.activeMigration(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNoActiveMigration
	}
	if row.Status == models.MigrationQueued && row.StartedAt == nil {
		res := m.db.WithContext(ctx).Model(&models.VMMigration{}).Where("id = ? AND status = ?", row.ID, models.MigrationQueued).
			Updates(map[string]interface{}{"status": models.MigrationCancelled, "phase": "Cancelled before it started", "finished_at": time.Now(), "updated_at": time.Now()})
		if res.Error == nil && res.RowsAffected == 1 {
			row.Status = models.MigrationCancelled
			return row, nil
		}
	}
	if err := m.db.WithContext(ctx).Model(&models.VMMigration{}).Where("id = ? AND status IN ?", row.ID,
		[]string{models.MigrationQueued, models.MigrationRunning}).Updates(map[string]interface{}{"status": models.MigrationCancelling, "updated_at": time.Now()}).Error; err != nil {
		return nil, err
	}
	row.Status = models.MigrationCancelling
	return row, nil
}

func (m *MigrationService) setPhase(ctx context.Context, row *models.VMMigration, phase string) {
	row.Phase = phase
	m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMMigration{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{"phase": phase, "updated_at": time.Now()})
}

// finish records a move's final state.
func (m *MigrationService) finish(ctx context.Context, row *models.VMMigration, st, errMsg, note string) {
	now := time.Now()
	row.Status, row.Error, row.FinishedAt = st, errMsg, &now
	if note != "" {
		row.Note = strings.TrimSpace(row.Note + " " + note)
	}
	phase := map[string]string{models.MigrationCompleted: "Moved", models.MigrationFailed: "Failed", models.MigrationCancelled: "Cancelled"}[st]
	if phase != "" {
		row.Phase = phase
	}
	if err := m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.VMMigration{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
		"status": st, "error": errMsg, "note": row.Note, "phase": row.Phase, "finished_at": now, "updated_at": now,
	}).Error; err != nil {
		m.logger.ErrorContext(ctx, "could not record the end of a move", "migration_id", row.ID, "error", err)
	}
}

// cancelRequested re-reads whether someone asked to stop the move.
func (m *MigrationService) cancelRequested(ctx context.Context, row *models.VMMigration) bool {
	var st string
	if m.db.WithContext(ctx).Table("vm_migrations").Select("status").Where("id = ?", row.ID).Scan(&st).Error != nil {
		return false
	}
	return st == models.MigrationCancelling
}
