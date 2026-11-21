package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

var (
	ErrNoNATPool         = errors.New("no NAT address is available in this location: no node here has both a NAT pool with a free address and a NAT ports range")
	ErrNATPortsExhausted = errors.New("the node has no free block of public ports for a NAT VM")
	ErrPortOutsideBlock  = errors.New("that public port is outside this VM's range")
	ErrNotNATVM          = errors.New("this VM has a public address of its own; hostname forwarding is for NAT VMs")
	ErrHostnameTaken     = errors.New("that hostname is already routed to another VM")
	ErrHostnameNotFound  = errors.New("hostname not found")
	ErrInvalidHostname   = errors.New("invalid hostname")
	ErrHostnameUnproven  = errors.New("the hostname could not be verified")
	ErrNeedsAgentUpdate  = errors.New("this VM's node runs an agent older than the panel and cannot route hostnames; staff need to update it under Nodes")
	ErrWebPortsForwarded = errors.New("ports 80 or 443 on this node are forwarded to a VM, so hostnames cannot be routed there")
)

const (
	// DefaultNATPorts is the block a NAT VM gets when no NAT plan says.
	DefaultNATPorts = 20
	// MaxNATPorts bounds a plan's block.
	MaxNATPorts = 1000
	// DefaultNATHostnames is the hostname allowance of a NAT VM on no NAT plan.
	DefaultNATHostnames = 5
	// HostnameVerifyPrefix names the TXT record that proves a hostname.
	HostnameVerifyPrefix = "_maburvm-verify."
	// featureHostnameProxy is the agent feature that routes hostnames.
	featureHostnameProxy = "hostname_proxy"
)

// ReservedHostPort reports a port no forward may take on a node: a low port or
// one the host itself listens on (agent gRPC, libvirt, VNC/SPICE displays,
// libvirt migration).
func ReservedHostPort(p int) bool {
	return p < 1024 || p > 65535 || p == 50051 || p == 16509 || p == 16514 ||
		(p >= 5900 && p <= 6100) || (p >= 49152 && p <= 49215)
}

// FindPortBlock is the lowest start of size contiguous ports in [lo, hi] that
// touch no taken range (inclusive pairs) and no reserved host port.
func FindPortBlock(lo, hi, size int, taken [][2]int) (int, bool) {
	if size < 1 || lo < 1 || hi > 65535 || hi-lo+1 < size {
		return 0, false
	}
	ranges := slices.Clone(taken)
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	start := lo
	for start+size-1 <= hi {
		end := start + size - 1
		moved := false
		for _, r := range ranges {
			if r[1] >= start && r[0] <= end {
				start, moved = r[1]+1, true
				break
			}
		}
		if moved {
			continue
		}
		for p := start; p <= end; p++ {
			if ReservedHostPort(p) {
				start, moved = p+1, true
				break
			}
		}
		if !moved {
			return start, true
		}
	}
	return 0, false
}

// natPorts is the block size for a NAT plan.
func natPorts(plan *models.Plan) int {
	if plan == nil || plan.NATPorts <= 0 {
		return DefaultNATPorts
	}
	return plan.NATPorts
}

// usableNATPools are the NAT pools a VM on nodeID can take an address from: a
// bridge to attach to and a gateway to route through.
func usableNATPools(pools []models.IPPool) []string {
	var out []string
	for i := range pools {
		if pools[i].NAT && pools[i].Family != models.IPFamilyIPv6 && pools[i].Bridge != "" && pools[i].Gateway != "" {
			out = append(out, pools[i].ID)
		}
	}
	return out
}

