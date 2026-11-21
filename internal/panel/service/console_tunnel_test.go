package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// The console socket is on the node's loopback whether a console is enabled or
// not, so the panel is what keeps a disabled one shut: no token, and no session
// for a token issued just before it was disabled. Neither may contact the node.
func TestDisabledConsoleIsRefusedByThePanel(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	port := 5901
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "console-off.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped, VNCPort: &port, VNCPassword: "abcd1234", ConsoleEnabled: true}
	if err := db.Create(vm).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db),
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := svc.GetVNCConfig(ctx, vm.ID, true); err != nil {
		t.Fatalf("an enabled console was refused: %v", err)
	}
	if err := db.Model(&models.VM{}).Where("id = ?", vm.ID).Update("console_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetVNCConfig(ctx, vm.ID, true); !errors.Is(err, ErrConsoleDisabled) {
		t.Fatalf("GetVNCConfig on a disabled console: %v", err)
	}
	if conn, err := svc.DialConsole(ctx, vm.ID); !errors.Is(err, ErrConsoleDisabled) || conn != nil {
		t.Fatalf("DialConsole on a disabled console: conn=%v err=%v", conn, err)
	}
}

// consoleAgent answers ConsoleStream the way the node agent does, with a
// console that sends back whatever it is sent in upper case.
type consoleAgent struct {
	pb.UnimplementedNodeAgentServer
	refuse error
	asked  chan string
	closed chan struct{}
}

func (a *consoleAgent) ConsoleStream(stream pb.NodeAgent_ConsoleStreamServer) error {
	defer close(a.closed)
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	a.asked <- first.GetVmId()
	if a.refuse != nil {
		return a.refuse
	}
	if err := stream.Send(&pb.ConsoleData{}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		if err := stream.Send(&pb.ConsoleData{Data: bytes.ToUpper(msg.GetData())}); err != nil {
			return err
		}
	}
}

// An agent built before ConsoleStream existed.
type olderAgent struct {
	pb.UnimplementedNodeAgentServer
}

func agentClient(t *testing.T, agent pb.NodeAgentServer) pb.NodeAgentClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, agent)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///node",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewNodeAgentClient(conn)
}

func TestOpenConsoleStreamCarriesTheSession(t *testing.T) {
	agent := &consoleAgent{asked: make(chan string, 1), closed: make(chan struct{})}
	conn, err := openConsoleStream(context.Background(), agentClient(t, agent), "vm-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := <-agent.asked; got != "vm-1" {
		t.Fatalf("agent was asked for %q", got)
	}

	if _, err := conn.Write([]byte("rfb 003.008")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "RFB 003.008" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}

	// Closing the panel's end must end the session on the node, or every
	// closed browser tab would leave a connection to the console behind.
	conn.Close()
	select {
	case <-agent.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the agent's session outlived the panel's connection")
	}
}

func TestOpenConsoleStreamReportsAnOlderAgent(t *testing.T) {
	_, err := openConsoleStream(context.Background(), agentClient(t, olderAgent{}), "vm-1")
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("got %v, want Unimplemented so the caller connects to the console port directly", err)
	}
}

func TestOpenConsoleStreamReportsARefusal(t *testing.T) {
	agent := &consoleAgent{
		refuse: status.Error(codes.FailedPrecondition, "the VM is not running"),
		asked:  make(chan string, 1), closed: make(chan struct{}),
	}
	conn, err := openConsoleStream(context.Background(), agentClient(t, agent), "vm-1")
	if status.Code(err) != codes.FailedPrecondition || conn != nil {
		t.Fatalf("conn=%v err=%v, want the agent's refusal and no connection", conn, err)
	}
}
