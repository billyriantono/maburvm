package server

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/maburvm/panel/internal/agent/libvirt"
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

func netConfigPath() string {
	dir := os.Getenv("MABURVM_DATA_DIR")
	if dir == "" {
		dir = "/var/lib/maburvm"
	}
	return filepath.Join(dir, "network-config.json")
}

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

func writeNetworkConfigs(path string, s savedNetworkConfigs) error {
	b, err := json.Marshal(s)
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
