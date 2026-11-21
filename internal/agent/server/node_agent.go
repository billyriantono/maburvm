package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/agent/agentenv"
	"github.com/maburvm/panel/internal/agent/cloudinit"
	"github.com/maburvm/panel/internal/agent/dhcp"
	"github.com/maburvm/panel/internal/agent/domtune"
	"github.com/maburvm/panel/internal/agent/guestos"
	"github.com/maburvm/panel/internal/agent/health"
	"github.com/maburvm/panel/internal/agent/hostproxy"
	"github.com/maburvm/panel/internal/agent/hwprofile"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/netrules"
	"github.com/maburvm/panel/internal/agent/network"
	"github.com/maburvm/panel/internal/agent/storage"
	"github.com/maburvm/panel/internal/agent/vmhealth"
	"github.com/maburvm/panel/internal/agent/vncproxy"
	"github.com/maburvm/panel/internal/shared/buildinfo"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/safefetch"
	sharedstorage "github.com/maburvm/panel/internal/shared/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	libvirtLib "libvirt.org/go/libvirt"
)

// VMMetricsCache tracks per-VM metrics history for calculating rates
type VMMetricsCache struct {
	mu              sync.RWMutex
	lastCPUTime     map[string]uint64
	lastCPUCollect  map[string]time.Time
	lastNetRX       map[string]int64
	lastNetTX       map[string]int64
	lastDiskRead    map[string]int64
	lastDiskWrite   map[string]int64
	lastNetCollect  map[string]time.Time
	lastDiskCollect map[string]time.Time
}

// NewVMMetricsCache creates a new VM metrics cache
func NewVMMetricsCache() *VMMetricsCache {
	return &VMMetricsCache{
		lastCPUTime:     make(map[string]uint64),
		lastCPUCollect:  make(map[string]time.Time),
		lastNetRX:       make(map[string]int64),
		lastNetTX:       make(map[string]int64),
		lastDiskRead:    make(map[string]int64),
		lastDiskWrite:   make(map[string]int64),
		lastNetCollect:  make(map[string]time.Time),
		lastDiskCollect: make(map[string]time.Time),
	}
}

// NodeAgentService implements the NodeAgent gRPC service
type NodeAgentService struct {
	pb.UnimplementedNodeAgentServer
	libvirt         *libvirt.VMManager
	networkMgr      *network.Manager
	healthCollector *health.MetricsCollector
	vncProxy        *vncproxy.Proxy
	metricsCache    *VMMetricsCache
	// hostProxy routes NAT VMs' hostnames on the node's publish address.
	hostProxy *hostproxy.Proxy
	// guestAgents bounds guest agent calls; crashes is the restart-on-crash
	// policy's memory.
	guestAgents *vmhealth.GuestAgents
	crashes     *vmhealth.Crashes
}

// NewNodeAgentService creates a new NodeAgentService with all dependencies
func NewNodeAgentService(libvirtMgr *libvirt.VMManager, networkMgr *network.Manager, healthCollector *health.MetricsCollector, vncProxy *vncproxy.Proxy) *NodeAgentService {
	// An export interrupted by a crash or a restart leaves its partial file
	// behind: the cleanup is deferred inside the agent, so a process that dies
	// never runs it. These are multi-GB files on the volume holding customer
	// disks, and nothing else would ever remove them.
	cleanStaleExports()

	publish := func() string { return "" }
	if networkMgr != nil {
		publish = networkMgr.NATAddress
	}
	return &NodeAgentService{
		libvirt:         libvirtMgr,
		networkMgr:      networkMgr,
		healthCollector: healthCollector,
		vncProxy:        vncProxy,
		metricsCache:    NewVMMetricsCache(),
		hostProxy:       hostproxy.New(publish),
		guestAgents:     vmhealth.NewGuestAgents(),
		crashes:         vmhealth.NewCrashes(),
	}
}

var _ pb.NodeAgentServer = (*NodeAgentService)(nil)

func generateAuthToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("token-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *NodeAgentService) RegisterNode(ctx context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	log.Printf("[NodeAgent] RegisterNode called: hostname=%s, version=%s", req.Hostname, req.AgentVersion)

	if req.Hostname == "" {
		return nil, status.Errorf(codes.InvalidArgument, "hostname is required")
	}

	nodeID := fmt.Sprintf("node-%s-%d", req.Hostname, time.Now().Unix())
	authToken := generateAuthToken()

	resp := &pb.RegisterNodeResponse{
		NodeId:          nodeID,
		AuthToken:       authToken,
		RefreshInterval: durationpb.New(30 * time.Second),
		MetricsInterval: durationpb.New(60 * time.Second),
	}

	log.Printf("[NodeAgent] Node registered successfully: node_id=%s", resp.NodeId)
	return resp, nil
}

func (s *NodeAgentService) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.HeartbeatResponse]) error {
	log.Println("[NodeAgent] Heartbeat stream started")

	nodeID, _ := GetNodeIDFromContext(stream.Context())
	if nodeID == "" {
		nodeID = "unknown"
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stream.Context().Done():
				return
			case <-ticker.C:
				resp := &pb.HeartbeatResponse{
					Timestamp:       timestamppb.Now(),
					CommandsPending: false,
					ConfigUpdate:    false,
					Acknowledged:    true,
				}
				if err := stream.Send(resp); err != nil {
					log.Printf("[NodeAgent] Failed to send heartbeat response: %v", err)
					return
				}
			}
		}
	}()

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			log.Printf("[NodeAgent] Heartbeat stream closed by client: %s", nodeID)
			return nil
		}
		if err != nil {
			log.Printf("[NodeAgent] Heartbeat stream error: %v", err)
			return err
		}

		log.Printf("[NodeAgent] Received heartbeat from node %s at %v", req.NodeId, req.Timestamp.AsTime())

		if s.healthCollector != nil {
			metrics := s.healthCollector.Collect()
			log.Printf("[NodeAgent] Node %s health metrics: CPU=%.2f%%, Memory=%d/%d MB, VMs=%d",
				nodeID, metrics.CPUPercent, metrics.MemoryUsed/1024/1024, metrics.MemoryTotal/1024/1024, metrics.RunningVMCount)
		}

		resp := &pb.HeartbeatResponse{
			Timestamp:       timestamppb.Now(),
			CommandsPending: false,
			ConfigUpdate:    false,
			Acknowledged:    true,
		}

		if err := stream.Send(resp); err != nil {
			log.Printf("[NodeAgent] Failed to send heartbeat ack: %v", err)
			return err
		}
	}
}

func mapVMStatusToPBState(status libvirt.VMStatus) pb.VMState {
	switch status {
	case libvirt.VMStatusRunning:
		return pb.VMState_VM_STATE_RUNNING
	case libvirt.VMStatusStopped:
		return pb.VMState_VM_STATE_STOPPED
	case libvirt.VMStatusPaused:
		return pb.VMState_VM_STATE_PAUSED
	case libvirt.VMStatusCrashed:
		return pb.VMState_VM_STATE_ERROR
	default:
		return pb.VMState_VM_STATE_UNSPECIFIED
	}
}