// allocateNATPortBlockTx gives a VM the lowest free block of size ports in its
// node's range, under a per-node lock so two orders never get the same ports.
func allocateNATPortBlockTx(ctx context.Context, tx *gorm.DB, nodeID, vmID string, size int) (*models.NATPortBlock, error) {
	if err := lockNodePorts(ctx, tx, nodeID); err != nil {
		return nil, err
	}
	var rng struct{ NATPortStart, NATPortEnd int }
	if err := tx.WithContext(ctx).Table("nodes").Select("nat_port_start, nat_port_end").Where("id = ?", nodeID).Scan(&rng).Error; err != nil {
		return nil, fmt.Errorf("failed to read the node's NAT port range: %w", err)
	}
	if rng.NATPortStart <= 0 || rng.NATPortEnd < rng.NATPortStart {
		return nil, fmt.Errorf("%w: it offers none until its NAT ports range is set on its page", ErrNATPortsExhausted)
	}
	var taken [][2]int
	var blocks []models.NATPortBlock
	if err := tx.WithContext(ctx).Where("node_id = ?", nodeID).Find(&blocks).Error; err != nil {
		return nil, err
	}
	for _, b := range blocks {
		taken = append(taken, [2]int{b.StartPort, b.EndPort()})
	}
	var forwarded []int
	if err := tx.WithContext(ctx).Model(&models.PortForward{}).Where("node_id = ?", nodeID).Pluck("external_port", &forwarded).Error; err != nil {
		return nil, err
	}
	for _, p := range forwarded {
		taken = append(taken, [2]int{p, p})
	}
	start, ok := FindPortBlock(rng.NATPortStart, rng.NATPortEnd, size, taken)
	if !ok {
		return nil, ErrNATPortsExhausted
	}
	b := &models.NATPortBlock{VMID: vmID, NodeID: nodeID, StartPort: start, PortCount: size}
	if err := tx.WithContext(ctx).Create(b).Error; err != nil {
		return nil, fmt.Errorf("failed to record the VM's ports: %w", err)
	}
	return b, nil
}

// natBlock is the VM's port block, nil when it is not a NAT VM.
func natBlock(ctx context.Context, db *gorm.DB, vmID string) (*models.NATPortBlock, error) {
	var b models.NATPortBlock
	err := db.WithContext(ctx).Where("vm_id = ?", vmID).First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// remotePort is how a fresh system of this kind is reached.
func remotePort(osType string) int {
	if osType == models.OSTypeWindows {
		return 3389
	}
	return 22
}

var hostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeHostname lower-cases a hostname and checks it is one a customer can
// own: a full DNS name, not an address and not a wildcard.
func NormalizeHostname(h string) (string, error) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" || len(h) > 253 {
		return "", fmt.Errorf("%w: give a hostname such as www.example.com", ErrInvalidHostname)
	}
	if net.ParseIP(h) != nil {
		return "", fmt.Errorf("%w: give a hostname, not an address", ErrInvalidHostname)
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%w: give a full hostname such as www.example.com", ErrInvalidHostname)
	}
	for _, l := range labels {
		if !hostnameLabel.MatchString(l) {
			return "", fmt.Errorf("%w: %q is not a valid part of a hostname", ErrInvalidHostname, l)
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", fmt.Errorf("%w: give a hostname, not an address", ErrInvalidHostname)
	}
	return h, nil
}

// HostResolver is the DNS the panel checks hostnames against.
type HostResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// NATService runs what NAT VMs need beyond the plain network: their public
// address and ports, and hostnames routed to them through the node.
type NATService struct {
	db       *gorm.DB
	net      *NetworkService
	resolver HostResolver
	// features asks a node's agent what it can do.
	features func(ctx context.Context, nodeID string) ([]string, error)
}

// NewNATService builds the service; vms gives it agent connections.
func NewNATService(db *gorm.DB, net *NetworkService, vms *VMService) *NATService {
	s := &NATService{db: db, net: net, resolver: dnsResolver{}}
	if vms != nil {
		s.features = func(ctx context.Context, nodeID string) ([]string, error) {
			client, err := vms.getAgentClient(ctx, nodeID)
			if err != nil {
				return nil, err
			}
			authCtx, err := vms.agentAuthContext(ctx, nodeID)
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(authCtx, 10*time.Second)
			defer cancel()
			info, err := client.GetNodeInfo(ctx, &pb.GetNodeInfoRequest{})
			if status.Code(err) == codes.Unimplemented {
				return nil, nil
			}
			return info.GetFeatures(), err
		}
	}
	return s
}

type dnsResolver struct{}

