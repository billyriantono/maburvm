package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/maburvm/panel/internal/agent/ceph"
	"github.com/maburvm/panel/internal/agent/cloudinit"
	"github.com/maburvm/panel/internal/agent/guestos"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/storage"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// Recovering a failed node's VMs on other nodes.
//
// On the node a VM is recovered onto: ADOPT defines it on its existing disks
// in a shared pool. Disks on Ceph RBD are first taken away from the failed
// node (adoptRBDDisk). On the node it was recovered from, once that node is
// back: the panel sends the VMs that live elsewhere now (SetFencedVMs), and
// this agent never starts one again. Nothing here acts on its own.

func init() {
	migration.FenceFile = filepath.Join(agentDataDir(), "fenced-vms.json")
}

// errFenced refuses to run a VM that was recovered onto another node.
var errFenced = errors.New("this VM was recovered onto another node while this node was down, so its copy here must not run; remove the copy from this node's recovery leftovers in the panel")

// adoptVM defines a VM on disks that exist already. It creates, copies,
// changes and starts nothing, and on failure removes nothing: the disks are
// the VM's only copy.
func (s *NodeAgentService) adoptVM(req *pb.VMCommandRequest) error {
	// Defined already: an earlier try whose answer was lost.
	if _, err := libvirt.GetVMStatus(req.VmId); err == nil {
		log.Printf("[Recovery] ADOPT: domain %s already exists; treating as done", req.VmId)
		return nil
	}
	release, busy := beginCreate(req.VmId)
	if busy {
		return fmt.Errorf("a create for VM %s is already running on this node", req.VmId)
	}
	defer release()

	cfg := req.Config
	if cfg == nil || cfg.Resources == nil {
		return fmt.Errorf("config with resources is required for ADOPT")
	}
	disks := req.GetAdoptDisks()
	if len(disks) == 0 || disks[0].GetTarget() != "vda" {
		return fmt.Errorf("the VM's system disk (vda) is required first for ADOPT")
	}
	inUse, err := libvirt.DiskSourceFiles()
	if err != nil {
		return fmt.Errorf("the disks other VMs use could not be listed: %w", err)
	}
	for i, d := range disks {
		if i > 0 && !validDataTarget(d.GetTarget()) {
			return fmt.Errorf("%q is not a data disk target", d.GetTarget())
		}
		if inUse[cleanDiskPath(d.GetPath())] {
			return fmt.Errorf("%s is a disk of another VM on this node", d.GetPath())
		}
		if ceph.IsPath(d.GetPath()) {
			continue // checked and fenced below, once every disk has passed
		}
		if err := storage.CheckAdoptDisk(req.VmId, d.GetTarget(), d.GetPath()); err != nil {
			return err
		}
	}
	osType, _ := models.SplitGuestImageRef(cfg.ImageId)
	if !guestos.Known(osType) {
		return fmt.Errorf("this node's agent does not know %q systems; update the agent", osType)
	}
	hw, pinned, firmware, err := newVMHardware(cfg, osType)
	if err != nil {
		return err
	}
	// An image on RBD is taken away from the failed node only now, after the
	// last refusal, so a refusal has fenced nothing.
	for _, d := range disks {
		if ceph.IsPath(d.GetPath()) {
			if err := adoptRBDDisk(context.Background(), req.VmId, d.GetTarget(), d.GetPath()); err != nil {
				return err
			}
		}
	}
	vmCfg := vmConfigFor(req, cfg, osType, hw, pinned, firmware)
	vmCfg.DiskPath = cleanDiskPath(disks[0].GetPath())
	for _, d := range disks[1:] {
		vmCfg.ExtraDisks = append(vmCfg.ExtraDisks, libvirt.ExtraDisk{Target: d.GetTarget(), Path: cleanDiskPath(d.GetPath())})
	}
	// Its setup disc sits beside its disk; a VM made without one has none.
	seed := filepath.Join(diskDir(vmCfg.DiskPath, defaultImageDir), req.VmId+"-seed.iso")
	if ceph.IsPath(vmCfg.DiskPath) && cfg.GetMetadata()[models.AdoptSeedKey] != "" {
		// The disc of a VM on RBD was on the failed node. It is written again
		// with the same instance id, so cloud-init sees the system it already
		// set up and changes nothing; it carries no password or key.
		if err := cloudinit.GenerateSeedISO(cloudinit.Config{
			InstanceID: req.VmId, Hostname: cmp.Or(cfg.GetMetadata()["hostname"], req.VmId), MACAddress: vmCfg.MACAddress,
			IPAddress: vmCfg.IPAddress, Prefix: vmCfg.Netmask, Gateway: vmCfg.Gateway, Nameservers: vmCfg.DNS,
			IPv6Address: vmCfg.IPv6Address, IPv6Gateway: vmCfg.IPv6Gateway, IPv6DNS: vmCfg.IPv6DNS,
			Windows: osType == guestos.Windows,
		}, seed); err != nil {
			log.Printf("[Recovery] VM %s: its setup disc was not written again (%v); it boots without one", req.VmId, err)
		}
	}
	if fi, err := os.Stat(seed); err == nil && fi.Mode().IsRegular() {
		vmCfg.CloudInitISOPath = seed
	}
	if _, err := libvirt.CreateVM(vmCfg); err != nil {
		return fmt.Errorf("failed to define domain: %w", err)
	}
	log.Printf("[Recovery] adopted VM %s on %s (+%d data disks), not started", req.VmId, vmCfg.DiskPath, len(vmCfg.ExtraDisks))
	return nil
}

