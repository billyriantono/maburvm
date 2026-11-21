package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// Shared storage pools: storage several nodes see at the same path, or (kind
// rbd, see ceph.go) one RBD pool of a Ceph cluster. The
// record groups one ordinary storage_pools row per member node, so placement,
// admission, disks and moves work on a member as on any pool of that node.
// Nothing here acts on its own: a node mounts or joins only when staff add it.

var (
	// ErrSharedPool is a shared-pool change that was refused; the text says why.
	ErrSharedPool = errors.New("shared storage pool")
	// ErrSharedPoolNotFound is an id that names no shared pool, or a node that
	// is not a member.
	ErrSharedPoolNotFound = errors.New("shared storage pool not found")
)

// errNoAnswer is a node that could not be asked, as opposed to one that
// answered that something is wrong.
var errNoAnswer = errors.New("the node did not answer")

// memberLocks holds one lock per shared pool, so membership changes one at a
// time: two nodes joining an empty pool at once would each become its first
// member, and neither would be checked against the other.
//
// ponytail: in-process lock; the panel runs as one process. Use an advisory
// lock if it ever runs as several.
var memberLocks sync.Map

func lockMembers(poolID string) (unlock func()) {
	m, _ := memberLocks.LoadOrStore(poolID, &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	return m.(*sync.Mutex).Unlock
}

// SharedPoolService manages shared pools and proves that members really see
// the same storage.
type SharedPoolService struct {
	db     *gorm.DB
	agent  func(ctx context.Context, node *models.Node) (pb.NodeAgentClient, context.Context, error)
	alerts *AlertService
}

// NewSharedPoolService builds the service on the VM service's node connections.
func NewSharedPoolService(db *gorm.DB, vms *VMService) *SharedPoolService {
	return &SharedPoolService{db: db, agent: nodeAgent(vms)}
}

// ceph is the Ceph service on the same database and node connections: an
// RBD pool's members are kept by the cluster, not added one by one.
func (s *SharedPoolService) ceph() *CephService {
	return &CephService{db: s.db, agent: s.agent, alerts: s.alerts}
}

// SetAlerts has changes made here (an RBD pool's first check) raise and
// clear the same alerts the storage sync does.
func (s *SharedPoolService) SetAlerts(a *AlertService) { s.alerts = a }

// SharedPoolMember is one node of a shared pool.
type SharedPoolMember struct {
	// PoolID is the node's own storage pool row for the shared pool.
	PoolID     string `json:"pool_id"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	Status     string `json:"status"`
	StatusNote string `json:"status_note"`
	IsPrimary  bool   `json:"is_primary"`
	// Tier is the storage tier of the node's row: a plan naming it puts its
	// VMs on that node in this pool.
	Tier string `json:"tier"`
}

// SharedPoolView is a shared pool with its members and its capacity, counted
// once however many nodes mount it.
type SharedPoolView struct {
	models.SharedStoragePool
	TotalSpace     int64 `json:"total_space"`
	UsedSpace      int64 `json:"used_space"`
	AvailableSpace int64 `json:"available_space"`
	// AllocatedSpace is the virtual disk promised on the pool by VMs on every
	// member; nil when it could not be worked out.
	AllocatedSpace  *int64             `json:"allocated_space,omitempty"`
	AllocationLimit int64              `json:"allocation_limit"`
	Members         []SharedPoolMember `json:"members"`
}

func refuseShared(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrSharedPool, fmt.Sprintf(format, args...))
}

func (s *SharedPoolService) pool(ctx context.Context, id string) (*models.SharedStoragePool, error) {
	var p models.SharedStoragePool
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrSharedPoolNotFound
	}
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSharedPoolNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (s *SharedPoolService) members(ctx context.Context, poolID string) ([]models.StoragePool, error) {
	var rows []models.StoragePool
	err := s.db.WithContext(ctx).Where("shared_pool_id = ?", poolID).Order("created_at, id").Find(&rows).Error
	return rows, err
}

func (s *SharedPoolService) node(ctx context.Context, id string) (*models.Node, error) {
	var n models.Node
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	return &n, nil
}

// List returns every shared pool with its members.
func (s *SharedPoolService) List(ctx context.Context) ([]SharedPoolView, error) {
	var pools []models.SharedStoragePool
	if err := s.db.WithContext(ctx).Order("name").Find(&pools).Error; err != nil {
		return nil, err
	}
	out := make([]SharedPoolView, 0, len(pools))
	if len(pools) == 0 {
		return out, nil
	}
	alloc, aerr := PoolAllocations(ctx, s.db, "")
	ratio := DiskAllocationRatio(ctx, s.db)
	names := map[string]string{}
	var nodes []models.Node
	if err := s.db.WithContext(ctx).Select("id, name").Find(&nodes).Error; err != nil {
		return nil, err
	}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	for _, p := range pools {
		v := SharedPoolView{SharedStoragePool: p, Members: []SharedPoolMember{}}
		rows, err := s.members(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		measured := false
		for _, r := range rows {
			v.Members = append(v.Members, SharedPoolMember{PoolID: r.ID, NodeID: r.NodeID, NodeName: names[r.NodeID],
				Status: r.Status, StatusNote: r.StatusNote, IsPrimary: r.IsPrimary, Tier: r.Tier})
			// Every member measures the same storage: one online reading is it.
			if !measured && r.Status == "online" {
				v.TotalSpace, v.UsedSpace, v.AvailableSpace, measured = r.TotalSpace, r.UsedSpace, r.AvailableSpace, true
				if aerr == nil {
					a := alloc[r.ID]
					v.AllocatedSpace = &a
				}
			}
		}
		if ratio > 0 {
			v.AllocationLimit = int64(float64(v.TotalSpace) * ratio)
		}
		sort.SliceStable(v.Members, func(i, j int) bool { return v.Members[i].NodeName < v.Members[j].NodeName })
		out = append(out, v)
	}
	return out, nil
}

// Create records a shared pool. No node is touched until one is added; an
// RBD pool is checked on, and given to, the nodes its cluster is enabled on.
func (s *SharedPoolService) Create(ctx context.Context, in models.SharedStoragePool) (*models.SharedStoragePool, error) {
	p := models.SharedStoragePool{
		ID: uuid.New().String(), Name: strings.TrimSpace(in.Name), Kind: strings.TrimSpace(in.Kind),
		NFSServer: strings.TrimSpace(in.NFSServer), NFSExport: strings.TrimSpace(in.NFSExport),
		MountOptions:  strings.ReplaceAll(strings.TrimSpace(in.MountOptions), " ", ""),
		CephClusterID: in.CephClusterID, RBDPool: strings.TrimSpace(in.RBDPool), RBDNamespace: strings.TrimSpace(in.RBDNamespace),
	}
	if p.Name == "" || len(p.Name) > 100 {
		return nil, refuseShared("a name of up to 100 characters is required")
	}
	if p.Kind == models.SharedPoolRBD {
		if taken, err := s.nameTaken(ctx, p.Name); err != nil || taken != nil {
			return nil, firstErr(err, taken)
		}
		if err := s.ceph().createRBDPool(ctx, &p); err != nil {
			return nil, err
		}
		return &p, nil
	}
	p.CephClusterID, p.RBDPool, p.RBDNamespace = nil, "", ""
	if p.Kind == models.SharedPoolMounted {
		p.NFSServer, p.NFSExport, p.MountOptions = "", "", ""
	}
	dir, err := models.ValidateSharedPool(p.ID, p.Kind, path.Clean(strings.TrimSpace(in.Path)), p.NFSServer, p.NFSExport, p.MountOptions)
	if err != nil {
		return nil, refuseShared("%v", err)
	}
	p.Path = dir
	if taken, err := s.nameTaken(ctx, p.Name); err != nil || taken != nil {
		return nil, firstErr(err, taken)
	}
	var n int64
	if p.Kind == models.SharedPoolMounted {
		if err := s.db.WithContext(ctx).Model(&models.SharedStoragePool{}).Where("path = ?", p.Path).Count(&n).Error; err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, refuseShared("%s is already a shared pool", p.Path)
		}
	}
	if err := s.db.WithContext(ctx).Create(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// nameTaken returns the refusal for a shared pool name already in use.
func (s *SharedPoolService) nameTaken(ctx context.Context, name string) (refusal, err error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.SharedStoragePool{}).Where("name = ?", name).Count(&n).Error; err != nil {
		return nil, err
	}
	if n > 0 {
		return refuseShared("a shared pool named %q already exists", name), nil
	}
	return nil, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a shared pool that has no members left. Files in it stay.
// An RBD pool's members go with it, once no VM or volume is in the pool;
// its images on the cluster stay.
func (s *SharedPoolService) Delete(ctx context.Context, id string) error {
	p, err := s.pool(ctx, id)
	if err != nil {
		return err
	}
	defer lockMembers(p.ID)()
	if p.Kind == models.SharedPoolRBD {
		return s.ceph().deleteRBDPool(ctx, p)
	}
	rows, err := s.members(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return refuseShared("%s still has %d member nodes; remove them first", p.Name, len(rows))
	}
	return s.db.WithContext(ctx).Where("id = ?", p.ID).Delete(&models.SharedStoragePool{}).Error
}

// token runs one token-file call on a node.
func (s *SharedPoolService) token(ctx context.Context, node *models.Node, dir, op, name, content string) (string, bool, error) {
	client, actx, err := s.agent(ctx, node)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", errNoAnswer, err)
	}
	cctx, cancel := context.WithTimeout(actx, 30*time.Second)
	defer cancel()
	resp, err := client.SharedPoolToken(cctx, &pb.SharedPoolTokenRequest{Path: dir, Name: name, Op: op, Content: content})
	switch {
	case unimplemented(err):
		return "", false, fmt.Errorf("node %s needs an agent update before it can use shared storage pools", node.Name)
	case err != nil:
		return "", false, fmt.Errorf("%w: node %s: %v", errNoAnswer, node.Name, err)
	case !resp.Success:
		return "", false, fmt.Errorf("node %s: %s", node.Name, agentMessage(resp.Error, "refused"))
	}
	return resp.Content, resp.Exists, nil
}

// markerOK checks that a node has the pool's marker file at the pool's path:
// without it the path is not the shared storage (not mounted, say).
func (s *SharedPoolService) markerOK(ctx context.Context, p *models.SharedStoragePool, node *models.Node) error {
	got, exists, err := s.token(ctx, node, p.Path, "read", models.SharedPoolMarker, "")
	switch {
	case err != nil:
		return err
	case !exists:
		return fmt.Errorf("node %s does not have the shared storage at %s (the pool's marker file is missing there, as when it is not mounted)", node.Name, p.Path)
	case got != p.ID:
		return fmt.Errorf("node %s has another pool's storage at %s", node.Name, p.Path)
	}
	return nil
}

// prove has writer put a random token file in the pool and reader read it
// back: only two nodes looking at the same storage pass.
func (s *SharedPoolService) prove(ctx context.Context, p *models.SharedStoragePool, writer, reader *models.Node) error {
	a, err := randomHex(8)
	if err != nil {
		return err
	}
	want, err := randomHex(16)
	if err != nil {
		return err
	}
	name := models.SharedPoolTokenPrefix + "probe-" + a
	if _, _, err := s.token(ctx, writer, p.Path, "write", name, want); err != nil {
		return fmt.Errorf("a test file could not be written in the pool: %w", err)
	}
	defer func() { _, _, _ = s.token(context.WithoutCancel(ctx), writer, p.Path, "remove", name, "") }()
	got, exists, err := s.token(ctx, reader, p.Path, "read", name, "")
	if err != nil {
		return err
	}
	if !exists || got != want {
		return fmt.Errorf("node %s does not see a test file node %s just wrote in %s: the two nodes are not looking at the same storage", reader.Name, writer.Name, p.Path)
	}
	return nil
}

func (s *SharedPoolService) setMemberStatus(ctx context.Context, rowID, status, note string) {
	s.db.WithContext(context.WithoutCancel(ctx)).Table("storage_pools").Where("id = ?", rowID).
		Updates(map[string]interface{}{"status": status, "status_note": note, "updated_at": time.Now()})
}

// ProveBetween checks, right now, that two member nodes see the same storage:
// both hold the pool's marker, and b reads a token a writes. A member that
// fails is marked unusable until the node's next storage report clears it.
func (s *SharedPoolService) ProveBetween(ctx context.Context, sharedPoolID string, a, b *models.Node) error {
	p, err := s.pool(ctx, sharedPoolID)
	if err != nil {
		return err
	}
	if p.Kind == models.SharedPoolRBD {
		// Both reach the pool's cluster (one fsid) and list the pool.
		for _, n := range []*models.Node{a, b} {
			if problem := s.ceph().rbdMemberProblem(ctx, p, n); problem != "" {
				return errors.New(problem)
			}
		}
		return nil
	}
	rows, err := s.members(ctx, p.ID)
	if err != nil {
		return err
	}
	mark := func(node *models.Node, err error) error {
		for _, r := range rows {
			if r.NodeID == node.ID {
				s.setMemberStatus(ctx, r.ID, "degraded", err.Error())
			}
		}
		return err
	}
	if err := s.markerOK(ctx, p, a); err != nil {
		return mark(a, err)
	}
	if err := s.markerOK(ctx, p, b); err != nil {
		return mark(b, err)
	}
	if err := s.prove(ctx, p, a, b); err != nil {
		return mark(b, err)
	}
	return nil
}

// MemberProblem says why a member node cannot use the pool right now: its
// agent does not hold the pool's marker (not mounted, or the agent forgot the
// pool). "" when it can. The storage sync asks every minute.
func (s *SharedPoolService) MemberProblem(ctx context.Context, sharedPoolID string, node *models.Node) string {
	p, err := s.pool(ctx, sharedPoolID)
	if err != nil || p.Kind == models.SharedPoolRBD {
		// The pool's record is gone: nothing to hold the node to. An RBD
		// pool's members are checked with their cluster (CephService.Poll).
		return ""
	}
	// A node that did not answer just now has not said the storage is gone.
	if err := s.markerOK(ctx, p, node); err != nil && !errors.Is(err, errNoAnswer) {
		return err.Error()
	}
	return ""
}

// setOnNode asks a node's agent to join the pool (mount an nfs pool, guard
// its path) or to leave it.
func (s *SharedPoolService) setOnNode(ctx context.Context, p *models.SharedStoragePool, node *models.Node, remove bool) error {
	client, actx, err := s.agent(ctx, node)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(actx, 3*time.Minute)
	defer cancel()
	resp, err := client.SetSharedPool(cctx, &pb.SetSharedPoolRequest{PoolId: p.ID, Kind: p.Kind, Path: p.Path,
		NfsServer: p.NFSServer, NfsExport: p.NFSExport, MountOptions: p.MountOptions, Remove: remove})
	switch {
	case unimplemented(err):
		return fmt.Errorf("node %s needs an agent update before it can use shared storage pools", node.Name)
	case err != nil:
		return fmt.Errorf("node %s did not answer: %w", node.Name, err)
	case !resp.Success:
		return fmt.Errorf("node %s: %s", node.Name, agentMessage(resp.Error, "refused"))
	case !remove && resp.Path != p.Path:
		return fmt.Errorf("node %s has the pool at %s, not at %s", node.Name, resp.Path, p.Path)
	}
	return nil
}

// AddMember makes a node a member: the node mounts the pool (kind nfs), and
// must then prove it sees the same storage as the members already there, by
// reading a token one of them writes and having its own token read back. A
// node that cannot is taken out again and refused with the reason.
func (s *SharedPoolService) AddMember(ctx context.Context, poolID, nodeID string) error {
	p, err := s.pool(ctx, poolID)
	if err != nil {
		return err
	}
	if p.Kind == models.SharedPoolRBD {
		return refuseShared("%s is an RBD pool: its members are the nodes its Ceph cluster is enabled on. Enable the cluster on the node instead", p.Name)
	}
	node, err := s.node(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.Status != models.NodeStatusActive {
		return refuseShared("node %s is %s", node.Name, node.Status)
	}
	defer lockMembers(p.ID)()
	rows, err := s.members(ctx, p.ID)
	if err != nil {
		return err
	}
	var peers []*models.Node // online members to check against
	for _, r := range rows {
		if r.NodeID == node.ID {
			return refuseShared("node %s is already a member of %s", node.Name, p.Name)
		}
		if r.Status != "online" {
			continue
		}
		n, err := s.node(ctx, r.NodeID)
		if err != nil || n.Status != models.NodeStatusActive {
			continue
		}
		// A member that lost the storage itself is no witness: with it, the
		// new node would be blamed for the member's missing mount.
		if err := s.markerOK(ctx, p, n); err != nil {
			if !errors.Is(err, errNoAnswer) {
				s.setMemberStatus(ctx, r.ID, "degraded", err.Error())
			}
			continue
		}
		peers = append(peers, n)
	}
	if len(rows) > 0 && len(peers) == 0 {
		return refuseShared("no member of %s has the storage right now to check node %s against; bring one back first", p.Name, node.Name)
	}

	if err := s.setOnNode(ctx, p, node, false); err != nil {
		return refuseShared("%v", err)
	}
	leave := func(why error) error {
		if err := s.setOnNode(context.WithoutCancel(ctx), p, node, true); err != nil {
			return refuseShared("%v (and the node could not be taken out again: %v)", why, err)
		}
		return refuseShared("%v", why)
	}
	if len(rows) == 0 {
		// The first member names the storage: its marker file says which pool
		// this directory is.
		got, _, err := s.token(ctx, node, p.Path, "write", models.SharedPoolMarker, p.ID)
		if err != nil {
			return leave(err)
		}
		if got != p.ID {
			return leave(fmt.Errorf("%s on node %s already belongs to another shared pool", p.Path, node.Name))
		}
	} else {
		if err := s.markerOK(ctx, p, node); err != nil {
			return leave(err)
		}
		for _, peer := range peers {
			if err := s.prove(ctx, p, peer, node); err != nil {
				return leave(err)
			}
		}
		// And the other way: the new node can write, and a member sees it.
		if err := s.prove(ctx, p, node, peers[0]); err != nil {
			return leave(err)
		}
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A directory pool already registered at the path becomes the member row.
		var existing models.StoragePool
		q := tx.Where("node_id = ? AND path = ? AND COALESCE(type, '') IN ?", node.ID, p.Path, []string{"", "dir", "file"}).Limit(1).Find(&existing)
		if q.Error != nil {
			return q.Error
		}
		id := existing.ID
		if id == "" {
			// Online at once: the node has just shown the storage is there. Its
			// capacity follows with the node's next storage report.
			row := models.StoragePool{Name: p.Name, Type: models.PoolTypeDir, Status: "online", Path: p.Path, FileFormat: "qcow2", NodeID: node.ID}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("node %s already has a storage pool named %q, or the pool could not be recorded: %w", node.Name, p.Name, err)
			}
			id = row.ID
		}
		return tx.Table("storage_pools").Where("id = ?", id).Update("shared_pool_id", p.ID).Error
	})
	if err != nil {
		return leave(err)
	}
	return nil
}

// RemoveMember takes a node out of a shared pool. It is refused while a VM on
// that node has a disk in the pool; an nfs pool is unmounted there. A node
// that no longer exists is just forgotten.
func (s *SharedPoolService) RemoveMember(ctx context.Context, poolID, nodeID string) error {
	p, err := s.pool(ctx, poolID)
	if err != nil {
		return err
	}
	if p.Kind == models.SharedPoolRBD {
		return refuseShared("%s is an RBD pool: its members are the nodes its Ceph cluster is enabled on. Disable the cluster on the node instead", p.Name)
	}
	defer lockMembers(p.ID)()
	var row models.StoragePool
	if err := s.db.WithContext(ctx).Where("shared_pool_id = ? AND node_id = ?", p.ID, nodeID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSharedPoolNotFound
		}
		return err
	}
	node, err := s.node(ctx, nodeID)
	if err != nil && !errors.Is(err, ErrNodeNotFound) {
		return err
	}
	if node != nil {
		var vms int64
		if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM vms v WHERE v.deleted_at IS NULL AND CAST(v.node_id AS TEXT) = ?
			AND (CAST(v.storage_pool_id AS TEXT) = ? OR EXISTS (SELECT 1 FROM vm_disks d
				WHERE CAST(d.vm_id AS TEXT) = CAST(v.id AS TEXT) AND d.deleted_at IS NULL AND d.path LIKE ?))`,
			nodeID, row.ID, likeEscape(p.Path)+"/%").Scan(&vms).Error; err != nil {
			return err
		}
		if vms > 0 {
			return refuseShared("%d VMs on node %s have disks in %s; move or delete them first", vms, node.Name, p.Name)
		}
		if err := s.setOnNode(ctx, p, node, true); err != nil {
			return refuseShared("%v", err)
		}
	}
	return s.db.WithContext(ctx).Where("id = ?", row.ID).Delete(&models.StoragePool{}).Error
}