func (dnsResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}
func (dnsResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// VMNATInfo is what a NAT VM's owner needs to reach it.
type VMNATInfo struct {
	NAT bool `json:"nat"`
	// PublicAddress is the node address the ports answer on; empty while the
	// node has not reported one.
	PublicAddress string `json:"public_address"`
	PortStart     int    `json:"port_start"`
	PortEnd       int    `json:"port_end"`
	// RemotePort is the first port, forwarded to RemoteTarget on the VM.
	RemotePort    int                 `json:"remote_port"`
	RemoteTarget  int                 `json:"remote_target"`
	Hostnames     []models.VMHostname `json:"hostnames"`
	HostnameLimit int                 `json:"hostname_limit"`
	VerifyPrefix  string              `json:"verify_prefix"`
}

// Info returns the VM's NAT details; NAT is false for a VM with its own address.
func (s *NATService) Info(ctx context.Context, vmID string) (*VMNATInfo, error) {
	b, err := natBlock(ctx, s.db, vmID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return &VMNATInfo{Hostnames: []models.VMHostname{}}, nil
	}
	info := &VMNATInfo{NAT: true, PortStart: b.StartPort, PortEnd: b.EndPort(), VerifyPrefix: HostnameVerifyPrefix, Hostnames: []models.VMHostname{}}
	_ = s.db.WithContext(ctx).Table("nodes").Select("nat_address").Where("id = ?", b.NodeID).Scan(&info.PublicAddress).Error
	var fwd models.PortForward
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND external_port = ?", vmID, b.StartPort).First(&fwd).Error; err == nil {
		info.RemotePort, info.RemoteTarget = fwd.ExternalPort, fwd.InternalPort
	}
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("hostname").Find(&info.Hostnames).Error; err != nil {
		return nil, err
	}
	if info.HostnameLimit, err = s.hostnameLimit(ctx, vmID); err != nil {
		return nil, err
	}
	return info, nil
}

// hostnameLimit is how many hostnames the VM's plan lets it route.
func (s *NATService) hostnameLimit(ctx context.Context, vmID string) (int, error) {
	var vm models.VM
	if err := s.db.WithContext(ctx).Where("id = ?", vmID).First(&vm).Error; err != nil {
		return 0, fmt.Errorf("VM not found")
	}
	plan, err := vmPlan(ctx, s.db, &vm)
	if err != nil {
		return 0, err
	}
	if plan == nil || plan.NetworkType != models.PlanNetworkNAT {
		return DefaultNATHostnames, nil
	}
	return plan.NATHostnames, nil
}

// AddHostnameRequest names a hostname and the VM ports its web traffic goes
// to; 0 turns that one off.
type AddHostnameRequest struct {
	Hostname  string `json:"hostname"`
	HTTPPort  *int   `json:"http_port"`
	HTTPSPort *int   `json:"https_port"`
}

func newVerifyToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddHostname records a hostname for a NAT VM. It is routed once verified.
func (s *NATService) AddHostname(ctx context.Context, vmID string, req *AddHostnameRequest) (*models.VMHostname, error) {
	b, err := natBlock(ctx, s.db, vmID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotNATVM
	}
	host, err := NormalizeHostname(req.Hostname)
	if err != nil {
		return nil, err
	}
	httpPort, httpsPort := 80, 443
	if req.HTTPPort != nil {
		httpPort = *req.HTTPPort
	}
	if req.HTTPSPort != nil {
		httpsPort = *req.HTTPSPort
	}
	for _, p := range []int{httpPort, httpsPort} {
		if p < 0 || p > 65535 {
			return nil, fmt.Errorf("%w: ports run from 1 to 65535 (0 turns one off)", ErrInvalidHostname)
		}
	}
	if httpPort == 0 && httpsPort == 0 {
		return nil, fmt.Errorf("%w: forward at least one of the web ports", ErrInvalidHostname)
	}
	limit, err := s.hostnameLimit(ctx, vmID)
	if err != nil {
		return nil, err
	}
	var held int64
	if err := s.db.WithContext(ctx).Model(&models.VMHostname{}).Where("vm_id = ?", vmID).Count(&held).Error; err != nil {
		return nil, err
	}
	if err := allowanceCheck(&limit, held, "hostname", "hostnames", "Remove one before adding another."); err != nil {
		return nil, err
	}
	var dup int64
	if err := s.db.WithContext(ctx).Model(&models.VMHostname{}).
		Where("hostname = ? AND (vm_id = ? OR verified_at IS NOT NULL)", host, vmID).Count(&dup).Error; err != nil {
		return nil, err
	}
	if dup > 0 {
		return nil, ErrHostnameTaken
	}
	if err := s.webPortsFree(ctx, b.NodeID); err != nil {
		return nil, err
	}
	if err := s.nodeRoutesHostnames(ctx, b.NodeID); err != nil {
		return nil, err
	}
	h := &models.VMHostname{VMID: vmID, Hostname: host, HTTPPort: httpPort, HTTPSPort: httpsPort, VerifyToken: newVerifyToken()}
	if err := s.db.WithContext(ctx).Create(h).Error; err != nil {
		return nil, fmt.Errorf("failed to save the hostname: %w", err)
	}
	return h, nil
}