func (s *NodeAgentService) ExecuteVMCommand(ctx context.Context, req *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	log.Printf("[NodeAgent] ExecuteVMCommand: vm_id=%s, command=%v", req.VmId, req.Command)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Executing command %v on VM %s from node %s", req.Command, req.VmId, nodeID)
	// A VM recovered onto another node may only be stopped here.
	if req.Command != pb.VMCommandType_VM_COMMAND_TYPE_STOP && req.Command != pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP && migration.IsFenced(req.VmId) {
		return &pb.VMCommandResponse{VmId: req.VmId, Command: req.Command, Message: errFenced.Error(),
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_CONFLICT, Message: errFenced.Error()}}, nil
	}
	if req.Command != pb.VMCommandType_VM_COMMAND_TYPE_START && req.Command != pb.VMCommandType_VM_COMMAND_TYPE_RESTART {
		s.crashes.Hold(req.VmId)
	}

	var err error
	var state pb.VMState
	var restored []*pb.BackupDiskFile
	var kept []*pb.KeptVolume

	switch req.Command {
	case pb.VMCommandType_VM_COMMAND_TYPE_CREATE:
		restored, err = s.createVM(req)
		// On success the domain is defined but not started.
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_ADOPT:
		err = s.adoptVM(req)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_START:
		state = pb.VMState_VM_STATE_RUNNING
		// Self-heal a VNC listen address this host cannot bind, for the same
		// reason the bridge is healed below: a domain imported from another
		// platform keeps that platform's node address, and qemu then refuses to
		// start with "Cannot assign requested address". Silent until someone
		// tries to boot, and twelve domains were in that state. Best-effort —
		// failing to repair graphics must not block a start that would work.
		if _, _, verr := libvirt.EnsureVNCGraphics(req.VmId); verr != nil {
			log.Printf("[NodeAgent] WARNING: could not verify VNC graphics for %s before start: %v", req.VmId, verr)
		}
		// Self-heal the NIC bridge from the pool (the panel sends the pool's
		// current bridge in NetworkConfig) before booting, so a VM whose domain
		// XML still points at a removed bridge (e.g. virbr0) can start once the
		// pool's bridge is corrected. No-op when unset or already correct.
		if bridge := primaryBridge(req.Config); bridge != "" {
			if changed, serr := libvirt.SyncInterfaceBridge(req.VmId, bridge); serr != nil {
				err = serr
			} else if changed {
				log.Printf("[NodeAgent] VM %s: synced NIC bridge to %q before start", req.VmId, bridge)
			}
		}
		if err == nil && migrations.Busy(req.VmId) {
			// A disk being copied or restored while the VM is off is whole only
			// while it stays off.
			err = errDisksBusy
		}
		if err == nil {
			s.applyPendingGuestBeforeStart(ctx, req.VmId)
			err = startVM(req.VmId)
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_STOP:
		err = libvirt.StopVM(req.VmId, false)
		state = pb.VMState_VM_STATE_STOPPED
		// StopVM only presses the power button. The VM is stopped when libvirt
		// says it is shut off, not before.
		if err == nil {
			err = waitShutOff(ctx, req.VmId, libvirt.GetVMStatus)
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP:
		err = libvirt.StopVM(req.VmId, true)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESTART:
		// A restart boots a stopped VM, which must wait for work on its disks.
		if st, _ := libvirt.GetVMStatus(req.VmId); st != libvirt.VMStatusRunning && migrations.Busy(req.VmId) {
			err = errDisksBusy
		} else {
			err = libvirt.RestartVM(req.VmId)
		}
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_DESTROY:
		// Block devices outlive DeleteVM, which removes only files: each is
		// removed below when it is provably this VM's, or reported as kept.
		blockDisks, _ := libvirt.BackupDisks(req.VmId)
		blockDisks = libvirt.UnderOverlays(blockDisks)
		err = libvirt.DeleteVM(req.VmId)
		state = pb.VMState_VM_STATE_DESTROYED
		// A deleted VM leaves the operator's ISOs alone (they are shared) and
		// its own disks are already gone; a customer's ISO it was the last to
		// use goes here.
		if err == nil {
			sweepCustomerISOs(0)
			kept = removeVMVolumes(ctx, req.VmId, blockDisks, lvmPoolsOf(req.GetLvmPools()), req.GetRemoveForeignVolumes())
		}
		// Destroying the domain left every host-side rule behind: firewall,
		// anti-spoof, bandwidth shaping and NAT. That was invisible while
		// br_netfilter was off, because the firewall rules matched nothing — but
		// a deleted VM's default-DROP outlives it, and its address is handed to
		// the next VM within minutes. The new owner's traffic is then dropped by
		// a rule belonging to a machine that no longer exists.
		//
		// Best-effort and after the domain is gone: failing to tidy up must not
		// turn a successful delete into a failed one.
		if s.networkMgr != nil {
			if cerr := s.networkMgr.CleanupVMNetwork(req.VmId); cerr != nil {
				log.Printf("[NodeAgent] WARNING: network cleanup after destroy of %s: %v", req.VmId, cerr)
			}
			s.networkMgr.ForgetVM(req.VmId)
			updateNetworkConfig(netConfigPath(), currentBootID(), req.VmId, nil)
			clearPendingGuest(req.VmId)
			_ = s.hostProxy.Set(req.VmId, nil)
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_PAUSE:
		err = libvirt.SuspendVM(req.VmId)
		state = pb.VMState_VM_STATE_PAUSED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESUME:
		err = libvirt.ResumeVM(req.VmId)
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_REBUILD:
		// A reinstall replaces the disk a restore or move is working on.
		if migrations.Busy(req.VmId) {
			err = errDisksBusy
		} else {
			err = s.rebuildVM(req)
		}
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESIZE:
		if req.Config != nil && req.Config.Resources != nil {
			var cpus []int
			if cpus, err = hostCPUs(req.Config.GetHardware().GetPinnedCpus(), int(req.Config.Resources.Vcpus)); err == nil {
				err = libvirt.ResizeVM(req.VmId, int(req.Config.Resources.Vcpus), int(req.Config.Resources.MemoryMb), cpus)
			}
			// Grow the primary disk too (no-op when unchanged; grow-only).
			if err == nil && req.Config.Resources.DiskGb > 0 {
				err = libvirt.ResizeDisk(req.VmId, int(req.Config.Resources.DiskGb), growLV(ctx, lvmPoolsOf(req.GetLvmPools())))
			}
		} else {
			err = fmt.Errorf("resources required for RESIZE")
		}
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESET_PASSWORD:
		if req.Config != nil && req.Config.RootPassword != "" && libvirt.GuestOSType(req.VmId) == guestos.RouterOS {
			err = s.runRouterOSScript(ctx, req.VmId, guestos.RouterOSPasswordScript(req.Config.RootPassword), true)
		} else if req.Config != nil && req.Config.RootPassword != "" {
			user := models.OSLoginUser(libvirt.GuestOSType(req.VmId))
			if st, serr := libvirt.GetVMStatus(req.VmId); serr == nil && st == libvirt.VMStatusStopped {
				err = resetPasswordOffline(req.VmId, user, req.Config.RootPassword)
			} else {
				// root, or Administrator on Windows: its guest agent sets it the same way.
				err = libvirt.SetVMPassword(req.VmId, user, req.Config.RootPassword)
			}
		} else {
			err = fmt.Errorf("root_password required for RESET_PASSWORD")
		}
		state = pb.VMState_VM_STATE_RUNNING
		// A stopped router takes its new password at its next start and stays
		// stopped, as does a stopped Linux VM, so say where it really is.
		if st, serr := libvirt.GetVMStatus(req.VmId); err == nil && serr == nil {
			state = mapVMStatusToPBState(st)
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_ATTACH_ISO:
		err = s.attachISO(req)
		state = pb.VMState_VM_STATE_STOPPED
		// A running VM takes a disc too now, and stays running.
		if st, serr := libvirt.GetVMStatus(req.VmId); err == nil && serr == nil {
			state = mapVMStatusToPBState(st)
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_DETACH_ISO:
		err = libvirt.DetachISOSlot(req.VmId, isoSlot(req.GetConfig()))
		state = pb.VMState_VM_STATE_STOPPED
		if err == nil {
			sweepCustomerISOs(0)
			if st, serr := libvirt.GetVMStatus(req.VmId); serr == nil {
				state = mapVMStatusToPBState(st)
			}
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_REPAIR_CONSOLE:
		// Inject a VNC graphics device into a domain that lacks one (common for
		// imported VMs). Not hot-pluggable, so a running domain is restarted.
		var restarted bool
		restarted, _, err = libvirt.EnsureVNCGraphics(req.VmId)
		// Report the resulting run state: a restart leaves it running; otherwise
		// the state is unchanged, so read it back rather than assume.
		if err == nil {
			if restarted {
				state = pb.VMState_VM_STATE_RUNNING
			} else if st, serr := libvirt.GetVMStatus(req.VmId); serr == nil {
				state = mapVMStatusToPBState(st)
			}
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_RESCUE:
		err = s.enterRescue(req)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_UNRESCUE:
		err = leaveRescue(req.VmId)
		state = pb.VMState_VM_STATE_STOPPED
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown command: %v", req.Command)
	}

	if err != nil {
		log.Printf("[NodeAgent] Failed to execute command %v on VM %s: %v", req.Command, req.VmId, err)
		msg := fmt.Sprintf("Failed to execute command: %v", err)
		// Report where the domain actually ended up, not where the command was
		// trying to take it. `state` is assigned optimistically at the top of
		// each case, so a failed START used to answer RUNNING — which the panel
		// wrote into its own status and its audit log, leaving a record that a
		// start had succeeded when the VM had never booted.
		if actual, serr := libvirt.GetVMStatus(req.VmId); serr == nil {
			state = mapVMStatusToPBState(actual)
		}
		return &pb.VMCommandResponse{
			Success: false,
			VmId:    req.VmId,
			Command: req.Command,
			State:   state,
			Message: msg,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg},
		}, nil
	}

	return &pb.VMCommandResponse{
		Success:       true,
		VmId:          req.VmId,
		Command:       req.Command,
		State:         state,
		Message:       fmt.Sprintf("Command %v executed successfully", req.Command),
		RestoredDisks: restored,
		KeptVolumes:   kept,
	}, nil
}

// primaryBridge returns the first interface's bridge name from a VM command's
// network config, or "" when none is set. Safe to call with a nil config.
func primaryBridge(cfg *pb.VMConfig) string {
	for _, iface := range cfg.GetNetworkConfig().GetInterfaces() {
		if b := iface.GetBridgeName(); b != "" {
			return b
		}
	}
	return ""
}

// cleanStaleExports removes leftover backup/image export files.
//
// Only files older than an hour are touched: an export running right now is a
// legitimate file of the same shape, and deleting one mid-write would turn a
// slow backup into a corrupt one. An agent restart during an export therefore
// leaves the file until the next restart, which is the safe order to get this
// wrong in.
func cleanStaleExports() {
	dirs := map[string]bool{defaultImageDir: true, blockScratchDir(): true}
	if locations, err := libvirt.DiskLocations(); err == nil {
		for path := range locations {
			dirs[path] = true
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	for dir := range dirs {
		// In a shared pool another node's export may be running, which this
		// node cannot tell from a stale one.
		if _, shared := storage.SharedPoolAt(dir); shared {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(dir, "*-backup.qcow2"))
		if err != nil {
			continue
		}
		for _, path := range matches {
			info, err := os.Stat(path)
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(path); err != nil {
				log.Printf("[NodeAgent] WARNING: could not remove stale export %s: %v", path, err)
				continue
			}
			log.Printf("[NodeAgent] removed stale export %s (%d bytes, last written %s)",
				path, info.Size(), info.ModTime().Format(time.RFC3339))
		}
	}
}

// defaultImageDir is where VM disk images are provisioned when the caller names
// no pool. It sits on the node's root filesystem, which is why it is a fallback
// and not a destination anyone should rely on: a node that fills its root
// filesystem loses libvirt, this agent and its logging, not merely the ability
// to create another VM.
const defaultImageDir = "/var/lib/libvirt/images"

// imageDirFor resolves where a VM's disk and seed ISO are provisioned: the
// node's default when the panel names no pool, else the pool, which must be an
// existing directory here. A pool that is missing is refused, never swapped for
// the default (see storage.ResolvePoolDir); the node's preflight report names it.
func imageDirFor(poolPath string) (string, error) {
	return storage.ResolvePoolDir(poolPath, defaultImageDir)
}

// templateCacheDir is where downloaded OS template images are cached on the node
// so repeated VM creations from the same template reuse a single download.
// TEMPLATE_CACHE_DIR moves it off the root filesystem: Windows images are
// tens of GB each.
var templateCacheDir = cacheDirFromEnv("TEMPLATE_CACHE_DIR", "/var/lib/libvirt/templates")

// cacheDirFromEnv reads an absolute directory from the environment, else def.
func cacheDirFromEnv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if !filepath.IsAbs(v) {
		log.Printf("[NodeAgent] WARNING: %s=%q is not an absolute path; using %s", key, v, def)
		return def
	}
	return filepath.Clean(v)
}

// templateCacheMu makes a cache hit and an eviction pass take turns, so a
// template found in the cache is not removed before its use is recorded.
var templateCacheMu sync.Mutex

// templateCacheKeep protects a template used this recently from eviction: a
// download in progress, or a create still copying it.
// ponytail: a time window, not a hold per create; a create that copies for
// longer than this could lose its source. Count holds if that is ever seen.
const templateCacheKeep = 6 * time.Hour

// cachedTemplate reports whether path is in the cache and, if so, records it
// as just used, which is what the eviction order goes by.
func cachedTemplate(path string) bool {
	templateCacheMu.Lock()
	defer templateCacheMu.Unlock()
	if _, err := os.Stat(path); err != nil {
		return false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return true
}

// evictTemplateCache keeps the images this agent downloaded into the template
// cache under TEMPLATE_CACHE_MAX_GB, removing the least recently used ones no
// VM uses. Files placed there by hand are never touched. Without that setting
// the cache grows as it always has.
func evictTemplateCache() {
	maxGB := envPositiveInt("TEMPLATE_CACHE_MAX_GB", 0)
	if maxGB == 0 {
		return
	}
	templateCacheMu.Lock()
	defer templateCacheMu.Unlock()
	var used map[string]bool
	var listed, failed bool
	removed, left := storage.EvictLRU(templateCacheDir, maxGB<<30, templateCacheKeep, func(path string) bool {
		if !listed {
			listed = true
			var err error
			if used, err = libvirt.DiskSourceFiles(); err != nil {
				failed = true
				log.Printf("[NodeAgent] WARNING: template cache eviction skipped, could not list what VMs use: %v", err)
			}
		}
		// Not knowing what is in use means keeping everything.
		return failed || used[path]
	})
	for _, path := range removed {
		log.Printf("[NodeAgent] removed least recently used template %s", path)
	}
	if left > maxGB<<30 && !failed {
		log.Printf("[NodeAgent] template cache holds %d GB, over its %d GB cap: the rest is in use or used in the last %s", left>>30, maxGB, templateCacheKeep)
	}
}

// evictTemplateCacheForever applies the cap now and then for as long as the
// agent runs, on top of the pass after each download.
func evictTemplateCacheForever() {
	for {
		evictTemplateCache()
		time.Sleep(time.Hour)
	}
}

// generateMAC derives a stable, locally-administered MAC address (using the
// QEMU OUI 52:54:00) from the VM ID, so the NIC in the domain XML and the
// cloud-init network-config refer to the same interface.
func generateMAC(vmID string) string { return models.DerivedMAC(vmID, 0) }

// injectGuestConfig writes the guest's network + hostname (and optional root
// password / SSH key) directly into its disk image using libguestfs
// (virt-customize), so the guest comes up configured on first boot WITHOUT
// relying on cloud-init running inside it.
//
// The static IP is written as a systemd-networkd .network file matched by MAC —
// systemd-networkd is present and enabled on Debian/Ubuntu cloud images (and
// most systemd distros), and matching by MAC binds the correct NIC regardless of
// its kernel name. When no static IP is configured, only hostname/password/SSH
// are applied and the guest keeps DHCP.
const (
	guestNetworkFilePath  = "/etc/systemd/network/10-maburvm.network"
	guestAnnounceUnitPath = "/etc/systemd/system/maburvm-announce.service"
)

// guestIPv6 is the routed IPv6 a guest is given: its own address in CIDR form
// ("2001:db8:20:5::1/64"), the link-local gateway and resolvers. A zero value
// means no IPv6 and leaves the rendered config exactly as before.
type guestIPv6 struct {
	Address string
	Gateway string
	DNS     []string
	// Extra are further addresses in the same prefix, without a length.
	Extra []string `json:",omitempty"`
}

// onLinkIPv6Prefix returns the interface's IPv6 address as a /128 when it sits
// on a shared subnet (an address but no routed prefix); empty otherwise.
func onLinkIPv6Prefix(iface *pb.NetworkInterface) string {
	if iface == nil || iface.Ipv6Prefix != "" || iface.Ipv6Address == "" {
		return ""
	}
	host := strings.SplitN(iface.Ipv6Address, "/", 2)[0]
	if ip := net.ParseIP(host); ip == nil || ip.To4() != nil {
		return ""
	}
	return host + "/128"
}

func guestIPv6From(address, gateway string, dns []string) guestIPv6 {
	if address == "" {
		return guestIPv6{}
	}
	if gateway == "" {
		gateway = models.IPv6LinkLocalGateway
	}
	if len(dns) == 0 {
		dns = models.DefaultIPv6DNS
	}
	return guestIPv6{Address: address, Gateway: gateway, DNS: dns}
}

// guestNet is all the addressing a guest is given: its primary IPv4 with the
// gateway, any further IPv4 addresses on the same card, and its IPv6.
type guestNet struct {
	IP      string              `json:"ip"`
	Prefix  int                 `json:"prefix"`
	Gateway string              `json:"gateway,omitempty"`
	Extra   []guestos.GuestAddr `json:"extra,omitempty"`
	V6      guestIPv6           `json:"v6"`
	// DNS are the resolvers; empty keeps the built-in ones.
	DNS []string `json:"dns,omitempty"`
	// NICs are the guest's further cards with the addresses on each.
	NICs []guestos.GuestNIC `json:"nics,omitempty"`
}

func linuxNetwork(mac string, n guestNet) guestos.LinuxNetwork {
	return guestos.LinuxNetwork{
		MAC: mac, Address: n.IP, Prefix: n.Prefix, Gateway: n.Gateway, Extra: n.Extra,
		IPv6Address: n.V6.Address, IPv6Gateway: n.V6.Gateway, IPv6DNS: n.V6.DNS, IPv6Extra: n.V6.Extra,
		DNS: n.DNS, NICs: n.NICs,
	}
}

// guestNetworkFile is the systemd-networkd unit giving a guest one address.
func guestNetworkFile(mac, ip string, prefix int, gateway string, v6 guestIPv6, dns []string) string {
	return guestos.NetworkdUnit(linuxNetwork(mac, guestNet{IP: ip, Prefix: prefix, Gateway: gateway, V6: v6, DNS: dns}))
}

// guestAnnounceUnit makes the guest announce its address after boot.
func guestAnnounceUnit(gateway string) string { return guestos.AnnounceUnit(gateway) }

// guestNotApplied says the guest kept its old addresses, and why, in words
// for the VM's page. Pending ones are retried by the agent: when the guest
// agent answers, or when the VM is next started from the panel.
type guestNotApplied struct {
	msg     string
	pending bool
	// applied: the change was made as far as it can be from outside; msg
	// says what the owner sets by hand.
	applied bool
}

func (e *guestNotApplied) Error() string { return e.msg }

// applyGuestNetwork makes the guest OS use n on its primary NIC — the in-guest
// half of an address change, the host rules being the other.
//
// A running guest is changed live through its guest agent, which gets wait to
// answer. A running guest that does not answer is never restarted for this:
// the change is left pending (guestNotApplied) and applied when the guest
// agent answers or the VM is next started. A stopped Linux guest has the
// config written onto its disk when it uses systemd-networkd (everything this
// agent provisions), and is otherwise changed live at its next start; a
// stopped router gets it as a start-up script. A running Linux guest is
// changed through whichever network configuration it uses (see
// guestos.LinuxNetworkScript).
//
// Calls for the same VM are serialized, and nothing is done once ctx is done.
func (s *NodeAgentService) applyGuestNetwork(ctx context.Context, vmID string, n guestNet, wait time.Duration) error {
	lock, _ := guestNetworkLocks.LoadOrStore(vmID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	switch libvirt.GuestOSType(vmID) {
	case guestos.RouterOS:
		script, err := guestos.RouterOSNetworkScript(routerOSNetworkFor(n))
		if err != nil {
			return err
		}
		if err := s.runRouterOSScript(ctx, vmID, script, false); err != nil {
			return err
		}
		return cardsLeftToOwner(n)
	case guestos.Windows:
		if err := applyWindowsNetwork(ctx, vmID, n, wait); err != nil {
			return err
		}
		return cardsLeftToOwner(n)
	}

	mac, err := libvirt.GetVMInterfaceMAC(vmID)
	if err != nil {
		return err
	}
	info, err := libvirt.GetVMInfo(vmID)
	if err != nil {
		return err
	}
	ln := linuxNetwork(mac, n)

	if info.Status == libvirt.VMStatusRunning {
		script, err := guestos.LinuxNetworkScript(ln)
		if err != nil {
			return err
		}
		// A guest that booted moments ago has no guest agent answering yet.
		out, err := guestExec(info.Name, script, 90*time.Second)
		var exited *guestScriptError
		for deadline := time.Now().Add(wait); err != nil && !errors.As(err, &exited) && time.Now().Before(deadline); {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			out, err = guestExec(info.Name, script, 90*time.Second)
		}
		switch {
		case err == nil:
			log.Printf("[NodeAgent] guest %s reconfigured live to %s/%d (+%d, %d more cards) via guest agent (%s)", vmID, n.IP, n.Prefix, len(n.Extra), len(n.NICs), firstLine(out))
			return nil
		case errors.As(err, &exited) && exited.code == guestos.ExitUnknownStack:
			return &guestNotApplied{msg: "The guest's network is set up by something the panel does not change (it knows systemd-networkd, netplan, ifupdown, NetworkManager and network-scripts), so its addresses were not changed. Set them inside the guest."}
		case errors.As(err, &exited) && exited.code == guestos.ExitNotApplied:
			log.Printf("[NodeAgent] guest %s (%s) did not take its new addresses: %s", vmID, firstLine(exited.stdout), exited.stderr)
			return &guestNotApplied{msg: "The guest did not take its new network settings (" + exited.stderr + "). " +
				"Restart the guest's networking or reboot it from inside; nothing was restarted."}
		case errors.As(err, &exited):
			// It ran: running it again could bounce the guest's network each time.
			log.Printf("[NodeAgent] guest %s network script ended without finishing (%d): %s", vmID, exited.code, exited.stderr)
			return &guestNotApplied{msg: "The guest's network change did not finish (" + exited.stderr + "). " +
				"Check the guest's network settings from its console; nothing was restarted."}
		case ctx.Err() != nil:
			return ctx.Err()
		}
		log.Printf("[NodeAgent] guest agent of VM %s did not answer (%v); address change left pending, VM left running", vmID, err)
		return &guestNotApplied{pending: true, msg: "The guest agent did not answer, so the guest still uses its old addresses. " +
			"Stop and start the VM from the panel to apply the change; it is also applied as soon as the guest agent answers. Nothing was restarted."}
	}

	diskPath, err := libvirt.GetVMPrimaryDiskPath(vmID)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("virt-customize"); err != nil {
		return fmt.Errorf("virt-customize not found (install libguestfs-tools): %w", err)
	}
	// Written on the disk only for a guest that uses systemd-networkd. Any
	// other is changed through its own configuration once it runs: written
	// here, networkd would start beside that one on the same card.
	if networkd, perr := guestos.DiskUsesNetworkd(diskPath); perr != nil {
		return perr
	} else if !networkd {
		return &guestNotApplied{pending: true, msg: "The VM is off and its guest does not use systemd-networkd, so its new addresses are set when it is next started from the panel and its guest agent answers."}
	}
	if err := ln.Validate(); err != nil {
		return err
	}
	args := append(guestos.DiskArgs(diskPath), "--no-network",
		"--mkdir", "/etc/systemd/network",
		"--run-command", "rm -f /etc/systemd/network/11-maburvm-*.network",
		"--write", guestNetworkFilePath+":"+guestos.NetworkdUnit(ln),
		"--chmod", "0644:"+guestNetworkFilePath,
		"--write", guestAnnounceUnitPath+":"+guestos.AnnounceUnit(n.Gateway),
	)
	for i, c := range ln.NICs {
		f := fmt.Sprintf("/etc/systemd/network/11-maburvm-%d.network", i+1)
		args = append(args, "--write", f+":"+guestos.NetworkdNICUnit(c), "--chmod", "0644:"+f)
	}
	args = append(args, "--run-command", "systemctl enable systemd-networkd maburvm-announce.service >/dev/null 2>&1 || true")
	if out, err := exec.Command("virt-customize", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("virt-customize: %w: %s", err, strings.TrimSpace(string(out)))
	}
	log.Printf("[NodeAgent] guest %s reconfigured on disk to %s/%d (+%d, %d more cards)", vmID, n.IP, n.Prefix, len(n.Extra), len(n.NICs))
	return nil
}

// cardsLeftToOwner says which addresses a router or Windows guest must be
// given by hand: only its first card is set up from outside, and without the
// further IPv6 addresses of its prefix.
func cardsLeftToOwner(n guestNet) error {
	var parts []string
	for _, c := range n.NICs {
		for _, a := range c.Addresses {
			parts = append(parts, fmt.Sprintf("%s/%d on the card with MAC %s", a.Address, a.Prefix, c.MAC))
		}
	}
	for _, a := range n.V6.Extra {
		parts = append(parts, a+" on the first card")
	}
	if len(parts) == 0 {
		return nil
	}
	return &guestNotApplied{applied: true, msg: "The first card was set up. Set " + strings.Join(parts, ", ") + " inside the guest."}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// applyWindowsNetwork re-addresses a running Windows guest through its guest
// agent. A Windows disk is not edited from outside, so a stopped VM, or one
// whose agent does not answer within wait, is left pending.
func applyWindowsNetwork(ctx context.Context, vmID string, n guestNet, wait time.Duration) error {
	mac, err := libvirt.GetVMInterfaceMAC(vmID)
	if err != nil {
		return err
	}
	info, err := libvirt.GetVMInfo(vmID)
	if err != nil {
		return err
	}
	script, err := guestos.WindowsNetworkScript(guestos.WindowsNetwork{
		MAC: mac, Address: n.IP, Prefix: n.Prefix, Gateway: n.Gateway, Extra: n.Extra,
		IPv6Address: n.V6.Address, IPv6Gateway: n.V6.Gateway, IPv6DNS: n.V6.DNS, DNS: n.DNS,
	})
	if err != nil {
		return err
	}
	if info.Status != libvirt.VMStatusRunning {
		return &guestNotApplied{pending: true, msg: "Windows is off, so it keeps its old addresses until it is started and its guest agent answers."}
	}
	_, err = guestos.WindowsExec(info.Name, script, 60*time.Second)
	for deadline := time.Now().Add(wait); err != nil && ctx.Err() == nil && time.Now().Before(deadline); {
		time.Sleep(5 * time.Second)
		_, err = guestos.WindowsExec(info.Name, script, 60*time.Second)
	}
	if err != nil {
		log.Printf("[NodeAgent] Windows guest %s not reconfigured: %v", vmID, err)
		return &guestNotApplied{pending: true, msg: "Windows' guest agent did not answer, so it still uses its old addresses. " +
			"It is applied as soon as the guest agent answers; or set the addresses in Windows' network settings. Nothing was restarted."}
	}
	log.Printf("[NodeAgent] Windows guest %s reconfigured live to %s/%d (+%d)", vmID, n.IP, n.Prefix, len(n.Extra))
	return nil
}

// routerOSNetworkFor carries a guest's addressing into the router script form.
func routerOSNetworkFor(n guestNet) guestos.RouterOSNetwork {
	r := routerOSNetwork(n.IP, n.Prefix, n.Gateway, n.V6)
	r.Extra, r.DNS = n.Extra, n.DNS
	return r
}

// routerOSNetwork carries a VM's addressing into the form the router scripts take.
func routerOSNetwork(ip string, prefix int, gateway string, v6 guestIPv6) guestos.RouterOSNetwork {
	return guestos.RouterOSNetwork{
		Address: ip, Prefix: prefix, Gateway: gateway,
		IPv6Address: v6.Address, IPv6Gateway: v6.Gateway, IPv6DNS: v6.DNS,
	}
}

// runRouterOSScript applies script to a RouterOS guest by whichever route it
// accepts.
//
// A running router is asked through its guest agent first, which takes effect
// at once. The agent may refuse (version 7 does not let it change users) or
// not answer; then, when restart allows it, the script goes onto the disk to be
// run at the next boot and the router is restarted so that boot happens now.
// Without restart the change is left pending (guestNotApplied). A stopped
// router is left stopped and picks the script up when it is started.
//
// As with a Linux guest, nothing is shut down once ctx is done.
func (s *NodeAgentService) runRouterOSScript(ctx context.Context, vmID, script string, restart bool) error {
	info, err := libvirt.GetVMInfo(vmID)
	if err != nil {
		return err
	}
	running := info.Status == libvirt.VMStatusRunning
	if running {
		_, liveErr := guestos.RouterOSExec(info.Name, script, 30*time.Second)
		if liveErr == nil {
			log.Printf("[NodeAgent] router %s reconfigured live through its guest agent", vmID)
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("the router did not accept the change live and the caller gave up; VM left running: %w", ctx.Err())
		}
		if !restart {
			log.Printf("[NodeAgent] router %s did not take the change live (%v); left pending, router left running", vmID, liveErr)
			return &guestNotApplied{pending: true, msg: "The router did not take the change live, so it still uses its old addresses. " +
				"Stop and start it from the panel to apply the change at boot; nothing was restarted."}
		}
		log.Printf("[NodeAgent] router %s did not take the change live (%v); applying it from disk with a restart", vmID, liveErr)
		if err := libvirt.StopVM(vmID, false); err != nil {
			return fmt.Errorf("shutdown before reconfigure: %w", err)
		}
		// From here the VM is going down, so it must come back up whatever happens.
		defer func() {
			if err := startVM(vmID); err != nil {
				log.Printf("[NodeAgent] WARNING: failed to start router %s after reconfigure: %v", vmID, err)
			}
		}()
		if !waitVMStatus(vmID, libvirt.VMStatusStopped, 60*time.Second) {
			if err := libvirt.StopVM(vmID, true); err != nil {
				return fmt.Errorf("force stop before reconfigure: %w", err)
			}
			waitVMStatus(vmID, libvirt.VMStatusStopped, 15*time.Second)
		}
	}
	diskPath, err := libvirt.GetVMPrimaryDiskPath(vmID)
	if err != nil {
		return err
	}
	if err := guestos.WriteRouterOSAutorun(diskPath, script); err != nil {
		return err
	}
	log.Printf("[NodeAgent] router %s takes its change at this boot (was running: %v)", vmID, running)
	return nil
}

// guestNetworkLocks holds one *sync.Mutex per VM ID for applyGuestNetwork.
var guestNetworkLocks sync.Map

// guestAgentWait is how long a running guest's qemu-guest-agent gets to answer
// before an address change is left pending.
const guestAgentWait = 60 * time.Second

// errDisksBusy refuses to boot a VM, or to start other work on its disks,
// while a disk move, copy or restore holds them (the migrations registry).
var errDisksBusy = errors.New("a disk of this VM is being moved, copied or restored; try again when that has finished")

// startVM boots a VM unless work on its disks holds it. Every start of an
// existing VM goes through it.
func startVM(vmID string) error {
	if migration.IsFenced(vmID) {
		return errFenced
	}
	if migrations.Busy(vmID) {
		return errDisksBusy
	}
	return libvirt.StartVM(vmID)
}

// gracefulStopWait is the longest a graceful stop waits for the guest to power off.
const gracefulStopWait = 2 * time.Minute

// stopWait is how long a graceful stop may wait: gracefulStopWait, cut short
// so the answer still reaches a caller with an earlier deadline.
func stopWait(ctx context.Context) time.Duration {
	wait := gracefulStopWait
	if dl, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(dl)-3*time.Second)
	}
	return max(wait, 0)
}

// waitShutOff waits for a VM asked to shut down to be shut off, for at most
// stopWait. A guest that has not powered off by then is an error, and the
// caller reports where the VM really is.
func waitShutOff(ctx context.Context, vmID string, state func(string) (libvirt.VMStatus, error)) error {
	wait := stopWait(ctx)
	deadline := time.Now().Add(wait)
	for {
		if st, err := state(vmID); err == nil && st == libvirt.VMStatusStopped {
			return nil
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return fmt.Errorf("the guest was asked to shut down and is still on after %s: it may still be shutting down, or it ignores the request (force stop powers it off)", wait.Round(time.Second))
		}
		time.Sleep(time.Second)
	}
}

// waitVMStatus polls until the VM reports want or timeout elapses.
func waitVMStatus(vmID string, want libvirt.VMStatus, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := libvirt.GetVMInfo(vmID); err == nil && info.Status == want {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// guestScriptError is a script that ran in the guest and exited non-zero.
type guestScriptError struct {
	code           int
	stdout, stderr string
}

func (e *guestScriptError) Error() string {
	return fmt.Sprintf("guest script exited %d: %s", e.code, e.stderr)
}

// guestExec runs script with /bin/sh inside the guest through qemu-guest-agent,
// waits for it to exit and returns what it printed. A script that ran and
// failed is a *guestScriptError; any other failure (no agent, agent not
// connected, no answer in time) is a plain error, worth retrying.
func guestExec(domainName, script string, timeout time.Duration) (string, error) {
	req, _ := json.Marshal(map[string]interface{}{
		"execute": "guest-exec",
		"arguments": map[string]interface{}{
			"path": "/bin/sh", "arg": []string{"-c", script}, "capture-output": true,
		},
	})
	out, err := exec.Command("virsh", "qemu-agent-command", domainName, string(req)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("guest-exec: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var started struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal(out, &started); err != nil || started.Return.PID == 0 {
		return "", fmt.Errorf("guest-exec: unexpected reply %s", strings.TrimSpace(string(out)))
	}
	statusReq, _ := json.Marshal(map[string]interface{}{
		"execute": "guest-exec-status", "arguments": map[string]int{"pid": started.Return.PID},
	})
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		out, err := exec.Command("virsh", "qemu-agent-command", domainName, string(statusReq)).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("guest-exec-status: %w: %s", err, strings.TrimSpace(string(out)))
		}
		var st struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
				ErrData  string `json:"err-data"`
			} `json:"return"`
		}
		if err := json.Unmarshal(out, &st); err != nil {
			return "", fmt.Errorf("guest-exec-status: unexpected reply %s", strings.TrimSpace(string(out)))
		}
		if st.Return.Exited {
			printed, _ := base64.StdEncoding.DecodeString(st.Return.OutData)
			failed, _ := base64.StdEncoding.DecodeString(st.Return.ErrData)
			if st.Return.ExitCode != 0 {
				return string(printed), &guestScriptError{code: st.Return.ExitCode, stdout: string(printed), stderr: strings.TrimSpace(string(failed))}
			}
			return string(printed), nil
		}
	}
	// The script started, so this is not a guest agent that did not answer.
	return "", &guestScriptError{code: -1, stderr: fmt.Sprintf("it was still running after %s", timeout)}
}

func injectGuestConfig(diskPath, hostname string, vmCfg libvirt.VMConfig, rootPassword, sshKey, userData string) error {
	if _, err := exec.LookPath("virt-customize"); err != nil {
		return fmt.Errorf("virt-customize not found (install libguestfs-tools): %w", err)
	}

	// --no-network: the customize appliance needs no outbound network.
	args := append(guestos.DiskArgs(diskPath), "--no-network")

	// Generate SSH host keys offline. Cloud images ship WITHOUT host keys and
	// delegate generation to cloud-init's once-per-instance ssh module — which the
	// baked NoCloud seed in these images makes cloud-init SKIP on clones (same
	// trap as the first-boot recipe below). On Ubuntu 24.04 sshd then accepts the
	// TCP connection and immediately resets it, so every SSH attempt fails until
	// someone runs `ssh-keygen -A` by hand on the console. ssh-keygen -A only
	// creates keys that are missing, so this is a no-op on images that have them.
	args = append(args, "--run-command", "ssh-keygen -A")
	// ...and stop cloud-init from UNDOING that: with the default
	// ssh_deletekeys=true, a first boot under a fresh instance-id DELETES the
	// host keys and regenerates them — and the post-provision hard power-cycle
	// (~40s into first boot) interrupts that regeneration, leaving zero-byte
	// key files that sshd rejects forever (second boot skips the module, so it
	// never self-heals). Seen on Ubuntu 24.04 and Debian 13 images; older
	// images skip the module entirely via their baked NoCloud seed.
	args = append(args,
		"--mkdir", "/etc/cloud/cloud.cfg.d",
		"--write", "/etc/cloud/cloud.cfg.d/99-maburvm-ssh.cfg:ssh_deletekeys: false\nssh_genkeytypes: []\n")

	if hostname != "" {
		args = append(args, "--hostname", hostname)
	}
	if rootPassword != "" {
		args = append(args, "--root-password", "password:"+rootPassword)
		// Debian/Ubuntu cloud images ship PasswordAuthentication no and
		// PermitRootLogin prohibit-password (in /etc/ssh/sshd_config.d/50-cloud-init.conf),
		// so an injected root password is useless over SSH — login fails with
		// "Permission denied (publickey)". sshd uses the FIRST value it sees for a
		// keyword and reads sshd_config.d/*.conf in alphabetical order, so a drop-in
		// sorted BEFORE 50-cloud-init.conf (00-...) wins. Enable root password login
		// only when a password is actually set (SSH-key-only VMs stay key-only).
		// --mkdir first: virt-customize aborts the WHOLE customize run if a
		// --write lands in a directory that doesn't exist, and sshd_config.d is
		// absent on images that never had cloud-init. The abort takes every later
		// step with it, so one missing directory silently costs the VM its SSH key
		// and its static addressing.
		args = append(args,
			"--mkdir", "/etc/ssh/sshd_config.d",
			"--write", "/etc/ssh/sshd_config.d/00-maburvm.conf:PermitRootLogin yes\nPasswordAuthentication yes\n")
	}
	if key := strings.TrimSpace(sshKey); key != "" {
		args = append(args, "--ssh-inject", "root:string:"+key)
	}

	var tmpNet string
	if vmCfg.IPAddress != "" && vmCfg.Netmask > 0 {
		f, err := os.CreateTemp("", "maburvm-*.network")
		if err != nil {
			return fmt.Errorf("failed to stage network config: %w", err)
		}
		tmpNet = f.Name()
		if _, err := f.WriteString(guestNetworkFile(vmCfg.MACAddress, vmCfg.IPAddress, vmCfg.Netmask, vmCfg.Gateway,
			guestIPv6From(vmCfg.IPv6Address, vmCfg.IPv6Gateway, vmCfg.IPv6DNS), vmCfg.DNS)); err != nil {
			f.Close()
			os.Remove(tmpNet)
			return fmt.Errorf("failed to write staged network config: %w", err)
		}
		f.Close()

		args = append(args,
			"--mkdir", "/etc/systemd/network",
			"--upload", tmpNet+":/etc/systemd/network/10-maburvm.network",
			// systemd-networkd runs as the unprivileged systemd-network user and
			// REFUSES to read a .network file it can't open — the uploaded temp file
			// is mode 0600 (root only), which yields "Permission denied" and the
			// guest silently falls back to the image's DHCP netplan. Make it 0644.
			"--chmod", "0644:/etc/systemd/network/10-maburvm.network",
			// Ensure the renderer is enabled (already so on cloud images; harmless otherwise).
			"--run-command", "systemctl enable systemd-networkd >/dev/null 2>&1 || true",
			// CRITICAL: stop cloud-init from ALSO configuring the network. The seed's
			// network-config matches the same NIC by MAC and does `set-name: eth0` +
			// its own addresses, so on FIRST boot cloud-init renders a competing
			// netplan/networkd config and renames the interface — racing our
			// 10-maburvm.network and leaving the link flapping between states, which
			// the upstream gateway caches wrong → the VM is unreachable until a reboot
			// (when cloud-init no longer touches the network). Disabling cloud-init's
			// network module makes our injected config the sole authority from the
			// very first boot. (This was the real cause of "new VM not reachable until
			// rebooted".)
			// Same trap as sshd_config.d, and this one has already bitten: a guest
			// with no cloud-init has no /etc/cloud/cloud.cfg.d, so this write
			// aborted the run — after the network file was uploaded, but before
			// anything that came later. Creating the directory is harmless on a
			// guest without cloud-init: nothing reads it.
			"--mkdir", "/etc/cloud/cloud.cfg.d",
			"--write", "/etc/cloud/cloud.cfg.d/99-maburvm-network.cfg:network: {config: disabled}\n")

		// Populate the upstream gateway's ARP cache as soon as the guest is online.
		// A brand-new VM that sends no outbound traffic on first boot never teaches
		// the gateway its MAC, so if the gateway still has a stale ARP entry for this
		// IP (from a previously-assigned VM) the VM is unreachable from the internet
		// until that entry ages out (minutes). Pinging the gateway makes the guest
		// emit an ARP request whose sender fields the gateway learns immediately.
		if vmCfg.Gateway != "" {
			// NOTE: deliberately NOT ordered After=network-online.target — on these
			// cloud images that target often never activates (networkd-wait-online
			// isn't enabled), which would leave the unit stuck and never run. Instead
			// it starts with multi-user.target and loops long enough for the network
			// to come up on its own.
			args = append(args,
				"--write", guestAnnounceUnitPath+":"+guestAnnounceUnit(vmCfg.Gateway),
				"--run-command", "systemctl enable maburvm-announce.service >/dev/null 2>&1 || true")
		}
	}
	if tmpNet != "" {
		defer os.Remove(tmpNet)
	}

	// First-boot recipe: run the user's script exactly once, AFTER the network is
	// up, via a self-disabling systemd oneshot. We do NOT rely on cloud-init for
	// this: Debian/Ubuntu cloud images ship a baked NoCloud seed
	// (/var/lib/cloud/seed/nocloud, instance-id "nocloud") that makes cloud-init
	// skip once-per-instance modules (write_files/scripts-user) on every clone, so
	// the recipe delivered via the cloud-init seed silently never runs. Injecting
	// it as a systemd unit is reliable and image-independent (same approach as the
	// rest of injectGuestConfig).
	var tmpRecipe string
	if s := strings.TrimSpace(userData); strings.HasPrefix(s, "#!") {
		f, err := os.CreateTemp("", "maburvm-recipe-*.sh")
		if err != nil {
			return fmt.Errorf("failed to stage recipe: %w", err)
		}
		tmpRecipe = f.Name()
		if _, err := f.WriteString(userData); err != nil {
			f.Close()
			os.Remove(tmpRecipe)
			return fmt.Errorf("failed to write staged recipe: %w", err)
		}
		f.Close()
		defer os.Remove(tmpRecipe)

		// NOT ordered After=network-online.target (it may never activate on these
		// images). Start with multi-user.target and wait for real connectivity in
		// the wrapper before running the user's script, so apt has network.
		unit := "[Unit]\n" +
			"Description=MaburVM first-boot recipe\n" +
			"ConditionPathExists=!/var/lib/maburvm/recipe.done\n\n" +
			"[Service]\n" +
			"Type=oneshot\n" +
			"RemainAfterExit=yes\n" +
			"TimeoutStartSec=1800\n" +
			"ExecStart=/bin/sh -c 'for i in $(seq 1 60); do ping -c1 -w2 1.1.1.1 >/dev/null 2>&1 && break; sleep 2; done; /usr/local/sbin/maburvm-recipe.sh'\n" +
			"ExecStartPost=/bin/sh -c 'mkdir -p /var/lib/maburvm && touch /var/lib/maburvm/recipe.done'\n" +
			"ExecStartPost=/bin/systemctl disable maburvm-recipe.service\n\n" +
			"[Install]\nWantedBy=multi-user.target\n"
		args = append(args,
			"--upload", tmpRecipe+":/usr/local/sbin/maburvm-recipe.sh",
			"--chmod", "0755:/usr/local/sbin/maburvm-recipe.sh",
			"--write", "/etc/systemd/system/maburvm-recipe.service:"+unit,
			"--run-command", "systemctl enable maburvm-recipe.service >/dev/null 2>&1 || true")
	}

	out, err := exec.Command("virt-customize", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("virt-customize failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	log.Printf("[NodeAgent] injected guest config into %s (ip=%s bridge-mac=%s)", diskPath, vmCfg.IPAddress, vmCfg.MACAddress)
	return nil
}

// resolveTemplateImage returns a local path to the template image. If ref is an
// HTTP(S) URL (e.g. a catalog cloud-image), the image is downloaded into the
// node's template cache once and the cached path is returned. Otherwise ref is
// treated as an existing local file path. wantSHA, when set, is the sha256 a
// download must have: it is checked before the image is kept, and names the
// cached copy, so a refreshed image is downloaded again beside the old one.
func resolveTemplateImage(ref string, store *pb.ObjectStore, wantSHA string) (string, error) {
	// vm://... resolves to a source VM's primary disk for the clone flow (source
	// must be stopped for a consistent copy):
	//   vm://<vmId>            → that VM's disk on THIS node (same-node clone)
	//   vm://<srcNodeIP>/<vmId> → pulled from the source node over the migration link
	if strings.HasPrefix(ref, "vm://") {
		spec := strings.TrimPrefix(ref, "vm://")
		if i := strings.IndexByte(spec, '/'); i >= 0 {
			srcIP, srcID := spec[:i], spec[i+1:]
			if net.ParseIP(srcIP) == nil || !migration.ValidUUID(srcID) {
				return "", fmt.Errorf("unusable clone source %q", ref)
			}
			// The source node finds the disk wherever its pool is, and hands it
			// over only while the VM is shut off; the link checks its host key.
			dstPath := filepath.Join(templateCacheDir, "clone-"+srcID+".qcow2")
			if err := os.MkdirAll(templateCacheDir, 0o755); err != nil {
				return "", fmt.Errorf("failed to create cache dir %s: %w", templateCacheDir, err)
			}
			log.Printf("[NodeAgent] clone: pulling the disk of %s from %s", srcID, srcIP)
			// Its own name: two copies of the same VM at once must not share a file.
			part := fmt.Sprintf("%s.%d.part", dstPath, time.Now().UnixNano())
			if _, err := pullDisk(context.Background(), migrationLayout(), srcIP, srcID, migration.PrimaryDrive, part, false, nil); err != nil {
				_ = os.Remove(part)
				return "", fmt.Errorf("failed to pull the source disk from %s: %w", srcIP, err)
			}
			// Its format is not known here and it is probed from now on.
			info, err := exec.Command("qemu-img", "info", "--output=json", part).Output()
			if err == nil {
				err = storage.StandaloneImage(info)
			}
			if err != nil {
				_ = os.Remove(part)
				return "", fmt.Errorf("the disk pulled from %s cannot be copied: %w", srcIP, err)
			}
			if err := os.Rename(part, dstPath); err != nil {
				return "", err
			}
			return dstPath, nil
		}
		// Ask libvirt where the source disk is; it is in whichever pool the VM
		// was placed in, which is often not the default directory.
		path, perr := libvirt.PrimaryDiskPath(spec)
		if perr != nil {
			path = filepath.Join(defaultImageDir, spec+".qcow2")
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("source VM disk not found at %s: %w", path, err)
		}
		return path, nil
	}

	// s3://<objectKey> resolves to a stored image in object storage (the
	// create-from-image / Vultr-style snapshot flow). Download it into the node's
	// template cache once, keyed by the object key, then clone as usual.
	if strings.HasPrefix(ref, "s3://") {
		key := strings.TrimPrefix(ref, "s3://")
		if key == "" {
			return "", fmt.Errorf("empty object key in image reference %q", ref)
		}
		client, _, err := objectStoreClient(store)
		if err != nil {
			return "", fmt.Errorf("object storage not configured for image source: %w", err)
		}
		sum := sha256.Sum256([]byte(key))
		cachePath := filepath.Join(templateCacheDir, fmt.Sprintf("img-%x.qcow2", sum[:8]))
		if cachedTemplate(cachePath) {
			return cachePath, nil
		}
		if err := os.MkdirAll(templateCacheDir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create template cache dir %s: %w", templateCacheDir, err)
		}
		log.Printf("[NodeAgent] Downloading image %s -> %s", key, cachePath)
		rc, err := client.Download(context.Background(), key)
		if err != nil {
			return "", fmt.Errorf("failed to download image %s: %w", key, err)
		}
		defer rc.Close()
		// Write to a temp file first so a partial download never gets cached as a
		// valid image.
		tmp := cachePath + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			return "", fmt.Errorf("failed to create image cache file: %w", err)
		}
		if _, err := io.Copy(f, rc); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return "", fmt.Errorf("failed to write image cache file: %w", err)
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("failed to flush image cache file: %w", err)
		}
		if err := os.Rename(tmp, cachePath); err != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("failed to finalize image cache file: %w", err)
		}
		log.Printf("[NodeAgent] Cached image at %s", cachePath)
		evictTemplateCache()
		return cachePath, nil
	}

	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		if sha, name, ok := models.SplitUploadImageRef(ref); ok {
			p := filepath.Join(templateCacheDir, storage.UploadCacheName(sha, name))
			if _, err := os.Stat(p); err != nil {
				return "", fmt.Errorf("the image %s has not been copied to this node yet; sync it to the node from the panel", name)
			}
			return p, nil
		}
		if _, err := os.Stat(ref); err != nil {
			return "", fmt.Errorf("template image not found at %s: %w", ref, err)
		}
		return ref, nil
	}

	wantSHA = strings.ToLower(strings.TrimSpace(wantSHA))
	if wantSHA != "" && !models.ValidSHA256(wantSHA) {
		return "", fmt.Errorf("the image checksum %q is not a sha256", wantSHA)
	}
	// A stable name per source (and checksum) maps every create to one file.
	cachePath := filepath.Join(templateCacheDir, storage.TemplateCacheName(ref, wantSHA))

	// Reuse the cached image if it was already downloaded.
	if cachedTemplate(cachePath) {
		return cachePath, nil
	}

	// The downloader writes a temp file into the cache dir, so it must exist first.
	if err := os.MkdirAll(templateCacheDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create template cache dir %s: %w", templateCacheDir, err)
	}

	log.Printf("[NodeAgent] Downloading template image %s -> %s", ref, cachePath)
	// Download beside the final name and move it there only when it is ready:
	// a download that is still being unpacked must never be picked up as a
	// cached image by another create.
	partPath := cachePath + ".part"
	if err := storage.NewTemplateManager().DownloadTemplate(ref, partPath); err != nil {
		return "", fmt.Errorf("failed to download template image from %s: %w", ref, err)
	}
	// The checksum is of the file as published, before any unpacking.
	if wantSHA != "" {
		if err := storage.VerifySHA256(partPath, wantSHA); err != nil {
			_ = os.Remove(partPath)
			return "", fmt.Errorf("template image from %s: %w", ref, err)
		}
	}
	if err := storage.UnpackIfZip(partPath); err != nil {
		_ = os.Remove(partPath)
		return "", fmt.Errorf("template image from %s: %w", ref, err)
	}
	if err := os.Rename(partPath, cachePath); err != nil {
		_ = os.Remove(partPath)
		return "", fmt.Errorf("failed to store template image: %w", err)
	}
	log.Printf("[NodeAgent] Cached template image at %s", cachePath)
	if wantSHA != "" {
		evictStaleTemplateCopies(ref, filepath.Base(cachePath))
	}
	evictTemplateCache()
	return cachePath, nil
}

// evictStaleTemplateCopies removes the copies of a template cached under an
// earlier checksum once a new one is in, unless a VM uses one (as a disc) or
// one was used lately (a create may still be copying it). VMs built from an
// old copy are independent of it.
func evictStaleTemplateCopies(ref, keep string) {
	templateCacheMu.Lock()
	defer templateCacheMu.Unlock()
	stale := storage.StaleTemplateCopies(templateCacheDir, ref, keep)
	if len(stale) == 0 {
		return
	}
	// Backing chains count: a disk made by an older agent may be built on a
	// cached image rather than copied from it.
	used, err := usedPaths(context.Background())
	if err != nil {
		log.Printf("[NodeAgent] WARNING: old copies of %s kept, could not list what VMs use: %v", ref, err)
		return
	}
	for _, p := range stale {
		info, err := os.Stat(p)
		if err != nil || used[p] || time.Since(info.ModTime()) < templateCacheKeep {
			continue
		}
		if err := os.Remove(p); err == nil {
			log.Printf("[NodeAgent] removed %s: superseded by a refreshed copy of %s", p, ref)
		}
	}
}

// createVM provisions a VM disk from the OS template image and defines a libvirt
// domain for it. The domain is created in the stopped state; a subsequent START
// command boots it. Network settings (bridge, MAC, VLAN, bandwidth) are embedded
// in the domain XML so libvirt enforces them when the VM starts.
// createsInFlight records the VMs whose CREATE is running on this node right now.
//
// In memory, like the export registry: a create cannot outlive the process that
// is running it, so a record that survived a restart would only ever describe
// work that has stopped.
var createsInFlight = struct {
	sync.Mutex
	ids map[string]bool
}{ids: map[string]bool{}}

// beginCreate claims a VM for provisioning, reporting whether another create for
// it is already running.
func beginCreate(vmID string) (release func(), busy bool) {
	createsInFlight.Lock()
	defer createsInFlight.Unlock()
	if createsInFlight.ids[vmID] {
		return func() {}, true
	}
	createsInFlight.ids[vmID] = true
	return func() {
		createsInFlight.Lock()
		delete(createsInFlight.ids, vmID)
		createsInFlight.Unlock()
	}, false
}

// vmConfigFor is the definition of a VM as the panel describes it in a CREATE
// or ADOPT command, without its disks: size, console, limits, virtual
// hardware and first network card.
func vmConfigFor(req *pb.VMCommandRequest, cfg *pb.VMConfig, osType string, hw models.Hardware, pinned []int, firmware *hwprofile.Firmware) libvirt.VMConfig {
	vmCfg := libvirt.VMConfig{
		Name:        req.VmId,
		UUID:        req.VmId,
		CPU:         int(cfg.Resources.Vcpus),
		Memory:      int(cfg.Resources.MemoryMb),
		VNCPassword: cfg.VncPassword,
		VNCPort:     -1, // auto-assign unless overridden via metadata
		Limits:      domtune.FromProto(cfg.Resources.GetLimits()),
	}

	// The proto VMConfig has no dedicated VNC port / VLAN fields, so the panel
	// passes them through the metadata map.
	if cfg.Metadata != nil {
		if v, ok := cfg.Metadata["vnc_port"]; ok {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				vmCfg.VNCPort = p
			}
		}
		if v, ok := cfg.Metadata["vlan_id"]; ok {
			if vlan, err := strconv.Atoi(v); err == nil && vlan > 0 {
				vmCfg.VLANID = vlan
			}
		}
		if v, ok := cfg.Metadata["cpu_model"]; ok {
			vmCfg.CPUModel = v
		}
	}
	if m := cfg.GetHardware().GetCpuModel(); m != "" {
		vmCfg.CPUModel = m
	}
	vmCfg.OSType = osType
	vmCfg.Hardware, vmCfg.PinnedCPUs, vmCfg.Firmware = hw, pinned, firmware

	// Apply the primary network interface from the network config.
	if cfg.NetworkConfig != nil {
		if len(cfg.NetworkConfig.Interfaces) > 0 {
			iface := cfg.NetworkConfig.Interfaces[0]
			vmCfg.Bridge = iface.BridgeName
			vmCfg.MACAddress = iface.MacAddress
			vmCfg.IPAddress = iface.IpAddress
			vmCfg.Netmask = int(iface.Netmask)
			vmCfg.Gateway = iface.Gateway
			vmCfg.AntiSpoofing = iface.AntiSpoofing
			vmCfg.IPv6Prefix = iface.Ipv6Prefix
			vmCfg.IPv6Address = iface.Ipv6Address
			vmCfg.IPv6Gateway = iface.Ipv6Gateway
			vmCfg.IPv6DNS = iface.Ipv6Dns
			vmCfg.DNS = iface.Dns
		}
		if cfg.NetworkConfig.BandwidthLimits != nil {
			vmCfg.BandwidthMbps = int(cfg.NetworkConfig.BandwidthLimits.EgressRateMbps)
		}
	}

	// Ensure a stable MAC so cloud-init can match the interface for static config.
	if vmCfg.MACAddress == "" {
		vmCfg.MACAddress = generateMAC(req.VmId)
	}
	return vmCfg
}

func (s *NodeAgentService) createVM(req *pb.VMCommandRequest) ([]*pb.BackupDiskFile, error) {
	var restored []*pb.BackupDiskFile
	err := s.provisionVM(req, &restored)
	return restored, err
}

// provisionVM is createVM; restored receives the data disks made from a
// backup's restore_disks.
func (s *NodeAgentService) provisionVM(req *pb.VMCommandRequest, restored *[]*pb.BackupDiskFile) error {
	if req.VmId == "" {
		return fmt.Errorf("vm_id is required")
	}
	// Idempotency guard [B5/V1]: if a domain for this VM already exists, a prior
	// CREATE already succeeded on this host — the panel is only retrying because
	// the gRPC response was lost. Re-running would fail on "disk already exists"
	// / "domain already exists" and wrongly mark the VM errored. The desired end
	// state (domain defined) is already met, so report success.
	if _, err := libvirt.GetVMStatus(req.VmId); err == nil {
		log.Printf("[NodeAgent] CREATE: domain %s already exists; treating as idempotent success", req.VmId)
		*restored = restoredDisksOf(req.VmId, req.GetConfig().GetRestoreDisks())
		return nil
	}
	// Claim the VM for the duration of this create. A create whose RPC deadline
	// expired keeps running on this node — the panel's cancellation does not stop
	// the download or the clone — so a retry can arrive while the first attempt is
	// still writing the disk. Without this claim, the recovery below would delete
	// a disk that is actively being written.
	releaseCreate, busy := beginCreate(req.VmId)
	if busy {
		// Retryable on purpose: the attempt in flight will either finish and
		// define the domain (making the next retry an idempotent success) or fail
		// and free the disk.
		return fmt.Errorf("a create for VM %s is already running on this node", req.VmId)
	}
	defer releaseCreate()

	cfg := req.Config
	if cfg == nil || cfg.Resources == nil {
		return fmt.Errorf("config with resources is required for CREATE")
	}

	if cfg.ImageId == "" {
		return fmt.Errorf("template image reference is required for CREATE")
	}
	// cfg.ImageId is the template's image_path, which may be a remote URL (from
	// the catalog) or an existing local file. Resolve it to a local path,
	// downloading and caching the image on this node if necessary.
	// The reference also says what kind of system the image holds, which
	// decides how the VM is set up below.
	osType, imageRef := models.SplitGuestImageRef(cfg.ImageId)
	if !guestos.Known(osType) {
		return fmt.Errorf("this node's agent cannot install %q systems; update the agent", osType)
	}
	// An empty disk and an installer: whoever installs it sets it up.
	blank := imageRef == models.BlankImageRef
	var installISO, driversISO string
	if blank {
		var err error
		if installISO, err = resolveISO(cfg.GetMetadata()[models.InstallISOKey], cfg.GetMetadata()[models.InstallISOSHA256Key]); err != nil {
			return err
		}
		if driversISO, err = resolveISO(cfg.GetMetadata()[models.DriversISOKey], cfg.GetMetadata()[models.DriversISOSHA256Key]); err != nil {
			return err
		}
	}
	// What the host cannot give is refused before any disk is made.
	hw, pinned, firmware, err := newVMHardware(cfg, osType)
	if err != nil {
		return err
	}
	diskGB := int(cfg.Resources.DiskGb)
	lvPool, onLVM, err := requestLVMPool(req.GetPoolType(), req.GetPoolPath())
	if err != nil {
		return err
	}
	imageDir, diskPath := defaultImageDir, ""
	if onLVM {
		// The disk is a logical volume; the small setup disc stays in the
		// default directory, where delete finds it.
		diskPath = "/dev/" + lvPool.VG + "/" + models.LVMDiskName(req.VmId, "vda")
		if err := clearOrphanLV(context.Background(), lvPool, req.VmId, diskPath); err != nil {
			return err
		}
	} else if imageDir, err = imageDirFor(req.GetPoolPath()); err != nil {
		return err
	} else {
		diskPath = filepath.Join(imageDir, req.VmId+".qcow2")
	}
	if _, err := os.Stat(diskPath); err == nil && !onLVM {
		// In a shared pool no domain here does not mean no VM: the file may be
		// this VM's disk, running or kept on another member node.
		if _, shared := storage.SharedPoolAt(diskPath); shared {
			return fmt.Errorf("%s already exists in a shared pool, where another node may use it; it was left as it is", diskPath)
		}
		// No domain uses this file: the check at the top of this function returned
		// early if one existed, and the claim above rules out a create in flight.
		// So it is debris from an attempt that died mid-provision — refusing here
		// (as this did) wedged the VM permanently, because every retry found the
		// same debris and failed the same way.
		log.Printf("[NodeAgent] CREATE: removing orphaned disk %s from a previous failed attempt", diskPath)
		if err := os.Remove(diskPath); err != nil {
			return fmt.Errorf("failed to clear orphaned disk %s: %w", diskPath, err)
		}
	}

	qcow := storage.NewQCOW2Manager()
	if blank && onLVM {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
		diskPath, err = lvm.Provision(ctx, lvPool, models.LVMDiskName(req.VmId, "vda"), int64(diskGB)<<30, "", "")
		cancel()
		if err != nil {
			return fmt.Errorf("failed to create the empty disk: %w", err)
		}
	} else if blank {
		if err := qcow.CreateImage(diskPath, diskGB); err != nil {
			return fmt.Errorf("failed to create the empty disk: %w", err)
		}
	} else if onLVM {
		if diskPath, err = provisionLVMDisk(req, imageRef, lvPool, restored); err != nil {
			return err
		}
	} else if key, once := strings.CutPrefix(imageRef, "s3://"); once && cfg.Metadata[models.ImageOnceKey] != "" {
		// A backup restored as a new VM is used once. It is downloaded beside
		// the new disk and becomes it, like a restore in place: kept in the
		// template cache it would sit on the root filesystem for good.
		// Not the caller's context: the download goes on when the panel
		// restarts. Bounded all the same, or a stalled transfer would hold
		// this VM's create for ever.
		fetchCtx, cancelFetch := context.WithTimeout(context.Background(), 8*time.Hour)
		client, _, err := backupStore(fetchCtx, req.GetObjectStore())
		if err != nil {
			cancelFetch()
			return fmt.Errorf("the backup destination cannot be used: %w", err)
		}
		defer client.Close()
		partPath := diskPath + ".part"
		_, err = fetchBackupImage(fetchCtx, client, cfg.GetRestoreLayers(), key, partPath, cfg.Metadata[models.ImageSHA256Key])
		cancelFetch()
		if err != nil {
			return err
		}
		if err := os.Rename(partPath, diskPath); err != nil {
			_ = os.Remove(partPath)
			return fmt.Errorf("failed to install the restored disk: %w", err)
		}
		extra, err := fetchRestoreDisks(client, imageDir, req.VmId, cfg.GetRestoreDisks())
		if err != nil {
			_ = os.Remove(diskPath)
			return err
		}
		*restored = extra
	} else {
		templatePath, err := resolveTemplateImage(imageRef, req.GetObjectStore(), cfg.GetMetadata()[models.ImageSHA256Key])
		if err != nil {
			return err
		}
		// Provision the disk as an independent copy of the template, then grow
		// it to the requested size. A clone of a raw disk is read as raw.
		if err := qcow.CloneImageFrom(templatePath, cloneSourceFormat(imageRef), diskPath); err != nil {
			return fmt.Errorf("failed to provision disk from template: %w", err)
		}
	}
	if diskGB > 0 && !onLVM && !blank {
		// qcow2 cannot shrink below the image's virtual size without data loss,
		// and `qemu-img resize` errors on a shrink. If the requested size is
		// smaller than the template's virtual size, keep the template size
		// instead of failing the whole create.
		requestedBytes := int64(diskGB) * 1024 * 1024 * 1024
		if info, infoErr := qcow.ImageInfo(diskPath); infoErr == nil && info.VirtualSize > requestedBytes {
			log.Printf("[NodeAgent] requested disk %dGB is smaller than template virtual size (%d bytes) for VM %s; keeping template size",
				diskGB, info.VirtualSize, req.VmId)
		} else if err := qcow.ResizeImage(diskPath, diskGB); err != nil {
			_ = os.Remove(diskPath)
			return fmt.Errorf("failed to resize disk to %dGB: %w", diskGB, err)
		}
	}

	vmCfg := vmConfigFor(req, cfg, osType, hw, pinned, firmware)
	vmCfg.DiskPath, vmCfg.DiskSize = diskPath, diskGB
	for _, d := range *restored {
		vmCfg.ExtraDisks = append(vmCfg.ExtraDisks, libvirt.ExtraDisk{Target: d.GetTarget(), Path: d.GetPath()})
	}
	removeRestored := func() {
		for _, d := range *restored {
			discardNewDisk(req.VmId, d.GetPath())
		}
	}
	if blank {
		vmCfg.ISODrives, vmCfg.InstallISO, vmCfg.DriversISO = true, installISO, driversISO
	}

	// Build a cloud-init NoCloud seed so the guest applies its hostname and
	// static IP on first boot (requires a cloud-init enabled image, e.g. the
	// catalog cloud images). Best-effort: if no ISO tool is available we log and
	// continue, and the guest falls back to DHCP.
	hostname := req.VmId
	if cfg.Metadata != nil {
		if h, ok := cfg.Metadata["hostname"]; ok && h != "" {
			hostname = h
		}
	}
	// Beside the disk, not in the default directory: a seed ISO left behind on
	// the root filesystem outlives nothing useful and is missed by every cleanup
	// that walks the pool.
	seedPath := filepath.Join(imageDir, req.VmId+"-seed.iso")
	if blank {
		// Nothing is installed yet to read a setup disc or a script.
		seedPath = ""
	} else if osType == guestos.RouterOS {
		// No cloud-init in RouterOS: its address, name and admin password go
		// onto the disk as a script it runs on first boot. This one is not
		// best-effort. A router that boots without it has an empty admin
		// password and no address, which is not a VM worth handing over.
		seedPath = ""
		rn := routerOSNetwork(vmCfg.IPAddress, vmCfg.Netmask, vmCfg.Gateway, guestIPv6From(vmCfg.IPv6Address, vmCfg.IPv6Gateway, vmCfg.IPv6DNS))
		rn.DNS = vmCfg.DNS
		script, err := guestos.RouterOSSetupScript(hostname, cfg.RootPassword, rn)
		if err == nil {
			err = guestos.WriteRouterOSAutorun(diskPath, script)
		}
		if err != nil {
			discardNewDisk(req.VmId, diskPath)
			removeRestored()
			return fmt.Errorf("failed to prepare the router for its first boot: %w", err)
		}
	} else if err := cloudinit.GenerateSeedISO(cloudinit.Config{
		InstanceID:   req.VmId,
		Hostname:     hostname,
		MACAddress:   vmCfg.MACAddress,
		IPAddress:    vmCfg.IPAddress,
		Prefix:       vmCfg.Netmask,
		Gateway:      vmCfg.Gateway,
		Nameservers:  vmCfg.DNS,
		IPv6Address:  vmCfg.IPv6Address,
		IPv6Gateway:  vmCfg.IPv6Gateway,
		IPv6DNS:      vmCfg.IPv6DNS,
		SSHPublicKey: cfg.SshPublicKey,
		Password:     cfg.RootPassword,
		UserData:     cfg.UserData,
		Windows:      osType == guestos.Windows,
	}, seedPath); err != nil {
		log.Printf("[NodeAgent] cloud-init seed not attached for VM %s (guest will use DHCP): %v", req.VmId, err)
		seedPath = ""
	} else {
		vmCfg.CloudInitISOPath = seedPath
	}

	// Inject network + hostname (+ optional password/SSH key) DIRECTLY into the
	// guest disk via libguestfs, so the guest is configured on first boot without
	// depending on the guest's cloud-init (which is unreliable across images —
	// a common host-side provisioning technique). The cloud-init seed above is kept as a
	// belt-and-suspenders for images where it does work. Best-effort: on failure
	// we log and still create the VM (it just may lack static networking).
	if osType == guestos.Linux && !blank {
		if err := injectGuestConfig(diskPath, hostname, vmCfg, cfg.RootPassword, cfg.SshPublicKey, cfg.UserData); err != nil {
			log.Printf("[NodeAgent] guest config injection failed for VM %s (guest may lack static networking): %v", req.VmId, err)
		}
	}

	domainUUID, err := libvirt.CreateVM(vmCfg)
	if err != nil {
		// Clean up provisioned artifacts if domain definition failed.
		discardNewDisk(req.VmId, diskPath)
		removeRestored()
		if seedPath != "" {
			_ = os.Remove(seedPath)
		}
		return fmt.Errorf("failed to define domain: %w", err)
	}

	// Boot the VM now, then announce its IP to the network from the HOST via
	// gratuitous ARP — this makes a new VM instantly
	// reachable. The host advertises guest_IP→guest_MAC on the bridge the moment
	// the VM comes up, so the upstream gateway learns it immediately (overwriting a
	// stale entry left by a previously-assigned VM) WITHOUT waiting for the guest to
	// finish booting and emit traffic on its own. (The in-guest announce service is
	// kept as a fallback for restarts/migrations.)
	if err := libvirt.StartVM(domainUUID); err != nil {
		log.Printf("[NodeAgent] VM %s defined but failed to auto-start: %v", req.VmId, err)
	} else if vmCfg.IPAddress != "" && vmCfg.MACAddress != "" && vmCfg.Bridge != "" {
		// Announce the guest's IP from the host repeatedly for the first ~2 minutes.
		// A single announce at start isn't enough: the vnet port has only just been
		// attached to the bridge and the guest hasn't booted, so the upstream can't
		// yet reach the MAC. Re-announcing across the boot window (once the vnet is
		// forwarding and the guest is up) reliably gets the gateway to learn
		// guest_IP→guest_MAC without waiting for a reboot.
		bridge, ip, mac, vmID := vmCfg.Bridge, vmCfg.IPAddress, vmCfg.MACAddress, domainUUID
		go func() {
			if osType != guestos.Linux || blank {
				// The restart below exists for Linux images edited on disk. A
				// router is configured within seconds of its first boot, and
				// Windows spends its first minutes finishing its own setup and
				// must not be switched off in the middle of it. Both are only
				// announced.
				for i := 0; i < 25; i++ {
					_ = sendGratuitousARP(bridge, ip, mac)
					time.Sleep(5 * time.Second)
				}
				return
			}
			// On this platform a brand-new VM built from a libguestfs-modified disk is
			// NOT reachable from the internet on its FIRST boot — the upstream never
			// commits its IP↔MAC binding (host + in-guest ARP announcements don't stick,
			// and re-attaching the vnet doesn't help; only the guest's own SECOND boot
			// registers cleanly). So we hard power-cycle once, EARLY: wait just long
			// enough for the first boot to bring the NIC up (~40s), then reboot. Doing it
			// early (rather than after ~90s) roughly halves provisioning time AND lets the
			// first-boot recipe run cleanly on the second boot instead of being
			// interrupted mid-apt.
			for i := 0; i < 7; i++ { // announce while the first boot comes up
				_ = sendGratuitousARP(bridge, ip, mac)
				time.Sleep(5 * time.Second)
			}
			// Stop GRACEFULLY, never with force. A hard power-cut here corrupts the
			// first boot: the guest's root fs is mounted with commit=30, so anything
			// cloud-init wrote in the preceding ~30s is lost while the (already
			// created) files survive as zero-length. That silently produced empty
			// /etc/sudoers.d/90-cloud-init-users (default user left with no NOPASSWD
			// sudo and a locked password => unusable VM when the customer supplied
			// only an SSH key) and an empty runcmd script, which cloud-init then
			// reports as "Missing #! in script" and marks the modules as already run,
			// so the second boot never repairs them.
			if serr := libvirt.StopVM(vmID, false); serr != nil {
				log.Printf("[NodeAgent] post-provision reboot: graceful stop %s failed: %v", vmID, serr)
			}
			// Give the guest time to flush and power off on its own. Only fall back to
			// a forced destroy if ACPI shutdown is ignored (e.g. the guest hasn't
			// reached a state where it handles the power button yet).
			stopped := false
			for i := 0; i < 30; i++ { // up to ~60s
				time.Sleep(2 * time.Second)
				info, ierr := libvirt.GetVMInfo(vmID)
				if ierr == nil && info.Status == libvirt.VMStatusStopped {
					stopped = true
					break
				}
			}
			if !stopped {
				log.Printf("[NodeAgent] post-provision reboot: %s ignored ACPI shutdown, forcing", vmID)
				if serr := libvirt.StopVM(vmID, true); serr != nil {
					log.Printf("[NodeAgent] post-provision reboot: force stop %s failed: %v", vmID, serr)
					return
				}
			}
			time.Sleep(2 * time.Second)
			if serr := startVM(vmID); serr != nil {
				log.Printf("[NodeAgent] post-provision reboot: start %s failed: %v", vmID, serr)
				return
			}
			for i := 0; i < 18; i++ { // announce through the (reachable) second boot
				_ = sendGratuitousARP(bridge, ip, mac)
				time.Sleep(5 * time.Second)
			}
			log.Printf("[NodeAgent] post-provision reboot done for %s (%s)", vmID, ip)
		}()
		log.Printf("[NodeAgent] announcing %s (%s) on %s + scheduling early post-provision reboot", ip, mac, bridge)
	}

	log.Printf("[NodeAgent] Created VM %s (disk=%s bridge=%s ip=%s vlan=%d bw=%dMbps)",
		req.VmId, diskPath, vmCfg.Bridge, vmCfg.IPAddress, vmCfg.VLANID, vmCfg.BandwidthMbps)
	return nil
}

// attachISO resolves the ISO reference (downloading if it's a URL) and puts it
// in the CD drive the request names (ISOSlotKey).
func (s *NodeAgentService) attachISO(req *pb.VMCommandRequest) error {
	if req.Config == nil || req.Config.ImageId == "" {
		return fmt.Errorf("ISO image is required for ATTACH_ISO")
	}
	slot := isoSlot(req.Config)
	// Attaching replaces what was in the drive, and a failed attach leaves a
	// download nobody uses; either way there may be a customer's ISO to evict.
	defer sweepCustomerISOs(0)
	if isoURL, customer := models.SplitCustomerISORef(req.Config.ImageId); customer {
		return attachCustomerISO(req.VmId, isoURL, slot)
	}
	isoPath, err := resolveISO(req.Config.ImageId, req.Config.GetMetadata()[models.ISOSHA256Key])
	if err != nil {
		return err
	}
	return libvirt.AttachISOSlot(req.VmId, isoPath, slot)
}

// isoSlot is the CD drive a command names: 1 (the install drive) unless the
// panel asked for another.
func isoSlot(cfg *pb.VMConfig) int {
	if n, err := strconv.Atoi(cfg.GetMetadata()[models.ISOSlotKey]); err == nil {
		return n
	}
	return models.ISOSlotInstall
}

// resolveISO finds an ISO the operator chose on this node, fetching it into
// the cache when it is a URL; "" is no ISO.
func resolveISO(ref, wantSHA string) (string, error) {
	if ref == "" {
		return "", nil
	}
	// vm:// and s3:// are disk sources for clone and image deploy, never ISOs;
	// a customer's address is fetched under its own rules, by attach alone.
	if strings.HasPrefix(ref, "vm://") || strings.HasPrefix(ref, "s3://") || strings.HasPrefix(ref, "customer:") || ref == models.BlankImageRef {
		return "", fmt.Errorf("unsupported ISO source")
	}
	return resolveTemplateImage(ref, nil, wantSHA)
}

// customerISODir holds ISOs downloaded from URLs that customers typed. It is
// apart from the operator's template cache so that evicting a customer's
// download can never remove one of the operator's images.
var customerISODir = filepath.Join(templateCacheDir, "customer-iso")

// Limits on a customer's ISO download, each overridable in the agent's
// environment. The cache shares the node's root filesystem with libvirt and
// this agent, so what a customer may put there is bounded three ways.
const (
	// CUSTOMER_ISO_MAX_GB: the largest ISO a customer may attach by URL. Above
	// every mainstream installer, including the Windows Server ones.
	defaultCustomerISOMaxGB = 8
	// CUSTOMER_ISO_MIN_FREE_GB: a download stops rather than leave less than
	// this free on the cache's filesystem.
	defaultCustomerISOMinFreeGB = 10
	// CUSTOMER_ISO_RETENTION_DAYS: an ISO no VM uses is removed once it has
	// been unused this long. Most go sooner, when they are detached.
	defaultCustomerISORetentionDays = 7
)

// envPositiveInt reads a whole number above zero from the environment.
func envPositiveInt(key string, def int64) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64); err == nil && n > 0 {
		return n
	}
	return def
}

// customerISOHolds counts the attaches in progress per cache file, so that an
// ISO which is downloaded but not yet in a domain is not evicted as unused.
var customerISOHolds = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

// customerISOFetches maps a cache file to the lock its download is made under.
var customerISOFetches sync.Map

// attachCustomerISO downloads an ISO from a URL a customer supplied, under the
// rules for such URLs (public addresses only, a size cap, a free-space floor),
// and attaches it.
func attachCustomerISO(vmID, isoURL string, slot int) error {
	u, err := url.Parse(isoURL)
	if err != nil {
		return fmt.Errorf("the ISO address is not a URL")
	}
	if err := safefetch.CheckURL(u); err != nil {
		return fmt.Errorf("the ISO must be a public http(s) URL: %w", err)
	}
	cachePath := filepath.Join(customerISODir, storage.CustomerISOName(isoURL))

	customerISOHolds.Lock()
	customerISOHolds.n[cachePath]++
	customerISOHolds.Unlock()
	defer func() {
		customerISOHolds.Lock()
		if customerISOHolds.n[cachePath]--; customerISOHolds.n[cachePath] <= 0 {
			delete(customerISOHolds.n, cachePath)
		}
		customerISOHolds.Unlock()
	}()

	// One download per URL. The panel gives up waiting after five minutes and
	// asks again; without this each retry fetched the same file beside the
	// first, several copies of a large ISO at once.
	// ponytail: one small entry per distinct URL until the agent restarts.
	fetch, _ := customerISOFetches.LoadOrStore(cachePath, &sync.Mutex{})
	fetch.(*sync.Mutex).Lock()
	defer fetch.(*sync.Mutex).Unlock()

	if _, err := os.Stat(cachePath); err == nil {
		// Counts as use, so the retention clock starts again.
		now := time.Now()
		_ = os.Chtimes(cachePath, now, now)
	} else {
		if err := os.MkdirAll(customerISODir, 0o755); err != nil {
			return fmt.Errorf("failed to create ISO cache dir %s: %w", customerISODir, err)
		}
		maxBytes := envPositiveInt("CUSTOMER_ISO_MAX_GB", defaultCustomerISOMaxGB) << 30
		minFree := envPositiveInt("CUSTOMER_ISO_MIN_FREE_GB", defaultCustomerISOMinFreeGB) << 30
		log.Printf("[NodeAgent] Downloading customer ISO %s -> %s", isoURL, cachePath)
		// Never unpacked: an archive is not an ISO, and unpacking one is a way
		// around the size cap.
		if err := storage.NewCustomerDownloader(maxBytes, minFree).DownloadTemplate(isoURL, cachePath); err != nil {
			return fmt.Errorf("failed to download the ISO: %w", err)
		}
	}
	return libvirt.AttachISOSlot(vmID, cachePath, slot)
}

// sweepCustomerISOs removes the customer ISO downloads that no VM uses and
// that have been unused for minAge. It only ever looks in customerISODir.
func sweepCustomerISOs(minAge time.Duration) {
	// Held throughout, so an attach cannot start relying on a file between the
	// moment it is found unused and the moment it is removed.
	customerISOHolds.Lock()
	defer customerISOHolds.Unlock()

	// Asked once, and only if there is a file old enough to consider.
	var used map[string]bool
	var listed, failed bool
	removed := storage.PruneUnused(customerISODir, func(path string) bool {
		if customerISOHolds.n[path] > 0 {
			return true
		}
		if !listed {
			listed = true
			var err error
			if used, err = libvirt.DiskSourceFiles(); err != nil {
				failed = true
				log.Printf("[NodeAgent] WARNING: customer ISO cleanup skipped, could not list what VMs use: %v", err)
			}
		}
		// Not knowing what is in use means keeping everything.
		return failed || used[path]
	}, minAge)
	for _, path := range removed {
		log.Printf("[NodeAgent] removed unused customer ISO %s", path)
	}
}

// pruneCustomerISOsForever removes long-unused customer ISOs now and then for
// as long as the agent runs. It catches what the sweeps on detach and delete
// cannot: a download orphaned by a restart, a VM removed outside the panel.
func pruneCustomerISOsForever() {
	for {
		days := envPositiveInt("CUSTOMER_ISO_RETENTION_DAYS", defaultCustomerISORetentionDays)
		sweepCustomerISOs(time.Duration(days) * 24 * time.Hour)
		time.Sleep(6 * time.Hour)
	}
}

// rebuildVM reinstalls a VM by replacing its disk with a fresh copy of the
// (resolved) OS template image. The VM must be stopped. Before swapping the
// disk it regenerates the cloud-init seed (at the VM's existing seed path, so
// the attached cidata cdrom keeps pointing at it) with any new root password /
// SSH keys; the fresh disk has no cloud-init state, so cloud-init re-runs and
// applies them on first boot.
func (s *NodeAgentService) rebuildVM(req *pb.VMCommandRequest) error {
	if req.Config == nil || req.Config.ImageId == "" {
		return fmt.Errorf("template image is required for REBUILD")
	}
	// Before the seed is rewritten or the definition converted.
	if err := libvirt.RefuseDuringBackup(req.VmId); err != nil {
		return err
	}
	osType, imageRef := models.SplitGuestImageRef(req.Config.ImageId)
	if !guestos.Known(osType) {
		return fmt.Errorf("this node's agent cannot install %q systems; update the agent", osType)
	}
	// The firmware stays what the VM was created with: a UEFI image does not
	// boot on BIOS, nor a BIOS image on UEFI.
	uefi, rescue, err := libvirt.BootSetup(req.VmId)
	if err != nil {
		return err
	}
	if rescue {
		return fmt.Errorf("the VM is in rescue mode; take it out of rescue mode first")
	}
	if wantUEFI := req.Config.GetMetadata()[models.FirmwareKey] == models.FirmwareUEFI; wantUEFI != uefi {
		return fmt.Errorf("%w: the VM boots with %s and the image needs %s; reinstall it with an image for %s",
			errFirmwareMismatch, firmwareName(uefi), firmwareName(wantUEFI), firmwareName(uefi))
	}
	templatePath, err := resolveTemplateImage(imageRef, req.GetObjectStore(), req.Config.GetMetadata()[models.ImageSHA256Key])
	if err != nil {
		return err
	}

	cfg := req.Config
	hostname := req.VmId
	mac, ip, gateway := "", "", ""
	prefix := 0
	var v6 guestIPv6
	var dns []string
	if cfg.Metadata != nil {
		if h := cfg.Metadata["hostname"]; h != "" {
			hostname = h
		}
	}
	if cfg.NetworkConfig != nil && len(cfg.NetworkConfig.Interfaces) > 0 {
		iface := cfg.NetworkConfig.Interfaces[0]
		mac = iface.MacAddress
		ip = iface.IpAddress
		prefix = int(iface.Netmask)
		gateway = iface.Gateway
		v6 = guestIPv6From(iface.Ipv6Address, iface.Ipv6Gateway, iface.Ipv6Dns)
		dns = iface.Dns
	}
	// The setup disc matches the network card by its MAC, so use the one the
	// domain really has (an imported VM's is not derived from its ID).
	if mac == "" {
		if real, merr := libvirt.GetVMInterfaceMAC(req.VmId); merr == nil && real != "" {
			mac = real
		} else {
			mac = generateMAC(req.VmId)
		}
	}

	// The seed sits beside the VM's disk, where create put it; writing it to
	// the default directory left the VM booting its old seed.
	seedDir := defaultImageDir
	if disk, derr := libvirt.PrimaryDiskPath(req.VmId); derr == nil && !models.IsBlockDevicePath(disk) {
		seedDir = filepath.Dir(disk)
	}
	seedPath := filepath.Join(seedDir, req.VmId+"-seed.iso")
	// An unmounted shared pool is the local disk underneath: the new disk and
	// disc would land there and vanish when the storage came back.
	if err := storage.CheckSharedDir(seedDir); err != nil {
		return err
	}

	// convert gives the VM the virtual hardware the new system needs when it
	// is of another kind than the one installed (see ConvertDomainKind). It is
	// called once the setup disc is ready, and undone if the reinstall fails.
	convert := func(attachSeed bool) (undo func(), err error) {
		have := libvirt.GuestOSType(req.VmId)
		if have == osType {
			return func() {}, nil
		}
		seed := ""
		if _, serr := os.Stat(seedPath); attachSeed && serr == nil {
			seed = seedPath
		}
		restore, err := libvirt.ConvertDomainKind(req.VmId, osType, seed)
		if err != nil {
			return nil, err
		}
		log.Printf("[NodeAgent] VM %s: definition converted from %s to %s for reinstall", req.VmId, have, osType)
		return func() {
			if rerr := restore(); rerr != nil {
				log.Printf("[NodeAgent] WARNING: could not put back the %s definition of VM %s after a failed reinstall: %v", have, req.VmId, rerr)
			}
		}, nil
	}

	if osType == guestos.RouterOS {
		rn := routerOSNetwork(ip, prefix, gateway, v6)
		rn.DNS = dns
		script, err := guestos.RouterOSSetupScript(hostname, cfg.RootPassword, rn)
		if err != nil {
			return err
		}
		undo, err := convert(false)
		if err != nil {
			return err
		}
		if err := libvirt.RebuildVM(req.VmId, templatePath, func(diskPath string) error {
			return guestos.WriteRouterOSAutorun(diskPath, script)
		}); err != nil {
			undo()
			return err
		}
		_ = os.Remove(seedPath) // a router reads no setup disc
		return nil
	}

	if err := cloudinit.GenerateSeedISO(cloudinit.Config{
		InstanceID:   req.VmId,
		Hostname:     hostname,
		MACAddress:   mac,
		IPAddress:    ip,
		Prefix:       prefix,
		Gateway:      gateway,
		Nameservers:  dns,
		IPv6Address:  v6.Address,
		IPv6Gateway:  v6.Gateway,
		IPv6DNS:      v6.DNS,
		SSHPublicKey: cfg.SshPublicKey,
		Password:     cfg.RootPassword,
		UserData:     cfg.UserData,
		Windows:      osType == guestos.Windows,
	}, seedPath); err != nil {
		// Non-fatal: the rebuilt guest still boots; it just won't pick up the new
		// password/keys via cloud-init.
		log.Printf("[NodeAgent] cloud-init seed not regenerated for rebuilt VM %s: %v", req.VmId, err)
	}

	undo, err := convert(true)
	if err != nil {
		return err
	}
	// A Linux disk is prepared exactly as at create: its address, name,
	// password and keys are written into it, because cloud-init alone does not
	// apply them on every image. Best-effort, as there: the setup disc is the
	// fallback.
	var prepare func(diskPath string) error
	if osType == guestos.Linux {
		prepare = func(diskPath string) error {
			vmCfg := libvirt.VMConfig{MACAddress: mac, IPAddress: ip, Netmask: prefix, Gateway: gateway,
				IPv6Address: v6.Address, IPv6Gateway: v6.Gateway, IPv6DNS: v6.DNS, DNS: dns}
			if ierr := injectGuestConfig(diskPath, hostname, vmCfg, cfg.RootPassword, cfg.SshPublicKey, cfg.UserData); ierr != nil {
				log.Printf("[NodeAgent] guest config injection failed for reinstalled VM %s (guest may lack static networking): %v", req.VmId, ierr)
			}
			return nil
		}
	}
	if err := libvirt.RebuildVM(req.VmId, templatePath, prepare); err != nil {
		undo()
		return err
	}
	// The old system's boot entries point at partitions that are gone.
	if uefi {
		if err := libvirt.ResetNVRAM(req.VmId); err != nil {
			log.Printf("[NodeAgent] VM %s: UEFI settings not reset after reinstall (it boots its default loader): %v", req.VmId, err)
		}
	}
	return nil
}

// BackupDisk exports a VM's disks (standalone qcow2 images, compressed as
// asked) and uploads them to object storage: the primary disk, or every disk
// with all_disks. A running VM's disks are first moved onto temporary
// overlays by one snapshot, so each image is copied as it was at one moment
// instead of while the guest writes to it.
func (s *NodeAgentService) BackupDisk(ctx context.Context, req *pb.BackupDiskRequest) (*pb.BackupDiskResponse, error) {
	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	s.crashes.Hold(req.VmId)

	for _, n := range []string{req.GetCheckpoint(), req.GetIncrementalFrom()} {
		if n != "" && !storage.ValidCheckpointName(n) {
			return backupDiskErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, fmt.Sprintf("invalid checkpoint name %q", n)), nil
		}
	}
	if req.GetIncrementalFrom() != "" && req.GetCheckpoint() == "" {
		return backupDiskErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "an incremental backup must create a checkpoint for the next one"), nil
	}

	all, err := libvirt.BackupDisks(req.VmId)
	if err != nil {
		return backupDiskErr(pb.ErrorCode_ERROR_CODE_NOT_FOUND, fmt.Sprintf("primary disk not found: %v", err)), nil
	}
	disks := all
	if !req.GetAllDisks() {
		disks = all[:1]
	}

	key := req.DestinationKey
	if key == "" {
		key = fmt.Sprintf("backups/%s/%d.qcow2", req.VmId, time.Now().Unix())
	}

	// The store is checked before the export, not after it: an export takes
	// hours, and one that cannot be uploaded was run for nothing.
	client, fromRequest, err := backupStore(ctx, req.GetObjectStore())
	if err != nil {
		return &pb.BackupDiskResponse{Error: storeFailure("the backup destination cannot be used", err)}, nil
	}
	defer client.Close()
	if resp := checkStoreWritable(ctx, client, key); resp != nil {
		return &pb.BackupDiskResponse{Error: resp}, nil
	}

	select {
	case <-overlaysRecovered:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	unlock := storage.LockExport(req.VmId)
	defer unlock()

	// Exports and overlays go beside the primary disk, not into the default
	// image directory. That directory is on the node's ROOT filesystem, so
	// exporting a large VM whose disk lives on a separate volume would pile tens
	// of GB onto the OS volume — and filling that takes libvirt and this agent
	// down with it, which is a far worse outcome than a failed backup.
	workDir := storage.DataDiskDir("", disks[0].Path, blockScratchDir())
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return backupDiskErr(pb.ErrorCode_ERROR_CODE_INTERNAL, fmt.Sprintf("cannot use %s for the export: %v", workDir, err)), nil
	}
	qcowExport := storage.NewQCOW2Manager()
	kind := "backup"
	if strings.Contains(req.GetDestinationKey(), "images/") {
		kind = "image"
	}

	// Refuse before the snapshot rather than fill the volume the customers'
	// disks live on. Exports run one at a time and each is removed once
	// uploaded, so the largest disk is what must fit. qemu pauses a VM the
	// moment a write fails, so an export that runs the volume dry does not fail
	// alone: it takes every VM on that filesystem down with it. The compressed
	// output is smaller than the source, so the source size is a generous
	// bound, and the margin leaves room to act afterwards.
	sizes := make([]int64, len(disks))
	var largest int64
	for i, d := range disks {
		if info, serr := os.Stat(d.Path); serr == nil && info.Mode().IsRegular() {
			sizes[i] = info.Size()
		} else if n, derr := storage.DeviceSize(d.Path); d.Block && derr == nil {
			sizes[i] = n
		} else if ii, ierr := qcowExport.ImageInfo(d.Path); ierr == nil {
			sizes[i] = ii.VirtualSize
		}
		largest = max(largest, sizes[i])
	}
	var total int64
	for _, n := range sizes {
		total += n
	}
	// The room an export needs is held until the backup ends, so a backup-API
	// copy (as large as the disks) and another backup started meanwhile do not
	// both count the same free space.
	releaseRoom := func() {}
	defer func() { releaseRoom() }()
	holdRoom := func(need int64, apiCopy bool) *pb.BackupDiskResponse {
		releaseRoom()
		releaseRoom = func() {}
		cap := storage.Capacity(workDir)
		if !cap.Exists || cap.Total == 0 {
			return nil
		}
		release, ok := storage.HoldRoom(cap.Filesystem, cap.Available, need, volumeReserveBytes, apiCopy)
		if !ok {
			promised := ""
			if held := storage.HeldRoom(cap.Filesystem); held > 0 {
				promised = fmt.Sprintf(" (%d of them held by backups running now)", held)
			}
			return backupDiskErr(pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, fmt.Sprintf(
				"not enough room on %s: %d bytes free%s, and this export could need up to %d plus a %d reserve. "+
					"Free space or move the export elsewhere — running it would risk pausing every VM on this volume.",
				cap.Filesystem, cap.Available, promised, need, int64(volumeReserveBytes)))
		}
		releaseRoom = release
		return nil
	}

	plan := planBackup(req, disks)
	consistency := libvirt.ConsistencyStopped
	outputs := map[string]string{}
	if plan.api {
		// libvirt copies every disk at once; a full copy is then compressed one
		// disk at a time beside it.
		need := total
		if plan.from == "" {
			need += largest
		}
		if resp := holdRoom(need, true); resp != nil {
			// The overlay route needs room for one compressed disk only.
			plan = backupPlan{}
			if req.GetCheckpoint() != "" {
				plan.note = "Full backup: not enough free space beside the disks for an incremental chain's copy."
			}
		}
	}
	if plan.api {
		for _, d := range disks {
			outputs[d.Target] = filepath.Join(workDir, req.VmId+"-"+d.Target+".backup-api.qcow2")
		}
		defer func() {
			for _, p := range outputs {
				_ = os.Remove(p)
			}
		}()
		consistency, err = runAPIBackup(ctx, req.VmId, all, disks, outputs, sizes[0], kind, &plan,
			func(need int64) *pb.BackupDiskResponse { return holdRoom(need, true) })
		if err != nil {
			if ctx.Err() != nil {
				return backupDiskErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err.Error()), nil
			}
			log.Printf("[NodeAgent] VM %s: the backup API failed (%v); copying the disks the older way", req.VmId, err)
			if plan.note == "" {
				plan.note = fmt.Sprintf("Copied without the backup API, which failed: %v.", err)
			}
			plan.api, plan.from, plan.checkpoint = false, "", ""
		}
	}
	if !plan.api {
		if resp := holdRoom(largest, false); resp != nil {
			return resp, nil
		}
		if running, _ := libvirt.IsRunning(req.VmId); running && libvirt.HasCheckpoints(req.VmId) {
			// Some libvirt versions refuse the overlay snapshot while any exist.
			if derr := libvirt.DropCheckpoints(req.VmId); derr != nil {
				log.Printf("[NodeAgent] VM %s: checkpoints not removed: %v", req.VmId, derr)
			}
		}
		hold, herr := libvirt.HoldDisksForBackup(req.VmId, workDir)
		switch {
		case herr != nil:
			// Better a copy flagged as live than no backup at all; the panel says so.
			log.Printf("[NodeAgent] WARNING: VM %s: no backup snapshot (%v); copying its disks while it runs", req.VmId, herr)
			consistency = libvirt.ConsistencyLive
		case hold != nil:
			consistency = hold.Consistency
			defer releaseBackupHold(req.VmId, hold)
		}
	}

	out := &pb.BackupDiskResponse{Success: true, UsedRequestStore: fromRequest, Consistency: consistency,
		Mode: "full", Checkpoint: plan.checkpoint, ModeNote: plan.note}
	if plan.from != "" {
		out.Mode = "incremental"
	}
	for i, d := range disks {
		diskKey, exportPath := key, filepath.Join(workDir, req.VmId+"-backup.qcow2")
		if i > 0 {
			diskKey, exportPath = storage.ExtraDiskKey(key, d.Target), filepath.Join(workDir, req.VmId+"-"+d.Target+"-backup.qcow2")
		}
		var file *pb.BackupDiskFile
		var used string
		var ferr *pb.ErrorResponse
		switch {
		case plan.api && plan.from != "":
			// Only the changed clusters are in it; re-encoding would turn the
			// zeroed ones into holes that read through to the older backup.
			used = "none"
			if err := storage.DetachBacking(outputs[d.Target]); err != nil {
				ferr = &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("disk %s: %v", d.Target, err)}
			} else {
				file, ferr = uploadExport(ctx, client, qcowExport, outputs[d.Target], diskKey, d.Target)
			}
		case plan.api:
			// The copy may name the live disk as its backing file; read alone,
			// nothing of the disk as it is now can leak into the backup.
			if err := storage.DetachBacking(outputs[d.Target]); err != nil {
				ferr = &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("disk %s: %v", d.Target, err)}
			} else {
				file, used, ferr = exportDisk(ctx, client, qcowExport, req.VmId, kind, storage.DomainDisk{Target: d.Target, Path: outputs[d.Target], Format: "qcow2"},
					sizes[i], exportPath, diskKey, req.GetCompression())
			}
		default:
			file, used, ferr = exportDisk(ctx, client, qcowExport, req.VmId, kind, d, sizes[i], exportPath, diskKey, req.GetCompression())
		}
		if plan.api {
			_ = os.Remove(outputs[d.Target])
		}
		if ferr != nil {
			if plan.checkpoint != "" {
				// Merged into the older checkpoint, so the next backup still sees these changes.
				if derr := libvirt.DeleteCheckpoint(req.VmId, plan.checkpoint); derr != nil {
					log.Printf("[NodeAgent] VM %s: checkpoint %s of a failed backup kept: %v", req.VmId, plan.checkpoint, derr)
				}
			}
			return &pb.BackupDiskResponse{Error: ferr}, nil
		}
		out.Compression = used
		out.Disks = append(out.Disks, file)
		log.Printf("[NodeAgent] Backed up VM %s disk %s -> %s (%d bytes, sha256=%s, %s, %s)", req.VmId, d.Target, diskKey, file.SizeBytes, file.Checksum, consistency, out.Mode)
	}
	if plan.checkpoint != "" {
		if err := libvirt.PruneCheckpoints(req.VmId, plan.checkpoint); err != nil {
			log.Printf("[NodeAgent] VM %s: older checkpoints not removed: %v", req.VmId, err)
		}
	}
	out.ObjectKey, out.SizeBytes, out.Checksum = out.Disks[0].Key, out.Disks[0].SizeBytes, out.Disks[0].Checksum
	return out, nil
}

