package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/agent/guestos"
	"github.com/maburvm/panel/internal/agent/hwprofile"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/storage"
	"github.com/maburvm/panel/internal/agent/vmhealth"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Compute and hardware: profiles at create and later, offline password
// resets, the built-in rescue system, restart on crash, and guest agent calls
// that cannot stall the status path.

var errFirmwareMismatch = errors.New("the firmware does not match")

func firmwareName(uefi bool) string {
	if uefi {
		return "UEFI"
	}
	return "BIOS"
}

// guestAgentWaitStatus is how long the status path waits on a guest agent
// before answering without it.
const guestAgentWaitStatus = 3 * time.Second

// hostFeatures are the agent features that depend on what the host has:
// "uefi" and "secure_boot" (OVMF), "tpm" (swtpm), "hugepages" (a reserved
// huge page pool).
func hostFeatures() []string {
	var out []string
	plain, secure := libvirt.HostFirmware()
	if plain != nil {
		out = append(out, models.FeatureUEFI)
	}
	if secure != nil {
		out = append(out, models.FeatureSecureBoot)
	}
	if swtpmInstalled() {
		out = append(out, models.FeatureTPM)
	}
	if total, _ := hwprofile.HostHugepagesMiB(); total > 0 {
		out = append(out, models.FeatureHugepages)
	}
	return out
}

func swtpmInstalled() bool {
	_, e1 := exec.LookPath("swtpm")
	_, e2 := exec.LookPath("swtpm_setup")
	return e1 == nil && e2 == nil
}

// newVMHardware reads a create's hardware profile and checks the host can
// give it, so a VM is never defined with something it cannot start with.
func newVMHardware(cfg *pb.VMConfig, osType string) (hw models.Hardware, cpus []int, fw *hwprofile.Firmware, err error) {
	p := cfg.GetHardware()
	hw = models.Hardware{
		Sockets: int(p.GetSockets()), Threads: int(p.GetThreads()), NUMA: p.GetNuma(), Hugepages: p.GetHugepages(),
		Watchdog: p.GetWatchdog(), RestartOnCrash: p.GetRestartOnCrash(), SecureBoot: p.GetSecureBoot(), TPM: p.GetTpm(),
	}
	if cpus, err = hostCPUs(p.GetPinnedCpus(), int(cfg.GetResources().GetVcpus())); err != nil {
		return hw, nil, nil, err
	}
	uefi := cfg.GetMetadata()[models.FirmwareKey] == models.FirmwareUEFI
	if hw.SecureBoot && !uefi {
		return hw, nil, nil, fmt.Errorf("Secure Boot needs a UEFI image")
	}
	if uefi {
		plain, secure := libvirt.HostFirmware()
		fw = plain
		if hw.SecureBoot {
			fw = secure
		}
		if fw == nil {
			what := "UEFI firmware (OVMF)"
			if hw.SecureBoot {
				what = "Secure Boot firmware (OVMF with Secure Boot and its key store)"
			}
			return hw, nil, nil, fmt.Errorf("this node has no %s; install the ovmf package on it", what)
		}
		if hw.SecureBoot && guestos.Machine(osType) != "q35" {
			return hw, nil, nil, fmt.Errorf("Secure Boot is not available for %s systems", osType)
		}
	}
	if hw.TPM && !swtpmInstalled() {
		return hw, nil, nil, fmt.Errorf("this node has no TPM emulator; install swtpm and swtpm-tools on it")
	}
	if hw.Hugepages {
		if err := hugepagesFit(int64(cfg.GetResources().GetMemoryMb())); err != nil {
			return hw, nil, nil, err
		}
	}
	return hw, cpus, fw, nil
}

// hostCPUs checks a dedicated VM's cores: one per vCPU, all online here.
func hostCPUs(set string, vcpus int) ([]int, error) {
	cpus, err := models.ParseCPUSet(set)
	if err != nil || len(cpus) == 0 {
		return nil, err
	}
	if vcpus > 0 && len(cpus) != vcpus {
		return nil, fmt.Errorf("a dedicated VM needs one host core per vCPU: %d cores for %d vCPUs", len(cpus), vcpus)
	}
	online, err := hwprofile.HostOnlineCPUs()
	if err != nil {
		return nil, fmt.Errorf("could not read this node's CPUs: %w", err)
	}
	have := map[int]bool{}
	for _, c := range online {
		have[c] = true
	}
	for _, c := range cpus {
		if !have[c] {
			return nil, fmt.Errorf("this node has no CPU %d online; check the node's dedicated CPU pool", c)
		}
	}
	return cpus, nil
}

// hugepagesFit refuses memory the host's free huge pages cannot back: such a
// VM would not start.
func hugepagesFit(memoryMB int64) error {
	total, free := hwprofile.HostHugepagesMiB()
	if total == 0 {
		return fmt.Errorf("this node has no huge pages reserved (vm.nr_hugepages)")
	}
	if free < memoryMB {
		return fmt.Errorf("this node has %d MiB of free huge pages and the VM needs %d MiB", free, memoryMB)
	}
	return nil
}