// webPortsFree refuses a node where staff forward 80 or 443: that forward
// would take the traffic before the node's hostname router saw it.
func (s *NATService) webPortsFree(ctx context.Context, nodeID string) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.PortForward{}).
		Where("node_id = ? AND external_port IN ?", nodeID, []int{80, 443}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrWebPortsForwarded
	}
	return nil
}

// nodeRoutesHostnames refuses a node whose agent cannot route hostnames.
func (s *NATService) nodeRoutesHostnames(ctx context.Context, nodeID string) error {
	if s.features == nil {
		return nil
	}
	f, err := s.features(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("the VM's node could not be asked whether it routes hostnames (%v); try again later", err)
	}
	if !slices.Contains(f, featureHostnameProxy) {
		return ErrNeedsAgentUpdate
	}
	return nil
}

func (s *NATService) hostname(ctx context.Context, vmID, id string) (*models.VMHostname, error) {
	var h models.VMHostname
	if err := s.db.WithContext(ctx).Where("id = ? AND vm_id = ?", id, vmID).First(&h).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrHostnameNotFound
		}
		return nil, err
	}
	return &h, nil
}

// VerifyHostname checks that the owner controls the hostname, by its TXT
// record or by it resolving to the node's public address, and routes it. For
// a hostname verified already it sends the VM's routes to the node again.
func (s *NATService) VerifyHostname(ctx context.Context, vmID, id string) (*models.VMHostname, error) {
	h, err := s.hostname(ctx, vmID, id)
	if err != nil {
		return nil, err
	}
	if h.VerifiedAt != nil {
		s.push(ctx, vmID)
		return h, nil
	}
	b, err := natBlock(ctx, s.db, vmID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotNATVM
	}
	var taken int64
	if err := s.db.WithContext(ctx).Model(&models.VMHostname{}).
		Where("hostname = ? AND vm_id <> ? AND verified_at IS NOT NULL", h.Hostname, vmID).Count(&taken).Error; err != nil {
		return nil, err
	}
	if taken > 0 {
		return nil, ErrHostnameTaken
	}
	if err := s.webPortsFree(ctx, b.NodeID); err != nil {
		return nil, err
	}
	var publish string
	_ = s.db.WithContext(ctx).Table("nodes").Select("nat_address").Where("id = ?", b.NodeID).Scan(&publish).Error
	// Every NAT VM on a node shares its address, so pointing a hostname there
	// proves nothing against another account's claim: then only TXT does.
	var rivals int64
	if err := s.db.WithContext(ctx).Table("vm_hostnames h").Joins("JOIN vms m ON m.id = h.vm_id").
		Where("h.hostname = ? AND h.vm_id <> ? AND m.user_id <> (SELECT user_id FROM vms WHERE id = ?)", h.Hostname, vmID, vmID).
		Count(&rivals).Error; err != nil {
		return nil, err
	}
	if err := s.proveHostname(ctx, h, publish, rivals > 0); err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.VMHostname{}).Where("id = ?", h.ID).Update("verified_at", now).Error; err != nil {
		if strings.Contains(err.Error(), "ux_vm_hostnames_verified") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrHostnameTaken
		}
		return nil, err
	}
	h.VerifiedAt = &now
	s.push(ctx, vmID)
	return h, nil
}

// proveHostname passes when the TXT record carries the token, or, unless
// contested, when the hostname resolves to publish.
func (s *NATService) proveHostname(ctx context.Context, h *models.VMHostname, publish string, contested bool) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	want := "maburvm-verify=" + h.VerifyToken
	txts, _ := s.resolver.LookupTXT(ctx, HostnameVerifyPrefix+h.Hostname)
	for _, t := range txts {
		if strings.TrimSpace(t) == want {
			return nil
		}
	}
	if contested {
		return fmt.Errorf("%w: another account has also added %s, so only a TXT record %s%s with the value %s proves it is yours",
			ErrHostnameUnproven, h.Hostname, HostnameVerifyPrefix, h.Hostname, want)
	}
	addrs, _ := s.resolver.LookupHost(ctx, h.Hostname)
	if publish != "" && slices.Contains(addrs, publish) {
		return nil
	}
	found := "no address"
	if len(addrs) > 0 {
		found = strings.Join(addrs, ", ")
	}
	target := "this VM's public address"
	if publish != "" {
		target = publish
	}
	return fmt.Errorf("%w: no TXT record %s%s with the value %s was found, and %s points to %s, not %s",
		ErrHostnameUnproven, HostnameVerifyPrefix, h.Hostname, want, h.Hostname, found, target)
}

