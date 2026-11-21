package service

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// nicAgent keeps a VM's cards the way a node would.
type nicAgent struct {
	pb.UnimplementedNodeAgentServer
	mu       sync.Mutex
	nics     []*pb.VMNIC
	features []string
	calls    []*pb.ConfigureNICRequest
}

func (a *nicAgent) ConfigureNIC(_ context.Context, req *pb.ConfigureNICRequest) (*pb.ConfigureNICResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, req)
	resp := &pb.ConfigureNICResponse{Success: true}
	switch req.Action {
	case pb.NICAction_NIC_ACTION_ATTACH:
		a.nics = append(a.nics, &pb.VMNIC{MacAddress: req.MacAddress, Bridge: req.Bridge})
		resp.Note = "The card is in place for the VM's next start."
	case pb.NICAction_NIC_ACTION_DETACH:
		for i, n := range a.nics {
			if n.MacAddress == req.MacAddress {
				a.nics = append(a.nics[:i], a.nics[i+1:]...)
				break
			}
		}
	case pb.NICAction_NIC_ACTION_SET_MAC:
		for _, n := range a.nics {
			if n.MacAddress == req.MacAddress {
				n.MacAddress = req.NewMacAddress
			}
		}
	}
	resp.Nics = append(resp.Nics, a.nics...)
	return resp, nil
}

func (a *nicAgent) GetNodeInfo(context.Context, *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	return &pb.GetNodeInfoResponse{Success: true, Features: a.features}, nil
}

type nicFixture struct {
	db      *gorm.DB
	svc     *NetworkService
	agent   *nicAgent
	vm      *models.VM
	node    *models.Node
	public  *models.IPPool
	private *models.IPPool
}

func newNICFixture(t *testing.T) *nicFixture {
	t.Helper()
	db := setupVMIPAMTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.VMNIC{}))
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	public := &models.IPPool{Name: "public", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "br0", Gateway: "203.0.113.1"}
	private := &models.IPPool{Name: "vpc-pool", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "10.20.0.0/24", Bridge: "vpcbr1", Gateway: "10.20.0.1"}
	require.NoError(t, db.Create(public).Error)
	require.NoError(t, db.Create(private).Error)
	for _, a := range []*models.IPAddress{
		{PoolID: public.ID, Address: "203.0.113.10", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable},
		{PoolID: private.ID, Address: "10.20.0.5", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable},
	} {
		require.NoError(t, db.Create(a).Error)
	}
	vmSvc := NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := vmSvc.CreateVM(context.Background(), &CreateVMRequest{
		UserID: user.ID.String(), Hostname: "nic.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, NodeID: node.ID, IPPoolID: public.ID,
	})
	require.NoError(t, err)

	agent := &nicAgent{features: []string{models.FeatureVMNICs}}
	svc := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	svc.SetIPAMService(NewIPAMService(db, repository.NewIPAMRepository(db)))
	svc.SetAgentDialer(dialerFor(map[string]pb.NodeAgentClient{node.ID: agentClient(t, agent)}))
	return &nicFixture{db: db, svc: svc, agent: agent, vm: resp.VM, node: node, public: public, private: private}
}

// A new VM's first card gets the MAC worked out from its id, kept on record;
// a MAC another card has is refused before anything is allocated.
func TestCreateVMRecordsTheFirstCardsMAC(t *testing.T) {
	f := newNICFixture(t)
	var nic models.VMNIC
	require.NoError(t, f.db.Where("vm_id = ?", f.vm.ID).First(&nic).Error)
	require.Equal(t, models.DerivedMAC(f.vm.ID, 0), nic.MACAddress)
	require.Equal(t, 0, nic.Position)
	require.Equal(t, "br0", nic.Bridge)

	_, err := CheckCreateMAC(context.Background(), f.db, strings.ToUpper(nic.MACAddress))
	require.ErrorIs(t, err, ErrMACInUse)
	_, err = CheckCreateMAC(context.Background(), f.db, "01:00:5e:00:00:01")
	require.ErrorIs(t, err, ErrInvalidMAC)
}