// SetVMHardware changes parts of a VM's hardware in its saved definition.
func (s *NodeAgentService) SetVMHardware(ctx context.Context, req *pb.SetVMHardwareRequest) (*pb.SetVMHardwareResponse, error) {
	if req.GetVmId() == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id is required")
	}
	if _, ok := GetNodeIDFromContext(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}
	fail := func(err error) (*pb.SetVMHardwareResponse, error) {
		return &pb.SetVMHardwareResponse{Message: err.Error(),
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()}}, nil
	}
	p := req.GetHardware()
	hw := models.Hardware{CPUModel: p.GetCpuModel(), Sockets: int(p.GetSockets()), Threads: int(p.GetThreads()), NUMA: p.GetNuma(),
		Hugepages: p.GetHugepages(), Watchdog: p.GetWatchdog(), RestartOnCrash: p.GetRestartOnCrash()}
	if err := hw.Validate(0); err != nil {
		return fail(err)
	}
	info, err := libvirt.GetVMInfo(req.GetVmId())
	if err != nil {
		return nil, vmLookupError(err)
	}
	cpus, err := hostCPUs(p.GetPinnedCpus(), info.CPU)
	if err != nil {
		return fail(err)
	}
	if hw.Hugepages && (len(req.GetFields()) == 0 || slices.Contains(req.GetFields(), "hugepages")) {
		if err := hugepagesFit(int64(info.MaxMemory >> 20)); err != nil {
			return fail(err)
		}
	}
	restart, err := libvirt.SetHardware(req.GetVmId(), hw, cpus, req.GetFields())
	if err != nil {
		return fail(err)
	}
	if !hw.RestartOnCrash {
		s.crashes.Forget(req.GetVmId())
	}
	msg := "Saved; the VM uses it from its next start."
	if restart {
		msg = "Saved; the VM is running and uses it once it is powered off and on. Nothing was restarted."
	}
	log.Printf("[NodeAgent] VM %s hardware changed (%v)", req.GetVmId(), req.GetFields())
	return &pb.SetVMHardwareResponse{Success: true, RestartRequired: restart, Message: msg}, nil
}

// crashPollEvery is how often the restart-on-crash policy looks at the VMs.
const crashPollEvery = 15 * time.Second

// watchCrashes starts again the VMs marked for restart on crash that crashed,
// as vmhealth.Crashes decides. It polls rather than listening to libvirt
// events, which would need an event loop on every libvirt connection.
// ponytail: up to crashPollEvery late; switch to lifecycle events if that matters.
func (s *NodeAgentService) watchCrashes(ctx context.Context) {
	t := time.NewTicker(crashPollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		doms, err := libvirt.RestartOnCrashDomains()
		if err != nil {
			continue
		}
		for _, d := range doms {
			switch {
			case d.Running:
				s.crashes.Running(d.UUID)
			case d.Crashed && !migrations.Busy(d.UUID) && s.crashes.Crashed(d.UUID):
				if err := libvirt.StartCrashed(d.UUID); err != nil {
					s.crashes.StartFailed(d.UUID)
					log.Printf("[NodeAgent] VM %s crashed and did not start again: %v", d.UUID, err)
					continue
				}
				n, _ := s.crashes.Status(d.UUID)
				log.Printf("[NodeAgent] VM %s crashed and was started again (%d crashes in the last hour)", d.UUID, n)
			case d.Crashed:
				if n, paused := s.crashes.Status(d.UUID); paused {
					log.Printf("[NodeAgent] VM %s crashed %d times within an hour; left off until someone starts it", d.UUID, n)
				}
			}
		}
	}
}

// statusIPs is the VM's addresses for the status path: its guest agent's
// answer when it gives one quickly, else what the host sees. silent says the
// guest agent is backed off.
func (s *NodeAgentService) statusIPs(vmID string) (ips []string, silent bool) {
	var agentIPs []string
	err := s.guestAgents.Do(vmID, guestAgentWaitStatus, func() error {
		var err error
		agentIPs, err = libvirt.GuestAgentIPs(vmID)
		return err
	})
	if err == nil && len(agentIPs) > 0 {
		return agentIPs, false
	}
	ips, _ = libvirt.HostSeenIPs(vmID)
	return ips, errors.Is(err, vmhealth.ErrGuestAgentSilent)
}

// guestAgentHostname is the VM's host name from its guest agent, bounded.
func (s *NodeAgentService) guestAgentHostname(vmID string) string {
	var name string
	_ = s.guestAgents.Do(vmID, guestAgentWaitStatus, func() error {
		var err error
		name, err = libvirt.GuestAgentHostname(vmID)
		return err
	})
	return name
}

