package server

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/netrules"
	"github.com/maburvm/panel/internal/shared/consolepipe"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// Consoles used to listen on every address of the node, with no password
// unless someone had opened that console since the VM started. New and
// restarted VMs listen on loopback with a password, and the panel reaches them
// through ConsoleStream. A VM that was already running keeps the socket it
// started with, and qemu cannot add a password to it, so until it is powered
// off and on its port is closed here to everything but the node itself.

// consoleShieldInterval is how often the closed ports are matched to the VMs
// that still need them: one stops needing it the moment it is powered off.
const consoleShieldInterval = 5 * time.Minute

// consoleShieldOff reports the operator's switch for the port rules. It exists
// for one case: a panel older than this agent, which still connects to console
// ports directly.
func consoleShieldOff() bool {
	return strings.EqualFold(os.Getenv("AGENT_CONSOLE_SHIELD"), "off")
}

var (
	consoleMu         sync.Mutex
	lastConsoleReport string
)

// secureConsoles runs one pass. redefine also rewrites stored definitions, and
// is only for startup: later passes must not save a definition back while an
// operation on that VM may be editing it.
func secureConsoles(redefine bool) {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	legacy, err := libvirt.SecureConsoles(redefine)
	if err != nil {
		// Not knowing which consoles are open is no reason to open ports.
		log.Printf("[Console] WARNING: could not check the consoles on this node: %v", err)
		return
	}
	var ports []int
	var open []string
	for _, c := range legacy {
		ports = append(ports, c.Port)
		if c.WebsocketPort > 0 {
			ports = append(ports, c.WebsocketPort)
		}
		if !c.HasPassword {
			open = append(open, c.Name)
		}
	}
	slices.Sort(open)

	state := "closed to everything but this node"
	if consoleShieldOff() {
		ports = nil
		state = "NOT closed (AGENT_CONSOLE_SHIELD=off)"
	}
	shieldErr := shieldConsolePorts(ports)
	if shieldErr != nil {
		state = fmt.Sprintf("NOT reliably closed: %v", shieldErr)
	}

	report := "every running VM has its console on loopback"
	if shieldErr != nil {
		report += fmt.Sprintf("; port rules left from earlier could not be removed: %v", shieldErr)
	}
	if len(legacy) > 0 {
		report = fmt.Sprintf("%d running VMs still hold a console socket from before consoles moved to loopback; their ports are %s",
			len(legacy), state)
		if len(open) > 0 {
			report += fmt.Sprintf(". %d of them have no console password until they are powered off and on: %s",
				len(open), strings.Join(open, ", "))
		}
	}
	if report != lastConsoleReport {
		log.Printf("[Console] %s", report)
		lastConsoleReport = report
	}
}

func secureConsolesForever() {
	for {
		time.Sleep(consoleShieldInterval)
		secureConsoles(false)
	}
}

func shieldConsolePorts(ports []int) error {
	var errs []error
	for _, proto := range []iptables.Protocol{iptables.ProtocolIPv4, iptables.ProtocolIPv6} {
		ipt, err := iptables.NewWithProtocol(proto)
		if err != nil {
			// No such firewall on this node, so no rules of ours in it either.
			if len(ports) > 0 {
				errs = append(errs, err)
			}
			continue
		}
		if err := syncConsoleShield(ipt, ports); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func syncConsoleShield(ipt *iptables.IPTables, ports []int) error {
	const table = "filter"
	exists, err := ipt.ChainExists(table, netrules.ConsoleChain)
	if err != nil {
		return err
	}
	if !exists {
		// A node with nothing to close keeps a firewall this never touched.
		if len(ports) == 0 {
			return nil
		}
		if err := ipt.NewChain(table, netrules.ConsoleChain); err != nil {
			return err
		}
	}
	rules, err := ipt.List(table, netrules.ConsoleChain)
	if err != nil {
		return err
	}
	add, remove := netrules.ConsoleShieldPlan(netrules.ShieldedPorts(rules), ports)
	for _, port := range add {
		if err := ipt.AppendUnique(table, netrules.ConsoleChain, netrules.ConsoleShieldRule(port)...); err != nil {
			return err
		}
	}
	if jumps, err := ipt.Exists(table, "INPUT", "-j", netrules.ConsoleChain); err != nil {
		return err
	} else if !jumps {
		if err := ipt.Insert(table, "INPUT", 1, "-j", netrules.ConsoleChain); err != nil {
			return err
		}
	}
	for _, port := range remove {
		if err := ipt.DeleteIfExists(table, netrules.ConsoleChain, netrules.ConsoleShieldRule(port)...); err != nil {
			return err
		}
	}
	return nil
}

// ConsoleStream connects the panel to a VM's console socket on this node. The
// stream is authenticated like every other call (streamAuthInterceptor) and
// runs inside the agent's TLS, which is what lets the socket stay on loopback.
func (s *NodeAgentService) ConsoleStream(stream pb.NodeAgent_ConsoleStreamServer) error {
	if _, authenticated := GetNodeIDFromContext(stream.Context()); !authenticated {
		return status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	vmID := first.GetVmId()
	if vmID == "" {
		return status.Errorf(codes.InvalidArgument, "vm_id is required")
	}
	addr, err := libvirt.ConsoleDialAddress(vmID)
	if err != nil {
		if libvirt.IsNoDomain(err) {
			return status.Errorf(codes.NotFound, "VM not found: %v", err)
		}
		return status.Errorf(codes.FailedPrecondition, "console of VM %s is not available: %v", vmID, err)
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return status.Errorf(codes.Unavailable, "console of VM %s did not answer: %v", vmID, err)
	}
	// An empty message tells the panel the console is connected.
	if err := stream.Send(&pb.ConsoleData{}); err != nil {
		conn.Close()
		return err
	}
	log.Printf("[Console] session opened for VM %s", vmID)
	err = consolepipe.Pipe(conn,
		func() ([]byte, error) {
			msg, err := stream.Recv()
			return msg.GetData(), err
		},
		func(data []byte) error { return stream.Send(&pb.ConsoleData{Data: data}) },
	)
	log.Printf("[Console] session closed for VM %s", vmID)
	if err != nil && status.Code(err) != codes.Canceled {
		return err
	}
	return nil
}