func TestAddAndRemoveASecondCard(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	f.agent.nics = []*pb.VMNIC{{MacAddress: models.DerivedMAC(f.vm.ID, 0), Bridge: "br0"}}

	_, _, err := f.svc.AddNIC(ctx, f.vm.ID, f.public.ID, "")
	require.ErrorIs(t, err, ErrNICSameBridge, "one card per network")

	nic, _, err := f.svc.AddNIC(ctx, f.vm.ID, f.private.ID, "52:54:00:aa:bb:cc")
	require.NoError(t, err)
	require.Equal(t, 1, nic.Position)
	require.Equal(t, "vpcbr1", nic.Bridge)
	require.Equal(t, []string{"10.20.0.5"}, nic.Addresses)
	attach := f.agent.calls[len(f.agent.calls)-1]
	require.Equal(t, pb.NICAction_NIC_ACTION_ATTACH, attach.Action)
	require.True(t, attach.AntiSpoofing)
	require.Equal(t, "10.20.0.5", attach.IpAddress, "the card's filter is pinned to its address")

	list, err := f.svc.ListNICs(ctx, f.vm.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, []string{"203.0.113.10"}, list[0].Addresses)
	require.Equal(t, []string{"10.20.0.5"}, list[1].Addresses)

	_, err = f.svc.RemoveNIC(ctx, f.vm.ID, list[0].ID)
	require.ErrorIs(t, err, ErrNICFirst)
	_, err = f.svc.RemoveNIC(ctx, f.vm.ID, nic.ID)
	require.NoError(t, err)
	var addr models.IPAddress
	require.NoError(t, f.db.Where("address = ?", "10.20.0.5").First(&addr).Error)
	require.Equal(t, models.IPAddressStatusAvailable, addr.Status, "the card's address goes back to its pool")
	require.Nil(t, addr.VMID)
	var left int64
	require.NoError(t, f.db.Model(&models.VMNIC{}).Where("vm_id = ?", f.vm.ID).Count(&left).Error)
	require.EqualValues(t, 1, left)
}

// When the node refuses the card, the address is not kept.
func TestAddCardGivesTheAddressBackWhenTheNodeRefuses(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	f.svc.SetAgentDialer(dialerFor(map[string]pb.NodeAgentClient{f.node.ID: agentClient(t, &pb.UnimplementedNodeAgentServer{})}))
	_, _, err := f.svc.AddNIC(ctx, f.vm.ID, f.private.ID, "")
	require.ErrorIs(t, err, ErrNICsUnsupported)
	// A VM whose cards were never read says the same, not "try again".
	require.NoError(t, f.db.Where("vm_id = ?", f.vm.ID).Delete(&models.VMNIC{}).Error)
	_, _, err = f.svc.AddNIC(ctx, f.vm.ID, f.private.ID, "")
	require.ErrorIs(t, err, ErrNICsUnsupported)
	var addr models.IPAddress
	require.NoError(t, f.db.Where("address = ?", "10.20.0.5").First(&addr).Error)
	require.Equal(t, models.IPAddressStatusAvailable, addr.Status)
	var cards int64
	require.NoError(t, f.db.Model(&models.VMNIC{}).Where("vm_id = ? AND position > 0", f.vm.ID).Count(&cards).Error)
	require.Zero(t, cards)
}

