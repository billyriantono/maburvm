package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/agent/cloudinit"
	"github.com/maburvm/panel/internal/agent/health"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/network"
	"github.com/maburvm/panel/internal/agent/storage"
	"github.com/maburvm/panel/internal/agent/vncproxy"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
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
}

// NewNodeAgentService creates a new NodeAgentService with all dependencies
func NewNodeAgentService(libvirtMgr *libvirt.VMManager, networkMgr *network.Manager, healthCollector *health.MetricsCollector, vncProxy *vncproxy.Proxy) *NodeAgentService {
	return &NodeAgentService{
		libvirt:         libvirtMgr,
		networkMgr:      networkMgr,
		healthCollector: healthCollector,
		vncProxy:        vncProxy,
		metricsCache:    NewVMMetricsCache(),
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

	var err error
	var state pb.VMState

	switch req.Command {
	case pb.VMCommandType_VM_COMMAND_TYPE_CREATE:
		err = s.createVM(req)
		// On success the domain is defined but not started.
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_START:
		state = pb.VMState_VM_STATE_RUNNING
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
		if err == nil {
			err = libvirt.StartVM(req.VmId)
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_STOP:
		err = libvirt.StopVM(req.VmId, false)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP:
		err = libvirt.StopVM(req.VmId, true)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESTART:
		err = libvirt.RestartVM(req.VmId)
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_DESTROY:
		err = libvirt.DeleteVM(req.VmId)
		state = pb.VMState_VM_STATE_DESTROYED
	case pb.VMCommandType_VM_COMMAND_TYPE_PAUSE:
		err = libvirt.SuspendVM(req.VmId)
		state = pb.VMState_VM_STATE_PAUSED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESUME:
		err = libvirt.ResumeVM(req.VmId)
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_REBUILD:
		err = s.rebuildVM(req)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESIZE:
		if req.Config != nil && req.Config.Resources != nil {
			err = libvirt.ResizeVM(req.VmId, int(req.Config.Resources.Vcpus), int(req.Config.Resources.MemoryMb))
			// Grow the primary disk too (no-op when unchanged; grow-only).
			if err == nil && req.Config.Resources.DiskGb > 0 {
				err = libvirt.ResizeDisk(req.VmId, int(req.Config.Resources.DiskGb))
			}
		} else {
			err = fmt.Errorf("resources required for RESIZE")
		}
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESET_PASSWORD:
		if req.Config != nil && req.Config.RootPassword != "" {
			err = libvirt.SetVMPassword(req.VmId, "root", req.Config.RootPassword)
		} else {
			err = fmt.Errorf("root_password required for RESET_PASSWORD")
		}
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_ATTACH_ISO:
		err = s.attachISO(req)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_DETACH_ISO:
		err = libvirt.DetachISO(req.VmId)
		state = pb.VMState_VM_STATE_STOPPED
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown command: %v", req.Command)
	}

	if err != nil {
		log.Printf("[NodeAgent] Failed to execute command %v on VM %s: %v", req.Command, req.VmId, err)
		msg := fmt.Sprintf("Failed to execute command: %v", err)
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
		Success: true,
		VmId:    req.VmId,
		Command: req.Command,
		State:   state,
		Message: fmt.Sprintf("Command %v executed successfully", req.Command),
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

// defaultImageDir is where VM disk images are provisioned on the node.
const defaultImageDir = "/var/lib/libvirt/images"

// templateCacheDir is where downloaded OS template images are cached on the node
// so repeated VM creations from the same template reuse a single download.
const templateCacheDir = "/var/lib/libvirt/templates"

// generateMAC derives a stable, locally-administered MAC address (using the
// QEMU OUI 52:54:00) from the VM ID, so the NIC in the domain XML and the
// cloud-init network-config refer to the same interface.
func generateMAC(vmID string) string {
	sum := sha256.Sum256([]byte(vmID))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", sum[0], sum[1], sum[2])
}

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
func injectGuestConfig(diskPath, hostname string, vmCfg libvirt.VMConfig, rootPassword, sshKey string) error {
	if _, err := exec.LookPath("virt-customize"); err != nil {
		return fmt.Errorf("virt-customize not found (install libguestfs-tools): %w", err)
	}

	// --no-network: the customize appliance needs no outbound network.
	args := []string{"-a", diskPath, "--no-network"}
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
		args = append(args,
			"--write", "/etc/ssh/sshd_config.d/00-maburvm.conf:PermitRootLogin yes\nPasswordAuthentication yes\n")
	}
	if key := strings.TrimSpace(sshKey); key != "" {
		args = append(args, "--ssh-inject", "root:string:"+key)
	}

	var tmpNet string
	if vmCfg.IPAddress != "" && vmCfg.Netmask > 0 {
		var b strings.Builder
		b.WriteString("[Match]\n")
		b.WriteString(fmt.Sprintf("MACAddress=%s\n\n", strings.ToLower(vmCfg.MACAddress)))
		b.WriteString("[Network]\n")
		b.WriteString(fmt.Sprintf("Address=%s/%d\n", vmCfg.IPAddress, vmCfg.Netmask))
		if vmCfg.Gateway != "" {
			b.WriteString(fmt.Sprintf("Gateway=%s\n", vmCfg.Gateway))
		}
		b.WriteString("DNS=1.1.1.1\nDNS=8.8.8.8\n")

		f, err := os.CreateTemp("", "maburvm-*.network")
		if err != nil {
			return fmt.Errorf("failed to stage network config: %w", err)
		}
		tmpNet = f.Name()
		if _, err := f.WriteString(b.String()); err != nil {
			f.Close()
			os.Remove(tmpNet)
			return fmt.Errorf("failed to write staged network config: %w", err)
		}
		f.Close()

		args = append(args,
			"--upload", tmpNet+":/etc/systemd/network/10-maburvm.network",
			// systemd-networkd runs as the unprivileged systemd-network user and
			// REFUSES to read a .network file it can't open — the uploaded temp file
			// is mode 0600 (root only), which yields "Permission denied" and the
			// guest silently falls back to the image's DHCP netplan. Make it 0644.
			"--chmod", "0644:/etc/systemd/network/10-maburvm.network",
			// Ensure the renderer is enabled (already so on cloud images; harmless otherwise).
			"--run-command", "systemctl enable systemd-networkd >/dev/null 2>&1 || true")
	}
	if tmpNet != "" {
		defer os.Remove(tmpNet)
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
// treated as an existing local file path.
func resolveTemplateImage(ref string) (string, error) {
	// vm://... resolves to a source VM's primary disk for the clone flow (source
	// must be stopped for a consistent copy):
	//   vm://<vmId>            → that VM's disk on THIS node (same-node clone)
	//   vm://<srcNodeIP>/<vmId> → pulled over SSH from the source node (cross-node)
	if strings.HasPrefix(ref, "vm://") {
		spec := strings.TrimPrefix(ref, "vm://")
		if i := strings.IndexByte(spec, '/'); i >= 0 {
			srcIP, srcID := spec[:i], spec[i+1:]
			// Both nodes run the same agent, so the source disk lives at the same
			// default path on the source node. Pull it via scp (requires this node
			// to have root SSH access to the source node — the same node↔node trust
			// migration uses).
			srcPath := filepath.Join(defaultImageDir, srcID+".qcow2")
			dstPath := filepath.Join(templateCacheDir, "clone-"+srcID+".qcow2")
			if err := os.MkdirAll(templateCacheDir, 0o755); err != nil {
				return "", fmt.Errorf("failed to create cache dir %s: %w", templateCacheDir, err)
			}
			log.Printf("[NodeAgent] clone: pulling source disk %s from %s", srcPath, srcIP)
			out, err := exec.Command("scp",
				"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes",
				fmt.Sprintf("root@%s:%s", srcIP, srcPath), dstPath,
			).CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("failed to pull source disk from %s: %v (%s)", srcIP, err, strings.TrimSpace(string(out)))
			}
			return dstPath, nil
		}
		path := filepath.Join(defaultImageDir, spec+".qcow2")
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("source VM disk not found at %s: %w", path, err)
		}
		return path, nil
	}

	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		if _, err := os.Stat(ref); err != nil {
			return "", fmt.Errorf("template image not found at %s: %w", ref, err)
		}
		return ref, nil
	}

	// Derive a stable cache filename from the URL so the same source maps to the
	// same cached file across creations.
	sum := sha256.Sum256([]byte(ref))
	base := filepath.Base(ref)
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if base == "" || base == "/" || base == "." {
		base = "image.qcow2"
	}
	cachePath := filepath.Join(templateCacheDir, fmt.Sprintf("%x-%s", sum[:6], base))

	// Reuse the cached image if it was already downloaded.
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	// The downloader writes a temp file into the cache dir, so it must exist first.
	if err := os.MkdirAll(templateCacheDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create template cache dir %s: %w", templateCacheDir, err)
	}

	log.Printf("[NodeAgent] Downloading template image %s -> %s", ref, cachePath)
	if err := storage.NewTemplateManager().DownloadTemplate(ref, cachePath); err != nil {
		return "", fmt.Errorf("failed to download template image from %s: %w", ref, err)
	}
	log.Printf("[NodeAgent] Cached template image at %s", cachePath)
	return cachePath, nil
}

// createVM provisions a VM disk from the OS template image and defines a libvirt
// domain for it. The domain is created in the stopped state; a subsequent START
// command boots it. Network settings (bridge, MAC, VLAN, bandwidth) are embedded
// in the domain XML so libvirt enforces them when the VM starts.
func (s *NodeAgentService) createVM(req *pb.VMCommandRequest) error {
	if req.VmId == "" {
		return fmt.Errorf("vm_id is required")
	}
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
	templatePath, err := resolveTemplateImage(cfg.ImageId)
	if err != nil {
		return err
	}

	diskGB := int(cfg.Resources.DiskGb)
	diskPath := filepath.Join(defaultImageDir, req.VmId+".qcow2")
	if _, err := os.Stat(diskPath); err == nil {
		return fmt.Errorf("disk already exists for VM %s at %s", req.VmId, diskPath)
	}

	// Provision the disk as an independent copy of the template, then grow it to
	// the requested size.
	qcow := storage.NewQCOW2Manager()
	if err := qcow.CloneImage(templatePath, diskPath); err != nil {
		return fmt.Errorf("failed to provision disk from template: %w", err)
	}
	if diskGB > 0 {
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

	vmCfg := libvirt.VMConfig{
		Name:        req.VmId,
		UUID:        req.VmId,
		CPU:         int(cfg.Resources.Vcpus),
		Memory:      int(cfg.Resources.MemoryMb),
		DiskPath:    diskPath,
		DiskSize:    diskGB,
		VNCPassword: cfg.VncPassword,
		VNCPort:     -1, // auto-assign unless overridden via metadata
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
		}
		if cfg.NetworkConfig.BandwidthLimits != nil {
			vmCfg.BandwidthMbps = int(cfg.NetworkConfig.BandwidthLimits.EgressRateMbps)
		}
	}

	// Ensure a stable MAC so cloud-init can match the interface for static config.
	if vmCfg.MACAddress == "" {
		vmCfg.MACAddress = generateMAC(req.VmId)
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
	seedPath := filepath.Join(defaultImageDir, req.VmId+"-seed.iso")
	if err := cloudinit.GenerateSeedISO(cloudinit.Config{
		InstanceID:   req.VmId,
		Hostname:     hostname,
		MACAddress:   vmCfg.MACAddress,
		IPAddress:    vmCfg.IPAddress,
		Prefix:       vmCfg.Netmask,
		Gateway:      vmCfg.Gateway,
		SSHPublicKey: cfg.SshPublicKey,
		Password:     cfg.RootPassword,
		UserData:     cfg.UserData,
	}, seedPath); err != nil {
		log.Printf("[NodeAgent] cloud-init seed not attached for VM %s (guest will use DHCP): %v", req.VmId, err)
		seedPath = ""
	} else {
		vmCfg.CloudInitISOPath = seedPath
	}

	// Inject network + hostname (+ optional password/SSH key) DIRECTLY into the
	// guest disk via libguestfs, so the guest is configured on first boot without
	// depending on the guest's cloud-init (which is unreliable across images —
	// this is how Virtualizor provisions). The cloud-init seed above is kept as a
	// belt-and-suspenders for images where it does work. Best-effort: on failure
	// we log and still create the VM (it just may lack static networking).
	if err := injectGuestConfig(diskPath, hostname, vmCfg, cfg.RootPassword, cfg.SshPublicKey); err != nil {
		log.Printf("[NodeAgent] guest config injection failed for VM %s (guest may lack static networking): %v", req.VmId, err)
	}

	if _, err := libvirt.CreateVM(vmCfg); err != nil {
		// Clean up provisioned artifacts if domain definition failed.
		_ = os.Remove(diskPath)
		if seedPath != "" {
			_ = os.Remove(seedPath)
		}
		return fmt.Errorf("failed to define domain: %w", err)
	}

	log.Printf("[NodeAgent] Created VM %s (disk=%s bridge=%s ip=%s vlan=%d bw=%dMbps)",
		req.VmId, diskPath, vmCfg.Bridge, vmCfg.IPAddress, vmCfg.VLANID, vmCfg.BandwidthMbps)
	return nil
}

// attachISO resolves the ISO reference (downloading if it's a URL) and attaches
// it as a bootable cdrom.
func (s *NodeAgentService) attachISO(req *pb.VMCommandRequest) error {
	if req.Config == nil || req.Config.ImageId == "" {
		return fmt.Errorf("ISO image is required for ATTACH_ISO")
	}
	isoPath, err := resolveTemplateImage(req.Config.ImageId)
	if err != nil {
		return err
	}
	return libvirt.AttachISO(req.VmId, isoPath)
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
	templatePath, err := resolveTemplateImage(req.Config.ImageId)
	if err != nil {
		return err
	}

	cfg := req.Config
	hostname := req.VmId
	mac, ip, gateway := "", "", ""
	prefix := 0
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
	}
	// Match create-time behaviour: a deterministic MAC keyed on the VM ID so the
	// regenerated network-config still matches the (unchanged) domain NIC.
	if mac == "" {
		mac = generateMAC(req.VmId)
	}

	seedPath := filepath.Join(defaultImageDir, req.VmId+"-seed.iso")
	if err := cloudinit.GenerateSeedISO(cloudinit.Config{
		InstanceID:   req.VmId,
		Hostname:     hostname,
		MACAddress:   mac,
		IPAddress:    ip,
		Prefix:       prefix,
		Gateway:      gateway,
		SSHPublicKey: cfg.SshPublicKey,
		Password:     cfg.RootPassword,
		UserData:     cfg.UserData,
	}, seedPath); err != nil {
		// Non-fatal: the rebuilt guest still boots; it just won't pick up the new
		// password/keys via cloud-init.
		log.Printf("[NodeAgent] cloud-init seed not regenerated for rebuilt VM %s: %v", req.VmId, err)
	}

	return libvirt.RebuildVM(req.VmId, templatePath)
}

// BackupDisk exports a VM's primary disk (compressed qcow2) and uploads it to
// the node's configured object storage. Unlike the old manifest-only backup,
// this produces a restorable disk image.
func (s *NodeAgentService) BackupDisk(ctx context.Context, req *pb.BackupDiskRequest) (*pb.BackupDiskResponse, error) {
	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	diskPath, err := libvirt.PrimaryDiskPath(req.VmId)
	if err != nil {
		return backupDiskErr(fmt.Sprintf("primary disk not found: %v", err)), nil
	}

	key := req.DestinationKey
	if key == "" {
		key = fmt.Sprintf("backups/%s/%d.qcow2", req.VmId, time.Now().Unix())
	}

	// Export a standalone, compressed copy of the disk.
	exportPath := filepath.Join(defaultImageDir, req.VmId+"-backup.qcow2")
	_ = os.Remove(exportPath)
	if err := storage.NewQCOW2Manager().ConvertCompressed(diskPath, exportPath); err != nil {
		return backupDiskErr(fmt.Sprintf("disk export failed: %v", err)), nil
	}
	defer os.Remove(exportPath)

	checksum, size, err := fileSHA256(exportPath)
	if err != nil {
		return backupDiskErr(fmt.Sprintf("checksum failed: %v", err)), nil
	}

	client, err := backupStorageClientFromEnv()
	if err != nil {
		return backupDiskErr(err.Error()), nil
	}
	f, err := os.Open(exportPath)
	if err != nil {
		return backupDiskErr(fmt.Sprintf("open export: %v", err)), nil
	}
	defer f.Close()
	if err := client.Upload(ctx, key, f, size, "application/octet-stream"); err != nil {
		return backupDiskErr(fmt.Sprintf("upload failed: %v", err)), nil
	}

	log.Printf("[NodeAgent] Backed up VM %s disk -> %s (%d bytes, sha256=%s)", req.VmId, key, size, checksum)
	return &pb.BackupDiskResponse{Success: true, ObjectKey: key, SizeBytes: size, Checksum: checksum}, nil
}

func backupDiskErr(msg string) *pb.BackupDiskResponse {
	return &pb.BackupDiskResponse{
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
	poolPath := req.PoolPath
	if poolPath == "" {
		poolPath = defaultImageDir
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
	if err := libvirt.DetachDisk(req.VmId, req.Device, req.Path); err != nil {
		return detachDiskErr(err.Error()), nil
	}
	if req.DeleteVolume && req.Path != "" {
		if err := storage.NewVolumeManager().DeleteVolume(req.PoolType, req.Path); err != nil {
			log.Printf("[NodeAgent] Warning: detached %s but failed to delete volume %s: %v", req.Device, req.Path, err)
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

// MigrateVM live-migrates a domain from this source node to a destination URI.
func (s *NodeAgentService) MigrateVM(ctx context.Context, req *pb.MigrateVMRequest) (*pb.MigrateVMResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.VmId == "" || req.DestUri == "" {
		return &pb.MigrateVMResponse{
			Success: false,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "vm_id and dest_uri are required"},
		}, nil
	}
	log.Printf("[NodeAgent] Migrating VM %s -> %s (live=%v, copy_storage=%v)", req.VmId, req.DestUri, req.Live, req.CopyStorage)
	if err := libvirt.MigrateVM(req.VmId, req.DestUri, req.Live, req.CopyStorage); err != nil {
		return &pb.MigrateVMResponse{
			Success: false,
			Message: err.Error(),
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()},
		}, nil
	}
	log.Printf("[NodeAgent] Migration of VM %s to %s completed", req.VmId, req.DestUri)
	return &pb.MigrateVMResponse{Success: true, Message: fmt.Sprintf("migrated %s to %s", req.VmId, req.DestUri)}, nil
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

	localPath, err := resolveTemplateImage(req.ImageUrl)
	if err != nil {
		return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()}}, nil
	}
	checksum, size, err := fileSHA256(localPath)
	if err != nil {
		return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("checksum failed: %v", err)}}, nil
	}
	if req.ExpectedChecksum != "" && !strings.EqualFold(req.ExpectedChecksum, checksum) {
		return &pb.SyncTemplateResponse{Success: false, Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: fmt.Sprintf("checksum mismatch: want %s, got %s", req.ExpectedChecksum, checksum)}}, nil
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

// backupStorageClientFromEnv builds an object-storage client from the agent's
// environment (S3_*/STORAGE_* vars in agent.env).
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
	if endpoint != "" && !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	region := first("STORAGE_REGION", "S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg := &sharedstorage.Config{
		Endpoint:  endpoint,
		AccessKey: first("STORAGE_ACCESS_KEY", "S3_ACCESS_KEY"),
		SecretKey: first("STORAGE_SECRET_KEY", "S3_SECRET_KEY"),
		Bucket:    first("STORAGE_BUCKET", "S3_BUCKET"),
		Region:    region,
		Provider:  sharedstorage.ProviderS3,
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
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
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
	}

	// Resolve IP addresses if VM is running. Prefer libvirt's interface-address
	// query (guest agent → DHCP lease → ARP) so VMs without a responsive
	// qemu-guest-agent still report IPs; fall back to the direct guest-agent
	// query for hostname-derived addresses.
	if vmStatus == "running" {
		if ips, ierr := libvirt.GetVMInterfaceIPs(req.VmId); ierr == nil && len(ips) > 0 {
			resp.IpAddresses = ips
		} else if _, gaIPs := queryGuestAgentInfo(vmInfo.Name); len(gaIPs) > 0 {
			resp.IpAddresses = gaIPs
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

	return &pb.VMMetricsResponse{
		Timestamp: timestamppb.New(now),
		VmId:      vmID,
		Cpu: &pb.CPUMetrics{
			UsagePercent: cpuPercent,
		},
		Memory: &pb.MemoryMetrics{
			UsedBytes:      vmStats.MemoryRSS,
			AvailableBytes: vmStats.MemoryActual - vmStats.MemoryRSS,
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

	switch req.Operation {
	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE:
		snap, err := libvirt.CreateSnapshot(req.VmId, req.Name, req.Description)
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
		if err := libvirt.RestoreSnapshot(req.VmId, req.SnapshotId); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to restore snapshot: %v", err)
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
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

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Applying network config to VM %s from node %s", req.VmId, nodeID)

	if s.networkMgr == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "network manager not initialized")
	}

	// Get VM info to verify VM exists
	_, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
	}

	// Determine the primary IP from the first interface with an IP
	var internalIP string
	for _, iface := range req.Config.Interfaces {
		if iface.IpAddress != "" {
			internalIP = iface.IpAddress
			break
		}
	}

	// Determine bandwidth limit
	var bandwidthMbps int
	if req.Config.BandwidthLimits != nil {
		bandwidthMbps = int(req.Config.BandwidthLimits.IngressRateMbps)
	}

	// Determine VLAN ID from first interface (if bridge-based)
	var vlanID int
	// VLAN is typically derived from bridge/network config — use 0 for now unless explicitly set

	// Convert proto firewall rules to model rules
	var fwRules []models.FirewallRule
	for _, rule := range req.Config.FirewallRules {
		fwRules = append(fwRules, models.FirewallRule{
			Direction: protoDirectionToModel(rule.Direction),
			Action:    protoActionToModel(rule.Action),
			Protocol:  rule.Protocol,
			SourceIP:  rule.SourceCidr,
			PortRange: rule.DestPort,
			Priority:  int(rule.Priority),
		})
	}

	// If replace_all, cleanup existing config first
	if req.ReplaceAll {
		_ = s.networkMgr.CleanupVMNetwork(req.VmId)
	}

	// Apply the full network configuration
	if err := s.networkMgr.SetupVMNetwork(req.VmId, internalIP, vlanID, bandwidthMbps, fwRules); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to apply network config: %v", err)
	}

	// Apply anti-spoofing rules if any interface has it enabled
	for _, iface := range req.Config.Interfaces {
		if iface.AntiSpoofing {
			mac := iface.MacAddress
			if mac == "" {
				// Generate stable MAC from VM ID (same algorithm as createVM)
				mac = generateMAC(req.VmId)
			}
			// Find the vnet interface for this VM
			vnetIface, vnetErr := libvirt.GetVMInterfaceName(req.VmId)
			if vnetErr != nil {
				log.Printf("[NodeAgent] WARNING: could not determine vnet interface for VM %s: %v (anti-spoof iptables/ebtables skipped)", req.VmId, vnetErr)
			} else {
				if err := s.networkMgr.EnableAntiSpoofing(req.VmId, iface.IpAddress, "", mac, vnetIface); err != nil {
					log.Printf("[NodeAgent] WARNING: failed to apply anti-spoof rules for VM %s: %v", req.VmId, err)
				} else {
					log.Printf("[NodeAgent] Anti-spoof rules applied for VM %s (IP: %s, MAC: %s, Interface: %s)",
						req.VmId, iface.IpAddress, mac, vnetIface)
				}
			}
			break // only apply for primary interface
		}
	}

	log.Printf("[NodeAgent] Network config applied to VM %s (IP: %s, bandwidth: %d Mbps, rules: %d)",
		req.VmId, internalIP, bandwidthMbps, len(fwRules))

	appliedInterfaces := make([]*pb.NetworkInterface, 0, len(req.Config.Interfaces))
	for _, iface := range req.Config.Interfaces {
		appliedInterfaces = append(appliedInterfaces, iface)
	}

	return &pb.NetworkConfigResponse{
		Success:           true,
		AppliedInterfaces: appliedInterfaces,
	}, nil
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
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
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
	}, nil
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
			gaHostname, gaIPs := queryGuestAgentInfo(vm.Name)
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
			if d.Source.File != "" {
				details.disks = append(details.disks, &pb.ScannedDisk{
					SourceFile: d.Source.File,
					Format:     d.Driver.Type,
					Device:     d.Device,
					Bus:        d.Target.Bus,
					TargetDev:  d.Target.Dev,
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

// extractHostnameFromMetadata extracts hostname from Virtualizor metadata XML
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

// queryGuestAgentInfo queries the qemu-guest-agent for hostname and IP addresses
// Returns hostname and list of non-loopback IPv4 addresses
func queryGuestAgentInfo(domainName string) (hostname string, ipAddresses []string) {
	// Query hostname via guest-agent
	hostOut, err := exec.Command("virsh", "qemu-agent-command", domainName,
		`{"execute":"guest-get-host-name"}`).Output()
	if err == nil {
		// Parse: {"return":{"host-name":"example.com"}}
		re := regexp.MustCompile(`"host-name"\s*:\s*"([^"]+)"`)
		if matches := re.FindSubmatch(hostOut); len(matches) > 1 {
			hostname = string(matches[1])
		}
	}

	// Query IP addresses via domifaddr (uses guest-agent source)
	ifOut, err := exec.Command("virsh", "domifaddr", domainName, "--source", "agent").Output()
	if err == nil {
		// Parse lines like: eth0  00:16:3e:54:e4:64  ipv4  192.0.2.81/24
		lines := strings.Split(string(ifOut), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[2] == "ipv4" {
				ip := strings.Split(fields[3], "/")[0]
				if ip != "127.0.0.1" {
					ipAddresses = append(ipAddresses, ip)
				}
			}
		}
	}

	return hostname, ipAddresses
}

// ProbeIPs ARP-probes each requested IP on the given bridge and returns those
// that answer (are live on the wire). This lets the panel detect IPs already in
// use by VMs it doesn't manage (e.g. pre-existing Virtualizor guests) so it
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
