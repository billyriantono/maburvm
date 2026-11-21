package migration

import (
	"fmt"
	"regexp"
	"strings"
)

// ShellCommand is the agent subcommand a peer's key is forced to run. Whatever
// the peer asks for, it gets a libvirt connection or, with ReadDiskCommand, the
// content of one disk of a VM that is shut off.
const ShellCommand = "migration-shell"

// ReadDiskCommand asks for one disk of a stopped VM: "maburvm-read-disk <vm> <target>".
const ReadDiskCommand = "maburvm-read-disk"

// PrimaryDrive in place of a target asks for the VM's first disk (cross-node clone).
const PrimaryDrive = "primary"

var targetRe = regexp.MustCompile(`^(vd|sd|hd|xvd)[a-z]{1,2}$`)

// ValidTarget reports whether s is a drive target such as vda or sdb.
func ValidTarget(s string) bool { return targetRe.MatchString(s) }

// ShellRequest is what a peer's connection asked for.
type ShellRequest struct {
	ReadDisk bool
	VMID     string
	Target   string
}

// ParseShellCommand reads SSH_ORIGINAL_COMMAND. Anything that is not a well
// formed disk read is a libvirt connection: the libvirt client sends its own
// proxy command, which is never run.
func ParseShellCommand(original string) (ShellRequest, error) {
	f := strings.Fields(original)
	if len(f) == 0 || f[0] != ReadDiskCommand {
		return ShellRequest{}, nil
	}
	if len(f) != 3 || !ValidUUID(f[1]) || (!ValidTarget(f[2]) && f[2] != PrimaryDrive) {
		return ShellRequest{}, fmt.Errorf("usage: %s <vm id> <drive>", ReadDiskCommand)
	}
	return ShellRequest{ReadDisk: true, VMID: f[1], Target: f[2]}, nil
}

// ReadDiskRemote is the remote command for a disk read.
func ReadDiskRemote(vmID, target string) string {
	return ReadDiskCommand + " " + vmID + " " + target
}

// Libvirt's system sockets: the monolithic daemon's, and the modular one's.
const (
	libvirtSock   = "/var/run/libvirt/libvirt-sock"
	virtqemudSock = "/var/run/libvirt/virtqemud-sock"
)

// ProxyCommand is what joins an incoming connection to the local libvirt:
// virt-ssh-helper where libvirt ships it, else socat or netcat on the socket.
// The second value names it; nil means nothing usable is installed.
func ProxyCommand(lookPath func(string) (string, error), socketExists func(string) bool, ncHasQ func(string) bool) ([]string, string) {
	if p, err := lookPath("virt-ssh-helper"); err == nil {
		return []string{p, "qemu:///system"}, "virt-ssh-helper"
	}
	sock := libvirtSock
	if !socketExists(sock) && socketExists(virtqemudSock) {
		sock = virtqemudSock
	}
	if p, err := lookPath("socat"); err == nil {
		return []string{p, "-", "UNIX-CONNECT:" + sock}, "socat"
	}
	if p, err := lookPath("nc"); err == nil {
		// Without -q0 netcat outlives the client and the session never closes.
		if ncHasQ(p) {
			return []string{p, "-q0", "-U", sock}, "nc"
		}
		return []string{p, "-U", sock}, "nc"
	}
	return nil, ""
}
