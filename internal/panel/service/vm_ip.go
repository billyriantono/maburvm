package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ErrVMHasNoSuchInterface is returned when releasing an interface that does not
// belong to the VM named in the request.
var ErrVMHasNoSuchInterface = errors.New("that interface does not belong to this VM")

// SetIPAMService wires address allocation into the network service.
func (s *NetworkService) SetIPAMService(ipam *IPAMService) { s.ipamService = ipam }

// AssignIPAddress gives a running VM an address from a pool.
//
// Until now an address could only be attached while creating a VM: a machine
// imported without one, or one whose address was released, had no route back to
// having a public IP short of a rebuild. The allocation itself is the same
// transaction the create path uses, so an address can never be handed to two
// machines.
//
// poolID may be empty, in which case any pool available on the VM's node is
// tried in turn — an exhausted pool is a reason to try the next one, not to
// fail. requestedIP pins a specific address and requires poolID, because "give
// me 1.2.3.4" is only answerable against the pool that owns it.
//
// What this does NOT do is configure the guest. The host is set up — the address
// is reserved, anti-spoofing and firewall rules follow it — but writing the
// address inside the VM needs cloud-init, which only regenerates on rebuild. For
// an imported machine the owner has to set it themselves, and the UI says so
// rather than leaving them to discover it.
func (s *NetworkService) AssignIPAddress(ctx context.Context, vmID, poolID, requestedIP string) (*models.Network, error) {
	if s.ipamService == nil {
		return nil, fmt.Errorf("address allocation is not configured")
	}
	if requestedIP != "" && poolID == "" {
		return nil, fmt.Errorf("pool_id is required when requesting a specific address")
	}

	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("VM not found")
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	candidates := []string{poolID}
	if poolID == "" {
		pools, perr := s.ipamService.ListPoolsForNode(ctx, vm.NodeID)
		if perr != nil {
			return nil, fmt.Errorf("failed to list pools for node: %w", perr)
		}
		candidates = candidates[:0]
		for i := range pools {
			candidates = append(candidates, pools[i].ID)
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no IP pool is available on this VM's node")
		}
	}

	var created *models.Network
	var allocated *models.IPAddress
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nodeID := vm.NodeID
		var lastErr error
		for _, pid := range candidates {
			a, aerr := s.ipamService.AllocateAddressInTx(ctx, tx, &AllocateIPAddressRequest{
				PoolID:      pid,
				NodeID:      &nodeID,
				VMID:        &vm.ID,
				RequestedIP: requestedIP,
			})
			if aerr == nil {
				allocated = a
				break
			}
			lastErr = aerr
			// In auto mode an exhausted or ineligible pool just means "try the
			// next"; with an explicit pool the caller asked for that one, so its
			// error is the answer.
			if poolID == "" && (errors.Is(aerr, ErrNoAvailableIPAddress) || errors.Is(aerr, ErrPoolNotAvailableOnNode)) {
				continue
			}
			return aerr
		}
		if allocated == nil {
			if poolID == "" {
				return fmt.Errorf("no address available in any pool on this VM's node")
			}
			return lastErr
		}

		network := &models.Network{VMID: vm.ID, IPAddress: allocated.Address}
		if err := s.networkRepo.WithDB(tx).Create(ctx, network); err != nil {
			return fmt.Errorf("failed to record the interface: %w", err)
		}
		created = network
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Push the host-side configuration. Failing here leaves the allocation in
	// place on purpose: the address is genuinely reserved, and the agent
	// re-applies full desired state when it next reconnects, so the right
	// recovery is to retry rather than to hand the address back.
	// The guest must learn the new address too, not just the host rules; the
	// pool supplies the prefix/gateway it needs.
	var pool models.IPPool
	if err := s.db.WithContext(ctx).Where("id = ?", allocated.PoolID).First(&pool).Error; err != nil {
		return created, fmt.Errorf("address assigned but its pool could not be loaded: %w", err)
	}
	if cerr := s.enqueueNetworkConfig(ctx, vm, created, &pool); cerr != nil {
		return created, fmt.Errorf("address assigned but node configuration could not be queued: %w", cerr)
	}
	return created, nil
}

