package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// A VM's network cards. The first is made with the VM; further ones sit on
// another bridge (a VPC, or a second network) and each holds one address from
// a pool on that bridge, so the node pins it to that card. The MAC of every
// card is kept here so it stays the same through reinstalls and moves, and so
// a MAC is never given to two cards.

var (
	ErrInvalidMAC      = errors.New("invalid MAC address")
	ErrMACInUse        = errors.New("that MAC address is already used by another card")
	ErrNICLimit        = fmt.Errorf("a VM can have at most %d network cards", models.MaxVMNICs)
	ErrNICSameBridge   = errors.New("the VM already has a card on that network")
	ErrNICNotFound     = errors.New("network card not found")
	ErrNICFirst        = errors.New("the VM's first card cannot be removed")
	ErrNICNeedsStopped = errors.New("stop the VM before changing a card's MAC address: the guest's network settings are rewritten for the new one")
	ErrNICsUnsupported = errors.New("the agent on this VM's node is too old to change network cards; update it first")
	ErrNICPoolInvalid  = errors.New("that pool cannot give a card an address: it needs IPv4 addresses, a bridge and to be available on the VM's node")
)

// SetAgentDialer gives the network service the node agents, for changing a
// VM's cards.
func (s *NetworkService) SetAgentDialer(d AgentDialer) { s.agent = d }

// VMNICView is a card as the VM's page shows it.
type VMNICView struct {
	models.VMNIC
	// Addresses are the VM's addresses on the card's bridge.
	Addresses []string `json:"addresses"`
	// Network names what the bridge is: a pool or a private network.
	Network string `json:"network,omitempty"`
}

// deleteVMNICs removes a VM's cards with it, so their MACs can be used again.
func deleteVMNICs(tx *gorm.DB, vmID string) error {
	if !tx.Migrator().HasTable(&models.VMNIC{}) {
		return nil
	}
	return tx.Where("vm_id = ?", vmID).Delete(&models.VMNIC{}).Error
}

// macTaken reports whether another card already has mac.
func macTaken(ctx context.Context, db *gorm.DB, mac, exceptID string) (bool, error) {
	if !db.Migrator().HasTable(&models.VMNIC{}) {
		return false, nil
	}
	q := db.WithContext(ctx).Model(&models.VMNIC{}).Where("lower(mac_address) = ?", strings.ToLower(mac))
	if exceptID != "" {
		q = q.Where("id <> ?", exceptID)
	}
	var n int64
	err := q.Count(&n).Error
	return n > 0, err
}

// pickMAC is want, checked and free, or a derived MAC no card has yet.
func pickMAC(ctx context.Context, db *gorm.DB, vmID string, position int, want string) (string, error) {
	if want != "" {
		mac, err := models.NormalizeMAC(want)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidMAC, err)
		}
		if taken, err := macTaken(ctx, db, mac, ""); err != nil {
			return "", err
		} else if taken {
			return "", ErrMACInUse
		}
		return mac, nil
	}
	for salt := 0; salt < 20; salt++ {
		mac := models.DerivedMAC(vmID, position+salt*models.MaxVMNICs)
		if taken, err := macTaken(ctx, db, mac, ""); err != nil {
			return "", err
		} else if !taken {
			return mac, nil
		}
	}
	return "", fmt.Errorf("no free MAC address could be derived for VM %s", vmID)
}

// CheckCreateMAC validates a MAC asked for at create time before anything is
// allocated.
func CheckCreateMAC(ctx context.Context, db *gorm.DB, want string) (string, error) {
	if want == "" {
		return "", nil
	}
	return pickMAC(ctx, db, "", 0, want)
}

// RecordFirstNIC keeps the MAC a new VM's first card is created with and
// returns it; "" when the database has no card table (an older test schema).
func RecordFirstNIC(ctx context.Context, db *gorm.DB, vmID, bridge, want string) (string, error) {
	if !db.Migrator().HasTable(&models.VMNIC{}) {
		return "", nil
	}
	mac, err := pickMAC(ctx, db, vmID, 0, want)
	if err != nil {
		return "", err
	}
	if err := db.WithContext(ctx).Create(&models.VMNIC{VMID: vmID, Position: 0, Bridge: bridge, MACAddress: mac}).Error; err != nil {
		if isUniqueViolation(err) {
			return "", ErrMACInUse
		}
		return "", err
	}
	return mac, nil
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}

