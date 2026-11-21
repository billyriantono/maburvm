package server

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/maburvm/panel/internal/agent/hostproxy"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/netrules"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// A VM's network config (firewall, anti-spoof, bandwidth limit, port forwards)
// is runtime-only host state, so a node reboot wipes all of it and nothing
// re-sends it: the panel pushes config only when it changes. The agent keeps
// the last request it applied per VM, stamped with the kernel boot id, and
// replays them when it starts on a new boot.

// savedNetworkConfigs is the on-disk form; requests are protojson.
type savedNetworkConfigs struct {
	BootID  string                     `json:"boot_id"`
	Configs map[string]json.RawMessage `json:"configs"`
}

var netConfigMu sync.Mutex

func agentDataDir() string {
	if dir := os.Getenv("MABURVM_DATA_DIR"); dir != "" {
		return dir
	}
	return "/var/lib/maburvm"
}

func netConfigPath() string { return filepath.Join(agentDataDir(), "network-config.json") }

func currentBootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

func loadNetworkConfigs(path string) savedNetworkConfigs {
	s := savedNetworkConfigs{Configs: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil || s.Configs == nil {
		log.Printf("[NetworkRestore] WARNING: ignoring unreadable %s: %v", path, err)
		return savedNetworkConfigs{Configs: map[string]json.RawMessage{}}
	}
	return s
}

func writeNetworkConfigs(path string, s savedNetworkConfigs) error { return writeJSONFile(path, s) }

// writeJSONFile replaces path with v as JSON, never leaving it half written.
func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// updateNetworkConfig stores req for its VM, or drops the VM when req is nil.
func updateNetworkConfig(path, bootID, vmID string, req *pb.NetworkConfigRequest) {
	netConfigMu.Lock()
	defer netConfigMu.Unlock()
	s := loadNetworkConfigs(path)
	s.BootID = bootID
	if req == nil {
		delete(s.Configs, vmID)
	} else {
		b, err := protojson.Marshal(req)
		if err != nil {
			log.Printf("[NetworkRestore] WARNING: not saving config of VM %s: %v", vmID, err)
			return
		}
		s.Configs[vmID] = b
	}
	if err := writeNetworkConfigs(path, s); err != nil {
		log.Printf("[NetworkRestore] WARNING: saving %s: %v", path, err)
	}
}

// saveNetworkConfig records a request ApplyNetworkConfig just applied. The
// guest-reconfigure flag is a one-off action, never part of the state to restore.
func saveNetworkConfig(req *pb.NetworkConfigRequest) {
	saved := proto.Clone(req).(*pb.NetworkConfigRequest)
	saved.ConfigureGuest = false
	updateNetworkConfig(netConfigPath(), currentBootID(), req.VmId, saved)
}

// restoreNetworkConfigs replays every saved request when the node has booted
// since they were saved. It runs before the gRPC server serves, so a newer push
// from the panel can't be overtaken by an older saved one. VMs libvirt no
// longer knows are dropped from the file.
func restoreNetworkConfigs(svc *NodeAgentService) {
	path, bootID := netConfigPath(), currentBootID()
	s := loadNetworkConfigs(path)
	if bootID == "" || s.BootID == bootID || len(s.Configs) == 0 {
		return
	}
	log.Printf("[NetworkRestore] node rebooted: restoring network config of %d VMs", len(s.Configs))
	ctx := context.WithValue(context.Background(), ContextKeyNodeID, "network-restore")
	restored := 0
	for vmID, raw := range s.Configs {
		// A VM recovered onto another node: its addresses are routed there.
		if migration.IsFenced(vmID) {
			updateNetworkConfig(path, bootID, vmID, nil)
			continue
		}
		req := &pb.NetworkConfigRequest{}
		if err := protojson.Unmarshal(raw, req); err != nil {
			log.Printf("[NetworkRestore] WARNING: VM %s: unreadable saved config: %v", vmID, err)
			continue
		}
		req.ConfigureGuest = false
		if _, err := svc.ApplyNetworkConfig(ctx, req); err != nil {
			if _, lerr := libvirt.GetVMInfo(vmID); libvirt.IsNoDomain(lerr) {
				updateNetworkConfig(path, bootID, vmID, nil)
				continue
			}
			log.Printf("[NetworkRestore] WARNING: VM %s: %v", vmID, err)
			continue
		}
		restored++
	}
	updateNetworkConfig(path, bootID, "", nil) // stamp the new boot even if nothing applied
	log.Printf("[NetworkRestore] restored network config of %d/%d VMs", restored, len(s.Configs))
}

// restoreHostRoutes gives the hostname router the routes of every saved
// config. They live in the agent process, so a restart without a reboot (which
// keeps the iptables rules and replays nothing) would otherwise drop them.
func restoreHostRoutes(svc *NodeAgentService) {
	n := 0
	for vmID, raw := range loadNetworkConfigs(netConfigPath()).Configs {
		req := &pb.NetworkConfigRequest{}
		if protojson.Unmarshal(raw, req) != nil || len(req.GetConfig().GetHostnameForwards()) == 0 {
			continue
		}
		primary := ""
		for _, iface := range req.Config.GetInterfaces() {
			if iface.GetIpAddress() != "" {
				primary = iface.GetIpAddress()
				break
			}
		}
		if err := svc.hostProxy.Set(vmID, hostproxy.FromProto(req.Config.GetHostnameForwards(), primary)); err != nil {
			log.Printf("[NetworkRestore] WARNING: VM %s: hostnames not routed: %v", vmID, err)
		}
		n++
	}
	if n > 0 {
		log.Printf("[NetworkRestore] hostname routes of %d VMs restored", n)
	}
}

// A tenant VPC (router namespace, bridge, veths, NAT) is runtime state too:
// a reboot wipes it, and guests whose NIC sits on its bridge cannot start
// until it is back. The agent keeps the VPCs it hosts and rebuilds them on
// every start, before anything else; the panel also re-sends them.

// savedVPC is one VPC this node hosts.
type savedVPC struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
}

