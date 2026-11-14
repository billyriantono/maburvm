package guestos

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
)

// WindowsNetwork is the addressing a Windows guest is given on the network
// card with the given MAC address.
type WindowsNetwork struct {
	MAC         string
	Address     string // IPv4, no prefix
	Prefix      int
	Gateway     string
	IPv6Address string // CIDR form
	IPv6Gateway string
	IPv6DNS     []string
}

// WindowsNetworkScript is the PowerShell that re-addresses a running Windows
// guest. It replaces every manually set address on that one card: Windows has
// no way to mark which addresses the panel put there.
func WindowsNetworkScript(n WindowsNetwork) (string, error) {
	hw, err := net.ParseMAC(n.MAC)
	if err != nil {
		return "", fmt.Errorf("invalid MAC address %q", n.MAC)
	}
	ip := net.ParseIP(n.Address)
	if ip == nil || ip.To4() == nil || n.Prefix < 1 || n.Prefix > 32 {
		return "", fmt.Errorf("invalid IPv4 address %q/%d", n.Address, n.Prefix)
	}
	if n.Gateway != "" {
		if gw := net.ParseIP(n.Gateway); gw == nil || gw.To4() == nil {
			return "", fmt.Errorf("invalid gateway %q", n.Gateway)
		}
	}
	// Every value below was parsed above and is written back from its parsed
	// form, so nothing the caller sent reaches PowerShell as text.
	mac := strings.ToUpper(strings.ReplaceAll(hw.String(), ":", "-"))
	dns := []string{"1.1.1.1", "8.8.8.8"}
	lines := []string{
		"$ErrorActionPreference = 'Stop'",
		"$a = Get-NetAdapter | Where-Object { $_.MacAddress -eq '" + mac + "' } | Select-Object -First 1",
		"if (-not $a) { throw 'no network adapter with MAC " + mac + "' }",
		"$i = $a.ifIndex",
		"Set-NetIPInterface -InterfaceIndex $i -AddressFamily IPv4 -Dhcp Disabled",
		"Get-NetIPAddress -InterfaceIndex $i -AddressFamily IPv4 -ErrorAction SilentlyContinue | Remove-NetIPAddress -Confirm:$false",
		"Get-NetRoute -InterfaceIndex $i -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false",
	}
	add := fmt.Sprintf("New-NetIPAddress -InterfaceIndex $i -IPAddress '%s' -PrefixLength %d", ip.To4(), n.Prefix)
	if n.Gateway != "" {
		add += " -DefaultGateway '" + net.ParseIP(n.Gateway).To4().String() + "'"
	}
	lines = append(lines, add+" | Out-Null")
	if n.IPv6Address != "" {
		v6, v6net, err := net.ParseCIDR(n.IPv6Address)
		if err != nil || v6.To4() != nil {
			return "", fmt.Errorf("invalid IPv6 address %q", n.IPv6Address)
		}
		ones, _ := v6net.Mask.Size()
		gw := net.ParseIP(n.IPv6Gateway)
		if gw == nil {
			gw = net.ParseIP("fe80::1")
		}
		for _, d := range n.IPv6DNS {
			if p := net.ParseIP(d); p != nil {
				dns = append(dns, p.String())
			}
		}
		lines = append(lines,
			"Get-NetIPAddress -InterfaceIndex $i -AddressFamily IPv6 -PrefixOrigin Manual -ErrorAction SilentlyContinue | Remove-NetIPAddress -Confirm:$false",
			"Get-NetRoute -InterfaceIndex $i -DestinationPrefix '::/0' -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false",
			fmt.Sprintf("New-NetIPAddress -InterfaceIndex $i -IPAddress '%s' -PrefixLength %d | Out-Null", v6, ones),
			fmt.Sprintf("New-NetRoute -InterfaceIndex $i -DestinationPrefix '::/0' -NextHop '%s' | Out-Null", gw),
		)
	}
	lines = append(lines, "Set-DnsClientServerAddress -InterfaceIndex $i -ServerAddresses "+strings.Join(dns, ","))
	return strings.Join(lines, "\n") + "\n", nil
}

// WindowsExec runs a PowerShell script inside a running Windows guest through
// its QEMU guest agent and returns what it printed. A script that fails is an
// error carrying PowerShell's own message.
func WindowsExec(domainName, script string, timeout time.Duration) (string, error) {
	// -EncodedCommand takes UTF-16LE in base64, which carries any script
	// without a quoting problem.
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[2*i:], u)
	}
	agent := func(req map[string]any) ([]byte, error) {
		body, _ := json.Marshal(req)
		out, err := exec.Command("virsh", "qemu-agent-command", domainName, string(body)).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	out, err := agent(map[string]any{
		"execute": "guest-exec",
		"arguments": map[string]any{
			"path":           "powershell.exe",
			"arg":            []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw)},
			"capture-output": true,
		},
	})
	if err != nil {
		return "", fmt.Errorf("guest-exec: %w", err)
	}
	var started struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal(out, &started); err != nil || started.Return.PID == 0 {
		return "", fmt.Errorf("guest-exec: unexpected reply %s", strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		out, err := agent(map[string]any{
			"execute": "guest-exec-status", "arguments": map[string]int{"pid": started.Return.PID},
		})
		if err != nil {
			return "", fmt.Errorf("guest-exec-status: %w", err)
		}
		var st struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
				ErrData  string `json:"err-data"`
			} `json:"return"`
		}
		if err := json.Unmarshal(out, &st); err != nil {
			return "", fmt.Errorf("guest-exec-status: unexpected reply %s", strings.TrimSpace(string(out)))
		}
		if !st.Return.Exited {
			continue
		}
		printed, _ := base64.StdEncoding.DecodeString(st.Return.OutData)
		if st.Return.ExitCode != 0 {
			failed, _ := base64.StdEncoding.DecodeString(st.Return.ErrData)
			return string(printed), fmt.Errorf("Windows refused the change: %s", strings.TrimSpace(string(failed)))
		}
		return strings.TrimSpace(string(printed)), nil
	}
	return "", fmt.Errorf("Windows did not finish the change within %s", timeout)
}