// configureNIC calls the VM's agent. An agent without the call says so.
func (s *NetworkService) configureNIC(ctx context.Context, vm *models.VM, req *pb.ConfigureNICRequest) (*pb.ConfigureNICResponse, error) {
	if s.agent == nil {
		return nil, fmt.Errorf("node agents are not reachable from here")
	}
	client, actx, err := s.agent(ctx, vm.NodeID)
	if err != nil {
		return nil, fmt.Errorf("the VM's node cannot be reached: %w", err)
	}
	cctx, cancel := context.WithTimeout(actx, 90*time.Second)
	defer cancel()
	req.VmId = vm.ID
	resp, err := client.ConfigureNIC(cctx, req)
	if status.Code(err) == codes.Unimplemented {
		return nil, ErrNICsUnsupported
	}
	if err != nil {
		return nil, fmt.Errorf("the VM's node did not answer: %w", err)
	}
	if !resp.Success {
		if resp.GetError().GetCode() == pb.ErrorCode_ERROR_CODE_NOT_FOUND {
			return resp, fmt.Errorf("%w: %s", ErrNICNotFound, resp.GetError().GetMessage())
		}
		return resp, errors.New(resp.GetError().GetMessage())
	}
	return resp, nil
}

// ListNICs returns a VM's cards. A VM with none on record (made before cards
// were kept, or imported) has them read from its node and kept, so the panel
// knows their real MACs from then on.
func (s *NetworkService) ListNICs(ctx context.Context, vmID string) ([]VMNICView, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil, ErrVMNotFound
	}
	nics, err := s.recordedNICs(ctx, vm.ID)
	if err != nil {
		return nil, err
	}
	if len(nics) == 0 {
		nics, _ = s.adoptNICs(ctx, vm)
	}
	addrs := s.vmIPBridges(ctx, vm.ID)
	out := make([]VMNICView, 0, len(nics))
	for i, n := range nics {
		v := VMNICView{VMNIC: n, Addresses: []string{}}
		for ip, bridge := range addrs {
			if bridge == n.Bridge || (i == 0 && cardIndex(nics, bridge) < 0) {
				v.Addresses = append(v.Addresses, ip)
			}
		}
		sort.Strings(v.Addresses)
		v.Network = s.bridgeName(ctx, n.Bridge)
		out = append(out, v)
	}
	return out, nil
}

func cardIndex(nics []models.VMNIC, bridge string) int {
	for i, n := range nics {
		if n.Bridge == bridge {
			return i
		}
	}
	return -1
}

func (s *NetworkService) recordedNICs(ctx context.Context, vmID string) ([]models.VMNIC, error) {
	var nics []models.VMNIC
	if !s.db.Migrator().HasTable(&models.VMNIC{}) {
		return nil, nil
	}
	err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("position").Find(&nics).Error
	return nics, err
}

// adoptNICs reads a VM's cards from its node and keeps them. A MAC another
// card already has on record is shown but not kept.
func (s *NetworkService) adoptNICs(ctx context.Context, vm *models.VM) ([]models.VMNIC, error) {
	resp, err := s.configureNIC(ctx, vm, &pb.ConfigureNICRequest{Action: pb.NICAction_NIC_ACTION_LIST})
	if err != nil {
		return nil, err
	}
	var out []models.VMNIC
	for i, n := range resp.GetNics() {
		nic := models.VMNIC{VMID: vm.ID, Position: i, Bridge: n.GetBridge(), MACAddress: strings.ToLower(n.GetMacAddress())}
		if taken, terr := macTaken(ctx, s.db, nic.MACAddress, ""); terr == nil && !taken {
			if cerr := s.db.WithContext(ctx).Create(&nic).Error; cerr != nil {
				nic.ID = ""
			}
		}
		out = append(out, nic)
	}
	return out, nil
}

// bridgeName says what a bridge is in the panel's terms.
func (s *NetworkService) bridgeName(ctx context.Context, bridge string) string {
	if bridge == "" {
		return ""
	}
	var mn models.ManagedNetwork
	if err := s.db.WithContext(ctx).Where("bridge = ?", bridge).First(&mn).Error; err == nil {
		return mn.Name
	}
	var pool models.IPPool
	if err := s.db.WithContext(ctx).Where("bridge = ?", bridge).First(&pool).Error; err == nil {
		return pool.Name
	}
	return ""
}

