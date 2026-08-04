package service

import (
	"context"
	"errors"
	"fmt"
	"net"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrNotAFloatingIP is returned when a floating-IP operation targets an
	// address that is delivered directly (bound in the guest) instead.
	ErrNotAFloatingIP = errors.New("address is not a floating IP")
	// ErrFloatingIPInUse is returned when releasing a floating IP that is still
	// attached to a VM.
	ErrFloatingIPInUse = errors.New("floating IP is still attached to a VM")
	// ErrFloatingIPWrongNode is returned when attaching a floating IP to a VM on
	// a different node. Phase 1 keeps floating IPs within one node's own address
	// block, because the nodes sit on different L2 segments and an address is
	// not routable to a node outside its allocated block.
	ErrFloatingIPWrongNode = errors.New("floating IP and VM are on different nodes")
	// ErrFloatingIPNoPoolBridge is returned when the floating IP's pool has no
	// bridge configured, so the agent has nothing to bind the address to.
	ErrFloatingIPNoPoolBridge = errors.New("floating IP pool has no bridge configured")
	// ErrVMHasNoAddress is returned when the target VM has no address recorded,
	// so there is nothing to NAT the floating IP to.
	ErrVMHasNoAddress = errors.New("VM has no IP address recorded")
	// ErrFullNATNeedsPrivateVM rejects full 1:1 NAT on a VM that holds its own
	// public address. Such a VM is bridged straight to the upstream gateway, so
	// its outbound traffic never passes through the host and the egress SNAT
	// would never match — the VM would keep egressing under its own address
	// while the panel claimed otherwise. Verified on a live node.
	ErrFullNATNeedsPrivateVM = errors.New(
		"full 1:1 NAT requires a VM on a private address routed through the node; " +
			"a VM with its own public IP cannot egress as a floating IP, use inbound mode")
)

// AllocateFloatingIP takes an address out of a pool and marks it floating: it is
// reserved to the owning tenant and, unlike a directly-assigned address, it is
// NOT released when the VM it is attached to is deleted. requestedIP may be
// empty to take the next free address in the pool.
func (s *VMService) AllocateFloatingIP(ctx context.Context, poolID, nodeID, userID, requestedIP string) (*models.IPAddress, error) {
	if requestedIP != "" && net.ParseIP(requestedIP) == nil {
		return nil, ErrInvalidIPAddress
	}
	var allocated *models.IPAddress
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		req := &AllocateIPAddressRequest{PoolID: poolID, RequestedIP: requestedIP}
		if nodeID != "" {
			req.NodeID = &nodeID
		}
		addr, err := s.ipamService.AllocateAddressInTx(ctx, tx, req)
		if err != nil {
			return err
		}
		// AllocateAddress marks it 'assigned'; a floating IP starts life attached
		// to nothing, so hold it as 'reserved' — reserved addresses are never
		// handed out by the allocator, which is what "owned but unattached" means.
		addr.Status = models.IPAddressStatusReserved
		addr.DeliveryMode = models.IPDeliveryFloating
		addr.NATMode = ""
		addr.VMID = nil
		if userID != "" {
			addr.UserID = &userID
		}
		if err := tx.Save(addr).Error; err != nil {
			return err
		}
		allocated = addr
		return nil
	})
	if err != nil {
		return nil, err
	}
	return allocated, nil
}

// ListFloatingIPs returns floating IPs, scoped to one owner when userID is set
// (empty means admin/all).
func (s *VMService) ListFloatingIPs(ctx context.Context, userID string) ([]models.IPAddress, error) {
	var addrs []models.IPAddress
	q := s.db.WithContext(ctx).Where("delivery_mode = ?", models.IPDeliveryFloating)
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	return addrs, q.Order("address ASC").Find(&addrs).Error
}

// GetFloatingIP loads a single floating IP by address ID.
func (s *VMService) GetFloatingIP(ctx context.Context, addressID string) (*models.IPAddress, error) {
	var addr models.IPAddress
	if err := s.db.WithContext(ctx).Where("id = ?", addressID).First(&addr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIPAddressNotFound
		}
		return nil, err
	}
	if addr.DeliveryMode != models.IPDeliveryFloating {
		return nil, ErrNotAFloatingIP
	}
	return &addr, nil
}