// forgetVMNetwork removes everything this node keeps for a VM's network: host
// rules, saved config, pending guest changes and hostname routes.
func (s *NodeAgentService) forgetVMNetwork(vmID string) {
	if s.networkMgr == nil {
		return
	}
	if err := s.networkMgr.CleanupVMNetwork(vmID); err != nil {
		log.Printf("[NodeAgent] WARNING: network cleanup of VM %s that left this node: %v", vmID, err)
	}
	s.networkMgr.ForgetVM(vmID)
	updateNetworkConfig(netConfigPath(), currentBootID(), vmID, nil)
	clearPendingGuest(vmID)
	_ = s.hostProxy.Set(vmID, nil)
}

// SetFencedVMs replaces the list of VMs that live on another node now. Each
// one's autostart is turned off, and a copy that is shut off loses its host
// rules, so this node stops answering for its addresses. A copy that runs is
// left running: stopping it is the operator's decision.
func (s *NodeAgentService) SetFencedVMs(ctx context.Context, req *pb.SetFencedVMsRequest) (*pb.SetFencedVMsResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	if err := migration.SetFenced(req.GetVmIds()); err != nil {
		return &pb.SetFencedVMsResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INTERNAL, fmt.Errorf("the list could not be saved: %w", err))}, nil
	}
	resp := &pb.SetFencedVMsResponse{Success: true}
	var held []string
	for _, id := range req.GetVmIds() {
		if !migration.ValidUUID(id) {
			continue
		}
		v := s.fenceVM(id)
		resp.Vms = append(resp.Vms, v)
		if v.Defined {
			held = append(held, id)
		}
	}
	// A VM with no copy here has nothing to fence. Kept on the list, it would
	// be refused here for good if it is ever moved back (the panel marks its
	// copy removed and does not send the list again).
	if len(held) < len(req.GetVmIds()) {
		if err := migration.SetFenced(held); err != nil {
			log.Printf("[Recovery] WARNING: VMs with no copy here are still on the fence list: %v", err)
		}
	}
	return resp, nil
}

