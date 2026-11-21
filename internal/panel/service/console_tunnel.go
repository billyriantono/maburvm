package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/consolepipe"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// consoleOpenTimeout bounds the wait for the node to connect to the console.
const consoleOpenTimeout = 15 * time.Second

// DialConsole opens a VM's console through its node's agent, inside the
// authenticated channel the panel already holds to that node. The connection
// lives until it is closed or ctx is cancelled.
//
// An agent too old to carry consoles answers with codes.Unimplemented, and the
// caller connects to the console port directly as before.
func (s *VMService) DialConsole(ctx context.Context, vmID string) (net.Conn, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	// Checked again here: the token was issued up to five minutes ago, and the
	// console socket is no longer what a disabled console is kept behind.
	if !vm.ConsoleEnabled {
		return nil, ErrConsoleDisabled
	}
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return nil, fmt.Errorf("agent unavailable: %w", err)
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return nil, err
	}
	return openConsoleStream(authCtx, client, vmID)
}

// openConsoleStream starts a console session on the agent and returns it as a
// connection. It returns only once the agent has reached the console, so an
// error here is the agent's answer and not a guess.
func openConsoleStream(ctx context.Context, client pb.NodeAgentClient, vmID string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := client.ConsoleStream(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	// A send to an agent that already refused returns EOF; Recv has the reason.
	if err := stream.Send(&pb.ConsoleData{VmId: vmID}); err != nil && !errors.Is(err, io.EOF) {
		cancel()
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			cancel()
			return nil, err
		}
	case <-time.After(consoleOpenTimeout):
		cancel()
		return nil, status.Error(codes.DeadlineExceeded, "the node did not open the console in time")
	}

	local, remote := net.Pipe()
	go func() {
		defer cancel()
		_ = consolepipe.Pipe(remote,
			func() ([]byte, error) {
				msg, err := stream.Recv()
				return msg.GetData(), err
			},
			func(data []byte) error { return stream.Send(&pb.ConsoleData{Data: data}) },
		)
	}()
	return local, nil
}