// AddNIC gives a VM another card on the bridge of poolID, with an address
// from that pool, and returns it with a note on what is left to happen (the
// card may wait for the VM's next start). Only staff choose mac; a private
// pool's address does not count toward the plan's extra IPv4 addresses.
func (s *NetworkService) AddNIC(ctx context.Context, vmID, poolID, mac string) (*VMNICView, string, error) {
	if s.ipamService == nil {
		return nil, "", fmt.Errorf("address allocation is not configured")
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil, "", ErrVMNotFound
	}
	if VMMigrating(ctx, s.db, vm.ID) {
		return nil, "", ErrVMMigrating
	}
	var pool models.IPPool
	if err := s.db.WithContext(ctx).First(&pool, "id = ?", poolID).Error; err != nil {
		return nil, "", ErrIPPoolNotFound
	}
	if pool.Family != models.IPFamilyIPv4 || pool.Bridge == "" || pool.IsDelegated() || !s.poolOnNode(ctx, &pool, vm.NodeID) {
		return nil, "", ErrNICPoolInvalid
	}
	// A customer's private network takes only that customer's VMs.
	var vpc models.ManagedNetwork
	if err := s.db.WithContext(ctx).Where("bridge = ? AND type = ?", pool.Bridge, NetworkTypeVPC).Take(&vpc).Error; err == nil &&
		vpc.UserID != nil && *vpc.UserID != vm.UserID {
		return nil, "", ErrVPCNotFound
	}
	nics, err := s.recordedNICs(ctx, vm.ID)
	if err != nil {
		return nil, "", err
	}
	if len(nics) == 0 {
		if nics, err = s.adoptNICs(ctx, vm); errors.Is(err, ErrNICsUnsupported) {
			return nil, "", err
		} else if len(nics) == 0 {
			return nil, "", fmt.Errorf("the VM's cards could not be read from its node; try again when the node answers")
		}
	}
	if len(nics) >= models.MaxVMNICs {
		return nil, "", ErrNICLimit
	}
	if cardIndex(nics, pool.Bridge) >= 0 {
		return nil, "", ErrNICSameBridge
	}
	position := nics[len(nics)-1].Position + 1
	mac, err = pickMAC(ctx, s.db, vm.ID, position, mac)
	if err != nil {
		return nil, "", err
	}
	if !privatePool(&pool) {
		have, extra, err := vmIPv4Counts(ctx, s.db, vm.ID)
		if err != nil {
			return nil, "", err
		}
		if have > 0 {
			if err := checkVMAllowance(ctx, s.db, vm, extra, func(p *models.Plan) *int { return p.MaxExtraIPv4 },
				"extra IPv4 address", "extra IPv4 addresses", "Move the VM to a plan with more, or raise the plan's allowance."); err != nil {
				return nil, "", err
			}
		}
	}

	// The card is protected like the VM's other addresses: staff may have
	// turned anti-spoofing off for a VM that routes other addresses.
	antiSpoof := true
	var off int64
	if err := s.db.WithContext(ctx).Model(&models.Network{}).Where("vm_id = ? AND anti_spoofing = ?", vm.ID, false).Count(&off).Error; err == nil && off > 0 {
		var on int64
		_ = s.db.WithContext(ctx).Model(&models.Network{}).Where("vm_id = ? AND anti_spoofing = ?", vm.ID, true).Count(&on).Error
		antiSpoof = on > 0
	}
	nic := &models.VMNIC{VMID: vm.ID, Position: position, Bridge: pool.Bridge, MACAddress: mac}
	var addr *models.IPAddress
	var network *models.Network
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		a, aerr := s.ipamService.AllocateAddressInTx(ctx, tx, &AllocateIPAddressRequest{PoolID: pool.ID, NodeID: &vm.NodeID, VMID: &vm.ID})
		if aerr != nil {
			return aerr
		}
		addr = a
		network = &models.Network{VMID: vm.ID, IPAddress: a.Address, AntiSpoofing: antiSpoof}
		if err := s.networkRepo.WithDB(tx).Create(ctx, network); err != nil {
			return fmt.Errorf("failed to record the address: %w", err)
		}
		if !antiSpoof { // a false bool is otherwise left to the column's default (on)
			if err := tx.Model(network).Update("anti_spoofing", false).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(nic).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrMACInUse
			}
			return err
		}
		return nil
	}); err != nil {
		return nil, "", err
	}

	resp, err := s.configureNIC(ctx, vm, &pb.ConfigureNICRequest{
		Action: pb.NICAction_NIC_ACTION_ATTACH, MacAddress: mac, Bridge: pool.Bridge,
		AntiSpoofing: antiSpoof, IpAddress: hostOnlyIP(addr.Address),
	})
	if err != nil {
		// Nothing reached the VM: give the address back.
		_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if derr := tx.Delete(&models.VMNIC{}, "id = ?", nic.ID).Error; derr != nil {
				return derr
			}
			if derr := tx.Delete(&models.Network{}, "id = ?", network.ID).Error; derr != nil {
				return derr
			}
			return tx.Model(&models.IPAddress{}).Where("id = ?", addr.ID).
				Updates(map[string]any{"status": models.IPAddressStatusAvailable, "vm_id": nil}).Error
		})
		return nil, "", err
	}
	note := resp.GetNote()
	if qerr := s.pushAddresses(ctx, vm); qerr != nil {
		note = strings.TrimSpace(note + " The card was added, but its address could not be sent to the node yet: " + qerr.Error())
	}
	view := &VMNICView{VMNIC: *nic, Addresses: []string{hostOnlyIP(addr.Address)}, Network: s.bridgeName(ctx, pool.Bridge)}
	return view, note, nil
}