// AssignIPv6Prefix delegates the next free prefix of pool poolID to the VM and
// pushes it to the node and into the guest. The VM must already have an IPv4
// interface: the prefix travels on that interface's network-config job, and
// the guest reconfigure that writes the address is the same one that writes
// the IPv4 config.
func (s *NetworkService) AssignIPv6Prefix(ctx context.Context, vmID, poolID string) (*models.VMIPv6Prefix, error) {
	if s.ipamService == nil {
		return nil, fmt.Errorf("address allocation is not configured")
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("VM not found")
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	network, v4Pool, err := s.primaryNetworkWithPool(ctx, vm.ID)
	if err != nil {
		return nil, err
	}

	var created *models.VMIPv6Prefix
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, aerr := s.ipamService.AllocateIPv6PrefixInTx(ctx, tx, poolID, vm.ID, vm.NodeID)
		created = p
		return aerr
	})
	if err != nil {
		return nil, err
	}
	if cerr := s.enqueueNetworkConfig(ctx, vm, network, v4Pool); cerr != nil {
		return created, fmt.Errorf("prefix assigned but node configuration could not be queued: %w", cerr)
	}
	return created, nil
}

// ReleaseIPv6Prefix returns the VM's delegated prefix to its pool and re-pushes
// the remaining config so the host route and guest address go away.
func (s *NetworkService) ReleaseIPv6Prefix(ctx context.Context, vmID string) error {
	if s.ipamService == nil {
		return fmt.Errorf("address allocation is not configured")
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	if err := s.ipamService.ReleaseIPv6PrefixByVMInTx(ctx, s.db, vm.ID); err != nil {
		return err
	}
	network, v4Pool, err := s.primaryNetworkWithPool(ctx, vm.ID)
	if err != nil {
		// Nothing left to configure on the host; the row is gone, which is the
		// release itself.
		return nil
	}
	if cerr := s.enqueueNetworkConfig(ctx, vm, network, v4Pool); cerr != nil {
		return fmt.Errorf("prefix released but node configuration could not be queued: %w", cerr)
	}
	return nil
}

// primaryNetworkWithPool returns the VM's first interface and the IPAM pool
// its address came from — what a guest reconfigure needs (prefix + gateway).
func (s *NetworkService) primaryNetworkWithPool(ctx context.Context, vmID string) (*models.Network, *models.IPPool, error) {
	network, err := s.networkRepo.GetByVMID(ctx, vmID)
	if err != nil {
		return nil, nil, fmt.Errorf("VM has no IPv4 interface to carry the IPv6 prefix on")
	}
	var addr models.IPAddress
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND address = ?", vmID, hostOnlyIP(network.IPAddress)).First(&addr).Error; err != nil {
		return nil, nil, fmt.Errorf("the VM's address %s is not tracked by IPAM", network.IPAddress)
	}
	var pool models.IPPool
	if err := s.db.WithContext(ctx).Where("id = ?", addr.PoolID).First(&pool).Error; err != nil {
		return nil, nil, fmt.Errorf("pool of %s could not be loaded: %w", network.IPAddress, err)
	}
	return network, &pool, nil
}

// ReleaseIPAddress takes an address off a VM and returns it to its pool.
//
// The interface must belong to the VM named in the request — checked rather than
// assumed, because the id comes from the caller and releasing another tenant's
// address would be both a leak and an outage.
func (s *NetworkService) ReleaseIPAddress(ctx context.Context, vmID, networkID string) error {
	if s.ipamService == nil {
		return fmt.Errorf("address allocation is not configured")
	}

	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	var network models.Network
	if err := s.db.WithContext(ctx).
		Where("id = ? AND vm_id = ?", networkID, vmID).
		First(&network).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMHasNoSuchInterface
		}
		return err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if derr := tx.Delete(&models.Network{}, "id = ?", networkID).Error; derr != nil {
			return derr
		}
		// Free the address itself, not just the interface record. Leaving it
		// marked as assigned is how a pool silently runs out while showing free
		// space.
		return tx.Model(&models.IPAddress{}).
			Where("address = ? AND vm_id = ?", network.IPAddress, vmID).
			Updates(map[string]any{
				"status": models.IPAddressStatusAvailable,
				"vm_id":  nil,
			}).Error
	})
	if err != nil {
		return err
	}

	// Re-push whatever the VM has left, so the host stops enforcing rules for an
	// address it no longer owns.
	if rerr := s.enqueueNetworkResync(ctx, vm); rerr != nil {
		return fmt.Errorf("address released but node configuration could not be queued: %w", rerr)
	}
	return nil
}