// backupPlan is how a backup is taken.
type backupPlan struct {
	api        bool   // libvirt's backup API, instead of overlays
	from       string // copy only what changed since this checkpoint; "" copies everything
	checkpoint string // created with the backup; "" makes none
	note       string // why less was done than was asked
}

// planBackup decides how to copy a VM. The backup API copies a running VM
// with no overlay to commit back, and can track changes; it needs a recent
// libvirt and qcow2 files. It is used only for VMs whose schedule asked for
// incremental backups (a checkpoint now, or one left by an earlier backup, which
// an overlay snapshot would have to drop); everything else takes the overlay
// route, full, as before.
func planBackup(req *pb.BackupDiskRequest, disks []storage.DomainDisk) backupPlan {
	var p backupPlan
	asked := req.GetCheckpoint() != ""
	if !asked && !libvirt.HasCheckpoints(req.VmId) {
		return p
	}
	if running, err := libvirt.IsRunning(req.VmId); err != nil || !running {
		if asked {
			p.note = "The VM was off, so this full backup does not start a chain of incremental ones."
		}
		return p
	}
	if ok, why := libvirt.IncrementalSupport(); !ok {
		if asked {
			p.note = "Full backup: " + why + "."
		}
		return p
	}
	if ok, why := storage.ChainableDisks(disks); !ok {
		if asked {
			p.note = "Full backup: " + why + "."
		}
		return p
	}
	p.api, p.checkpoint = true, req.GetCheckpoint()
	if from := req.GetIncrementalFrom(); from != "" {
		targets := make([]string, len(disks))
		for i, d := range disks {
			targets[i] = d.Target
		}
		if ok, why := libvirt.CheckpointUsable(req.VmId, from, targets); ok {
			p.from = from
		} else {
			p.note = "Full backup: " + why + "."
		}
	}
	return p
}