// RemoveNIC takes a further card off a VM and gives its addresses back.
func (s *NetworkService) RemoveNIC(ctx context.Context, vmID, nicID string) (string, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return "", ErrVMNotFound
	}
	if VMMigrating(ctx, s.db, vm.ID) {
		return "", ErrVMMigrating
	}
	var nic models.VMNIC
	if err := s.db.WithContext(ctx).Where("id = ? AND vm_id = ?", nicID, vm.ID).First(&nic).Error; err != nil {
		return "", ErrNICNotFound
	}
	if nic.Position == 0 {
		return "", ErrNICFirst
	}
	resp, err := s.configureNIC(ctx, vm, &pb.ConfigureNICRequest{Action: pb.NICAction_NIC_ACTION_DETACH, MacAddress: nic.MACAddress})
	if err != nil && !errors.Is(err, ErrNICNotFound) {
		return "", err
	}
	if err == nil {
		// The card's bytes leave the VM's traffic counters with it.
		_ = repository.NewBandwidthUsageRepository(s.db).ForgetCounters(ctx, vm.ID)
	}
	// The card's addresses go back to their pools.
	held := s.vmIPBridges(ctx, vm.ID)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for ip, bridge := range held {
			if bridge != nic.Bridge {
				continue
			}
			if derr := tx.Where("vm_id = ? AND ip_address = ?", vm.ID, ip).Delete(&models.Network{}).Error; derr != nil {
				return derr
			}
			if derr := tx.Model(&models.IPAddress{}).Where("vm_id = ? AND address = ?", vm.ID, ip).
				Updates(map[string]any{"status": models.IPAddressStatusAvailable, "vm_id": nil}).Error; derr != nil {
				return derr
			}
		}
		return tx.Delete(&models.VMNIC{}, "id = ?", nic.ID).Error
	}); err != nil {
		return "", err
	}
	note := resp.GetNote()
	if qerr := s.pushAddresses(ctx, vm); qerr != nil {
		note = strings.TrimSpace(note + " The node could not be told yet: " + qerr.Error())
	}
	return note, nil
}