// likeEscape escapes a string for the left side of LIKE.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Check proves every reachable member against one that holds the pool's
// marker, now, and returns each node's verdict ("" passes). A member that
// fails is marked unusable; one that passes is left to the node's next
// storage report.
func (s *SharedPoolService) Check(ctx context.Context, poolID string) (map[string]string, error) {
	p, err := s.pool(ctx, poolID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if p.Kind == models.SharedPoolRBD {
		// The cluster is checked on every node it is enabled on; each node's
		// verdict on this pool is then on its member row.
		if p.CephClusterID == nil {
			return out, nil
		}
		if out, err = s.ceph().Check(ctx, *p.CephClusterID); err != nil {
			return nil, err
		}
		rows, err := s.members(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if out[r.NodeID] == "" && r.Status != "online" {
				out[r.NodeID] = firstNonEmpty(r.StatusNote, "the node has not checked the pool yet")
			}
		}
		return out, nil
	}
	rows, err := s.members(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var writer *models.Node
	var readers []models.StoragePool
	nodes := map[string]*models.Node{}
	for _, r := range rows {
		n, err := s.node(ctx, r.NodeID)
		if err != nil {
			out[r.NodeID] = "the node no longer exists"
			continue
		}
		nodes[r.NodeID] = n
		if err := s.markerOK(ctx, p, n); err != nil {
			out[r.NodeID] = err.Error()
			s.setMemberStatus(ctx, r.ID, "degraded", err.Error())
			continue
		}
		out[r.NodeID] = ""
		if writer == nil {
			writer = n
		} else {
			readers = append(readers, r)
		}
	}
	for _, r := range readers {
		if err := s.prove(ctx, p, writer, nodes[r.NodeID]); err != nil {
			out[r.NodeID] = err.Error()
			s.setMemberStatus(ctx, r.ID, "degraded", err.Error())
		}
	}
	return out, nil
}