func (s *NodeAgentService) fenceVM(id string) *pb.FencedVM {
	out := &pb.FencedVM{VmId: id}
	state, found, err := libvirt.DomainState(id)
	if err != nil {
		// Not known: say it may run, so the panel keeps asking.
		log.Printf("[Recovery] WARNING: state of fenced VM %s: %v", id, err)
		out.Defined, out.State = true, "unknown"
		return out
	}
	if !found {
		s.forgetVMNetwork(id)
		return out
	}
	out.Defined, out.State = true, state
	if _, err := libvirt.SetAutostart(id, false); err != nil {
		log.Printf("[Recovery] WARNING: autostart of fenced VM %s not turned off: %v", id, err)
	}
	if state == "shutoff" {
		s.forgetVMNetwork(id)
	} else {
		log.Printf("[Recovery] WARNING: VM %s lives on another node now and is %s here; it was not stopped", id, state)
	}
	disks, _ := libvirt.BackupDisks(id)
	for _, d := range libvirt.UnderOverlays(disks) {
		fd := &pb.FencedDisk{Target: d.Target, Path: d.Path}
		_, fd.Shared = storage.SharedPoolAt(d.Path)
		if d.RBD {
			// The image is the disk the VM runs on now: never this node's copy.
			fd.Shared = true
		} else if d.Block {
			fd.SizeBytes, _ = storage.DeviceSize(d.Path)
		} else if fi, err := os.Stat(d.Path); err == nil {
			fd.SizeBytes = fi.Size()
		}
		out.Disks = append(out.Disks, fd)
	}
	return out
}

// RemoveFencedVM removes this node's copy of a VM that lives on another node:
// its definition, and its disks unless they are in a shared pool, where they
// are the disks the VM runs on now. Only a fenced VM is removed this way.
func (s *NodeAgentService) RemoveFencedVM(ctx context.Context, req *pb.RemoveFencedVMRequest) (*pb.RemoveFencedVMResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(code pb.ErrorCode, err error) (*pb.RemoveFencedVMResponse, error) {
		return &pb.RemoveFencedVMResponse{Error: migErr(code, err)}, nil
	}
	id := req.GetVmId()
	if !migration.ValidUUID(id) {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, errors.New("a VM id is required"))
	}
	if !migration.IsFenced(id) {
		return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, errors.New("this is not a VM that was recovered onto another node, so nothing was removed"))
	}
	state, found, err := libvirt.DomainState(id)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_INTERNAL, err)
	}
	resp := &pb.RemoveFencedVMResponse{Success: true}
	if found {
		if state != "shutoff" {
			if !req.GetForceStop() {
				return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, fmt.Errorf("the old copy is %s on this node; it is removed only when shut off, or when you choose to power it off", state))
			}
			if err := libvirt.StopVM(id, true); err != nil {
				return fail(pb.ErrorCode_ERROR_CODE_INTERNAL, fmt.Errorf("the old copy could not be powered off: %w", err))
			}
		}
		blockDisks, _ := libvirt.BackupDisks(id)
		blockDisks = libvirt.UnderOverlays(blockDisks)
		removed, err := libvirt.RemoveMovedSource(id, func(p string) bool {
			_, keep := storage.SharedPoolAt(p)
			if !keep {
				// The copy here is off, so a lock on the file is another
				// node's VM, on storage this node shares without a pool saying so.
				held, herr := storage.ImageHeldOpen(p)
				keep = held || herr != nil
			}
			if keep {
				resp.Kept = append(resp.Kept, p)
			}
			return keep
		})
		resp.Removed = removed
		if err != nil && !libvirt.IsNoDomain(err) {
			resp.Success, resp.Error = false, migErr(pb.ErrorCode_ERROR_CODE_CONFLICT, err)
			return resp, nil
		}
		if err == nil {
			for _, k := range removeVMVolumes(ctx, id, blockDisks, lvmPoolsOf(req.GetLvmPools()), false) {
				resp.Kept = append(resp.Kept, k.Path)
			}
			for _, d := range blockDisks {
				if d.Block {
					if _, serr := os.Stat(d.Path); errors.Is(serr, os.ErrNotExist) {
						resp.Removed = append(resp.Removed, d.Path)
					}
				}
			}
		}
	}
	s.forgetVMNetwork(id)
	if err := migration.Unfence(id); err != nil {
		log.Printf("[Recovery] WARNING: %s is still on the fence list: %v", id, err)
	}
	log.Printf("[Recovery] removed the old copy of %s: removed %v, kept %v", id, resp.Removed, resp.Kept)
	return resp, nil
}