// resetPasswordOffline sets the password of a stopped Linux VM by writing it
// onto its disk with libguestfs. The password goes through a file only root
// can read, never the command line.
func resetPasswordOffline(vmID, user, password string) error {
	switch libvirt.GuestOSType(vmID) {
	case guestos.Windows:
		return fmt.Errorf("Windows takes a new password only while it runs with its guest agent: start the VM, wait for it to finish starting, then try again")
	case guestos.RouterOS:
		return fmt.Errorf("internal: RouterOS has its own route")
	}
	if migrations.Busy(vmID) {
		return fmt.Errorf("a disk of this VM is being moved or copied; try again when that has finished")
	}
	if _, err := exec.LookPath("virt-customize"); err != nil {
		return fmt.Errorf("virt-customize not found on the node (install libguestfs-tools): %w", err)
	}
	disk, err := libvirt.GetVMPrimaryDiskPath(vmID)
	if err != nil {
		return err
	}
	if err := noHostEdit(disk, "a password is set only while the VM runs with its guest agent: start the VM, wait for it to finish starting, then try again"); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "maburvm-pw-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(password); err != nil {
		f.Close()
		return err
	}
	f.Close()
	opt := "--root-password"
	val := "file:" + f.Name()
	args := append(guestos.DiskArgs(disk), "--no-network")
	if user != "" && user != "root" {
		opt, val = "--password", user+":file:"+f.Name()
	}
	args = append(args, opt, val)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// QEMU's image lock keeps the VM from starting while the disk is open here.
	out, err := exec.CommandContext(ctx, "virt-customize", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not write the password onto the disk: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// A guest it does not take for Linux (an imported Windows VM without its
	// kind recorded) gets only a warning and an exit code of 0.
	if strings.Contains(string(out), "passwords could not be set") {
		return fmt.Errorf("the system on this VM's disk is not one whose password can be set while it is stopped; start it and reset the password while it runs")
	}
	log.Printf("[NodeAgent] VM %s: %s password written onto its disk while stopped", vmID, user)
	return nil
}

// enterRescue boots a stopped VM into the built-in rescue system: a fresh copy
// of the rescue image, set up with its own address and one-time credentials,
// attached as the first boot disk with the VM's own disks behind it.
func (s *NodeAgentService) enterRescue(req *pb.VMCommandRequest) error {
	cfg := req.GetConfig()
	if cfg.GetImageId() == "" || cfg.GetRootPassword() == "" {
		return fmt.Errorf("the rescue image and a password are required")
	}
	info, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return err
	}
	if info.Status != libvirt.VMStatusStopped {
		return fmt.Errorf("stop the VM first")
	}
	if migrations.Busy(req.VmId) {
		return fmt.Errorf("a disk of this VM is being moved or copied; try again when that has finished")
	}
	// A rescue disk from an earlier rescue goes first: the credentials are new.
	if err := leaveRescue(req.VmId); err != nil {
		return err
	}
	primary, err := libvirt.GetVMPrimaryDiskPath(req.VmId)
	if err != nil {
		return err
	}
	dir := diskDir(primary, defaultImageDir)
	// Not into an unmounted shared pool: that is the local disk underneath.
	if err := storage.CheckSharedDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, rescueDiskName(req.VmId))
	_, imageRef := models.SplitGuestImageRef(cfg.GetImageId())
	template, err := resolveTemplateImage(imageRef, req.GetObjectStore(), cfg.GetMetadata()[models.ImageSHA256Key])
	if err != nil {
		return fmt.Errorf("the rescue image could not be fetched: %w", err)
	}
	if err := storage.NewQCOW2Manager().CloneImageFrom(template, cloneSourceFormat(imageRef), path); err != nil {
		return fmt.Errorf("failed to copy the rescue image: %w", err)
	}
	vmCfg := libvirt.VMConfig{}
	if ifaces := cfg.GetNetworkConfig().GetInterfaces(); len(ifaces) > 0 {
		vmCfg.IPAddress, vmCfg.Netmask, vmCfg.Gateway = ifaces[0].GetIpAddress(), int(ifaces[0].GetNetmask()), ifaces[0].GetGateway()
	}
	if vmCfg.MACAddress, err = libvirt.GetVMInterfaceMAC(req.VmId); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := injectGuestConfig(path, "rescue", vmCfg, cfg.GetRootPassword(), cfg.GetSshPublicKey(), ""); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("could not set up the rescue system: %w", err)
	}
	if err := libvirt.EnterRescue(req.VmId, path); err != nil {
		_ = os.Remove(path)
		return err
	}
	log.Printf("[NodeAgent] VM %s set to boot the rescue system from %s", req.VmId, path)
	return nil
}

func rescueDiskName(vmID string) string { return vmID + "-rescue.qcow2" }

// leaveRescue takes the rescue system's disk out of a stopped VM and deletes
// it. A disk at the rescue drive that enterRescue did not make (a customer's
// disk someone attached there) is detached but never deleted.
func leaveRescue(vmID string) error {
	path, err := libvirt.LeaveRescue(vmID)
	if err != nil {
		return err
	}
	if path != "" && filepath.Base(path) != rescueDiskName(vmID) {
		log.Printf("[NodeAgent] VM %s: %s was at the rescue drive and is kept", vmID, path)
	} else if path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[NodeAgent] VM %s: rescue disk %s not removed: %v", vmID, path, err)
		}
	}
	return nil
}
