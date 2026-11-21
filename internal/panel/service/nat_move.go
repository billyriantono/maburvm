package service

import (
	"context"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// A NAT VM's public ports live on its node's address, so a move gives it a
// block on the destination: the same ports when they are free there, else the
// lowest free block of the same size, with its port forwards shifted along.
// Its private address goes with it only where its NAT pool reaches (checked
// with the other address pools). Its owner is told the new address and ports.

// natMove is where a NAT VM's ports go on another node.
type natMove struct {
	Block    models.NATPortBlock
	Start    int
	Address  string
	SamePort bool
}

// End is the last port on the destination.
func (m *natMove) End() int { return m.Start + m.Block.PortCount - 1 }

// planNATMove finds the VM's block on dstID; nil for a VM that is not NAT.
func planNATMove(ctx context.Context, db *gorm.DB, vmID, dstID string) (*natMove, error) {
	b, err := natBlock(ctx, db, vmID)
	if err != nil || b == nil {
		return nil, err
	}
	var dst struct {
		NATPortStart, NATPortEnd int
		NATAddress               string
	}
	if err := db.WithContext(ctx).Table("nodes").Select("nat_port_start, nat_port_end, COALESCE(nat_address, '') AS nat_address").
		Where("id = ?", dstID).Scan(&dst).Error; err != nil {
		return nil, fmt.Errorf("failed to read the node's NAT port range: %w", err)
	}
	if dst.NATPortStart <= 0 || dst.NATPortEnd < dst.NATPortStart {
		return nil, fmt.Errorf("%w: the node offers no NAT ports; set its NAT port range on its page", ErrNATPortsExhausted)
	}
	var taken [][2]int
	var blocks []models.NATPortBlock
	if err := db.WithContext(ctx).Where("node_id = ? AND vm_id <> ?", dstID, vmID).Find(&blocks).Error; err != nil {
		return nil, err
	}
	for _, o := range blocks {
		taken = append(taken, [2]int{o.StartPort, o.EndPort()})
	}
	var forwarded []int
	if err := db.WithContext(ctx).Model(&models.PortForward{}).Where("node_id = ? AND vm_id <> ?", dstID, vmID).
		Pluck("external_port", &forwarded).Error; err != nil {
		return nil, err
	}
	for _, p := range forwarded {
		taken = append(taken, [2]int{p, p})
	}
	m := &natMove{Block: *b, Address: hostOnlyIP(dst.NATAddress)}
	// The same ports, when the destination's range holds them and nobody there has them.
	if b.StartPort >= dst.NATPortStart && b.EndPort() <= dst.NATPortEnd {
		if start, ok := FindPortBlock(b.StartPort, b.EndPort(), b.PortCount, taken); ok && start == b.StartPort {
			m.Start, m.SamePort = start, true
			return m, nil
		}
	}
	start, ok := FindPortBlock(dst.NATPortStart, dst.NATPortEnd, b.PortCount, taken)
	if !ok {
		return nil, fmt.Errorf("%w: the node has no %d free NAT ports in a row", ErrNATPortsExhausted, b.PortCount)
	}
	m.Start = start
	return m, nil
}

// moveNATBlockTx gives the VM its block on dstID and shifts its forwards into
// it. The caller holds the destination's port lock.
func moveNATBlockTx(ctx context.Context, tx *gorm.DB, vmID, dstID string) (*natMove, error) {
	m, err := planNATMove(ctx, tx, vmID, dstID)
	if err != nil || m == nil {
		return m, err
	}
	shift := m.Start - m.Block.StartPort
	if err := tx.WithContext(ctx).Model(&models.NATPortBlock{}).Where("id = ?", m.Block.ID).
		Updates(map[string]any{"node_id": dstID, "start_port": m.Start}).Error; err != nil {
		return nil, fmt.Errorf("failed to move the VM's NAT ports: %w", err)
	}
	// Node and port in one statement: no row ever takes a port another holds.
	if err := tx.WithContext(ctx).Model(&models.PortForward{}).
		Where("vm_id = ? AND external_port BETWEEN ? AND ?", vmID, m.Block.StartPort, m.Block.EndPort()).
		Updates(map[string]any{"node_id": dstID, "external_port": gorm.Expr("external_port + ?", shift)}).Error; err != nil {
		return nil, fmt.Errorf("failed to move the VM's port forwards: %w", err)
	}
	return m, nil
}

// natMoveChecks adds what a NAT VM's move to dst would do, and what stops it.
func (m *MigrationService) natMoveChecks(ctx context.Context, vm *models.VM, dst *models.Node, chk *MigrationCheck) {
	plan, err := planNATMove(ctx, m.db, vm.ID, dst.ID)
	switch {
	case err != nil:
		chk.block("node %s cannot take the VM's public ports: %v", dst.Name, err)
		return
	case plan == nil:
		return
	}
	address := plan.Address
	if address == "" {
		address = "its address"
	}
	if plan.SamePort {
		chk.warn("the VM keeps public ports %d-%d, which then answer on node %s's address %s instead of %s; its owner is told",
			plan.Start, plan.End(), dst.Name, address, m.natAddress(ctx, vm.NodeID))
	} else {
		chk.warn("the VM's public ports change from %d-%d to %d-%d on node %s's address %s; its port forwards move with them and its owner is told",
			plan.Block.StartPort, plan.Block.EndPort(), plan.Start, plan.End(), dst.Name, address)
	}
	var hostnames int64
	m.db.WithContext(ctx).Model(&models.VMHostname{}).Where("vm_id = ? AND verified_at IS NOT NULL", vm.ID).Count(&hostnames)
	if hostnames == 0 {
		return
	}
	if !m.nodeHasFeature(ctx, dst, "hostname_proxy") {
		chk.block("node %s needs an agent update before it can route the VM's %d hostnames", dst.Name, hostnames)
	}
	var web int64
	m.db.WithContext(ctx).Model(&models.PortForward{}).Where("node_id = ? AND vm_id <> ? AND external_port IN ?", dst.ID, vm.ID, []int{80, 443}).Count(&web)
	if web > 0 {
		chk.block("node %s forwards port 80 or 443 to a VM, so it cannot route the VM's hostnames", dst.Name)
	}
	chk.warn("the VM's %d hostnames are routed by node %s from then on: their DNS must point at %s", hostnames, dst.Name, address)
}

func (m *MigrationService) natAddress(ctx context.Context, nodeID string) string {
	var a string
	m.db.WithContext(ctx).Table("nodes").Select("COALESCE(nat_address, '')").Where("id = ?", nodeID).Scan(&a)
	if a == "" {
		return "the old one"
	}
	return hostOnlyIP(a)
}