// runAPIBackup runs the backup job. An incremental one that fails (a bitmap
// lost when the VM was killed, say) is run again as a full one.
func runAPIBackup(ctx context.Context, vmID string, all, disks []storage.DomainDisk, outputs map[string]string, primaryBytes int64, kind string,
	plan *backupPlan, roomFor func(int64) *pb.BackupDiskResponse) (string, error) {
	var total, largest int64
	for _, d := range disks {
		n := primaryBytes
		if info, err := os.Stat(d.Path); err == nil {
			n = info.Size()
		}
		total, largest = total+n, max(largest, n)
	}
	run := func() (string, error) {
		for _, p := range outputs {
			_ = os.Remove(p) // libvirt creates each output and refuses one that exists
		}
		done := storage.TrackExport(vmID, kind, disks[0].Path, outputs[disks[0].Target], primaryBytes)
		defer done()
		return libvirt.RunBackup(ctx, vmID, all, outputs, plan.from, plan.checkpoint, nil)
	}
	consistency, err := run()
	if err != nil && plan.from != "" && ctx.Err() == nil {
		log.Printf("[NodeAgent] VM %s: incremental backup failed (%v); taking a full one", vmID, err)
		plan.note = fmt.Sprintf("Full backup: the changes since the previous backup could not be read (%v).", err)
		plan.from = ""
		if resp := roomFor(total + largest); resp != nil {
			return "", errors.New(resp.GetError().GetMessage())
		}
		consistency, err = run()
	}
	return consistency, err
}

