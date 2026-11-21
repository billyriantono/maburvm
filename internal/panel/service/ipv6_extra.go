package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// ErrIPv6ExtraUnsupported is a node whose agent cannot route further IPv6
// addresses into a guest.
var ErrIPv6ExtraUnsupported = errors.New("the agent on this VM's node is too old to give a guest more IPv6 addresses; update it first")

// ErrNoRoutedIPv6 is a VM without a routed prefix to take addresses from.
var ErrNoRoutedIPv6 = errors.New("the VM has no routed IPv6 prefix; further addresses come from one")

// VMIPv6Addresses is a VM's routed IPv6 and the further addresses its guest
// holds in it.
type VMIPv6Addresses struct {
	Prefix  string   `json:"prefix"`
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	Extra   []string `json:"extra_addresses"`
	Max     int      `json:"max"`
}

// GetIPv6Addresses returns the VM's routed IPv6 and its further addresses.
func (s *NetworkService) GetIPv6Addresses(ctx context.Context, vmID string) (*VMIPv6Addresses, error) {
	p, err := vmIPv6Prefix(ctx, s.db, vmID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrNoRoutedIPv6
	}
	extra := models.SplitList(p.ExtraAddresses)
	if extra == nil {
		extra = []string{}
	}
	return &VMIPv6Addresses{Prefix: p.Prefix, Address: p.Address(), Gateway: models.IPv6LinkLocalGateway, Extra: extra, Max: models.MaxIPv6ExtraAddresses}, nil
}

// SetIPv6Addresses replaces the further addresses the VM's guest holds in its
// routed prefix (see models.ParseIPv6Extra) and gives them to the guest the
// way an address change does: live where it can be, never by restarting it.
func (s *NetworkService) SetIPv6Addresses(ctx context.Context, vmID string, addrs []string) (*VMIPv6Addresses, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil, ErrVMNotFound
	}
	if VMMigrating(ctx, s.db, vm.ID) {
		return nil, ErrVMMigrating
	}
	p, err := vmIPv6Prefix(ctx, s.db, vm.ID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrNoRoutedIPv6
	}
	list, err := models.ParseIPv6Extra(p.Prefix, addrs)
	if err != nil {
		return nil, errors.Join(ErrInvalidPolicy, err)
	}
	if len(list) > 0 && !s.nodeCan(ctx, vm.NodeID, models.FeatureIPv6Extra) {
		return nil, ErrIPv6ExtraUnsupported
	}
	if err := s.db.WithContext(ctx).Model(&models.VMIPv6Prefix{}).Where("vm_id = ?", vm.ID).
		Update("extra_addresses", strings.Join(list, ",")).Error; err != nil {
		return nil, err
	}
	if err := s.pushAddresses(ctx, vm); err != nil {
		return nil, err
	}
	return s.GetIPv6Addresses(ctx, vm.ID)
}

// nodeCan asks a node's agent whether it has a feature; a node that cannot
// be asked is taken not to.
func (s *NetworkService) nodeCan(ctx context.Context, nodeID, feature string) bool {
	if s.agent == nil {
		return false
	}
	client, actx, err := s.agent(ctx, nodeID)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(actx, 15*time.Second)
	defer cancel()
	features, err := nodeFeatures(cctx, client)
	return err == nil && hasFeature(features, feature)
}
