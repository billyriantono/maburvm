// Package cloudinit generates NoCloud seed images so the panel can inject
// network configuration (static IP/gateway), hostname, and SSH keys into a
// guest at first boot. Cloud images (Ubuntu, Debian, Rocky, …) ship with
// cloud-init, which reads a "cidata" labelled volume attached to the VM.
package cloudinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config describes the guest configuration to inject via cloud-init.
type Config struct {
	InstanceID   string   // unique per VM (the VM ID)
	Hostname     string   // guest hostname
	MACAddress   string   // NIC MAC to match the static config against
	IPAddress    string   // static IPv4 (empty → DHCP)
	Prefix       int      // CIDR prefix length for the static IP (e.g. 24)
	Gateway      string   // default gateway
	Nameservers  []string // DNS servers (defaults applied when empty)
	SSHPublicKey string   // optional authorized key(s); one per line for multiple
	SSHKeys      []string // optional additional authorized keys
	Password     string   // optional root password (plaintext) to set via chpasswd
}

// authorizedKeys returns the de-duplicated set of public keys from both
// SSHPublicKey (newline-separated) and SSHKeys.
func (c Config) authorizedKeys() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	for _, line := range strings.Split(c.SSHPublicKey, "\n") {
		add(line)
	}
	for _, k := range c.SSHKeys {
		add(k)
	}
	return out
}

// GenerateSeedISO writes a NoCloud seed ISO (volume label "cidata") to outPath.
// It returns an error if no ISO authoring tool is available on the host.
func GenerateSeedISO(cfg Config, outPath string) error {
	tmpDir, err := os.MkdirTemp("", "cloudinit-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	files := map[string]string{
		"meta-data":      metaData(cfg),
		"user-data":      userData(cfg),
		"network-config": networkConfig(cfg),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", name, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}
	return writeISO(tmpDir, outPath)
}

func metaData(cfg Config) string {
	host := cfg.Hostname
	if host == "" {
		host = cfg.InstanceID
	}
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", cfg.InstanceID, host)
}

func userData(cfg Config) string {
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	if cfg.Hostname != "" {
		b.WriteString(fmt.Sprintf("hostname: %s\n", cfg.Hostname))
		b.WriteString("manage_etc_hosts: true\n")
	}
	// Set the root password (chpasswd "list" form is the most broadly supported
	// across cloud-init versions) and enable password SSH auth so it's usable.
	if cfg.Password != "" {
		b.WriteString("ssh_pwauth: true\n")
		b.WriteString("chpasswd:\n")
		b.WriteString("  expire: false\n")
		b.WriteString("  list: |\n")
		b.WriteString(fmt.Sprintf("    root:%s\n", cfg.Password))
	}
	if keys := cfg.authorizedKeys(); len(keys) > 0 {
		b.WriteString("ssh_authorized_keys:\n")
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("  - %s\n", k))
		}
	}
	return b.String()
}

// networkConfig renders cloud-init network-config v2 (netplan-style).
func networkConfig(cfg Config) string {
	var b strings.Builder
	b.WriteString("version: 2\n")
	b.WriteString("ethernets:\n")
	b.WriteString("  primary:\n")

	if cfg.MACAddress != "" {
		b.WriteString("    match:\n")
		b.WriteString(fmt.Sprintf("      macaddress: \"%s\"\n", strings.ToLower(cfg.MACAddress)))
		b.WriteString("    set-name: eth0\n")
	}

	if cfg.IPAddress != "" && cfg.Prefix > 0 {
		b.WriteString("    dhcp4: false\n")
		b.WriteString("    addresses:\n")
		b.WriteString(fmt.Sprintf("      - %s/%d\n", cfg.IPAddress, cfg.Prefix))
		if cfg.Gateway != "" {
			b.WriteString("    routes:\n")
			b.WriteString("      - to: default\n")
			b.WriteString(fmt.Sprintf("        via: %s\n", cfg.Gateway))
		}
		ns := cfg.Nameservers
		if len(ns) == 0 {
			ns = []string{"1.1.1.1", "8.8.8.8"}
		}
		b.WriteString("    nameservers:\n")
		b.WriteString(fmt.Sprintf("      addresses: [%s]\n", strings.Join(ns, ", ")))
	} else {
		b.WriteString("    dhcp4: true\n")
	}

	return b.String()
}

// writeISO authors an ISO9660 image labelled "cidata" containing the seed files.
// It tries the common authoring tools found on KVM hosts in order.
func writeISO(srcDir, outPath string) error {
	ud := filepath.Join(srcDir, "user-data")
	md := filepath.Join(srcDir, "meta-data")
	nc := filepath.Join(srcDir, "network-config")

	candidates := [][]string{
		{"genisoimage", "-output", outPath, "-volid", "cidata", "-joliet", "-rock", ud, md, nc},
		{"mkisofs", "-output", outPath, "-volid", "cidata", "-joliet", "-rock", ud, md, nc},
		{"xorriso", "-as", "mkisofs", "-output", outPath, "-volid", "cidata", "-joliet", "-rock", ud, md, nc},
		// cloud-localds takes the network config as a flag, not a positional arg.
		{"cloud-localds", "--network-config=" + nc, outPath, ud, md},
	}

	var lastErr error
	for _, args := range candidates {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%s failed: %v (%s)", args[0], err, strings.TrimSpace(string(out)))
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no ISO authoring tool found (install genisoimage, xorriso, or cloud-image-utils)")
}