// exportDisk exports one disk to exportPath, uploads it to key and removes
// the export. The disk's own image is stable here: the VM is off, or writes
// to a backup overlay, or d is the backup API's copy.
func exportDisk(ctx context.Context, client sharedstorage.Store, q *storage.QCOW2Manager, vmID, kind string, d storage.DomainDisk,
	sourceBytes int64, exportPath, key, compression string) (*pb.BackupDiskFile, string, *pb.ErrorResponse) {
	_ = os.Remove(exportPath)
	// Registered before the export, not after it: a failed export leaves a
	// partial file that is as large as the work it got through — 17.9 GB was
	// found sitting on a node after one timed out.
	defer os.Remove(exportPath)
	// Make the export observable while it runs: without this the panel showed
	// "pending" for hours, which looks identical to a job that never started.
	doneTracking := storage.TrackExport(vmID, kind, d.Path, exportPath, sourceBytes)
	defer doneTracking()

	// The deadline is derived from the disk's size inside ConvertCompressed;
	// compression runs at single-digit MiB/s, so a flat ceiling is either
	// absurd for a small disk or fatal for a large one.
	used, err := q.ConvertCompressed(d.Path, d.Format, exportPath, compression)
	if err != nil {
		return nil, "", &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("export of disk %s failed: %v", d.Target, err)}
	}
	file, ferr := uploadExport(ctx, client, q, exportPath, key, d.Target)
	return file, used, ferr
}

// uploadExport checksums an exported image and uploads it to key.
func uploadExport(ctx context.Context, client sharedstorage.Store, q *storage.QCOW2Manager, exportPath, key, target string) (*pb.BackupDiskFile, *pb.ErrorResponse) {
	checksum, size, err := fileSHA256(exportPath)
	if err != nil {
		return nil, &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("checksum failed: %v", err)}
	}
	file := &pb.BackupDiskFile{Target: target, Key: key, SizeBytes: size, Checksum: checksum}
	if info, ierr := q.ImageInfo(exportPath); ierr == nil {
		file.VirtualSizeBytes = info.VirtualSize
	}
	f, err := os.Open(exportPath)
	if err != nil {
		return nil, &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("open export: %v", err)}
	}
	defer f.Close()
	if err := client.Upload(ctx, key, f, size, "application/octet-stream"); err != nil {
		return nil, storeFailure("upload failed", err)
	}
	file.EncryptionKeyId = storage.SealingKeyID(client)
	return file, nil
}

// overlaysRecovered is closed once the disks an interrupted backup left on
// overlays are back on their own images. Backups wait for it.
var overlaysRecovered = make(chan struct{})

// recoverBackupOverlays runs once, when the agent starts.
func recoverBackupOverlays() {
	defer close(overlaysRecovered)
	if err := libvirt.RecoverBackupOverlays(); err != nil {
		log.Printf("[NodeAgent] WARNING: disks left on backup overlays could not all be committed back: %v", err)
	}
}

// releaseBackupHold moves the VM back onto its own disks. A commit that fails
// is tried again in the background for a while; whatever is still on an
// overlay after that is committed when the agent next starts.
func releaseBackupHold(vmID string, hold *libvirt.BackupHold) {
	err := hold.Release()
	if err == nil {
		return
	}
	log.Printf("[NodeAgent] WARNING: VM %s is still on a backup overlay: %v; trying again", vmID, err)
	go func() {
		for i := 0; i < 12 && err != nil; i++ {
			time.Sleep(5 * time.Minute)
			if err = hold.Release(); err != nil {
				log.Printf("[NodeAgent] WARNING: VM %s still on a backup overlay: %v", vmID, err)
			}
		}
		if err == nil {
			log.Printf("[NodeAgent] VM %s is back on its own disks", vmID)
		}
	}()
}

func backupDiskErr(code pb.ErrorCode, msg string) *pb.BackupDiskResponse {
	return &pb.BackupDiskResponse{
		Success: false,
		Error:   &pb.ErrorResponse{Code: code, Message: msg},
	}
}

// agentFeatures are reported in GetNodeInfo (see the proto). "backup_targets":
// SFTP and mounted-path destinations, DeleteBackupFiles. "incremental_backup":
// checkpoints, and restores of chains of backups. "hostname_proxy": routes
// VMNetworkConfig.hostname_forwards on ports 80 and 443. "template_checksum":
// images are checked against the sha256 sent with them and cached under it.
// "blank_install": BlankImageRef creates a VM from an empty disk and ISOs.
// "iso_slots": a second CD drive (ISOSlotKey), and discs changed while a VM
// runs. "lvm_migration": VMs whose disks are logical volumes move between
// nodes, and onto LVM pools. models.FeatureSharedStorage: shared pools (see
// shared_pool.go) and moves that leave a disk on shared storage where it is.
//
// "vm_hardware": VMConfig.hardware, SetVMHardware and restart on crash.
// "snapshot_options": disk-only and quiesced snapshots, and reverts that keep
// the snapshot's power state. "offline_password": RESET_PASSWORD of a stopped
// Linux VM. "builtin_rescue": the RESCUE and UNRESCUE commands. What depends on
// the host ("uefi", "secure_boot", "tpm", "hugepages") is added by hostFeatures.
//
// models.FeatureGuestDNS, FeatureDHCPServer, FeatureVMNICs, FeatureVLANBridges
// and FeatureIPv6Extra: the network card, resolver, DHCP, VLAN bridge and
// extra IPv6 address fields and calls (see the proto). FeatureBackupEncryption:
// ObjectStore.encryption and decryption_keys. FeatureVMRecovery: the ADOPT
// command, SetFencedVMs and RemoveFencedVM (see recovery.go).
var agentFeatures = []string{"restore_disks", "lvm_disks", "backup_targets", "incremental_backup", "hostname_proxy",
	"template_checksum", "blank_install", "iso_slots", "lvm_migration", models.FeatureSharedStorage,
	models.FeatureVMHardware, models.FeatureSnapshotOptions, models.FeatureOfflinePassword, models.FeatureBuiltinRescue,
	models.FeatureGuestDNS, models.FeatureDHCPServer, models.FeatureVMNICs, models.FeatureVLANBridges, models.FeatureIPv6Extra,
	models.FeatureBackupEncryption, models.FeatureVMRecovery}

// volumeReserveBytes is the room an export or a restore must leave free on the
// volume it writes to. qemu pauses a VM the moment a write fails, so filling a
// volume customers' disks live on stops every VM on it.
const volumeReserveBytes = 20 * 1024 * 1024 * 1024

// objectStoreClient builds the client for a request: the store the panel sent,
// or, without one, the store in this node's own environment file. fromRequest
// says which, so the panel knows where an upload went.
func objectStoreClient(store *pb.ObjectStore) (client *sharedstorage.Client, fromRequest bool, err error) {
	if store.GetBucket() == "" {
		client, err = backupStorageClientFromEnv()
		return client, false, err
	}
	client, err = sharedstorage.Destination{
		Endpoint:  store.GetEndpoint(),
		Region:    store.GetRegion(),
		Bucket:    store.GetBucket(),
		AccessKey: store.GetAccessKey(),
		SecretKey: store.GetSecretKey(),
		PlainHTTP: store.GetPlainHttp(),
	}.Client()
	return client, true, err
}

// backupStore opens the destination a request names: an S3 store (the one
// sent, or this node's own), an SFTP server, or a mounted directory. With the
// keys the request sent, what it uploads is encrypted and what it downloads
// decrypted.
func backupStore(ctx context.Context, store *pb.ObjectStore) (sharedstorage.Store, bool, error) {
	switch store.GetKind() {
	case "", sharedstorage.KindS3:
		c, fromRequest, err := objectStoreClient(store)
		if err != nil {
			return nil, fromRequest, &sharedstorage.RefusedError{Err: err}
		}
		return storage.SealFor(c, store), fromRequest, nil
	}
	sftp := store.GetSftp()
	s, err := sharedstorage.Target{
		Kind: store.GetKind(),
		Path: store.GetPath(),
		SFTP: sharedstorage.SFTPTarget{Host: sftp.GetHost(), Port: int(sftp.GetPort()), User: sftp.GetUser(), Password: sftp.GetPassword(),
			PrivateKey: sftp.GetPrivateKey(), HostKey: sftp.GetHostKey(), Path: sftp.GetPath()},
	}.Open(ctx)
	if err != nil {
		return nil, true, err
	}
	return storage.SealFor(s, store), true, nil
}

// storeFailure reports a failed object-storage call. A store that answered and
// refused will refuse again; one that could not be reached may be back.
func storeFailure(what string, err error) *pb.ErrorResponse {
	if sharedstorage.Rejected(err) {
		return &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Message: fmt.Sprintf("%s: %v", what, err)}
	}
	return &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_UNAVAILABLE, Message: fmt.Sprintf("%s: %v", what, err), Retryable: true}
}

// checkStoreWritable writes a small object beside key and removes it. It
// returns nil when the store took the write. Failing to read it back or delete
// it is logged only: credentials that may only write can still back up.
func checkStoreWritable(ctx context.Context, client sharedstorage.Store, key string) *pb.ErrorResponse {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	err := sharedstorage.ProbeStore(ctx, client, path.Join(path.Dir(key), ".write-test"))
	var probe *sharedstorage.ProbeError
	if err == nil || (errors.As(err, &probe) && probe.Step != sharedstorage.ProbeWrite) {
		if err != nil {
			log.Printf("[NodeAgent] object storage accepts writes, but: %v", err)
		}
		return nil
	}
	return storeFailure("the backup destination did not accept a test write", err)
}

// TestObjectStore proves this node can reach and use an object store: it
// writes a small object, reads it back and deletes it.
func (s *NodeAgentService) TestObjectStore(ctx context.Context, req *pb.TestObjectStoreRequest) (*pb.TestObjectStoreResponse, error) {
	if req.GetKey() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "key is required")
	}
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	client, _, err := backupStore(ctx, req.GetObjectStore())
	if err != nil {
		return &pb.TestObjectStoreResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: err.Error()}}, nil
	}
	defer client.Close()
	if err := sharedstorage.ProbeStore(ctx, client, req.GetKey()); err != nil {
		return &pb.TestObjectStoreResponse{Error: storeFailure("test failed", err)}, nil
	}
	for _, k := range req.GetExpectKeys() {
		found, err := client.Exists(ctx, k)
		if err != nil {
			return &pb.TestObjectStoreResponse{Error: storeFailure("could not look for "+k, err)}, nil
		}
		if !found {
			return &pb.TestObjectStoreResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_NOT_FOUND,
				Message: fmt.Sprintf("%s is not there as this node sees the destination; a disk mounted on one node holds that node's backups only", k)}}, nil
		}
	}
	return &pb.TestObjectStoreResponse{Success: true}, nil
}

// DeleteBackupFiles removes backup images from a destination the panel cannot
// reach itself. Only backup images go: a key must name a .qcow2 file under a
// "backups" folder, inside the destination.
func (s *NodeAgentService) DeleteBackupFiles(ctx context.Context, req *pb.DeleteBackupFilesRequest) (*pb.DeleteBackupFilesResponse, error) {
	if len(req.GetKeys()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "keys are required")
	}
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	for _, k := range req.GetKeys() {
		if !storage.IsBackupImageKey(k) {
			return &pb.DeleteBackupFilesResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				Message: fmt.Sprintf("%q is not a backup image", k)}}, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client, _, err := backupStore(ctx, req.GetObjectStore())
	if err != nil {
		return &pb.DeleteBackupFilesResponse{Error: storeFailure("the backup destination cannot be used", err)}, nil
	}
	defer client.Close()
	out := &pb.DeleteBackupFilesResponse{}
	for _, k := range req.GetKeys() {
		found, err := client.Exists(ctx, k)
		if err == nil && found {
			err = client.Delete(ctx, k)
		}
		if err != nil {
			out.Error = storeFailure("delete failed", err)
			return out, nil
		}
		if found {
			out.Deleted = append(out.Deleted, k)
		} else {
			out.Missing = append(out.Missing, k)
		}
	}
	return out, nil
}

// fetchStoredDisk downloads a disk image from object storage to tmpPath and
// checks it against the checksum recorded when it was stored (when one is
// given). It refuses to start when the volume has no room for the image, and
// leaves nothing behind when it fails.
func fetchStoredDisk(ctx context.Context, client sharedstorage.Store, key, tmpPath, wantSHA256 string) (int64, error) {
	want, err := client.GetSize(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("could not read %s from object storage: %w", key, err)
	}
	if cap := storage.Capacity(filepath.Dir(tmpPath)); cap.Exists && cap.Total > 0 && cap.Available < want+volumeReserveBytes {
		return 0, fmt.Errorf("not enough room on %s: %d bytes free, and this image needs %d plus a %d reserve",
			cap.Filesystem, cap.Available, want, int64(volumeReserveBytes))
	}
	_ = os.Remove(tmpPath)
	if err := func() error {
		reader, err := client.Download(ctx, key)
		if err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		defer reader.Close()
		out, err := os.Create(tmpPath)
		if err != nil {
			return fmt.Errorf("create temp: %w", err)
		}
		defer out.Close()
		if _, err := io.Copy(out, reader); err != nil {
			return fmt.Errorf("write temp: %w", err)
		}
		return out.Sync()
	}(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	checksum, size, err := fileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("checksum failed: %w", err)
	}
	if wantSHA256 != "" && !strings.EqualFold(wantSHA256, checksum) {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("checksum mismatch: got %s, want %s", checksum, wantSHA256)
	}
	return size, nil
}

// fetchBackupImage downloads a backup's disk image to tmpPath. For an
// incremental backup every image under it (layers, the full one first) is
// downloaded and checked too, and the chain is read into one standalone image.
// Nothing but tmpPath is left behind.
func fetchBackupImage(ctx context.Context, client sharedstorage.Store, layers []*pb.BackupLayer, key, tmpPath, wantSHA256 string) (int64, error) {
	if len(layers) == 0 {
		return fetchStoredDisk(ctx, client, key, tmpPath, wantSHA256)
	}
	chain := append(append([]*pb.BackupLayer(nil), layers...), &pb.BackupLayer{Key: key, Checksum: wantSHA256})
	var total int64
	for _, l := range chain {
		if l.GetChecksum() == "" {
			return 0, fmt.Errorf("%s has no recorded checksum; the chain cannot be restored safely", l.GetKey())
		}
		n, err := client.GetSize(ctx, l.GetKey())
		if err != nil {
			return 0, fmt.Errorf("could not read %s from the backup destination: %w", l.GetKey(), err)
		}
		total += n
	}
	dir := filepath.Dir(tmpPath)
	if cap := storage.Capacity(dir); cap.Exists && cap.Total > 0 && cap.Available < total+volumeReserveBytes {
		return 0, fmt.Errorf("not enough room on %s: %d bytes free, and the %d images of this backup need %d plus a %d reserve",
			cap.Filesystem, cap.Available, len(chain), total, int64(volumeReserveBytes))
	}
	files := make([]string, len(chain))
	defer func() {
		for _, f := range files {
			if f != "" {
				_ = os.Remove(f)
			}
		}
	}()
	for i, l := range chain {
		files[i] = fmt.Sprintf("%s.layer%d", tmpPath, i)
		if _, err := fetchStoredDisk(ctx, client, l.GetKey(), files[i], l.GetChecksum()); err != nil {
			return 0, fmt.Errorf("image %d of %d (%s): %w", i+1, len(chain), l.GetKey(), err)
		}
	}
	_ = os.Remove(tmpPath)
	room := func(need int64) error {
		if cap := storage.Capacity(dir); cap.Exists && cap.Total > 0 && cap.Available < need+volumeReserveBytes {
			return fmt.Errorf("not enough room on %s: %d bytes free, and the restored disk needs %d plus a %d reserve",
				cap.Filesystem, cap.Available, need, int64(volumeReserveBytes))
		}
		return nil
	}
	if err := storage.FlattenChain(ctx, files, tmpPath, room); err != nil {
		return 0, err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// fetchRestoreDisks downloads the data disks of a backup restored as a new VM
// into dir, each checked against its checksum. On failure nothing is left.
func fetchRestoreDisks(client sharedstorage.Store, dir, vmID string, files []*pb.BackupDiskFile) ([]*pb.BackupDiskFile, error) {
	var out []*pb.BackupDiskFile
	undo := func() {
		for _, d := range out {
			_ = os.Remove(d.GetPath())
		}
	}
	for _, f := range files {
		if !validDataTarget(f.GetTarget()) {
			undo()
			return nil, fmt.Errorf("the backup names disk %q, which is not a data disk", f.GetTarget())
		}
		path := storage.RestoredDiskPath(dir, vmID, f.GetTarget())
		part := path + ".part"
		// Debris from an earlier attempt: no domain uses it yet.
		_ = os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
		_, err := fetchBackupImage(ctx, client, f.GetLayers(), f.GetKey(), part, f.GetChecksum())
		cancel()
		if err == nil {
			err = os.Rename(part, path)
		}
		if err != nil {
			_ = os.Remove(part)
			undo()
			return nil, fmt.Errorf("disk %s: %w", f.GetTarget(), err)
		}
		d := &pb.BackupDiskFile{Target: f.GetTarget(), Path: path}
		if info, ierr := storage.NewQCOW2Manager().ImageInfo(path); ierr == nil {
			d.VirtualSizeBytes = info.VirtualSize
		}
		out = append(out, d)
	}
	return out, nil
}

// validDataTarget accepts vdb..vdz, the targets data disks are given.
func validDataTarget(t string) bool {
	return len(t) == 3 && strings.HasPrefix(t, "vd") && t[2] >= 'b' && t[2] <= 'z'
}

// restoredDisksOf answers a repeated CREATE of a VM restored with data disks:
// the domain exists, so report the disks it has at those targets.
func restoredDisksOf(vmID string, files []*pb.BackupDiskFile) []*pb.BackupDiskFile {
	if len(files) == 0 {
		return nil
	}
	have, err := libvirt.BackupDisks(vmID)
	if err != nil {
		return nil
	}
	want := map[string]bool{}
	for _, f := range files {
		want[f.GetTarget()] = true
	}
	var out []*pb.BackupDiskFile
	for _, d := range have {
		if want[d.Target] {
			out = append(out, &pb.BackupDiskFile{Target: d.Target, Path: d.Path})
		}
	}
	return out
}

// RestoreDisk downloads a backup's disk images from object storage, verifies
// each against the checksum recorded at backup time, and puts them in place of
// the VM's disks with the same targets. The VM must be shut off. Every image is
// downloaded and verified before any disk is touched, and file disks are kept
// aside until all are in place, so a failed download or verify never destroys
// a live disk.
func (s *NodeAgentService) RestoreDisk(ctx context.Context, req *pb.RestoreDiskRequest) (*pb.RestoreDiskResponse, error) {
	if req.VmId == "" || (req.SourceKey == "" && len(req.GetDisks()) == 0) {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id and source_key are required")
	}
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	// The VM must be shut off: overwriting a disk under a running qemu corrupts it.
	s.crashes.Hold(req.VmId)
	// Held in the registry of work on the VM's disks until done, so a start
	// (or the crash watcher) does not boot it meanwhile.
	if !migrations.Start(req.VmId, restoreRun, "Restoring a backup", nil) {
		return restoreDiskErr(errDisksBusy.Error()), nil
	}
	defer migrations.Finish(req.VmId, restoreRun, nil)
	if why := notShutOff(req.VmId); why != "" {
		return restoreDiskErr(why), nil
	}

	have, err := libvirt.BackupDisks(req.VmId)
	if err != nil {
		return restoreDiskErr(fmt.Sprintf("primary disk not found: %v", err)), nil
	}
	if storage.OnBackupOverlay(have) {
		return restoreDiskErr(libvirt.ErrBackupRunning.Error()), nil
	}
	files := req.GetDisks()
	if len(files) == 0 {
		files = []*pb.BackupDiskFile{{Target: have[0].Target, Key: req.SourceKey, Checksum: req.ExpectedChecksum}}
	}
	targets := make([]string, len(files))
	for i, f := range files {
		targets[i] = f.GetTarget()
	}
	if missing := storage.MissingTargets(targets, have); len(missing) > 0 {
		return restoreDiskErr(fmt.Sprintf("the backup has disk %s, which this VM no longer has; attach a disk there or restore the backup as a new VM",
			strings.Join(missing, ", "))), nil
	}
	byTarget := map[string]storage.DomainDisk{}
	for _, d := range have {
		byTarget[d.Target] = d
	}

	client, _, err := backupStore(ctx, req.GetObjectStore())
	if err != nil {
		return restoreDiskErr(err.Error()), nil
	}
	defer client.Close()

	// Download next to each disk (same filesystem => rename is atomic); a
	// block device's image waits beside the primary disk.
	type staged struct {
		disk storage.DomainDisk
		tmp  string
	}
	var ready []staged
	discard := func() {
		for _, st := range ready {
			_ = os.Remove(st.tmp)
		}
	}
	var total int64
	for _, f := range files {
		d := byTarget[f.GetTarget()]
		dir := storage.DataDiskDir("", d.Path, storage.DataDiskDir("", have[0].Path, blockScratchDir()))
		// An unmounted shared pool is the local disk underneath: the restored
		// disk would land there and vanish when the storage is back.
		if err := storage.CheckSharedDir(dir); err != nil {
			discard()
			return restoreDiskErr(fmt.Sprintf("disk %s: %v", d.Target, err)), nil
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			discard()
			return restoreDiskErr(fmt.Sprintf("cannot stage disk %s in %s: %v", d.Target, dir, err)), nil
		}
		tmp := filepath.Join(dir, req.VmId+"-"+d.Target+"-restore.qcow2")
		size, err := fetchBackupImage(ctx, client, f.GetLayers(), f.GetKey(), tmp, f.GetChecksum())
		if err != nil {
			discard()
			return restoreDiskErr(fmt.Sprintf("disk %s: %v", d.Target, err)), nil
		}
		ready = append(ready, staged{disk: d, tmp: tmp})
		total += size
		if d.Block {
			if err := fitsDevice(tmp, d.Path); err != nil {
				discard()
				return restoreDiskErr(fmt.Sprintf("disk %s: %v", d.Target, err)), nil
			}
		}
	}
	// Files first: they can be put back if a later disk fails. A device is
	// written in place and cannot.
	sort.SliceStable(ready, func(i, j int) bool { return !ready[i].disk.Block && ready[j].disk.Block })

	// Again now, right before the swap: the downloads took a while.
	if why := notShutOff(req.VmId); why != "" {
		discard()
		return restoreDiskErr(why), nil
	}

	// The snapshots live in the disks being replaced.
	if err := libvirt.DropSnapshots(req.VmId); err != nil {
		log.Printf("[NodeAgent] VM %s: some snapshots could not be removed before the restore: %v", req.VmId, err)
	}

	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	var kept []string
	for _, st := range ready {
		bak, err := installRestoredDisk(st.disk, st.tmp, stamp)
		if err != nil {
			rollback()
			discard()
			return restoreDiskErr(fmt.Sprintf("failed to install disk %s: %v", st.disk.Target, err)), nil
		}
		if bak != "" {
			disk := st.disk
			kept = append(kept, bak)
			undo = append(undo, func() { _ = os.Rename(bak, disk.Path) })
		}
	}
	for _, bak := range kept {
		_ = os.Remove(bak)
	}
	discard()

	log.Printf("[NodeAgent] Restored VM %s disks %s (%d bytes)", req.VmId, strings.Join(targets, ", "), total)
	return &pb.RestoreDiskResponse{Success: true, BytesRestored: total}, nil
}

// installRestoredDisk puts the downloaded image tmp in place of disk. For a
// file disk the old image is kept as the returned path until the caller knows
// every disk is in place.
func installRestoredDisk(disk storage.DomainDisk, tmp, stamp string) (bak string, err error) {
	switch storage.RestoreMethod(disk) {
	case storage.RestoreWriteDevice:
		ctx, cancel := context.WithTimeout(context.Background(), diskWriteTimeout)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-n", "-f", "qcow2", "-O", "raw", tmp, disk.Path).CombinedOutput(); err != nil {
			return "", fmt.Errorf("writing %s failed, and it may now hold part of the backup: %w (%s)", disk.Path, err, strings.TrimSpace(string(out)))
		}
		return "", nil
	case storage.RestoreConvertFile:
		raw := tmp + ".raw"
		ctx, cancel := context.WithTimeout(context.Background(), diskWriteTimeout)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-O", "raw", tmp, raw).CombinedOutput(); err != nil {
			_ = os.Remove(raw)
			return "", fmt.Errorf("converting to raw failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		_ = os.Remove(tmp)
		tmp = raw
	}
	bak = disk.Path + ".restorebak." + stamp
	if err := os.Rename(disk.Path, bak); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("failed to move current disk aside: %w", err)
	}
	if err := os.Rename(tmp, disk.Path); err != nil {
		_ = os.Rename(bak, disk.Path)
		_ = os.Remove(tmp)
		return "", err
	}
	return bak, nil
}

// diskWriteTimeout bounds writing one restored image onto a disk.
const diskWriteTimeout = 8 * time.Hour

// fitsDevice checks a block device is at least as large as the image to be
// written over it.
func fitsDevice(image, device string) error {
	q := storage.NewQCOW2Manager()
	img, err := q.ImageInfo(image)
	if err != nil {
		return err
	}
	have, err := storage.DeviceSize(device)
	if err != nil {
		return err
	}
	if img.VirtualSize > have {
		return fmt.Errorf("the backup is %d bytes and %s holds only %d", img.VirtualSize, device, have)
	}
	return nil
}

// restoreRun names a restore in the migrations registry (one per VM at a time).
const restoreRun = "restore"

// notShutOff says why a VM's disks cannot be replaced now, "" when it is shut off.
func notShutOff(vmID string) string {
	st, err := libvirt.GetVMStatus(vmID)
	if err != nil {
		return fmt.Sprintf("failed to read VM state: %v", err)
	}
	if st != libvirt.VMStatusStopped {
		return fmt.Sprintf("VM must be stopped to restore (state: %s)", st)
	}
	return ""
}

func restoreDiskErr(msg string) *pb.RestoreDiskResponse {
	return &pb.RestoreDiskResponse{
		Success: false,
		Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg},
	}
}

