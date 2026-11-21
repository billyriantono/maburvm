package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/panel/sshconsole"
	"gorm.io/gorm"
)

// sshHostResolver resolves a VM's reachable SSH host from its tracked (or live)
// interfaces, so the console proxy never connects to a client-supplied address.
type sshHostResolver struct {
	net *service.NetworkService
}

// vmOwnerResolver applies service.CheckVMAccess for the SSH console: the
// owner, or a member whose team role operates the VM.
type vmOwnerResolver struct {
	vmRepo *repository.VMRepository
	db     *gorm.DB
}

// MayOpenConsole is nil when userID may open a console to the VM.
func (r *vmOwnerResolver) MayOpenConsole(ctx context.Context, vmID, userID string) error {
	vm, err := r.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return err
	}
	return service.CheckVMAccess(ctx, r.db, vm, userID, false, service.AccessOperate)
}

// ResolveVMHost returns the VM's first usable IP (strips any /prefix).
func (r *sshHostResolver) ResolveVMHost(ctx context.Context, vmID string) (string, error) {
	details, err := r.net.GetNetworkInterfaceDetails(ctx, vmID)
	if err != nil {
		return "", err
	}
	for _, d := range details {
		ip := d.IPAddress
		if i := strings.IndexByte(ip, '/'); i >= 0 {
			ip = ip[:i]
		}
		if ip = strings.TrimSpace(ip); ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no reachable IP for VM %s", vmID)
}

// setupSSHConsoleRoutes wires the in-browser SSH console: an authenticated token
// mint (POST /api/v1/vms/:id/ssh/token) and the WebSocket bridge (/ws/ssh).
func (s *Server) setupSSHConsoleRoutes(logger *slog.Logger) {
	networkRepo := repository.NewNetworkRepository(s.db)
	firewallRepo := repository.NewFirewallRepository(s.db)
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	networkService := service.NewNetworkService(s.db, networkRepo, firewallRepo, vmRepo, nodeRepo, s.riverClient)

	proxy := sshconsole.NewProxyServer(logger, s.cfg.JWT.SecretKey)
	h := sshconsole.NewHandler(proxy, &sshHostResolver{net: networkService}, &vmOwnerResolver{vmRepo: vmRepo, db: s.db})

	s.echo.GET("/ws/ssh", h.HandleWebSocket)

	g := s.echo.Group("/api/v1/vms/:id/ssh")
	g.Use(panelMiddleware.RequireAuth(s.db))
	g.Use(panelMiddleware.RequirePermission("vm:console"))
	g.POST("/token", h.GenerateToken)
}
