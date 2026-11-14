//go:build linux

package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/guestos"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/shared/config"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// TestWindowsLifecycle creates a Windows VM through the agent's own create
// path from an image built by scripts/build-windows-template.sh, waits for
// Windows to finish setting itself up, and asks it what it ended up with. Then
// it changes the address and resets the password the way the panel would.
//
//	MABURVM_E2E_WINDOWS_IMAGE  path to the built image
//	MABURVM_E2E_BRIDGE         a bridge to attach to; use an empty one made for the test
//	MABURVM_E2E_DIR            an existing directory for the disk
//
// Allow half an hour. The VM is removed at the end, whatever happens.
func TestWindowsLifecycle(t *testing.T) {
	image, bridge, dir := os.Getenv("MABURVM_E2E_WINDOWS_IMAGE"), os.Getenv("MABURVM_E2E_BRIDGE"), os.Getenv("MABURVM_E2E_DIR")
	if image == "" || bridge == "" || dir == "" {
		t.Skip("set MABURVM_E2E_WINDOWS_IMAGE, MABURVM_E2E_BRIDGE and MABURVM_E2E_DIR to run against a hypervisor")
	}
	if err := libvirt.Initialize(config.LibvirtConfig{URI: "qemu:///system", PoolMinSize: 1, PoolMaxSize: 4}); err != nil {
		t.Fatalf("libvirt: %v", err)
	}
	s := &NodeAgentService{}
	id := uuid.New().String()
	const firstPassword, secondPassword = `Fir5t-Pa55 "$x'`, `Sec0nd-Pa55word!`
	t.Cleanup(func() {
		if err := libvirt.DeleteVM(id); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	request := func(password string) *pb.VMCommandRequest {
		return &pb.VMCommandRequest{
			VmId:     id,
			PoolPath: dir,
			Config: &pb.VMConfig{
				Resources:    &pb.VMResources{Vcpus: 2, MemoryMb: 4096, DiskGb: 40},
				ImageId:      "guest:windows:" + image,
				RootPassword: password,
				VncPassword:  uuid.New().String()[:8],
				Metadata:     map[string]string{"hostname": "e2e-win"},
				NetworkConfig: &pb.VMNetworkConfig{Interfaces: []*pb.NetworkInterface{{
					BridgeName: bridge, IpAddress: "192.0.2.10", Netmask: 24, Gateway: "192.0.2.1",
				}}},
			},
		}
	}
	if err := s.createVM(request(firstPassword)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := libvirt.GuestOSType(id); got != guestos.Windows {
		t.Fatalf("the domain records %q, want windows", got)
	}

	ps := func(script string) (string, error) { return guestos.WindowsExec(id, script, 60*time.Second) }
	// eventually waits for a PowerShell check to print want. Windows restarts
	// more than once on its first start, so the agent comes and goes.
	eventually := func(what string, wait time.Duration, script, want string) {
		t.Helper()
		var out string
		var err error
		for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(10 * time.Second) {
			if out, err = ps(script); err == nil && strings.Contains(out, want) {
				t.Logf("%s: %s", what, strings.TrimSpace(out))
				return
			}
		}
		t.Fatalf("%s: wanted %q, last answer %q (err %v)", what, want, out, err)
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	accepts := func(password string) string {
		return "Add-Type -AssemblyName System.DirectoryServices.AccountManagement; " +
			"$c = New-Object System.DirectoryServices.AccountManagement.PrincipalContext('Machine'); " +
			"'password ' + $c.ValidateCredentials('Administrator', " + quote(password) + ")"
	}
	started := time.Now()

	eventually("name", 25*time.Minute, "$env:COMPUTERNAME", "E2E-WIN")
	t.Logf("Windows was set up %s after create", time.Since(started).Round(time.Second))
	eventually("address", 5*time.Minute, "(Get-NetIPAddress -AddressFamily IPv4 -PrefixOrigin Manual).IPAddress", "192.0.2.10")
	eventually("gateway", time.Minute, "(Get-NetRoute -DestinationPrefix '0.0.0.0/0').NextHop", "192.0.2.1")
	eventually("password from create", 5*time.Minute, accepts(firstPassword), "password True")
	eventually("wrong password", time.Minute, accepts("not-the-password"), "password False")
	eventually("remote desktop", time.Minute, "'listening ' + @(Get-NetTCPConnection -LocalPort 3389 -State Listen -ErrorAction SilentlyContinue).Count", "listening ")
	eventually("disk grown to the VM's size", 2*time.Minute, "'{0:N0} GB' -f ((Get-Partition -DriveLetter C).Size / 1GB)", "40 GB")
	eventually("edition", time.Minute, "(Get-CimInstance Win32_OperatingSystem).Caption", "Windows Server")
	// The clock must agree with the host's to the minute, in absolute time,
	// whatever time zone Windows is set to.
	clock, err := ps("[DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')")
	if err != nil {
		t.Fatalf("clock: %v", err)
	}
	guestNow, err := time.Parse(time.RFC3339, strings.TrimSpace(clock))
	if err != nil {
		t.Fatalf("clock: unreadable answer %q", clock)
	}
	if off := time.Since(guestNow); off > 2*time.Minute || off < -2*time.Minute {
		t.Errorf("Windows' clock is %s away from the host's", off.Round(time.Second))
	} else {
		t.Logf("clock: within %s of the host's", off.Round(time.Second).Abs())
	}

	if err := s.applyGuestNetwork(context.Background(), id, "198.51.100.7", 24, "198.51.100.1", guestIPv6{}); err != nil {
		t.Fatalf("address change: %v", err)
	}
	eventually("address after change", time.Minute, "(Get-NetIPAddress -AddressFamily IPv4 -PrefixOrigin Manual).IPAddress", "198.51.100.7")

	if err := libvirt.SetVMPassword(id, "Administrator", secondPassword); err != nil {
		t.Fatalf("password reset: %v", err)
	}
	eventually("password after reset", time.Minute, accepts(secondPassword), "password True")

	// Reinstall: a fresh disk, which Windows sets up again from the new seed.
	const thirdPassword = "Th1rd-Pa55word!"
	if err := libvirt.StopVM(id, true); err != nil {
		t.Fatalf("stop before reinstall: %v", err)
	}
	waitVMStatus(id, libvirt.VMStatusStopped, 30*time.Second)
	if err := s.rebuildVM(request(thirdPassword)); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if err := libvirt.StartVM(id); err != nil {
		t.Fatalf("start after reinstall: %v", err)
	}
	eventually("name after reinstall", 25*time.Minute, "$env:COMPUTERNAME", "E2E-WIN")
	eventually("address after reinstall", 5*time.Minute, "(Get-NetIPAddress -AddressFamily IPv4 -PrefixOrigin Manual).IPAddress", "192.0.2.10")
	eventually("password after reinstall", 5*time.Minute, accepts(thirdPassword), "password True")
	eventually("disk after reinstall", 2*time.Minute, "'{0:N0} GB' -f ((Get-Partition -DriveLetter C).Size / 1GB)", "40 GB")
}