// SetNICMAC gives a card of a stopped VM another MAC, and has the guest's
// network settings rewritten for it before the VM next starts.
func (s *NetworkService) SetNICMAC(ctx context.Context, vmID, nicID, mac string) (*models.VMNIC, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil, ErrVMNotFound
	}
	if vm.Status != models.VMStatusStopped {
		return nil, ErrNICNeedsStopped
	}
	if VMMigrating(ctx, s.db, vm.ID) {
		return nil, ErrVMMigrating
	}
	var nic models.VMNIC
	if err := s.db.WithContext(ctx).Where("id = ? AND vm_id = ?", nicID, vm.ID).First(&nic).Error; err != nil {
		return nil, ErrNICNotFound
	}
	norm, err := models.NormalizeMAC(mac)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMAC, err)
	}
	if norm == strings.ToLower(nic.MACAddress) {
		return &nic, nil
	}
	if taken, err := macTaken(ctx, s.db, norm, nic.ID); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrMACInUse
	}
	if _, err := s.configureNIC(ctx, vm, &pb.ConfigureNICRequest{Action: pb.NICAction_NIC_ACTION_SET_MAC, MacAddress: nic.MACAddress, NewMacAddress: norm}); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&nic).Updates(map[string]any{"mac_address": norm, "updated_at": time.Now()}).Error; err != nil {
		return nil, err
	}
	nic.MACAddress = norm
	// The guest's settings name the card by its MAC.
	_ = s.pushAddresses(ctx, vm)
	return &nic, nil
}

// pushAddresses sends the VM's addresses to its node with the guest
// reconfigured, the way an address change does.
func (s *NetworkService) pushAddresses(ctx context.Context, vm *models.VM) error {
	if primary, pool, err := s.primaryNetworkWithPool(ctx, vm.ID); err == nil {
		return s.enqueueNetworkConfig(ctx, vm, primary, pool)
	}
	return s.enqueueNetworkResync(ctx, vm)
}

// poolOnNode reports whether a pool hands out addresses on the node.
func (s *NetworkService) poolOnNode(ctx context.Context, pool *models.IPPool, nodeID string) bool {
	pools, err := s.ipamService.ListPoolsForNode(ctx, nodeID)
	if err != nil {
		return false
	}
	for _, p := range pools {
		if p.ID == pool.ID {
			return true
		}
	}
	return false
}

// privatePool is a pool of private addresses (a VPC's, or a NAT pool): its
// addresses are not public ones a plan counts.
func privatePool(p *models.IPPool) bool {
	ip, _, err := net.ParseCIDR(p.CIDR)
	return p.NAT || (err == nil && ip.IsPrivate())
}

// AddVPCNIC is AddNIC for a customer's own VPC: the card goes on the VPC's
// bridge with an address from its pool. The VPC must be the owner's and on
// the VM's node.
func (s *NetworkService) AddVPCNIC(ctx context.Context, vm *models.VM, ownerID, vpcID string) (*VMNICView, string, error) {
	var mn models.ManagedNetwork
	if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ? AND type = ?", vpcID, ownerID, NetworkTypeVPC).First(&mn).Error; err != nil {
		return nil, "", ErrVPCNotFound
	}
	if mn.NodeID == nil || *mn.NodeID != vm.NodeID || mn.Bridge == "" {
		return nil, "", fmt.Errorf("that private network is in another location; a VM joins only one on its own node")
	}
	var pool models.IPPool
	if err := s.db.WithContext(ctx).Where("bridge = ?", mn.Bridge).First(&pool).Error; err != nil {
		return nil, "", fmt.Errorf("that private network has no address pool")
	}
	return s.AddNIC(ctx, vm.ID, pool.ID, "")
}

// VMForCard loads the VM a card request is for.
func (s *NetworkService) VMForCard(ctx context.Context, vmID string) (*models.VM, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil, ErrVMNotFound
	}
	return vm, nil
}

// CardOnOwnVPC reports whether a VM's card sits on a VPC of the VM's owner,
// which is the only kind a customer removes themselves.
func (s *NetworkService) CardOnOwnVPC(ctx context.Context, vmID, nicID string) bool {
	var row struct{ N int64 }
	err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) AS n FROM vm_nics c
		JOIN vms v ON CAST(v.id AS TEXT) = c.vm_id
		JOIN managed_networks m ON m.bridge = c.bridge AND m.type = ? AND m.deleted_at IS NULL
			AND CAST(m.user_id AS TEXT) = CAST(v.user_id AS TEXT)
		WHERE CAST(c.id AS TEXT) = ? AND c.vm_id = ? AND c.position > 0`, NetworkTypeVPC, nicID, vmID).Scan(&row).Error
	return err == nil && row.N > 0
}