// Staff naming the pool of another customer's private network do not put a
// VM on it.
func TestAddCardRefusesAnotherCustomersVPC(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	require.NoError(t, f.db.Exec(`CREATE TABLE IF NOT EXISTS managed_networks (id TEXT PRIMARY KEY, name TEXT, type TEXT, bridge TEXT,
		subnet TEXT, gateway TEXT, dhcp_start TEXT, dhcp_end TEXT, vlan_id INTEGER DEFAULT 0, node_id TEXT, user_id TEXT,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO managed_networks (id, name, type, bridge, user_id) VALUES ('vpc-1', 'theirs', 'vpc', 'vpcbr1', 'someone-else')`).Error)
	f.agent.nics = []*pb.VMNIC{{MacAddress: models.DerivedMAC(f.vm.ID, 0), Bridge: "br0"}}
	_, _, err := f.svc.AddNIC(ctx, f.vm.ID, f.private.ID, "")
	require.ErrorIs(t, err, ErrVPCNotFound)

	require.NoError(t, f.db.Exec(`UPDATE managed_networks SET user_id = ? WHERE id = 'vpc-1'`, f.vm.UserID).Error)
	_, _, err = f.svc.AddNIC(ctx, f.vm.ID, f.private.ID, "")
	require.NoError(t, err, "the VM owner's own private network is fine")
}

// A VM without cards on record has them read from its node and kept.
func TestListNICsAdoptsTheCardsANodeReports(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	require.NoError(t, f.db.Where("vm_id = ?", f.vm.ID).Delete(&models.VMNIC{}).Error)
	f.agent.nics = []*pb.VMNIC{{MacAddress: "52:54:00:12:34:56", Bridge: "br0"}}
	list, err := f.svc.ListNICs(ctx, f.vm.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "52:54:00:12:34:56", list[0].MACAddress)
	var kept models.VMNIC
	require.NoError(t, f.db.Where("vm_id = ?", f.vm.ID).First(&kept).Error)
	require.Equal(t, "52:54:00:12:34:56", kept.MACAddress)
}

func TestSetNICMACNeedsAStoppedVMAndAFreeMAC(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	var nic models.VMNIC
	require.NoError(t, f.db.Where("vm_id = ?", f.vm.ID).First(&nic).Error)
	f.agent.nics = []*pb.VMNIC{{MacAddress: nic.MACAddress, Bridge: "br0"}}

	require.NoError(t, f.db.Model(&models.VM{}).Where("id = ?", f.vm.ID).Update("status", models.VMStatusRunning).Error)
	_, err := f.svc.SetNICMAC(ctx, f.vm.ID, nic.ID, "52:54:00:00:00:99")
	require.ErrorIs(t, err, ErrNICNeedsStopped)

	require.NoError(t, f.db.Model(&models.VM{}).Where("id = ?", f.vm.ID).Update("status", models.VMStatusStopped).Error)
	_, err = f.svc.SetNICMAC(ctx, f.vm.ID, nic.ID, "ff:ff:ff:ff:ff:ff")
	require.ErrorIs(t, err, ErrInvalidMAC)
	got, err := f.svc.SetNICMAC(ctx, f.vm.ID, nic.ID, "52:54:00:00:00:99")
	require.NoError(t, err)
	require.Equal(t, "52:54:00:00:00:99", got.MACAddress)
	require.Equal(t, "52:54:00:00:00:99", f.agent.nics[0].MacAddress)
}

// A VM whose anti-spoofing staff turned off (one that routes other
// addresses) gets its added card without the filter too.
func TestAddedCardFollowsTheVMsAntiSpoofing(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	f.agent.nics = []*pb.VMNIC{{MacAddress: models.DerivedMAC(f.vm.ID, 0), Bridge: "br0"}}
	require.NoError(t, f.db.Model(&models.Network{}).Where("vm_id = ?", f.vm.ID).Update("anti_spoofing", false).Error)
	_, _, err := f.svc.AddNIC(ctx, f.vm.ID, f.private.ID, "")
	require.NoError(t, err)
	require.False(t, f.agent.calls[len(f.agent.calls)-1].AntiSpoofing)
	var off int64
	require.NoError(t, f.db.Model(&models.Network{}).Where("vm_id = ? AND anti_spoofing = ?", f.vm.ID, false).Count(&off).Error)
	require.EqualValues(t, 2, off)
}
