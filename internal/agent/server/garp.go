//go:build linux

package server

import (
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
)

// htons converts a uint16 from host to network byte order.
func htons(h uint16) uint16 { return (h<<8)&0xff00 | (h>>8)&0x00ff }

// sendGratuitousARP broadcasts a gratuitous ARP for ip/mac on the given host
// bridge, exactly as commercial VM panels do when a VM starts. It announces
// "ip is at mac" to the whole L2 segment (including the uplink to the gateway),
// so the upstream immediately learns the binding — overwriting any stale entry a
// previously-assigned VM left — and the new VM is reachable from the internet at
// once, without waiting for the guest to boot and emit traffic itself.
//
// The frame's source MAC and ARP sender MAC are the GUEST's, so the gateway maps
// the IP to the guest's MAC (not the host's). This needs raw packet access, so
// the agent must run as root (it does).
func sendGratuitousARP(bridge, ipStr, macStr string) error {
	ifi, err := net.InterfaceByName(bridge)
	if err != nil {
		return fmt.Errorf("interface %q: %w", bridge, err)
	}
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		return fmt.Errorf("invalid IPv4 %q", ipStr)
	}
	mac, err := net.ParseMAC(macStr)
	if err != nil || len(mac) != 6 {
		return fmt.Errorf("invalid MAC %q", macStr)
	}

	const etherTypeARP = 0x0806
	buf := make([]byte, 42)
	// Ethernet header: dst=broadcast, src=guest MAC, type=ARP.
	broadcast := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	copy(buf[0:6], broadcast)
	copy(buf[6:12], mac)
	binary.BigEndian.PutUint16(buf[12:14], etherTypeARP)
	// ARP payload (gratuitous request: sender IP == target IP).
	binary.BigEndian.PutUint16(buf[14:16], 1)      // htype: Ethernet
	binary.BigEndian.PutUint16(buf[16:18], 0x0800) // ptype: IPv4
	buf[18] = 6                                    // hlen
	buf[19] = 4                                    // plen
	binary.BigEndian.PutUint16(buf[20:22], 1)      // opcode: request
	copy(buf[22:28], mac)                          // sender hardware addr
	copy(buf[28:32], ip)                           // sender protocol addr
	// target hardware addr left zero
	copy(buf[38:42], ip) // target protocol addr == sender (gratuitous)

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(etherTypeARP)))
	if err != nil {
		return fmt.Errorf("open raw socket: %w", err)
	}
	defer syscall.Close(fd)

	addr := &syscall.SockaddrLinklayer{
		Protocol: htons(etherTypeARP),
		Ifindex:  ifi.Index,
		Halen:    6,
	}
	copy(addr.Addr[:6], broadcast)
	if err := syscall.Sendto(fd, buf, 0, addr); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}