// AttachFloatingIP points a floating IP at a VM. natMode may be empty, in which
// case it defaults to "inbound" for a VM that already holds a public address
// (leaving its existing egress identity alone) and "full" otherwise, so a VM on
// a private address egresses as its floating IP.
//
// Attaching an already-attached floating IP moves it: the agent call is
// idempotent and keyed on the address, so the old VM's rules are replaced.
func (s *VMService) AttachFloatingIP(ctx context.Context, addressID, vmID, natMode string) (*models.IPAddress, error) {
	addr, err := s.GetFloatingIP(ctx, addressID)
	if err != nil {
		return nil, err
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, err
	}
	if addr.NodeID != nil && *addr.NodeID != "" && *addr.NodeID != vm.NodeID {
		return nil, ErrFloatingIPWrongNode
	}

	bridge, err := s.floatingPoolBridge(ctx, addr)
	if err != nil {
		return nil, err
	}
	internalIP, err := s.vmInternalIP(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if natMode == "" {
		natMode = defaultNATMode(internalIP)
	}
	if natMode != models.NATModeInbound && natMode != models.NATModeFull {
		return nil, fmt.Errorf("invalid nat_mode %q (want %q or %q)", natMode, models.NATModeInbound, models.NATModeFull)
	}
	// Refuse rather than store a mode the data path cannot honour.
	if natMode == models.NATModeFull && !isHostRoutedAddress(internalIP) {
		return nil, ErrFullNATNeedsPrivateVM
	}

	if err := s.applyFloatingIP(ctx, vm.NodeID, addr.Address, vmID, internalIP, bridge, natMode, true); err != nil {
		return nil, err
	}

	addr.VMID = &vmID
	addr.NATMode = natMode
	addr.Status = models.IPAddressStatusAssigned
	addr.NodeID = &vm.NodeID
	if err := s.db.WithContext(ctx).Save(addr).Error; err != nil {
		return nil, err
	}
	return addr, nil
}

// DetachFloatingIP removes a floating IP from its VM. It tears down only the
// floating IP's own rules; the VM keeps the baseline masquerade and therefore
// keeps outbound internet on its own address.
func (s *VMService) DetachFloatingIP(ctx context.Context, addressID string) (*models.IPAddress, error) {
	addr, err := s.GetFloatingIP(ctx, addressID)
	if err != nil {
		return nil, err
	}
	if addr.VMID == nil {
		return addr, nil // already detached
	}
	bridge, err := s.floatingPoolBridge(ctx, addr)
	if err != nil {
		return nil, err
	}
	nodeID := ""
	if addr.NodeID != nil {
		nodeID = *addr.NodeID
	}
	if nodeID == "" {
		if vm, verr := s.vmRepo.GetByID(ctx, *addr.VMID); verr == nil {
			nodeID = vm.NodeID
		}
	}
	if err := s.applyFloatingIP(ctx, nodeID, addr.Address, *addr.VMID, "", bridge, "", false); err != nil {
		return nil, err
	}

	addr.VMID = nil
	addr.NATMode = ""
	addr.Status = models.IPAddressStatusReserved
	if err := s.db.WithContext(ctx).Save(addr).Error; err != nil {
		return nil, err
	}
	return addr, nil
}

// ReleaseFloatingIP returns a floating IP to its pool as an ordinary allocatable
// address. It refuses while the address is still attached, so a release can't
// silently pull an address out from under a running VM.
func (s *VMService) ReleaseFloatingIP(ctx context.Context, addressID string) error {
	addr, err := s.GetFloatingIP(ctx, addressID)
	if err != nil {
		return err
	}
	if addr.VMID != nil {
		return ErrFloatingIPInUse
	}
	return s.db.WithContext(ctx).Model(addr).Updates(map[string]interface{}{
		"status":        models.IPAddressStatusAvailable,
		"delivery_mode": models.IPDeliveryDirect,
		"nat_mode":      "",
		"user_id":       nil,
	}).Error
}

// ReconcileFloatingIPs re-applies every attached floating IP on a node.
//
// This is not an optimisation: iptables rules and host addresses are runtime
// state, so a node reboot silently drops every floating IP. Attach is
// idempotent, so replaying the desired set restores them. Without this the
// feature works right up until the first reboot and then quietly stops.
func (s *VMService) ReconcileFloatingIPs(ctx context.Context, nodeID string) {
	var addrs []models.IPAddress
	if err := s.db.WithContext(ctx).
		Where("delivery_mode = ? AND vm_id IS NOT NULL AND node_id = ?", models.IPDeliveryFloating, nodeID).
		Find(&addrs).Error; err != nil {
		s.logger.WarnContext(ctx, "floating IP reconcile: list failed", "node_id", nodeID, "error", err)
		return
	}
	for i := range addrs {
		addr := &addrs[i]
		bridge, err := s.floatingPoolBridge(ctx, addr)
		if err != nil {
			s.logger.WarnContext(ctx, "floating IP reconcile: pool bridge", "address", addr.Address, "error", err)
			continue
		}
		internalIP, err := s.vmInternalIP(ctx, *addr.VMID)
		if err != nil {
			s.logger.WarnContext(ctx, "floating IP reconcile: vm address", "address", addr.Address, "error", err)
			continue
		}
		natMode := addr.NATMode
		if natMode == "" {
			natMode = models.NATModeInbound
		}
		if err := s.applyFloatingIP(ctx, nodeID, addr.Address, *addr.VMID, internalIP, bridge, natMode, true); err != nil {
			s.logger.WarnContext(ctx, "floating IP reconcile: reapply failed", "address", addr.Address, "error", err)
		}
	}
}

// detachFloatingIPsOnAgentForVM tears down the host-side rules of every floating
// IP attached to a VM that is about to be deleted. The DB side (clearing vm_id
// while keeping the address for its owner) happens in the deletion transaction;
// this is the node-side half, which can't run inside that transaction. It is
// best-effort — an unreachable node leaves rules pointing at a dead VM's
// address, which the next attach or reconcile pass overwrites.
func (s *VMService) detachFloatingIPsOnAgentForVM(ctx context.Context, vmID, nodeID string) {
	var addrs []models.IPAddress
	if err := s.db.WithContext(ctx).
		Where("vm_id = ? AND delivery_mode = ?", vmID, models.IPDeliveryFloating).
		Find(&addrs).Error; err != nil || len(addrs) == 0 {
		return
	}
	for i := range addrs {
		bridge, err := s.floatingPoolBridge(ctx, &addrs[i])
		if err != nil {
			continue
		}
		if err := s.applyFloatingIP(ctx, nodeID, addrs[i].Address, vmID, "", bridge, "", false); err != nil {
			s.logger.WarnContext(ctx, "floating IP detach on VM delete failed",
				"vm_id", vmID, "address", addrs[i].Address, "error", err)
		}
	}
}

// applyFloatingIP issues the attach/detach RPC to the node agent.
func (s *VMService) applyFloatingIP(ctx context.Context, nodeID, floatingIP, vmID, internalIP, bridge, natMode string, attach bool) error {
	if nodeID == "" {
		return ErrNodeNotFound
	}
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return err
	}
	authCtx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return err
	}
	_, err = client.ConfigureFloatingIP(authCtx, &pb.FloatingIPRequest{
		FloatingIp: floatingIP,
		VmId:       vmID,
		InternalIp: internalIP,
		Bridge:     bridge,
		Attach:     attach,
		NatMode:    natMode,
	})
	return err
}