// CreateStorageVolume provisions a real volume in a storage pool. The backend
// (dir/file via qemu-img, lvm via lvcreate, zfs via zfs create) is selected by
// req.PoolType; see storage.VolumeManager.
func (s *NodeAgentService) CreateStorageVolume(ctx context.Context, req *pb.CreateStorageVolumeRequest) (*pb.CreateStorageVolumeResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	// An empty pool_path means "this node's default image directory" — the node
	// (not the panel) owns where dir/file volumes live, so callers needn't hardcode it.
	if pool, onLVM, err := requestLVMPool(req.PoolType, req.PoolPath); err != nil {
		return createVolumeErr(err.Error()), nil
	} else if onLVM {
		if req.SizeGb <= 0 {
			return createVolumeErr("size_gb must be positive"), nil
		}
		dev, err := lvm.Provision(ctx, pool, lvVolumeName(req.Name), req.SizeGb<<30, "", "")
		if err != nil {
			return createVolumeErr(err.Error()), nil
		}
		log.Printf("[NodeAgent] Provisioned volume %s (%dGB)", dev, req.SizeGb)
		return &pb.CreateStorageVolumeResponse{Success: true, Path: dev, ActualSizeBytes: req.SizeGb << 30}, nil
	}
	poolPath := req.PoolPath
	if poolPath == "" {
		poolPath = defaultImageDir
	}
	path, size, err := storage.NewVolumeManager().CreateVolume(req.PoolType, poolPath, req.Name, req.Format, int(req.SizeGb))
	if err != nil {
		return createVolumeErr(err.Error()), nil
	}
	log.Printf("[NodeAgent] Provisioned %q volume %s (%dGB, %d bytes)", req.PoolType, path, req.SizeGb, size)
	return &pb.CreateStorageVolumeResponse{Success: true, Path: path, ActualSizeBytes: size}, nil
}

// DeleteStorageVolume removes a previously provisioned volume (backend selected
// by req.PoolType).
func (s *NodeAgentService) DeleteStorageVolume(ctx context.Context, req *pb.DeleteStorageVolumeRequest) (*pb.DeleteStorageVolumeResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if pool, onLVM, err := requestLVMPool(req.PoolType, req.PoolPath); err != nil {
		return deleteVolumeErr(err.Error()), nil
	} else if onLVM {
		// An operator's explicit delete from the pool: an imported volume
		// qualifies too, as long as it is in this pool and nothing uses it.
		if err := removeLV(ctx, "", req.Path, []storage.LVMPool{pool}, true); err != nil {
			return deleteVolumeErr(err.Error()), nil
		}
		return &pb.DeleteStorageVolumeResponse{Success: true}, nil
	}
	if err := storage.NewVolumeManager().DeleteVolume(req.PoolType, req.Path); err != nil {
		return deleteVolumeErr(err.Error()), nil
	}
	log.Printf("[NodeAgent] Deleted %q volume %s", req.PoolType, req.Path)
	return &pb.DeleteStorageVolumeResponse{Success: true}, nil
}

func createVolumeErr(msg string) *pb.CreateStorageVolumeResponse {
	return &pb.CreateStorageVolumeResponse{
		Success: false,
		Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg},
	}
}

func deleteVolumeErr(msg string) *pb.DeleteStorageVolumeResponse {
	return &pb.DeleteStorageVolumeResponse{
		Success: false,
		Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg},
	}
}

// AttachDisk provisions a data volume and hot-plugs it into the VM as the next
// free virtio target. A failed attach removes the freshly created volume so it
// doesn't leak.
func (s *NodeAgentService) AttachDisk(ctx context.Context, req *pb.AttachDiskRequest) (*pb.AttachDiskResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.VmId == "" || req.SizeGb <= 0 {
		return attachDiskErr("vm_id and a positive size_gb are required"), nil
	}
	if pool, onLVM, err := requestLVMPool(req.PoolType, req.PoolPath); err != nil {
		return attachDiskErr(err.Error()), nil
	} else if onLVM {
		// The caller's context: a panel that gave up must not find a disk it
		// never recorded attached to the VM, so a cancelled create is undone.
		dev, err := lvm.Provision(ctx, pool, lvDataDiskName(req.VmId), req.SizeGb<<30, "", "")
		if err != nil {
			return attachDiskErr(fmt.Sprintf("create volume: %v", err)), nil
		}
		device, err := libvirt.AttachDisk(req.VmId, dev)
		if err != nil {
			discardNewDisk(req.VmId, dev)
			return attachDiskErr(fmt.Sprintf("attach: %v", err)), nil
		}
		log.Printf("[NodeAgent] Attached %dGB volume %s as %s to VM %s", req.SizeGb, dev, device, req.VmId)
		return &pb.AttachDiskResponse{Success: true, Device: device, Path: dev}, nil
	}
	poolPath := req.PoolPath
	switch req.PoolType {
	case "", "dir", "file":
		if _, err := storage.ResolvePoolDir(req.PoolPath, defaultImageDir); err != nil {
			return attachDiskErr(err.Error()), nil
		}
		// Beside the VM's own disk when the panel names no pool, not on the
		// root filesystem. An unreadable domain just leaves primary empty.
		primary, _ := libvirt.PrimaryDiskPath(req.VmId)
		poolPath = storage.DataDiskDir(req.PoolPath, primary, defaultImageDir)
	default:
		if poolPath == "" {
			poolPath = defaultImageDir
		}
	}
	name := fmt.Sprintf("%s-data-%d", req.VmId, time.Now().UnixNano())
	path, _, err := storage.NewVolumeManager().CreateVolume(req.PoolType, poolPath, name, "qcow2", int(req.SizeGb))
	if err != nil {
		return attachDiskErr(fmt.Sprintf("create volume: %v", err)), nil
	}
	device, err := libvirt.AttachDisk(req.VmId, path)
	if err != nil {
		_ = storage.NewVolumeManager().DeleteVolume(req.PoolType, path) // don't leak the orphan
		return attachDiskErr(fmt.Sprintf("attach: %v", err)), nil
	}
	log.Printf("[NodeAgent] Attached %dGB disk %s as %s to VM %s", req.SizeGb, path, device, req.VmId)
	return &pb.AttachDiskResponse{Success: true, Device: device, Path: path}, nil
}

// DetachDisk detaches a data disk from the VM and optionally deletes its volume.
func (s *NodeAgentService) DetachDisk(ctx context.Context, req *pb.DetachDiskRequest) (*pb.DetachDiskResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.VmId == "" || req.Device == "" {
		return detachDiskErr("vm_id and device are required"), nil
	}
	// A logical volume is proven this VM's before it is detached, so a refusal
	// leaves the VM as it was.
	var lvPools []storage.LVMPool
	block := req.DeleteVolume && models.IsBlockDevicePath(req.Path)
	if block {
		pool, onLVM, err := requestLVMPool(req.PoolType, req.PoolPath)
		if err != nil || !onLVM {
			return detachDiskErr(fmt.Sprintf("%s is a block device; it is deleted only from the LVM pool that holds it", req.Path)), nil
		}
		lv, err := lvm.Lookup(ctx, req.Path)
		if _, serr := os.Stat(req.Path); err != nil && os.IsNotExist(serr) {
			// Removed by an earlier try whose answer was lost: only the detach is left.
		} else if err != nil {
			return detachDiskErr(err.Error()), nil
		} else if err := storage.CheckOwnership(lv, req.VmId, []storage.LVMPool{pool}, false); err != nil {
			return detachDiskErr(err.Error()), nil
		} else {
			lvPools = []storage.LVMPool{pool}
		}
	}
	if err := libvirt.DetachDisk(req.VmId, req.Device, req.Path); err != nil {
		// Idempotency: if the device is already gone from the domain, a prior
		// DetachDisk already detached it (e.g. a retry after a delete-volume
		// failure). Treat that as detached and fall through to volume deletion so
		// the operation is retry-safe instead of getting stuck.
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "device not found") && !strings.Contains(msg, "no target device") {
			return detachDiskErr(err.Error()), nil
		}
		log.Printf("[NodeAgent] DetachDisk: device %s already absent on VM %s; treating as detached", req.Device, req.VmId)
	}
	if block {
		if lvPools != nil {
			if err := removeDetachedLV(ctx, req.VmId, req.Path, lvPools); err != nil {
				return detachDiskErr(fmt.Sprintf("detached %s but failed to delete volume %s: %v", req.Device, req.Path, err)), nil
			}
		}
	} else if req.DeleteVolume && req.Path != "" {
		if err := storage.NewVolumeManager().DeleteVolume(req.PoolType, req.Path); err != nil {
			// Disk is detached from the domain but its backing volume could not be
			// removed: report failure so the leak surfaces instead of being silently
			// swallowed. The detach above is now idempotent, so retrying this call
			// after the underlying cause is cleared completes the deletion.
			return detachDiskErr(fmt.Sprintf("detached %s but failed to delete volume %s: %v", req.Device, req.Path, err)), nil
		}
	}
	log.Printf("[NodeAgent] Detached disk %s from VM %s (delete=%v)", req.Device, req.VmId, req.DeleteVolume)
	return &pb.DetachDiskResponse{Success: true}, nil
}

func attachDiskErr(msg string) *pb.AttachDiskResponse {
	return &pb.AttachDiskResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg}}
}

func detachDiskErr(msg string) *pb.DetachDiskResponse {
	return &pb.DetachDiskResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg}}
}

// DefineNetwork creates+starts a managed libvirt network (private VPC segment).
func (s *NodeAgentService) DefineNetwork(ctx context.Context, req *pb.DefineNetworkRequest) (*pb.DefineNetworkResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.Name == "" {
		return &pb.DefineNetworkResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "network name is required"}}, nil
	}
	bridge, err := libvirt.DefineNetwork(req.Name, req.Mode, req.Bridge, req.Cidr, req.Dhcp)
	if err != nil {
		return &pb.DefineNetworkResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()}}, nil
	}
	log.Printf("[NodeAgent] Defined %q network %s (bridge=%s)", req.Mode, req.Name, bridge)
	return &pb.DefineNetworkResponse{Success: true, Bridge: bridge}, nil
}

// UndefineNetwork stops+removes a managed libvirt network.
func (s *NodeAgentService) UndefineNetwork(ctx context.Context, req *pb.UndefineNetworkRequest) (*pb.UndefineNetworkResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if err := libvirt.UndefineNetwork(req.Name); err != nil {
		return &pb.UndefineNetworkResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()}}, nil
	}
	log.Printf("[NodeAgent] Undefined network %s", req.Name)
	return &pb.UndefineNetworkResponse{Success: true}, nil
}

// ImportDisk imports a disk image into the node's storage for a VM. The action
// is "copy" (default; converts to the target format via qemu-img), "move"
// (rename, falling back to convert+remove across filesystems), or "symlink".
func (s *NodeAgentService) ImportDisk(ctx context.Context, req *pb.DiskImportRequest) (*pb.DiskImportResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.SourcePath == "" || req.TargetPath == "" {
		return importDiskErr(req.VmId, "source_path and target_path are required"), nil
	}
	if _, err := os.Stat(req.SourcePath); err != nil {
		return importDiskErr(req.VmId, fmt.Sprintf("source image not found: %s", req.SourcePath)), nil
	}
	if err := os.MkdirAll(filepath.Dir(req.TargetPath), 0o755); err != nil {
		return importDiskErr(req.VmId, fmt.Sprintf("failed to create target dir: %v", err)), nil
	}

	format := req.Format
	if format == "" {
		format = "qcow2"
	}
	qcow := storage.NewQCOW2Manager()

	switch req.Action {
	case "symlink":
		_ = os.Remove(req.TargetPath)
		if err := os.Symlink(req.SourcePath, req.TargetPath); err != nil {
			return importDiskErr(req.VmId, fmt.Sprintf("symlink failed: %v", err)), nil
		}
	case "move":
		if err := os.Rename(req.SourcePath, req.TargetPath); err != nil {
			// Cross-filesystem rename fails; fall back to convert + remove source.
			if cerr := qcow.ConvertImage(req.SourcePath, req.TargetPath, format); cerr != nil {
				return importDiskErr(req.VmId, fmt.Sprintf("move (convert) failed: %v", cerr)), nil
			}
			_ = os.Remove(req.SourcePath)
		}
	default: // "copy"
		if err := qcow.ConvertImage(req.SourcePath, req.TargetPath, format); err != nil {
			return importDiskErr(req.VmId, fmt.Sprintf("import (convert) failed: %v", err)), nil
		}
	}

	var size int64
	if fi, err := os.Stat(req.TargetPath); err == nil {
		size = fi.Size()
	}
	log.Printf("[NodeAgent] Imported disk %s -> %s (action=%s, format=%s, %d bytes)", req.SourcePath, req.TargetPath, req.Action, format, size)
	return &pb.DiskImportResponse{Success: true, VmId: req.VmId, ImportedPath: req.TargetPath, SizeBytes: size}, nil
}

func importDiskErr(vmID, msg string) *pb.DiskImportResponse {
	return &pb.DiskImportResponse{
		Success: false,
		VmId:    vmID,
		Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg},
	}
}

// SyncTemplate downloads/caches an OS template image on this node (idempotent)
// and returns its local path, size and checksum.
func (s *NodeAgentService) SyncTemplate(ctx context.Context, req *pb.SyncTemplateRequest) (*pb.SyncTemplateResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.ImageUrl == "" {
		return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "image_url is required"}}, nil
	}

	localPath, err := resolveTemplateImage(req.ImageUrl, nil, req.ExpectedChecksum)
	if err != nil {
		return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()}}, nil
	}
	var checksum string
	var size int64
	if want := strings.ToLower(req.ExpectedChecksum); want != "" && (strings.HasPrefix(req.ImageUrl, "http://") || strings.HasPrefix(req.ImageUrl, "https://")) {
		// Checked against it when it was downloaded, and cached under it. The
		// cached file is the image unpacked, whose own hash is another one.
		checksum = want
		if fi, serr := os.Stat(localPath); serr == nil {
			size = fi.Size()
		}
	} else {
		if checksum, size, err = fileSHA256(localPath); err != nil {
			return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("checksum failed: %v", err)}}, nil
		}
		if req.ExpectedChecksum != "" && !strings.EqualFold(req.ExpectedChecksum, checksum) {
			return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("checksum mismatch: want %s, got %s", req.ExpectedChecksum, checksum)}}, nil
		}
	}

	log.Printf("[NodeAgent] Template synced: %s -> %s (%d bytes, sha256=%s)", req.ImageUrl, localPath, size, checksum)
	return &pb.SyncTemplateResponse{Success: true, LocalPath: localPath, SizeBytes: size, Checksum: checksum}, nil
}

// fileSHA256 returns the hex SHA256 and byte size of a file.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// parseBoolFlag reads a boolean storage flag, consulting STORAGE_* before
// S3_*. An explicit non-empty value is parsed strictly via strconv.ParseBool
// and a malformed value fails closed (returns false + error) instead of being
// silently coerced to a default.
func parseBoolFlag(keys ...string) (bool, error) {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return strconv.ParseBool(strings.TrimSpace(v))
		}
	}
	return false, nil
}

// backupStorageClientFromEnv builds an object-storage client from the agent's
// environment (S3_*/STORAGE_* vars in agent.env). Precedence mirrors the
// existing endpoint/key lookup: STORAGE_* wins over S3_*. Endpoint scheme
// normalization and path-style handling are delegated to shared storage, so
// flags are passed through rather than pre-resolved here.
func backupStorageClientFromEnv() (*sharedstorage.Client, error) {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	endpoint := first("STORAGE_ENDPOINT", "S3_ENDPOINT")
	region := first("STORAGE_REGION", "S3_REGION")
	if region == "" {
		region = "us-east-1"
	}

	// Path-style addressing is the historical production default (MinIO). It is
	// only disabled if the operator explicitly opts out via the flag to avoid
	// silently breaking existing MinIO callers that omit the field.
	usePathStyle := true
	if v, err := parseBoolFlag("STORAGE_USE_PATH_STYLE", "S3_USE_PATH_STYLE"); err != nil {
		return nil, fmt.Errorf("invalid STORAGE_USE_PATH_STYLE/S3_USE_PATH_STYLE: %w", err)
	} else if os.Getenv("STORAGE_USE_PATH_STYLE") != "" || os.Getenv("S3_USE_PATH_STYLE") != "" {
		usePathStyle = v
	}

	forceHTTP := false
	if v, err := parseBoolFlag("STORAGE_FORCE_HTTP", "S3_FORCE_HTTP"); err != nil {
		return nil, fmt.Errorf("invalid STORAGE_FORCE_HTTP/S3_FORCE_HTTP: %w", err)
	} else if os.Getenv("STORAGE_FORCE_HTTP") != "" || os.Getenv("S3_FORCE_HTTP") != "" {
		forceHTTP = v
	}

	cfg := &sharedstorage.Config{
		Endpoint:     endpoint,
		AccessKey:    first("STORAGE_ACCESS_KEY", "S3_ACCESS_KEY"),
		SecretKey:    first("STORAGE_SECRET_KEY", "S3_SECRET_KEY"),
		Bucket:       first("STORAGE_BUCKET", "S3_BUCKET"),
		Region:       region,
		Provider:     sharedstorage.ProviderS3,
		ForceHTTP:    forceHTTP,
		UsePathStyle: usePathStyle,
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("missing object storage configuration (set S3_ACCESS_KEY/S3_SECRET_KEY/S3_BUCKET)")
	}
	return sharedstorage.NewClient(cfg)
}

func (s *NodeAgentService) GetVMStatus(ctx context.Context, req *pb.VMStatusRequest) (*pb.VMStatusResponse, error) {
	log.Printf("[NodeAgent] GetVMStatus: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Getting status for VM %s from node %s", req.VmId, nodeID)

	vmStatus, err := libvirt.GetVMStatus(req.VmId)
	if err != nil {
		return nil, vmLookupError(err)
	}

	vmInfo, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get VM info: %v", err)
	}

	resp := &pb.VMStatusResponse{
		VmId:            req.VmId,
		State:           mapVMStatusToPBState(vmStatus),
		UptimeSeconds:   0,
		Pid:             0,
		IpAddresses:     []string{},
		VncPort:         int32(vmInfo.VNCPort),
		LastStateChange: timestamppb.Now(),
		ConfiguredResources: &pb.VMResources{
			Vcpus:    int32(vmInfo.CPU),
			MemoryMb: int64(vmInfo.MaxMemory / (1024 * 1024)),
			DiskGb:   int64(vmInfo.DiskBytes / (1024 * 1024 * 1024)),
		},
	}

	n, paused := s.crashes.Status(req.VmId)
	resp.CrashRestarts, resp.CrashRestartPaused = int32(n), paused

	// Addresses of a running VM: its guest agent's answer, bounded so a guest
	// agent that never answers costs this call seconds, not its deadline, then
	// the host's DHCP leases and ARP table.
	if vmStatus == "running" {
		ips, silent := s.statusIPs(req.VmId)
		if len(ips) > 0 {
			resp.IpAddresses = ips
		}
		resp.GuestAgentSilent = silent

		// Cumulative traffic counters: the panel's monthly bandwidth accounting
		// reads these and nothing else. They restart from zero when the VM
		// does; the panel treats a decrease as a restart.
		if st, serr := libvirt.GetVMStats(req.VmId); serr == nil {
			resp.CurrentResources = &pb.VMResourceUsage{
				NetworkRxBytes: st.NetRXBytes,
				NetworkTxBytes: st.NetTXBytes,
			}
		}
	}

	return resp, nil
}

