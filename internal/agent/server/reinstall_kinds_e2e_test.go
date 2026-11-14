//go:build linux

package server

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/guestos"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/shared/config"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// TestReinstallAcrossKinds takes one VM through every kind of system by
// reinstalling it: RouterOS 7, then Linux, then Windows, then RouterOS 6. Each
// step changes the VM's virtual hardware, and each system is asked (or its
// disk read) for what it was given.
//
//	MABURVM_E2E_CHR_IMAGE, MABURVM_E2E_CHR6_IMAGE   RouterOS 7 and 6 images (URL or path)
//	MABURVM_E2E_LINUX_IMAGE                         a cloud image (URL or path)
//	MABURVM_E2E_WINDOWS_IMAGE                       an image from scripts/build-windows-template.sh
//	MABURVM_E2E_BRIDGE, MABURVM_E2E_DIR             an empty bridge and a directory
func TestReinstallAcrossKinds(t *testing.T) {
	env := func(k string) string { return os.Getenv("MABURVM_E2E_" + k) }
	chr, chr6, linux, windows, bridge, dir := env("CHR_IMAGE"), env("CHR6_IMAGE"), env("LINUX_IMAGE"), env("WINDOWS_IMAGE"), env("BRIDGE"), env("DIR")
	if chr == "" || chr6 == "" || linux == "" || windows == "" || bridge == "" || dir == "" {
		t.Skip("set the MABURVM_E2E_* variables named above to run against a hypervisor")
	}
	if err := libvirt.Initialize(config.LibvirtConfig{URI: "qemu:///system", PoolMinSize: 1, PoolMaxSize: 4}); err != nil {
		t.Fatalf("libvirt: %v", err)
	}
	s := &NodeAgentService{}
	id := uuid.New().String()
	t.Cleanup(func() {
		if err := libvirt.DeleteVM(id); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	request := func(image, password string) *pb.VMCommandRequest {
		return &pb.VMCommandRequest{
			VmId:     id,
			PoolPath: dir,
			Config: &pb.VMConfig{
				Resources:    &pb.VMResources{Vcpus: 2, MemoryMb: 4096, DiskGb: 40},
				ImageId:      image,
				RootPassword: password,
				VncPassword:  "e2e-vnc1",
				Metadata:     map[string]string{"hostname": "e2e-kinds"},
				NetworkConfig: &pb.VMNetworkConfig{Interfaces: []*pb.NetworkInterface{{
					BridgeName: bridge, IpAddress: "192.0.2.10", Netmask: 24, Gateway: "192.0.2.1",
				}}},
			},
		}
	}
	definition := func() string {
		out, err := exec.Command("virsh", "dumpxml", "--inactive", "--security-info", id).Output()
		if err != nil {
			t.Fatalf("dumpxml: %v", err)
		}
		return string(out)
	}
	// reinstall stops the VM, reinstalls it and checks the definition it now has.
	reinstall := func(kind, image, password, machine string, seedDisc bool) {
		t.Helper()
		_ = libvirt.StopVM(id, true)
		waitVMStatus(id, libvirt.VMStatusStopped, 30*time.Second)
		before := definition()
		if err := s.rebuildVM(request(image, password)); err != nil {
			t.Fatalf("reinstall as %s: %v", kind, err)
		}
		after := definition()
		if got := libvirt.GuestOSType(id); got != kind {
			t.Fatalf("after reinstalling as %s the domain records %q", kind, got)
		}
		// libvirt writes the machine in full ("pc-q35-8.2", "pc-i440fx-8.2").
		if family := map[string]string{"pc": "i440fx", "q35": "q35"}[machine]; !strings.Contains(firstLines(after, "<type "), family) {
			t.Errorf("%s: machine type is not %s: %s", kind, machine, firstLines(after, "<type "))
		}
		if has := strings.Contains(after, "-seed.iso"); has != seedDisc {
			t.Errorf("%s: setup disc attached = %v, want %v", kind, has, seedDisc)
		}
		if has := strings.Contains(after, "<hyperv"); has != (kind == guestos.Windows) {
			t.Errorf("%s: Hyper-V settings present = %v", kind, has)
		}
		// What must survive every conversion: the network card and the console password.
		for _, keep := range []string{"<mac address=", "source bridge='" + bridge + "'", "passwd='e2e-vnc1'", "<uuid>" + id} {
			if strings.Contains(before, keep) && !strings.Contains(after, keep) {
				t.Errorf("%s: the definition lost %s", kind, keep)
			}
		}
		if mac := between(before, "<mac address='", "'"); mac != between(after, "<mac address='", "'") {
			t.Errorf("%s: the MAC address changed from %s", kind, mac)
		}
	}
	routerHas := func(step string) {
		t.Helper()
		var out string
		var err error
		for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(4 * time.Second) {
			if out, err = guestos.RouterOSExec(id, ":put [/system resource get version]; /ip address print terse", 20*time.Second); err == nil && strings.Contains(out, "address=192.0.2.10/24") {
				t.Logf("%s: RouterOS %s has its address", step, strings.SplitN(out, "\n", 2)[0])
				return
			}
		}
		t.Fatalf("%s: the router does not have its address: %q (err %v)", step, out, err)
	}

	// 1. RouterOS 7, created.
	if err := s.createVM(request("guest:routeros:"+chr, "R0uter-pw-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	routerHas("created as RouterOS 7")

	// 2. Linux. No guest agent to ask (it is installed from the network, and
	// this bridge has none), so read what was written into the disk.
	reinstall(guestos.Linux, linux, "L1nux-pw-2", "q35", true)
	disk, err := libvirt.PrimaryDiskPath(id)
	if err != nil {
		t.Fatal(err)
	}
	netFile, err := exec.Command("virt-cat", "-a", disk, guestNetworkFilePath).CombinedOutput()
	if err != nil || !strings.Contains(string(netFile), "Address=192.0.2.10/24") {
		t.Errorf("Linux disk has no static address: %s (err %v)", netFile, err)
	}
	shadow, _ := exec.Command("virt-cat", "-a", disk, "/etc/shadow").CombinedOutput()
	if root := between(string(shadow), "root:", ":"); len(root) < 20 {
		t.Errorf("Linux disk has no root password set (shadow field %q)", root)
	}
	if err := libvirt.StartVM(id); err != nil {
		t.Fatalf("start Linux: %v", err)
	}
	time.Sleep(25 * time.Second)
	if st, _ := libvirt.GetVMStatus(id); st != libvirt.VMStatusRunning {
		t.Errorf("Linux is %s 25 s after start", st)
	}
	t.Log("reinstalled as Linux: address and password are on its disk, and it runs")

	// 3. Windows.
	reinstall(guestos.Windows, "guest:windows:"+windows, "W1ndows-Pa55-3", "q35", true)
	if err := libvirt.StartVM(id); err != nil {
		t.Fatalf("start Windows: %v", err)
	}
	win := func(what, script, want string) {
		t.Helper()
		var out string
		var err error
		for deadline := time.Now().Add(25 * time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Second) {
			if out, err = guestos.WindowsExec(id, script, 60*time.Second); err == nil && strings.Contains(out, want) {
				t.Logf("reinstalled as Windows, %s: %s", what, strings.TrimSpace(out))
				return
			}
		}
		t.Fatalf("Windows %s: wanted %q, got %q (err %v)", what, want, out, err)
	}
	win("name", "$env:COMPUTERNAME", "E2E-KINDS")
	win("address", "(Get-NetIPAddress -AddressFamily IPv4 -PrefixOrigin Manual).IPAddress", "192.0.2.10")
	win("password", "Add-Type -AssemblyName System.DirectoryServices.AccountManagement; "+
		"'password ' + (New-Object System.DirectoryServices.AccountManagement.PrincipalContext('Machine')).ValidateCredentials('Administrator', 'W1ndows-Pa55-3')", "password True")

	// 4. RouterOS 6, which needs the older machine type.
	reinstall(guestos.RouterOS, "guest:routeros:"+chr6, "R0uter-pw-4", "pc", false)
	if err := libvirt.StartVM(id); err != nil {
		t.Fatalf("start RouterOS 6: %v", err)
	}
	routerHas("reinstalled as RouterOS 6")
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return ""
}

func firstLines(s, containing string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, containing) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