// floatingPoolBridge resolves the uplink bridge the address must be bound to.
func (s *VMService) floatingPoolBridge(ctx context.Context, addr *models.IPAddress) (string, error) {
	pool, err := s.ipamService.GetPool(ctx, addr.PoolID)
	if err != nil {
		return "", err
	}
	if pool.Bridge == "" {
		return "", ErrFloatingIPNoPoolBridge
	}
	return pool.Bridge, nil
}

// vmInternalIP returns the address traffic for a floating IP must be NATed to.
func (s *VMService) vmInternalIP(ctx context.Context, vmID string) (string, error) {
	network, err := s.networkRepo.GetByVMID(ctx, vmID)
	if err != nil || network == nil || network.IPAddress == "" {
		return "", ErrVMHasNoAddress
	}
	return network.IPAddress, nil
}

// isHostRoutedAddress reports whether a VM's traffic actually transits this node.
// A private address means the node is the VM's gateway, so host NAT applies in
// both directions. Any other address is bridged straight to the upstream gateway
// and the node only ever sees inbound traffic it has DNATed.
func isHostRoutedAddress(internalIP string) bool {
	ip := net.ParseIP(internalIP)
	return ip != nil && ip.IsPrivate()
}

// defaultNATMode picks the safe default for a VM's existing egress identity: a
// VM routed through the node should egress as its floating IP ("full"), but a VM
// bridged with its own public address keeps that identity and only gains inbound
// reachability ("inbound") — full mode cannot be honoured for it at all.
func defaultNATMode(internalIP string) string {
	if isHostRoutedAddress(internalIP) {
		return models.NATModeFull
	}
	return models.NATModeInbound
}