func (s *NodeAgentService) StreamVMMetrics(req *pb.VMMetricsRequest, stream grpc.ServerStreamingServer[pb.VMMetricsResponse]) error {
	log.Printf("[NodeAgent] StreamVMMetrics started for VMs: %v", req.VmIds)

	nodeID, authenticated := GetNodeIDFromContext(stream.Context())
	if !authenticated {
		return status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Streaming metrics from node %s", nodeID)

	interval := time.Duration(req.IntervalMs) * time.Millisecond
	if interval == 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	vmIDs := req.VmIds
	if len(vmIDs) == 0 {
		vms, err := libvirt.ListVMs()
		if err != nil {
			log.Printf("[NodeAgent] Failed to list VMs for metrics streaming: %v", err)
			return status.Errorf(codes.Internal, "failed to list VMs: %v", err)
		}
		for _, vm := range vms {
			vmIDs = append(vmIDs, vm.UUID)
		}
	}

	if len(vmIDs) == 0 {
		log.Println("[NodeAgent] No VMs available for metrics streaming")
		return nil
	}

	for {
		select {
		case <-stream.Context().Done():
			log.Println("[NodeAgent] StreamVMMetrics context cancelled")
			return nil
		case <-ticker.C:
			for _, vmID := range vmIDs {
				metrics := s.collectVMMetrics(vmID)
				if err := stream.Send(metrics); err != nil {
					log.Printf("[NodeAgent] Failed to send metrics: %v", err)
					return err
				}
			}
		}
	}
}

// collectVMMetrics collects REAL per-VM metrics using libvirt
func (s *NodeAgentService) collectVMMetrics(vmID string) *pb.VMMetricsResponse {
	now := time.Now()

	// Get real VM stats from libvirt
	vmStats, err := libvirt.GetVMStats(vmID)
	if err != nil {
		log.Printf("[NodeAgent] Failed to get VM stats for %s: %v", vmID, err)
		return &pb.VMMetricsResponse{
			Timestamp: timestamppb.New(now),
			VmId:      vmID,
		}
	}

	s.metricsCache.mu.Lock()
	defer s.metricsCache.mu.Unlock()

	// Calculate CPU percentage
	cpuPercent := 0.0
	if lastCPUTime, exists := s.metricsCache.lastCPUTime[vmID]; exists {
		if lastCollect, exists := s.metricsCache.lastCPUCollect[vmID]; exists {
			timeDelta := now.Sub(lastCollect).Seconds()
			if timeDelta > 0 {
				cpuDelta := float64(vmStats.CPUTime - lastCPUTime)
				cpuPercent = (cpuDelta / timeDelta / 1e9 / float64(vmStats.NumCPUs)) * 100
				if cpuPercent < 0 {
					cpuPercent = 0
				}
				if cpuPercent > 100*float64(vmStats.NumCPUs) {
					cpuPercent = 100 * float64(vmStats.NumCPUs)
				}
			}
		}
	}

	// Calculate network rates (bytes/sec)
	var netRXRate, netTXRate int64
	if lastNetRX, exists := s.metricsCache.lastNetRX[vmID]; exists {
		if lastNetTX, exists := s.metricsCache.lastNetTX[vmID]; exists {
			if lastCollect, exists := s.metricsCache.lastNetCollect[vmID]; exists {
				timeDelta := now.Sub(lastCollect).Seconds()
				if timeDelta > 0 {
					netRXRate = int64(float64(vmStats.NetRXBytes-lastNetRX) / timeDelta)
					netTXRate = int64(float64(vmStats.NetTXBytes-lastNetTX) / timeDelta)
					if netRXRate < 0 {
						netRXRate = 0
					}
					if netTXRate < 0 {
						netTXRate = 0
					}
				}
			}
		}
	}

	// Calculate disk rates (bytes/sec)
	var diskReadRate, diskWriteRate int64
	if lastDiskRead, exists := s.metricsCache.lastDiskRead[vmID]; exists {
		if lastDiskWrite, exists := s.metricsCache.lastDiskWrite[vmID]; exists {
			if lastCollect, exists := s.metricsCache.lastDiskCollect[vmID]; exists {
				timeDelta := now.Sub(lastCollect).Seconds()
				if timeDelta > 0 {
					diskReadRate = int64(float64(vmStats.DiskReadBytes-lastDiskRead) / timeDelta)
					diskWriteRate = int64(float64(vmStats.DiskWriteBytes-lastDiskWrite) / timeDelta)
					if diskReadRate < 0 {
						diskReadRate = 0
					}
					if diskWriteRate < 0 {
						diskWriteRate = 0
					}
				}
			}
		}
	}

	// Update cache
	s.metricsCache.lastCPUTime[vmID] = vmStats.CPUTime
	s.metricsCache.lastCPUCollect[vmID] = now
	s.metricsCache.lastNetRX[vmID] = vmStats.NetRXBytes
	s.metricsCache.lastNetTX[vmID] = vmStats.NetTXBytes
	s.metricsCache.lastDiskRead[vmID] = vmStats.DiskReadBytes
	s.metricsCache.lastDiskWrite[vmID] = vmStats.DiskWriteBytes
	s.metricsCache.lastNetCollect[vmID] = now
	s.metricsCache.lastDiskCollect[vmID] = now

	// Prefer the guest's own view of memory. RSS is the host-side QEMU
	// resident set: it covers every guest page ever touched plus QEMU
	// overhead and never shrinks, so a long-lived guest reads as ~100% used
	// however idle it actually is.
	//
	// available-minus-usable is what `free` calls "used": it excludes page
	// cache, which the guest can reclaim on demand. Using "unused" (MemFree)
	// instead would count all cache as used and peg a busy guest near 100%
	// again. Fall back to RSS only when no balloon driver is reporting.
	memUsed := vmStats.MemoryRSS
	if vmStats.MemoryAvailable > 0 && vmStats.MemoryUsable > 0 {
		if used := vmStats.MemoryAvailable - vmStats.MemoryUsable; used >= 0 {
			memUsed = used
		}
	}

	return &pb.VMMetricsResponse{
		Timestamp: timestamppb.New(now),
		VmId:      vmID,
		Cpu: &pb.CPUMetrics{
			UsagePercent: cpuPercent,
		},
		Memory: &pb.MemoryMetrics{
			UsedBytes:      memUsed,
			AvailableBytes: vmStats.MemoryActual - memUsed,
			TotalBytes:     vmStats.MemoryActual,
		},
		Disk: &pb.DiskMetrics{
			ReadBytesPerSec:  diskReadRate,
			WriteBytesPerSec: diskWriteRate,
			ReadIops:         0,
			WriteIops:        0,
		},
		Network: &pb.NetworkMetrics{
			RxBytesPerSec:   netRXRate,
			TxBytesPerSec:   netTXRate,
			RxPacketsPerSec: 0,
			TxPacketsPerSec: 0,
		},
		Process: &pb.ProcessMetrics{
			ProcessCount: 0,
			ThreadCount:  0,
		},
	}
}

func (s *NodeAgentService) CreateSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	log.Printf("[NodeAgent] CreateSnapshot: vm_id=%s, operation=%v", req.VmId, req.Operation)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Snapshot operation %v on VM %s from node %s", req.Operation, req.VmId, nodeID)
	s.crashes.Hold(req.VmId)

	switch req.Operation {
	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE:
		if disks, derr := libvirt.BackupDisks(req.VmId); derr == nil {
			if why := storage.SnapshotBlocker(disks); why != "" {
				return nil, status.Error(codes.FailedPrecondition, why)
			}
		}
		snap, err := libvirt.CreateSnapshot(req.VmId, req.Name, req.Description,
			libvirt.SnapshotOptions{DiskOnly: req.GetDiskOnly(), Quiesce: req.GetQuiesce()})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create snapshot: %v", err)
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
			Snapshot: &pb.SnapshotInfo{
				SnapshotId:  snap.UUID,
				Name:        snap.Name,
				Description: snap.Description,
				CreatedAt:   timestamppb.New(snap.CreatedAt),
				SizeBytes:   snap.Size,
				VmState:     mapVMStatusToPBState(snap.State),
			},
		}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST:
		snaps, err := libvirt.ListSnapshots(req.VmId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list snapshots: %v", err)
		}
		var pbSnaps []*pb.SnapshotInfo
		for _, snap := range snaps {
			pbSnaps = append(pbSnaps, &pb.SnapshotInfo{
				SnapshotId:  snap.UUID,
				Name:        snap.Name,
				Description: snap.Description,
				CreatedAt:   timestamppb.New(snap.CreatedAt),
				SizeBytes:   snap.Size,
				VmState:     mapVMStatusToPBState(snap.State),
			})
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
			Snapshots: pbSnaps,
		}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_RESTORE:
		if req.SnapshotId == "" {
			return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for restore")
		}
		// A revert can boot the VM, and replaces its disks' contents.
		if migrations.Busy(req.VmId) {
			return nil, status.Error(codes.FailedPrecondition, errDisksBusy.Error())
		}
		after, err := libvirt.RestoreSnapshot(req.VmId, req.SnapshotId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to restore snapshot: %v", err)
		}
		// The VM may now be running on the console socket of an older definition.
		secureConsoles(false)
		// Where the revert left the VM, so the panel records it as it is.
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
			Snapshot:  &pb.SnapshotInfo{SnapshotId: req.SnapshotId, VmState: mapVMStatusToPBState(after)},
		}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE:
		if req.SnapshotId == "" {
			return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for delete")
		}
		if err := libvirt.DeleteSnapshot(req.VmId, req.SnapshotId); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to delete snapshot: %v", err)
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
		}, nil

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown snapshot operation: %v", req.Operation)
	}
}

func (s *NodeAgentService) ApplyNetworkConfig(ctx context.Context, req *pb.NetworkConfigRequest) (*pb.NetworkConfigResponse, error) {
	log.Printf("[NodeAgent] ApplyNetworkConfig: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	if req.Config == nil {
		return nil, status.Errorf(codes.InvalidArgument, "config is required")
	}

	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	if s.networkMgr == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "network manager not initialized")
	}
	// Its addresses are routed to another node now.
	if migration.IsFenced(req.VmId) {
		return nil, status.Error(codes.FailedPrecondition, errFenced.Error())
	}

	unlock := lockVMNetwork(req.VmId)
	defer unlock()
	return s.applyNetworkConfigLocked(ctx, req)
}

// vmNetworkLocks holds one *sync.Mutex per VM: a push from the panel and a
// drift repair of the same VM must not interleave.
var vmNetworkLocks sync.Map

func lockVMNetwork(vmID string) func() {
	l, _ := vmNetworkLocks.LoadOrStore(vmID, &sync.Mutex{})
	l.(*sync.Mutex).Lock()
	return l.(*sync.Mutex).Unlock
}

// desiredFromRequest turns a network-config request into the host state it
// asks for, plus the interface holding the primary address. The apply and the
// drift check both go through it, so they always agree on what is wanted.
func desiredFromRequest(req *pb.NetworkConfigRequest) (network.VMDesired, *pb.NetworkInterface) {
	cfg := req.Config
	d := network.VMDesired{
		VMID:          req.VmId,
		DefaultAccept: cfg.GetFirewallDefault() == models.FirewallDefaultAccept,
		BlockSMTP:     cfg.GetBlockSmtp(),
	}
	// The first interface with an address is the primary; further addressed
	// interfaces are firewalled with the same rule set.
	var primary *pb.NetworkInterface
	seen := map[string]bool{}
	antiSpoof := false
	for _, iface := range cfg.Interfaces {
		antiSpoof = antiSpoof || iface.AntiSpoofing
		if d.VLANID == 0 && iface.VlanId > 0 {
			d.VLANID = int(iface.VlanId)
		}
		if iface.IpAddress == "" || seen[iface.IpAddress] {
			continue
		}
		seen[iface.IpAddress] = true
		if primary == nil {
			primary, d.IP = iface, iface.IpAddress
		} else {
			d.ExtraIPs = append(d.ExtraIPs, iface.IpAddress)
		}
		// Each address is pinned to the NIC on its pool's bridge, so a VM with
		// several NICs keeps them all.
		d.AntiSpoof = append(d.AntiSpoof, network.SpoofAddr{IP: iface.IpAddress, Bridge: iface.BridgeName})
	}
	if !antiSpoof {
		d.AntiSpoof = nil
	}
	if cfg.BandwidthLimits != nil {
		d.BandwidthMbps = int(cfg.BandwidthLimits.IngressRateMbps)
	}
	for _, rule := range cfg.FirewallRules {
		action := protoActionToModel(rule.Action)
		if rule.Reject {
			action = "reject"
		}
		d.Rules = append(d.Rules, models.FirewallRule{
			Direction: protoDirectionToModel(rule.Direction),
			Action:    action,
			Protocol:  rule.Protocol,
			SourceIP:  rule.SourceCidr,
			PortRange: rule.DestPort,
			Priority:  int(rule.Priority),
			ICMPType:  rule.IcmpType,
			Log:       rule.Log,
			Family:    rule.Family,
		})
	}
	for _, pf := range cfg.PortForwards {
		d.PortForwards = append(d.PortForwards, network.PortForwardSpec{
			External: int(pf.ExternalPort), Internal: int(pf.InternalPort), Protocol: pf.Protocol, SourceCIDR: pf.SourceCidr,
		})
	}
	// The primary interface also carries the VM's routed IPv6 prefix, if it has
	// one. MAC and bridge come from the domain: the route's next hop is the
	// guest's MAC-derived link-local, and an imported VM does not use the
	// panel's deterministic MAC.
	if primary != nil && primary.Ipv6Prefix != "" {
		mac, merr := libvirt.GetVMInterfaceMAC(req.VmId)
		if merr != nil {
			if mac = primary.MacAddress; mac == "" {
				mac = generateMAC(req.VmId)
			}
		}
		bridge, berr := libvirt.GetVMInterfaceBridge(req.VmId)
		if berr != nil {
			bridge = primary.BridgeName
		}
		d.IPv6 = &network.IPv6Delegation{Prefix: primary.Ipv6Prefix, MAC: mac, Bridge: bridge, Extra: primary.Ipv6Extra}
	} else if onLink := onLinkIPv6Prefix(primary); onLink != "" {
		// An address on a shared subnet, with no routed prefix behind it.
		d.IPv6 = &network.IPv6Delegation{Prefix: onLink, OnLink: true}
	}
	d.DHCP = dhcpHosts(req.VmId, cfg.Interfaces, primary)
	return d, primary
}

// dhcpHosts are the static leases a VM's addresses ask for: each goes to the
// VM's card on the address's bridge, read from the domain (an imported VM's
// MACs are its own). Only an address with its prefix can be served, and the
// first one per card wins, since DHCP gives a card one address.
func dhcpHosts(vmID string, ifaces []*pb.NetworkInterface, primary *pb.NetworkInterface) []dhcp.Host {
	var want []*pb.NetworkInterface
	for _, iface := range ifaces {
		if iface.GetDhcpServer() && iface.Netmask > 0 && !strings.Contains(iface.IpAddress, ":") {
			want = append(want, iface)
		}
	}
	if len(want) == 0 {
		return nil
	}
	nics, err := libvirt.GetVMNICs(vmID)
	if err != nil || len(nics) == 0 {
		log.Printf("[NodeAgent] VM %s: DHCP not served, its cards could not be read: %v", vmID, err)
		return nil
	}
	var out []dhcp.Host
	for _, iface := range want {
		nic := nics[0]
		if iface != primary || iface.BridgeName != "" {
			found := false
			for _, n := range nics {
				if iface.BridgeName != "" && n.Bridge == iface.BridgeName {
					nic, found = n, true
					break
				}
			}
			if !found && iface != primary {
				continue
			}
		}
		if nic.Bridge == "" || slices.ContainsFunc(out, func(h dhcp.Host) bool { return strings.EqualFold(h.MAC, nic.MAC) }) {
			continue
		}
		out = append(out, dhcp.Host{Bridge: nic.Bridge, MAC: strings.ToLower(nic.MAC), IP: iface.IpAddress, Prefix: int(iface.Netmask),
			Gateway: iface.Gateway, DNS: iface.Dns})
	}
	return out
}

// guestNetFromRequest is the addressing configure_guest writes into the guest:
// the primary with its gateway, every further address the panel sent a prefix
// for, and the IPv6. ok is false when the primary has no prefix.
//
// An address whose bridge is another card's (a VPC, a second network) goes on
// that card; nics are the running VM's cards, the first being the primary.
// An address of a card only the definition has (added for the next start) is
// left out: on the first card it would put a private network on the public one.
func guestNetFromRequest(req *pb.NetworkConfigRequest, primary *pb.NetworkInterface, nics, defined []libvirt.VMNIC) (guestNet, bool) {
	if primary == nil || primary.Netmask <= 0 {
		return guestNet{}, false
	}
	n := guestNet{
		IP: primary.IpAddress, Prefix: int(primary.Netmask), Gateway: primary.Gateway,
		V6:  guestIPv6From(primary.Ipv6Address, primary.Ipv6Gateway, primary.Ipv6Dns),
		DNS: primary.Dns,
	}
	if n.V6.Address != "" {
		n.V6.Extra = primary.Ipv6Extra
	}
	seen := map[string]bool{primary.IpAddress: true}
	for _, iface := range req.Config.Interfaces {
		if iface.IpAddress == "" || seen[iface.IpAddress] {
			continue
		}
		seen[iface.IpAddress] = true
		if iface.Netmask <= 0 || strings.Contains(iface.IpAddress, ":") {
			log.Printf("[NodeAgent] WARNING: VM %s: %s has no prefix, left out of the guest's config", req.VmId, iface.IpAddress)
			continue
		}
		addr := guestos.GuestAddr{Address: iface.IpAddress, Prefix: int(iface.Netmask)}
		if card := cardOnBridge(nics, iface.BridgeName); card != "" {
			n.NICs = addToCard(n.NICs, card, addr)
			continue
		}
		if card := cardOnBridge(defined, iface.BridgeName); card != "" {
			log.Printf("[NodeAgent] VM %s: %s is for card %s, which the running VM gets at its next start; left out of the guest's config", req.VmId, iface.IpAddress, card)
			continue
		}
		n.Extra = append(n.Extra, addr)
	}
	return n, true
}

// cardOnBridge is the MAC of the VM's card on bridge when that is not its
// first card; "" otherwise (the address then goes on the first).
func cardOnBridge(nics []libvirt.VMNIC, bridge string) string {
	if bridge == "" || len(nics) < 2 || nics[0].Bridge == bridge {
		return ""
	}
	for _, c := range nics[1:] {
		if c.Bridge == bridge {
			return strings.ToLower(c.MAC)
		}
	}
	return ""
}

func addToCard(cards []guestos.GuestNIC, mac string, a guestos.GuestAddr) []guestos.GuestNIC {
	for i := range cards {
		if cards[i].MAC == mac {
			cards[i].Addresses = append(cards[i].Addresses, a)
			return cards
		}
	}
	return append(cards, guestos.GuestNIC{MAC: mac, Addresses: []guestos.GuestAddr{a}})
}

func (s *NodeAgentService) applyNetworkConfigLocked(ctx context.Context, req *pb.NetworkConfigRequest) (*pb.NetworkConfigResponse, error) {
	// Get VM info to verify VM exists
	if _, err := libvirt.GetVMInfo(req.VmId); err != nil {
		return nil, vmLookupError(err)
	}

	d, primary := desiredFromRequest(req)

	// No address left: the last one was released, and nothing of the VM may
	// stay on the host to keep using it.
	if d.IP == "" {
		if err := s.networkMgr.CleanupVMNetwork(req.VmId); err != nil {
			log.Printf("[NodeAgent] WARNING: VM %s has no address left; removing its host rules: %v", req.VmId, err)
		}
		updateNetworkConfig(netConfigPath(), currentBootID(), req.VmId, nil)
		clearPendingGuest(req.VmId)
		_ = s.hostProxy.Set(req.VmId, nil)
		log.Printf("[NodeAgent] VM %s has no address left: host rules removed", req.VmId)
		return &pb.NetworkConfigResponse{Success: true}, nil
	}

	warnings, err := s.networkMgr.ApplyVM(d)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to apply network config: %v", err)
	}
	for _, w := range warnings {
		log.Printf("[NodeAgent] WARNING: VM %s: %s", req.VmId, w)
	}

	// Layer 1 of anti-spoofing follows the addressing too: the IPv6-aware
	// nwfilter when the VM has a prefix, stock clean-traffic when it lost one,
	// and a refreshed IP parameter when the address changed. Domains this
	// feature never touched are left alone (see SyncInterfaceFilter).
	if len(d.AntiSpoof) > 0 {
		var v6Prefix string
		if primary != nil {
			if v6Prefix = primary.Ipv6Prefix; v6Prefix == "" {
				v6Prefix = onLinkIPv6Prefix(primary) // a /128: the guest filter treats it as on-link
			}
		}
		if err := libvirt.SyncInterfaceFilter(req.VmId, d.IP, v6Prefix); err != nil {
			log.Printf("[NodeAgent] WARNING: nwfilter not synced for VM %s: %v", req.VmId, err)
		}
	}

	log.Printf("[NodeAgent] Network config applied to VM %s (IP: %s +%d, bandwidth: %d Mbps, rules: %d, smtp blocked: %v)",
		req.VmId, d.IP, len(d.ExtraIPs), d.BandwidthMbps, len(d.Rules), d.BlockSMTP)

	resp := &pb.NetworkConfigResponse{Success: true, AppliedInterfaces: req.Config.Interfaces}

	// Hostnames go to the primary address. Not routing them leaves the rest of
	// the config in force; the panel shows why beside each hostname.
	if err := s.hostProxy.Set(req.VmId, hostproxy.FromProto(req.Config.GetHostnameForwards(), d.IP)); err != nil {
		resp.HostnameError = err.Error()
		log.Printf("[NodeAgent] WARNING: VM %s: hostnames not routed: %v", req.VmId, err)
	}

	// The addresses changed: the guest must adopt them too. It gets every
	// address it holds, so adding one never takes the others away.
	if req.ConfigureGuest {
		nics, _ := libvirt.GetVMNICs(req.VmId)
		defined, _ := libvirt.GetVMDefinedNICs(req.VmId)
		if n, ok := guestNetFromRequest(req, primary, nics, defined); !ok {
			log.Printf("[NodeAgent] WARNING: guest reconfigure for VM %s skipped: no prefix for %s", req.VmId, d.IP)
		} else if err := s.applyGuestNetwork(ctx, req.VmId, n, guestAgentWait); err != nil {
			var na *guestNotApplied
			if !errors.As(err, &na) {
				return nil, status.Errorf(codes.Internal, "host rules applied but guest reconfiguration failed: %v", err)
			}
			resp.GuestPending, resp.GuestRetry = na.msg, na.pending
			if na.pending {
				savePendingGuest(req.VmId, n)
			} else {
				clearPendingGuest(req.VmId)
			}
		} else {
			clearPendingGuest(req.VmId)
		}
	}

	saveNetworkConfig(req) // replayed after a node reboot, and checked for drift
	return resp, nil
}

// protoDirectionToModel converts proto FirewallDirection to model string
func protoDirectionToModel(d pb.FirewallDirection) string {
	switch d {
	case pb.FirewallDirection_FIREWALL_DIRECTION_INBOUND:
		return "inbound"
	case pb.FirewallDirection_FIREWALL_DIRECTION_OUTBOUND:
		return "outbound"
	default:
		return "inbound"
	}
}

// protoActionToModel converts proto FirewallAction to model string
func protoActionToModel(a pb.FirewallAction) string {
	switch a {
	case pb.FirewallAction_FIREWALL_ACTION_ALLOW:
		return "allow"
	case pb.FirewallAction_FIREWALL_ACTION_DENY:
		return "deny"
	case pb.FirewallAction_FIREWALL_ACTION_REJECT:
		return "reject"
	default:
		return "deny"
	}
}

func (s *NodeAgentService) StartVNCProxy(ctx context.Context, req *pb.VNCProxyRequest) (*pb.VNCProxyResponse, error) {
	log.Printf("[NodeAgent] StartVNCProxy: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Starting VNC proxy for VM %s from node %s", req.VmId, nodeID)

	// Set VNC password if provided via metadata
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		if passwords := md.Get("vnc-password"); len(passwords) > 0 && passwords[0] != "" {
			if err := libvirt.SetVNCPassword(req.VmId, passwords[0]); err != nil {
				log.Printf("[NodeAgent] Warning: failed to set VNC password for VM %s: %v", req.VmId, err)
			} else {
				log.Printf("[NodeAgent] VNC password set for VM %s", req.VmId)
			}
		}
	}

	vmInfo, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, vmLookupError(err)
	}

	vncPort := vmInfo.VNCPort
	if vncPort == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "VM %s does not have VNC configured", req.VmId)
	}

	websocketPort := int(req.WebsocketPort)
	if websocketPort == 0 {
		websocketPort = 6080
	}

	expireSeconds := int32(3600)
	if req.ExpirySeconds > 0 {
		expireSeconds = req.ExpirySeconds
	}

	var wsURL, token string
	var expiresAt time.Time

	if s.vncProxy != nil {
		wsURL, token, expiresAt, err = s.vncProxy.StartSession(req.VmId, vncPort, websocketPort, expireSeconds)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to start VNC proxy: %v", err)
		}
	} else {
		token = generateAuthToken()
		expiresAt = time.Now().Add(time.Duration(expireSeconds) * time.Second)
		wsURL = fmt.Sprintf("ws://localhost:%d/websockify?token=%s", websocketPort, token)
	}

	return &pb.VNCProxyResponse{
		Success:       true,
		WebsocketUrl:  wsURL,
		WebsocketPort: int32(websocketPort),
		Token:         token,
		ExpiresAt:     timestamppb.New(expiresAt),
	}, nil
}

// GetNodeInfo retrieves detailed system information about the node
func (s *NodeAgentService) GetNodeInfo(ctx context.Context, req *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	log.Printf("[NodeAgent] GetNodeInfo called")

	// Get OS info
	osName := runtime.GOOS
	osVersion := ""
	kernelVersion := ""
	architecture := runtime.GOARCH

	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				osName = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			}
			if strings.HasPrefix(line, "VERSION_ID=") {
				osVersion = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"")
			}
		}
	}

	if out, err := exec.Command("uname", "-r").Output(); err == nil {
		kernelVersion = strings.TrimSpace(string(out))
	}

	// Get CPU info
	cpuModel := ""
	var cpuCores int32
	cpuThreads := int32(runtime.NumCPU())

	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					cpuModel = strings.TrimSpace(parts[1])
				}
			}
			if strings.HasPrefix(line, "cpu cores") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &cpuCores)
				}
			}
		}
	}
	if cpuCores == 0 {
		cpuCores = cpuThreads
	}

	// Get memory info
	var memTotalBytes int64
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				var memKB int64
				fmt.Sscanf(line, "MemTotal: %d kB", &memKB)
				memTotalBytes = memKB * 1024
				break
			}
		}
	}

	// Get disk info (root filesystem)
	var diskTotalBytes int64
	if out, err := exec.Command("df", "-B1", "/").Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 2 {
				fmt.Sscanf(fields[1], "%d", &diskTotalBytes)
			}
		}
	}

	// Get libvirt version
	libvirtVersion := ""
	if out, err := exec.Command("virsh", "version", "--daemon").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "libvirtd") || strings.Contains(line, "daemon") {
				libvirtVersion = strings.TrimSpace(line)
				break
			}
		}
	}
	if libvirtVersion == "" {
		if out, err := exec.Command("libvirtd", "--version").Output(); err == nil {
			libvirtVersion = strings.TrimSpace(string(out))
		}
	}

	// Collect live metrics from healthCollector
	var cpuPercent, memUsedPercent, diskUsedPercent float64
	var memUsedBytes, diskUsedBytes int64
	var networkRx, networkTx, diskRead, diskWrite int64
	var loadAvg1, loadAvg5, loadAvg15 float64
	var runningVMs int32
	var availableCPUs int32
	var availableMemMB, availableDiskGB int64

	if s.healthCollector != nil {
		metrics := s.healthCollector.Collect()
		cpuPercent = metrics.CPUPercent
		memUsedBytes = metrics.MemoryUsed
		memUsedPercent = metrics.MemoryUsedPercent
		diskUsedBytes = metrics.DiskUsed
		diskUsedPercent = metrics.DiskUsedPercent
		networkRx = metrics.NetworkRXBytesPerSec
		networkTx = metrics.NetworkTXBytesPerSec
		diskRead = metrics.DiskReadBytesPerSec
		diskWrite = metrics.DiskWriteBytesPerSec
		runningVMs = int32(metrics.RunningVMCount)
		availableCPUs = metrics.AvailableCPUs
		availableMemMB = metrics.AvailableMemoryMB
		availableDiskGB = metrics.AvailableDiskGB
		if len(metrics.LoadAvg) >= 3 {
			loadAvg1 = metrics.LoadAvg[0]
			loadAvg5 = metrics.LoadAvg[1]
			loadAvg15 = metrics.LoadAvg[2]
		}
	}

	// Query libvirt directly for running VM count if healthCollector didn't provide it
	if runningVMs == 0 {
		if out, err := exec.Command("virsh", "list", "--state-running", "--name").Output(); err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			count := 0
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					count++
				}
			}
			runningVMs = int32(count)
		}
	}

	agentBuild := buildinfo.Get()
	return &pb.GetNodeInfoResponse{
		Success: true,
		OsInfo: &pb.OSInfo{
			OsName:        osName,
			OsVersion:     osVersion,
			KernelVersion: kernelVersion,
			Architecture:  architecture,
		},
		CpuInfo: &pb.CPUInfo{
			Model:   cpuModel,
			Cores:   cpuCores,
			Threads: cpuThreads,
		},
		MemoryTotalBytes:     memTotalBytes,
		DiskTotalBytes:       diskTotalBytes,
		LibvirtVersion:       libvirtVersion,
		AgentCommit:          agentBuild.Commit,
		AgentBuildTime:       agentBuild.BuildTime,
		AgentVersion:         agentBuild.Version,
		Features:             append(append([]string{}, agentFeatures...), hostFeatures()...),
		AgentSettings:        agentenv.Report(os.Getenv),
		CpuPercent:           cpuPercent,
		MemoryUsedBytes:      memUsedBytes,
		MemoryUsedPercent:    memUsedPercent,
		DiskUsedBytes:        diskUsedBytes,
		DiskUsedPercent:      diskUsedPercent,
		NetworkRxBytesPerSec: networkRx,
		NetworkTxBytesPerSec: networkTx,
		DiskReadBytesPerSec:  diskRead,
		DiskWriteBytesPerSec: diskWrite,
		LoadAvg_1:            loadAvg1,
		LoadAvg_5:            loadAvg5,
		LoadAvg_15:           loadAvg15,
		RunningVmCount:       runningVMs,
		AvailableCpus:        availableCPUs,
		AvailableMemoryMb:    availableMemMB,
		AvailableDiskGb:      availableDiskGB,
	}, nil
}