// RemoveHostname stops routing a hostname to the VM.
func (s *NATService) RemoveHostname(ctx context.Context, vmID, id string) error {
	h, err := s.hostname(ctx, vmID, id)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Where("id = ?", h.ID).Delete(&models.VMHostname{}).Error; err != nil {
		return err
	}
	if h.VerifiedAt != nil {
		s.push(ctx, vmID)
	}
	return nil
}

func (s *NATService) push(ctx context.Context, vmID string) {
	var vm models.VM
	if s.net == nil || s.db.WithContext(ctx).Where("id = ?", vmID).First(&vm).Error != nil {
		return
	}
	if err := s.net.enqueueNetworkResync(ctx, &vm); err != nil {
		slog.Default().Warn("hostname change saved but not pushed to the node", "vm_id", vmID, "error", err)
	}
}

// NodeNATSettings is a node's range of public ports for NAT VMs.
type NodeNATSettings struct {
	PortStart int    `json:"port_start"`
	PortEnd   int    `json:"port_end"`
	Address   string `json:"address"`
	// BlocksUsed and PortsUsed are what NAT VMs on the node hold now.
	BlocksUsed int64 `json:"blocks_used"`
	PortsUsed  int64 `json:"ports_used"`
}

// NodeNAT reads a node's NAT port range and how much of it is used.
func (s *NATService) NodeNAT(ctx context.Context, nodeID string) (*NodeNATSettings, error) {
	var out NodeNATSettings
	var row struct {
		NATPortStart, NATPortEnd int
		NATAddress               string
	}
	res := s.db.WithContext(ctx).Table("nodes").Select("nat_port_start, nat_port_end, nat_address").Where("id = ? AND deleted_at IS NULL", nodeID).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNodeNotFound
	}
	out.PortStart, out.PortEnd, out.Address = row.NATPortStart, row.NATPortEnd, row.NATAddress
	var used struct{ Blocks, Ports int64 }
	if err := s.db.WithContext(ctx).Model(&models.NATPortBlock{}).Select("COUNT(*) AS blocks, COALESCE(SUM(port_count), 0) AS ports").
		Where("node_id = ?", nodeID).Scan(&used).Error; err != nil {
		return nil, err
	}
	out.BlocksUsed, out.PortsUsed = used.Blocks, used.Ports
	return &out, nil
}

// SetNodeNAT changes a node's NAT port range (0 and 0 offers none). Blocks
// already handed out must stay inside it.
func (s *NATService) SetNodeNAT(ctx context.Context, nodeID string, start, end int) (*NodeNATSettings, error) {
	if !(start == 0 && end == 0) {
		if start < 1024 || end > 65535 || end < start {
			return nil, fmt.Errorf("%w: give a range between 1024 and 65535, or 0 and 0 for none", ErrInvalidPortRange)
		}
	}
	// Under the node's port lock, so no block is handed out meanwhile outside it.
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockNodePorts(ctx, tx, nodeID); err != nil {
			return err
		}
		var outside int64
		q := tx.WithContext(ctx).Model(&models.NATPortBlock{}).Where("node_id = ?", nodeID)
		if !(start == 0 && end == 0) {
			q = q.Where("start_port < ? OR start_port + port_count - 1 > ?", start, end)
		}
		if err := q.Count(&outside).Error; err != nil {
			return err
		}
		if outside > 0 {
			return fmt.Errorf("%w: %d NAT VMs hold ports outside that range", ErrInvalidPortRange, outside)
		}
		res := tx.WithContext(ctx).Table("nodes").Where("id = ? AND deleted_at IS NULL", nodeID).
			Updates(map[string]interface{}{"nat_port_start": start, "nat_port_end": end})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNodeNotFound
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.NodeNAT(ctx, nodeID)
}
