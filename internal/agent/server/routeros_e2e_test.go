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

// TestRouterOSLifecycle drives the agent's own create, address change,
// password reset and reinstall against a real hypervisor, and asks the router
// itself what it ended up with. It needs a node, so it runs only when told
// where to work:
//
//	MABURVM_E2E_CHR_IMAGE   image URL or path, e.g. https://download.mikrotik.com/routeros/7.24.6/chr-7.24.6.img.zip
//	MABURVM_E2E_BRIDGE      a bridge to attach to; use an empty one made for the test
//	MABURVM_E2E_DIR         an existing directory for the disk
//
// The VM it creates is removed at the end, whatever happens.
func TestRouterOSLifecycle(t *testing.T) {
	image, bridge, dir := os.Getenv("MABURVM_E2E_CHR_IMAGE"), os.Getenv("MABURVM_E2E_BRIDGE"), os.Getenv("MABURVM_E2E_DIR")
	if image == "" || bridge == "" || dir == "" {
		t.Skip("set MABURVM_E2E_CHR_IMAGE, MABURVM_E2E_BRIDGE and MABURVM_E2E_DIR to run against a hypervisor")
	}
	if err := libvirt.Initialize(config.LibvirtConfig{URI: "qemu:///system", PoolMinSize: 1, PoolMaxSize: 4}); err != nil {
		t.Fatalf("libvirt: %v", err)
	}
	s := &NodeAgentService{}
	ctx := context.Background()
	id := uuid.New().String()
	const firstPassword, secondPassword = `Fir5t "$\[pw]`, `Sec0nd-pw`

	request := func(password string) *pb.VMCommandRequest {
		return &pb.VMCommandRequest{
			VmId:     id,
			PoolPath: dir,
			Config: &pb.VMConfig{
				Resources:    &pb.VMResources{Vcpus: 1, MemoryMb: 384, DiskGb: 1},
				ImageId:      "guest:routeros:" + image,
				RootPassword: password,
				VncPassword:  uuid.New().String()[:8],
				Metadata:     map[string]string{"hostname": "e2e-router"},
				NetworkConfig: &pb.VMNetworkConfig{Interfaces: []*pb.NetworkInterface{{
					BridgeName: bridge, IpAddress: "192.0.2.10", Netmask: 24, Gateway: "192.0.2.1",
				}}},
			},
		}
	}
	t.Cleanup(func() {
		if err := libvirt.DeleteVM(id); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	// ask runs a script in the router, waiting out the seconds after a boot in
	// which its guest agent is not answering yet.
	ask := func(script string) string {
		t.Helper()
		var out string
		var err error
		for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
			if out, err = guestos.RouterOSExec(id, script, 20*time.Second); err == nil {
				return out
			}
		}
		t.Fatalf("router did not answer: %v", err)
		return ""
	}
	// wantAddress waits for the first-boot script to have run, then checks it.
	wantAddress := func(step, address string) {
		t.Helper()
		var got string
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
			if got = ask("/ip address print terse"); strings.Contains(got, "address="+address) {
				t.Logf("%s: router has %s", step, address)
				return
			}
		}
		t.Fatalf("%s: router does not have %s:\n%s", step, address, got)
	}
	// accepts asks the router to sign in to itself. Version 7 only: it is the
	// one with an HTTP API to sign in to.
	accepts := func(password string) bool {
		out := ask(`:do { :put ([/tool fetch url="http://127.0.0.1/rest/system/identity" user=admin password=` +
			`"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(password) + `" output=user as-value]->"data") } on-error={ :put "REFUSED" }`)
		return !strings.Contains(out, "REFUSED")
	}
	version := func() string { return ask(":put [/system resource get version]") }
	// holds waits for the router to accept password: after a restart its guest
	// agent answers a moment before the start-up script has run.
	holds := func(password string) bool {
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
			if accepts(password) {
				return true
			}
		}
		return false
	}

	if err := s.createVM(request(firstPassword)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := libvirt.GuestOSType(id); got != guestos.RouterOS {
		t.Fatalf("the domain records %q, want routeros", got)
	}
	wantAddress("create", "192.0.2.10/24")
	v := version()
	t.Logf("RouterOS %s", v)
	if got := ask("/system identity print"); !strings.Contains(got, "e2e-router") {
		t.Errorf("identity not set:\n%s", got)
	}
	v7 := strings.HasPrefix(v, "7")
	if v7 && (!accepts(firstPassword) || accepts("not-the-password")) {
		t.Errorf("after create the router does not hold the password it was given")
	}

	// Address change on the running router: no restart expected.
	if err := s.applyGuestNetwork(ctx, id, "198.51.100.7", 24, "198.51.100.1", guestIPv6{}); err != nil {
		t.Fatalf("address change: %v", err)
	}
	wantAddress("address change", "198.51.100.7/24")

	// Password reset. Version 7 refuses it through the guest agent, so this
	// also proves the restart route.
	if err := s.runRouterOSScript(ctx, id, guestos.RouterOSPasswordScript(secondPassword)); err != nil {
		t.Fatalf("password reset: %v", err)
	}
	wantAddress("after password reset", "198.51.100.7/24") // the earlier change survived it
	if v7 && (!holds(secondPassword) || accepts(firstPassword)) {
		t.Errorf("after the reset the router does not hold the new password")
	}

	// Reinstall: a fresh disk, set up again from scratch.
	if err := libvirt.StopVM(id, true); err != nil {
		t.Fatalf("stop before reinstall: %v", err)
	}
	waitVMStatus(id, libvirt.VMStatusStopped, 30*time.Second)
	if err := s.rebuildVM(request(secondPassword)); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if err := libvirt.StartVM(id); err != nil {
		t.Fatalf("start after reinstall: %v", err)
	}
	wantAddress("reinstall", "192.0.2.10/24")
	if v7 && !holds(secondPassword) {
		t.Errorf("after reinstall the router does not hold its password")
	}

	// A Linux template must be refused for this VM, before its disk is touched.
	linux := request("x")
	linux.Config.ImageId = image
	if err := s.rebuildVM(linux); err == nil || !strings.Contains(err.Error(), "cannot be reinstalled") {
		t.Errorf("reinstalling a router as Linux: err = %v, want a refusal", err)
	}
}