// GetLiveMetrics retrieves real-time system metrics from the node (CPU, memory,
// disk, network I/O, load average) without the extra static system-info probing
// that GetNodeInfo does (no uname/df/virsh exec calls) — cheap enough for
// frequent polling.
func (s *NodeAgentService) GetLiveMetrics(ctx context.Context, req *pb.GetLiveMetricsRequest) (*pb.GetLiveMetricsResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	if s.healthCollector == nil {
		return &pb.GetLiveMetricsResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "health collector not initialized"},
		}, nil
	}

	metrics := s.healthCollector.Collect()

	// Read straight from procfs rather than through the health collector: this is
	// a kernel table with a hard ceiling, not a sampled rate, and it needs no
	// smoothing or history to be meaningful.
	conntrackCount, conntrackMax := network.ConntrackUsage()

	var natAddress string
	if s.networkMgr != nil {
		natAddress = s.networkMgr.NATAddress()
	}

	var loadAvg1, loadAvg5, loadAvg15 float64
	if len(metrics.LoadAvg) >= 3 {
		loadAvg1 = metrics.LoadAvg[0]
		loadAvg5 = metrics.LoadAvg[1]
		loadAvg15 = metrics.LoadAvg[2]
	}

	return &pb.GetLiveMetricsResponse{
		Success:              true,
		CpuPercent:           metrics.CPUPercent,
		MemoryUsedBytes:      metrics.MemoryUsed,
		MemoryTotalBytes:     metrics.MemoryTotal,
		MemoryUsedPercent:    metrics.MemoryUsedPercent,
		DiskUsedBytes:        metrics.DiskUsed,
		DiskTotalBytes:       metrics.DiskTotal,
		DiskUsedPercent:      metrics.DiskUsedPercent,
		NetworkRxBytesPerSec: metrics.NetworkRXBytesPerSec,
		NetworkTxBytesPerSec: metrics.NetworkTXBytesPerSec,
		DiskReadBytesPerSec:  metrics.DiskReadBytesPerSec,
		DiskWriteBytesPerSec: metrics.DiskWriteBytesPerSec,
		LoadAvg_1:            loadAvg1,
		LoadAvg_5:            loadAvg5,
		LoadAvg_15:           loadAvg15,
		RunningVmCount:       int32(metrics.RunningVMCount),
		AvailableCpus:        metrics.AvailableCPUs,
		AvailableMemoryMb:    metrics.AvailableMemoryMB,
		AvailableDiskGb:      metrics.AvailableDiskGB,
		ConntrackCount:       conntrackCount,
		ConntrackMax:         conntrackMax,
		FirewallEnforcement:  network.FirewallEnforcement(),
		NatAddress:           natAddress,
		GuestNetworkPending:  pendingGuestIDs(),
		NetworkRepairs:       networkRepairs.Load(),
		NetworkRepairLast:    networkRepairLastText(),
	}, nil
}

// GetExportProgress reports the disk exports running on this node right now.
func (s *NodeAgentService) GetExportProgress(ctx context.Context, req *pb.GetExportProgressRequest) (*pb.GetExportProgressResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	running := storage.ListExports()
	out := make([]*pb.ExportProgress, 0, len(running))
	for _, e := range running {
		out = append(out, &pb.ExportProgress{
			VmId:         e.VMID,
			Kind:         e.Kind,
			SourceBytes:  e.SourceBytes,
			WrittenBytes: storage.WrittenBytes(e.DestPath),
			StartedAt:    timestamppb.New(e.StartedAt),
		})
	}
	return &pb.GetExportProgressResponse{Success: true, Exports: out}, nil
}

// SetConsoleAccess enforces console enable/disable on the domain itself.
//
// Recording the flag in the panel alone gated only the panel's own proxy: qemu
// carried on listening on 0.0.0.0, so a console shown as disabled was still
// reachable by anyone who could route to the node and knew the VNC password.
func (s *NodeAgentService) SetConsoleAccess(ctx context.Context, req *pb.SetConsoleAccessRequest) (*pb.SetConsoleAccessResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.GetVmId() == "" {
		return &pb.SetConsoleAccessResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "vm_id is required"},
		}, nil
	}

	port, restartRequired, err := libvirt.SetConsoleAccess(req.GetVmId(), req.GetEnabled(), req.GetVncPassword())
	if err != nil {
		return &pb.SetConsoleAccessResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()},
		}, nil
	}

	log.Printf("[NodeAgent] console access for %s set to %v (port=%d, restart_required=%v)",
		req.GetVmId(), req.GetEnabled(), port, restartRequired)
	return &pb.SetConsoleAccessResponse{
		Success:         true,
		VncPort:         int32(port),
		RestartRequired: restartRequired,
	}, nil
}

// GetStorageReport measures each pool path's own filesystem and reports where
// the node's domains actually keep their disks.
//
// The panel used to derive every pool's usage from the node's root filesystem,
// which was wrong in both directions at once: a pool directory holding nothing
// reported gigabytes in use, while the separate volume holding every customer's
// disk sat at 76% full and invisible. Measuring the pool's own path fixes the
// number; the disk locations are what reveal storage in use that was never
// registered as a pool.
func (s *NodeAgentService) GetStorageReport(ctx context.Context, req *pb.GetStorageReportRequest) (*pb.GetStorageReportResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	pools := make([]*pb.PoolCapacity, 0, len(req.GetPaths()))
	for _, p := range req.GetPaths() {
		c := storage.Capacity(p)
		pools = append(pools, &pb.PoolCapacity{
			Path:           c.Path,
			Exists:         c.Exists,
			TotalBytes:     c.Total,
			UsedBytes:      c.Used,
			AvailableBytes: c.Available,
			Filesystem:     c.Filesystem,
			Problem:        c.Problem,
		})
	}

	// Best-effort: a node whose libvirt is unreachable should still report the
	// capacity figures, which are the part that stops an operator overfilling a
	// volume.
	var locations []*pb.DiskLocation
	if found, err := libvirt.DiskLocations(); err != nil {
		log.Printf("[NodeAgent] WARNING: could not enumerate disk locations: %v", err)
	} else {
		for path, count := range found {
			locations = append(locations, &pb.DiskLocation{Path: path, DiskCount: int32(count)})
		}
	}

	return &pb.GetStorageReportResponse{Success: true, Pools: pools, DiskLocations: locations, LvmPools: lvmCapacities(ctx, req.GetLvmPools())}, nil
}

// GetGuestConnections reports how fast each guest on this node is opening new
// outbound connections, so the panel can tell a compromised guest from a busy
// one. The guest list comes from libvirt, not from anything the panel told us,
// so guests the panel has never seen are still reported.
func (s *NodeAgentService) GetGuestConnections(ctx context.Context, req *pb.GetGuestConnectionsRequest) (*pb.GetGuestConnectionsResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if s.networkMgr == nil {
		return &pb.GetGuestConnectionsResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "network manager not initialized"},
		}, nil
	}

	ifaces, err := libvirt.ListGuestInterfaces()
	if err != nil {
		return &pb.GetGuestConnectionsResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()},
		}, nil
	}

	nics := make([]network.GuestNIC, 0, len(ifaces))
	byMAC := make(map[string]libvirt.GuestInterface, len(ifaces))
	for _, iface := range ifaces {
		nics = append(nics, network.GuestNIC{MAC: iface.MAC, Tap: iface.Target})
		byMAC[iface.MAC] = iface
	}

	counters, err := s.networkMgr.GuestConnectionCounters(nics)
	if err != nil {
		return &pb.GetGuestConnectionsResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()},
		}, nil
	}

	guests := make([]*pb.GuestConnectionReport, 0, len(counters))
	for _, c := range counters {
		iface := byMAC[c.MAC]
		guests = append(guests, &pb.GuestConnectionReport{
			Mac:           c.MAC,
			VmId:          iface.VMID,
			InterfaceName: iface.Target,
			SynPackets:    c.SYNPackets,
			Quarantined:   c.Quarantined,
			SynInPackets:  c.SYNInPackets,
			PacketsOut:    c.PacketsOut,
			PacketsIn:     c.PacketsIn,
		})
	}
	return &pb.GetGuestConnectionsResponse{Success: true, Guests: guests, NullRoutes: nullRoutesPB(s.networkMgr.NullRoutes())}, nil
}

func nullRoutesPB(routes []netrules.NullRoute) []*pb.NullRoute {
	out := make([]*pb.NullRoute, 0, len(routes))
	for _, r := range routes {
		out = append(out, &pb.NullRoute{Ip: r.IP, UntilUnix: r.Until.Unix(), Reason: r.Reason})
	}
	return out
}

// SetNullRoute drops all traffic to one address for a while, or lifts it. The
// node lifts it by itself when the time is up.
func (s *NodeAgentService) SetNullRoute(ctx context.Context, req *pb.SetNullRouteRequest) (*pb.SetNullRouteResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if s.networkMgr == nil {
		return &pb.SetNullRouteResponse{
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "network manager not initialized"},
		}, nil
	}
	routes, err := s.networkMgr.SetNullRoute(req.GetIp(), time.Duration(req.GetMinutes())*time.Minute, req.GetReason(), req.GetOn())
	if err != nil {
		return &pb.SetNullRouteResponse{
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: err.Error()},
		}, nil
	}
	log.Printf("[NodeAgent] null-route %v for %s, %d min (%s)", req.GetOn(), req.GetIp(), req.GetMinutes(), req.GetReason())
	return &pb.SetNullRouteResponse{Success: true, NullRoutes: nullRoutesPB(routes)}, nil
}

// SetQuarantine cuts a guest off the network, or puts it back, without stopping
// it — the owner keeps console access and their data while the abuse stops.
func (s *NodeAgentService) SetQuarantine(ctx context.Context, req *pb.SetQuarantineRequest) (*pb.SetQuarantineResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if s.networkMgr == nil {
		return &pb.SetQuarantineResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "network manager not initialized"},
		}, nil
	}

	macs, err := s.networkMgr.SetQuarantine(req.Mac, req.Reason, req.Quarantined)
	if err != nil {
		return &pb.SetQuarantineResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: err.Error()},
		}, nil
	}

	log.Printf("[NodeAgent] quarantine %v for %s (%s)", req.Quarantined, req.Mac, req.Reason)
	return &pb.SetQuarantineResponse{Success: true, QuarantinedMacs: macs}, nil
}

// ScanVMs scans the node for all VMs via libvirt and returns their info for import
func (s *NodeAgentService) ScanVMs(ctx context.Context, req *pb.ScanVMsRequest) (*pb.ScanVMsResponse, error) {
	log.Printf("[NodeAgent] ScanVMs called")

	vms, err := libvirt.ListVMs()
	if err != nil {
		return &pb.ScanVMsResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: 500, Message: fmt.Sprintf("failed to list VMs: %v", err)},
		}, nil
	}

	var scannedVMs []*pb.ScannedVM
	for _, vm := range vms {
		scanned := &pb.ScannedVM{
			Uuid:     vm.UUID,
			Name:     vm.Name,
			Cpu:      int32(vm.CPU),
			MemoryMb: int64(vm.Memory / (1024 * 1024)), // Convert from bytes to MB
			VncPort:  int32(vm.VNCPort),
			Status:   string(vm.Status),
		}

		// Try to get detailed info from XML
		xmlInfo, err := getVMXMLDetails(vm.UUID)
		if err == nil {
			scanned.Hostname = xmlInfo.hostname
			scanned.VncPassword = xmlInfo.vncPassword
			scanned.Disks = xmlInfo.disks
			scanned.Networks = xmlInfo.networks
			scanned.XmlPath = xmlInfo.xmlPath
		}

		// Query guest-agent for real hostname and IP (overrides XML metadata)
		if vm.Status == "running" {
			gaHostname, gaIPs := s.guestAgentHostname(vm.UUID), []string(nil)
			if ips, silent := s.statusIPs(vm.UUID); !silent {
				gaIPs = ipv4Only(ips)
			}
			if gaHostname != "" {
				scanned.Hostname = gaHostname
			}
			// Add IPs to network info
			for i, ip := range gaIPs {
				if i < len(scanned.Networks) {
					scanned.Networks[i].IpAddress = ip
				} else {
					scanned.Networks = append(scanned.Networks, &pb.ScannedNetwork{
						IpAddress: ip,
					})
				}
			}
		}

		scannedVMs = append(scannedVMs, scanned)
	}

	log.Printf("[NodeAgent] ScanVMs found %d VMs", len(scannedVMs))

	return &pb.ScanVMsResponse{
		Success:    true,
		Vms:        scannedVMs,
		TotalFound: int32(len(scannedVMs)),
	}, nil
}

// vmXMLDetails holds detailed info extracted from VM XML
type vmXMLDetails struct {
	hostname    string
	vncPassword string
	disks       []*pb.ScannedDisk
	networks    []*pb.ScannedNetwork
	xmlPath     string
}

// getVMXMLDetails extracts detailed VM info from libvirt XML
func getVMXMLDetails(uuidStr string) (*vmXMLDetails, error) {
	details := &vmXMLDetails{}

	err := libvirt.WithConnection(func(conn *libvirtLib.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()

		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return err
		}

		// Parse XML to extract details
		var domain struct {
			Name    string `xml:"name"`
			Devices struct {
				Disks []struct {
					Device string `xml:"device,attr"`
					Driver struct {
						Type string `xml:"type,attr"`
					} `xml:"driver"`
					Source struct {
						File string `xml:"file,attr"`
						Dev  string `xml:"dev,attr"`
					} `xml:"source"`
					Target struct {
						Dev string `xml:"dev,attr"`
						Bus string `xml:"bus,attr"`
					} `xml:"target"`
				} `xml:"disk"`
				Interfaces []struct {
					Type string `xml:"type,attr"`
					MAC  struct {
						Address string `xml:"address,attr"`
					} `xml:"mac"`
					Source struct {
						Bridge string `xml:"bridge,attr"`
					} `xml:"source"`
					Model struct {
						Type string `xml:"type,attr"`
					} `xml:"model"`
				} `xml:"interface"`
				Graphics []struct {
					Type     string `xml:"type,attr"`
					Port     int    `xml:"port,attr"`
					Password string `xml:"passwd,attr"`
				} `xml:"graphics"`
			} `xml:"devices"`
			Metadata struct {
				Raw string `xml:",innerxml"`
			} `xml:"metadata"`
		}

		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return err
		}

		// Extract hostname from metadata
		details.hostname = extractHostnameFromMetadata(domain.Metadata.Raw)
		if details.hostname == "" {
			details.hostname = domain.Name
		}

		// Extract VNC password
		for _, g := range domain.Devices.Graphics {
			if g.Type == "vnc" {
				details.vncPassword = g.Password
				break
			}
		}

		// Extract disks
		for _, d := range domain.Devices.Disks {
			if d.Source.File == "" && d.Source.Dev != "" && d.Device == "disk" {
				// A block device (an LVM volume): measured, never probed.
				size, _ := storage.DeviceSize(d.Source.Dev)
				details.disks = append(details.disks, &pb.ScannedDisk{
					SourceFile: d.Source.Dev,
					Format:     d.Driver.Type,
					Device:     d.Device,
					Bus:        d.Target.Bus,
					TargetDev:  d.Target.Dev,
					SizeBytes:  size,
				})
				continue
			}
			if d.Source.File != "" {
				// Without a size the panel falls back to a 20GB placeholder.
				var size int64
				if d.Device == "disk" {
					if info, err := storage.NewQCOW2Manager().ImageInfo(d.Source.File); err == nil {
						size = info.VirtualSize
					}
				}
				details.disks = append(details.disks, &pb.ScannedDisk{
					SourceFile: d.Source.File,
					Format:     d.Driver.Type,
					Device:     d.Device,
					Bus:        d.Target.Bus,
					TargetDev:  d.Target.Dev,
					SizeBytes:  size,
				})
			}
		}

		// Extract networks
		for _, iface := range domain.Devices.Interfaces {
			details.networks = append(details.networks, &pb.ScannedNetwork{
				MacAddress: iface.MAC.Address,
				Bridge:     iface.Source.Bridge,
				Model:      iface.Model.Type,
			})
		}

		// XML path
		details.xmlPath = fmt.Sprintf("/etc/libvirt/qemu/%s.xml", domain.Name)

		return nil
	})

	return details, err
}

// extractHostnameFromMetadata extracts hostname from imported-guest metadata XML
func extractHostnameFromMetadata(rawXML string) string {
	if rawXML == "" {
		return ""
	}
	// Try common patterns
	patterns := []string{
		`<hostname>([^<]+)</hostname>`,
		`<vm_hostname>([^<]+)</vm_hostname>`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(rawXML); len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}
	return ""
}

// ipv4Only keeps the IPv4 addresses of a list.
func ipv4Only(ips []string) []string {
	var out []string
	for _, ip := range ips {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			out = append(out, ip)
		}
	}
	return out
}

// ProbeIPs ARP-probes each requested IP on the given bridge and returns those
// that answer (are live on the wire). This lets the panel detect IPs already in
// use by VMs it doesn't manage (e.g. pre-existing unmanaged guests) so it
// never double-assigns a live customer IP and its IPAM view matches reality.
// Probes run concurrently (bounded) so scanning a whole /24-/25 stays fast.
func (s *NodeAgentService) ProbeIPs(ctx context.Context, req *pb.ProbeIPsRequest) (*pb.ProbeIPsResponse, error) {
	bridge := strings.TrimSpace(req.GetBridge())
	if bridge == "" {
		return nil, status.Error(codes.InvalidArgument, "bridge is required")
	}
	timeout := time.Duration(req.GetTimeoutMs()) * time.Millisecond
	if timeout <= 0 {
		timeout = 700 * time.Millisecond
	}

	var (
		mu      sync.Mutex
		inUse   []string
		checked []string
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 32) // bound concurrency
	)
	for _, raw := range req.GetIpAddresses() {
		ip := strings.TrimSpace(raw)
		if ip == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			live := arpProbe(bridge, ip, timeout)
			mu.Lock()
			checked = append(checked, ip)
			if live {
				inUse = append(inUse, ip)
			}
			mu.Unlock()
		}(ip)
	}
	wg.Wait()

	log.Printf("[NodeAgent] ProbeIPs: bridge=%s probed=%d in_use=%d", bridge, len(checked), len(inUse))
	return &pb.ProbeIPsResponse{InUse: inUse, Checked: checked}, nil
}

// arpProbe sends an ARP request for ip on bridge and reports whether any host
// answered. Uses arping (iputils or Thomas Habets' variant — both print
// "reply"). A missing arping binary yields false (fail-open: better to risk a
// rare collision than to wrongly mark every IP used and block all allocation).
func arpProbe(bridge, ip string, timeout time.Duration) bool {
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	// Prefer arping if present (a direct ARP request/reply, works even on a bridge
	// with no host IP). arping isn't installed on every host, so we don't rely on it.
	if _, err := exec.LookPath("arping"); err == nil {
		if out, _ := exec.Command("arping", "-I", bridge, "-c", "1", "-w", strconv.Itoa(secs), ip).CombinedOutput(); strings.Contains(strings.ToLower(string(out)), "reply") {
			return true
		}
	}
	// Dependency-free path (iproute2 is always present): send one ping — even if
	// the target firewalls ICMP, the kernel must ARP-resolve it first to send the
	// packet, which populates the neighbor cache — then read that cache. A resolved
	// lladdr in any non-FAILED/INCOMPLETE state means a host answered ARP → in use.
	_ = exec.Command("ping", "-c", "1", "-w", strconv.Itoa(secs), "-n", ip).Run()
	out, _ := exec.Command("ip", "neigh", "show", ip).CombinedOutput()
	s := strings.ToLower(string(out))
	if !strings.Contains(s, "lladdr") {
		return false
	}
	return !strings.Contains(s, "failed") && !strings.Contains(s, "incomplete")
}

// ConfigureFloatingIP attaches or detaches a floating (elastic) IP on this node.
//
// The address lives on the HOST (bound to the uplink bridge and 1:1-NATed to the
// VM), never inside the guest, so it works on every VM including imported ones
// with no guest agent and can be repointed in the time it takes to rewrite two
// iptables rules. Attach is idempotent — the panel re-runs it periodically so
// floating IPs survive a node reboot, which wipes all iptables state.
func (s *NodeAgentService) ConfigureFloatingIP(ctx context.Context, req *pb.FloatingIPRequest) (*pb.FloatingIPResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}
	if s.networkMgr == nil {
		return nil, status.Error(codes.FailedPrecondition, "network manager not initialized")
	}

	fip := strings.TrimSpace(req.GetFloatingIp())
	bridge := strings.TrimSpace(req.GetBridge())
	internalIP := strings.TrimSpace(req.GetInternalIp())
	if fip == "" {
		return nil, status.Error(codes.InvalidArgument, "floating_ip is required")
	}

	vpcID := strings.TrimSpace(req.GetVpcId())

	if !req.GetAttach() {
		if vpcID != "" {
			if err := s.networkMgr.DetachFloatingIPVPC(vpcID, fip); err != nil {
				return nil, status.Errorf(codes.Internal, "detach floating IP in VPC: %v", err)
			}
			log.Printf("[NodeAgent] Floating IP %s detached from VPC %s", fip, vpcID)
			return &pb.FloatingIPResponse{Success: true}, nil
		}
		if err := s.networkMgr.DetachFloatingIP(req.GetVmId(), fip, bridge); err != nil {
			return nil, status.Errorf(codes.Internal, "detach floating IP: %v", err)
		}
		log.Printf("[NodeAgent] Floating IP %s detached", fip)
		return &pb.FloatingIPResponse{Success: true}, nil
	}

	if internalIP == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_ip is required to attach")
	}
	if vpcID != "" {
		// Inside a VPC everything happens in the router namespace, including the
		// gratuitous ARP — the namespace owns the address, so it must be the one
		// to announce it.
		if err := s.networkMgr.AttachFloatingIPVPC(vpcID, fip, internalIP, bridge, strings.TrimSpace(req.GetGateway())); err != nil {
			return nil, status.Errorf(codes.Internal, "attach floating IP in VPC: %v", err)
		}
		// Announce the address from the router namespace's MAC. Without this,
		// upstream keeps sending the address to whoever held it before until its
		// ARP cache expires, so moving a floating IP appears to do nothing for
		// minutes. Injected from the root namespace onto the same bridge the
		// namespace's public leg sits on, which reaches the same L2 domain.
		if bridge != "" {
			if mac, merr := s.networkMgr.VPCFloatingMAC(vpcID); merr != nil {
				log.Printf("[NodeAgent] WARNING: no GARP for %s in VPC %s: %v", fip, vpcID, merr)
			} else if err := sendGratuitousARP(bridge, fip, mac); err != nil {
				log.Printf("[NodeAgent] WARNING: GARP for %s on %s failed: %v", fip, bridge, err)
			}
		}
		log.Printf("[NodeAgent] Floating IP %s -> %s attached inside VPC %s", fip, internalIP, vpcID)
		return &pb.FloatingIPResponse{Success: true}, nil
	}
	if err := s.networkMgr.AttachFloatingIP(req.GetVmId(), fip, internalIP, bridge, req.GetNatMode()); err != nil {
		return nil, status.Errorf(codes.Internal, "attach floating IP: %v", err)
	}

	// Announce the address from the HOST's MAC: unlike a directly-assigned IP,
	// a floating IP is answered by the host, so upstream must map it to the
	// host's MAC. Without this the gateway keeps a stale binding (the previous
	// VM's MAC, or the host's from before a move) until its ARP cache expires
	// and traffic goes nowhere in the meantime.
	if bridge != "" {
		if iface, err := net.InterfaceByName(bridge); err == nil && len(iface.HardwareAddr) == 6 {
			if err := sendGratuitousARP(bridge, fip, iface.HardwareAddr.String()); err != nil {
				log.Printf("[NodeAgent] WARNING: GARP for floating IP %s on %s failed: %v", fip, bridge, err)
			}
		} else if err != nil {
			log.Printf("[NodeAgent] WARNING: cannot resolve bridge %s for floating IP GARP: %v", bridge, err)
		}
	}

	log.Printf("[NodeAgent] Floating IP %s -> %s (%s) attached on %s", fip, internalIP, req.GetNatMode(), bridge)
	return &pb.FloatingIPResponse{Success: true}, nil
}

// ConfigureVPC creates or removes a tenant VPC on this node.
//
// The VPC's gateway lives in a router network namespace rather than on the host
// so that two tenants may use the same subnet: the host has one routing table,
// and two identical subnets on it make the kernel deliver both tenants' traffic
// to whichever bridge was created first.
func (s *NodeAgentService) ConfigureVPC(ctx context.Context, req *pb.VPCRequest) (*pb.VPCResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}
	if s.networkMgr == nil {
		return nil, status.Error(codes.FailedPrecondition, "network manager not initialized")
	}
	vpcID := strings.TrimSpace(req.GetVpcId())
	if vpcID == "" {
		return nil, status.Error(codes.InvalidArgument, "vpc_id is required")
	}

	if !req.GetCreate() {
		if err := s.networkMgr.DeleteVPC(vpcID); err != nil {
			return nil, status.Errorf(codes.Internal, "delete VPC: %v", err)
		}
		updateVPC(vpcStatePath(), currentBootID(), vpcID, nil)
		return &pb.VPCResponse{Success: true}, nil
	}

	subnet, gateway := strings.TrimSpace(req.GetSubnet()), strings.TrimSpace(req.GetGateway())
	bridge, err := s.networkMgr.CreateVPC(vpcID, subnet, gateway)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "create VPC: %v", err)
	}
	// Kept so a reboot rebuilds it before its guests start; and a bridge that
	// had to be recreated gets the running guests' taps back.
	updateVPC(vpcStatePath(), currentBootID(), vpcID, &savedVPC{Subnet: subnet, Gateway: gateway})
	reattachVPCTaps(map[string]bool{bridge: true})
	return &pb.VPCResponse{Success: true, Bridge: bridge, RouterNetns: network.VPCNetnsName(vpcID)}, nil
}

// vmLookupError maps a failed libvirt domain lookup to a gRPC status. Only a
// domain libvirt says does not exist is NotFound (the panel then marks the VM
// stopped); libvirt being unreachable is Unavailable, so a libvirtd outage
// doesn't flip every running VM to stopped.
func vmLookupError(err error) error {
	if libvirt.IsNoDomain(err) {
		return status.Errorf(codes.NotFound, "VM not found: %v", err)
	}
	return status.Errorf(codes.Unavailable, "libvirt unavailable: %v", err)
}