type savedVPCs struct {
	BootID string              `json:"boot_id"`
	VPCs   map[string]savedVPC `json:"vpcs"`
}

var vpcStateMu sync.Mutex

func vpcStatePath() string { return filepath.Join(agentDataDir(), "vpc-config.json") }

func loadVPCs(path string) savedVPCs {
	s := savedVPCs{VPCs: map[string]savedVPC{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil || s.VPCs == nil {
		log.Printf("[NetworkRestore] WARNING: ignoring unreadable %s: %v", path, err)
		return savedVPCs{VPCs: map[string]savedVPC{}}
	}
	return s
}

// updateVPC records a VPC, or forgets it when v is nil; either way the file
// is stamped with bootID. An empty vpcID only stamps.
func updateVPC(path, bootID, vpcID string, v *savedVPC) {
	vpcStateMu.Lock()
	defer vpcStateMu.Unlock()
	s := loadVPCs(path)
	s.BootID = bootID
	if vpcID != "" {
		if v == nil {
			delete(s.VPCs, vpcID)
		} else {
			s.VPCs[vpcID] = *v
		}
	}
	if err := writeJSONFile(path, s); err != nil {
		log.Printf("[NetworkRestore] WARNING: saving %s: %v", path, err)
	}
}

// restoreVPCs rebuilds every VPC on record (idempotent: a healthy one is left
// as it is) and puts running guests' taps back on their bridges. It returns
// the bridges rebuilt and whether the node rebooted since the file was last
// stamped, which is when guests on them could not autostart.
func restoreVPCs(svc *NodeAgentService) (map[string]bool, bool) {
	path, bootID := vpcStatePath(), currentBootID()
	s := loadVPCs(path)
	newBoot := s.BootID != "" && bootID != "" && s.BootID != bootID
	bridges := map[string]bool{}
	for id, v := range s.VPCs {
		br, err := svc.networkMgr.CreateVPC(id, v.Subnet, v.Gateway)
		if err != nil {
			log.Printf("[NetworkRestore] WARNING: VPC %s not rebuilt: %v", id, err)
			continue
		}
		bridges[br] = true
	}
	if len(bridges) > 0 {
		log.Printf("[NetworkRestore] %d/%d VPCs in place", len(bridges), len(s.VPCs))
		reattachVPCTaps(bridges)
	}
	if bootID != "" {
		updateVPC(path, bootID, "", nil)
	}
	return bridges, newBoot
}

// reattachVPCTaps puts running guests' taps back on their VPC bridge: a
// bridge that had to be recreated starts out with no ports.
func reattachVPCTaps(bridges map[string]bool) {
	ifaces, err := libvirt.ListGuestInterfaces()
	if err != nil {
		log.Printf("[NetworkRestore] WARNING: listing guest interfaces: %v", err)
		return
	}
	for _, gi := range ifaces {
		if gi.Target == "" || !bridges[gi.Bridge] {
			continue
		}
		if link, err := os.Readlink("/sys/class/net/" + gi.Target + "/master"); err == nil && filepath.Base(link) == gi.Bridge {
			continue
		}
		if out, err := exec.Command("ip", "link", "set", gi.Target, "master", gi.Bridge).CombinedOutput(); err != nil {
			log.Printf("[NetworkRestore] WARNING: VM %s: tap %s not put back on %s: %v (%s)", gi.VMID, gi.Target, gi.Bridge, err, strings.TrimSpace(string(out)))
			continue
		}
		log.Printf("[NetworkRestore] VM %s: tap %s put back on VPC bridge %s", gi.VMID, gi.Target, gi.Bridge)
	}
}

// startVPCGuests starts, after a reboot, the guests libvirt could not autostart
// because their VPC bridge was not there yet.
func startVPCGuests(bridges map[string]bool) {
	vms, err := libvirt.ListVMs()
	if err != nil {
		log.Printf("[NetworkRestore] WARNING: listing VMs to start on VPCs: %v", err)
		return
	}
	var guests []netrules.Guest
	for _, vm := range vms {
		if !vm.Autostart || vm.Status != libvirt.VMStatusStopped {
			continue
		}
		brs, err := libvirt.DomainBridges(vm.UUID)
		if err != nil {
			continue
		}
		guests = append(guests, netrules.Guest{ID: vm.UUID, Autostart: true, Bridges: brs})
	}
	for _, id := range netrules.VPCGuestsToStart(guests, bridges) {
		if err := startVM(id); err != nil {
			log.Printf("[NetworkRestore] WARNING: VM %s on a VPC did not start after the reboot: %v", id, err)
			continue
		}
		log.Printf("[NetworkRestore] VM %s started: its VPC was missing when the node booted", id)
	}
}
