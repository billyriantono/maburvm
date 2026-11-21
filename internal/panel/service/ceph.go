package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// Ceph clusters: existing clusters staff register, and enable on the nodes
// that reach them. MaburVM is a client only: it never installs, grows or
// upgrades a cluster. Nothing here acts on its own: a node gets a cluster's
// conf, keyring and libvirt secret only when staff enable the cluster on it.
//
// An RBD pool of a cluster is a shared pool of kind rbd. Its members are the
// nodes the cluster is enabled on: each gets a storage_pools row of type rbd,
// usable once the node has listed the pool's images.

var (
	// ErrCeph is a Ceph cluster change that was refused; the text says why.
	ErrCeph = errors.New("ceph cluster")
	// ErrCephNotFound is an id that names no cluster, or a node the cluster is
	// not enabled on.
	ErrCephNotFound = errors.New("ceph cluster not found")
)

func refuseCeph(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrCeph, fmt.Sprintf(format, args...))
}

// nodeAgent reaches a node's agent over the VM service's connections.
func nodeAgent(vms *VMService) func(ctx context.Context, node *models.Node) (pb.NodeAgentClient, context.Context, error) {
	return func(ctx context.Context, node *models.Node) (pb.NodeAgentClient, context.Context, error) {
		client, err := vms.getAgentClient(ctx, node.ID)
		if err != nil {
			return nil, ctx, fmt.Errorf("node %s cannot be reached: %w", node.Name, err)
		}
		return client, metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+node.Token, "x-node-id", node.ID), nil
	}
}

// CephService manages registered clusters and what each node says of them.
type CephService struct {
	db     *gorm.DB
	agent  func(ctx context.Context, node *models.Node) (pb.NodeAgentClient, context.Context, error)
	alerts *AlertService
}

// NewCephService builds the service on the VM service's node connections.
// alerts may be nil: nobody is told then.
func NewCephService(db *gorm.DB, vms *VMService, alerts *AlertService) *CephService {
	return &CephService{db: db, agent: nodeAgent(vms), alerts: alerts}
}

// CephClusterInput is what staff give to register or change a cluster. The
// key is write-only: no answer of the API carries it.
type CephClusterInput struct {
	Name      string `json:"name"`
	Monitors  string `json:"monitors"`
	ClientID  string `json:"client_id"`
	ClientKey string `json:"client_key"`
	FSID      string `json:"fsid"`
}

// CephNodeView is one node a cluster is enabled on.
type CephNodeView struct {
	NodeID     string     `json:"node_id"`
	NodeName   string     `json:"node_name"`
	Status     string     `json:"status"`
	StatusNote string     `json:"status_note"`
	CheckedAt  *time.Time `json:"checked_at"`
}

// CephPoolView is one RBD pool of a cluster, measured once.
type CephPoolView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RBDPool        string `json:"rbd_pool"`
	RBDNamespace   string `json:"rbd_namespace"`
	TotalSpace     int64  `json:"total_space"`
	UsedSpace      int64  `json:"used_space"`
	AvailableSpace int64  `json:"available_space"`
}

// CephClusterView is a cluster with its nodes and RBD pools.
type CephClusterView struct {
	models.CephCluster
	Nodes []CephNodeView `json:"nodes"`
	Pools []CephPoolView `json:"pools"`
}

func (s *CephService) cluster(ctx context.Context, id string) (*models.CephCluster, error) {
	var c models.CephCluster
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrCephNotFound
	}
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCephNotFound
		}
		return nil, err
	}
	return &c, nil
}

// enabled lists the rows of the nodes a cluster is enabled on.
func (s *CephService) enabled(ctx context.Context, clusterID string) ([]models.CephClusterNode, error) {
	var rows []models.CephClusterNode
	err := s.db.WithContext(ctx).Where("cluster_id = ?", clusterID).Order("created_at, node_id").Find(&rows).Error
	return rows, err
}

// pools lists a cluster's RBD pools.
func (s *CephService) pools(ctx context.Context, clusterID string) ([]models.SharedStoragePool, error) {
	var rows []models.SharedStoragePool
	err := s.db.WithContext(ctx).Where("ceph_cluster_id = ? AND kind = ?", clusterID, models.SharedPoolRBD).Order("name").Find(&rows).Error
	return rows, err
}

func (s *CephService) node(ctx context.Context, id string) (*models.Node, error) {
	var n models.Node
	if _, err := uuid.Parse(id); err != nil { // ceph_cluster_nodes.node_id is a uuid
		return nil, ErrNodeNotFound
	}
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	return &n, nil
}

// List returns every cluster with its nodes and RBD pools. No key is in it.
func (s *CephService) List(ctx context.Context) ([]CephClusterView, error) {
	var clusters []models.CephCluster
	if err := s.db.WithContext(ctx).Order("name").Find(&clusters).Error; err != nil {
		return nil, err
	}
	out := make([]CephClusterView, 0, len(clusters))
	if len(clusters) == 0 {
		return out, nil
	}
	names := map[string]string{}
	var nodes []models.Node
	if err := s.db.WithContext(ctx).Select("id, name").Find(&nodes).Error; err != nil {
		return nil, err
	}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	for _, c := range clusters {
		v := CephClusterView{CephCluster: c, Nodes: []CephNodeView{}, Pools: []CephPoolView{}}
		rows, err := s.enabled(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			v.Nodes = append(v.Nodes, CephNodeView{NodeID: r.NodeID, NodeName: names[r.NodeID], Status: r.Status, StatusNote: r.StatusNote, CheckedAt: r.CheckedAt})
		}
		sort.SliceStable(v.Nodes, func(i, j int) bool { return v.Nodes[i].NodeName < v.Nodes[j].NodeName })
		pools, err := s.pools(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		for _, p := range pools {
			pv := CephPoolView{ID: p.ID, Name: p.Name, RBDPool: p.RBDPool, RBDNamespace: p.RBDNamespace}
			// Every member measures the same pool: one online reading is it.
			var m models.StoragePool
			if err := s.db.WithContext(ctx).Where("shared_pool_id = ? AND status = ?", p.ID, "online").
				Order("total_space DESC").Limit(1).Find(&m).Error; err != nil {
				return nil, err
			}
			pv.TotalSpace, pv.UsedSpace, pv.AvailableSpace = m.TotalSpace, m.UsedSpace, m.AvailableSpace
			v.Pools = append(v.Pools, pv)
		}
		out = append(out, v)
	}
	return out, nil
}

// normalize checks staff's input and fills c from it. An empty key keeps the
// one c has.
func (in CephClusterInput) normalize(c *models.CephCluster) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return refuseCeph("a name of up to 100 characters is required")
	}
	mons, err := models.ParseCephMonitors(in.Monitors)
	if err != nil {
		return refuseCeph("%v", err)
	}
	client, err := models.NormalizeCephClientID(in.ClientID)
	if err != nil {
		return refuseCeph("%v", err)
	}
	key := strings.TrimSpace(in.ClientKey)
	if key == "" {
		key = c.ClientKey
	}
	if !models.ValidCephKey(key) {
		return refuseCeph("the key is not a cephx key: paste the output of ceph auth get-key client.%s", client)
	}
	c.Name, c.Monitors, c.ClientID, c.ClientKey = name, strings.Join(mons, ","), client, key
	return nil
}

func (s *CephService) nameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.CephCluster{}).Where("name = ? AND id <> ?", name, exceptID).Count(&n).Error
	return n > 0, err
}

// Create registers a cluster. No node is touched until it is enabled on one.
func (s *CephService) Create(ctx context.Context, in CephClusterInput) (*models.CephCluster, error) {
	c := models.CephCluster{ID: uuid.New().String(), FSID: strings.ToLower(strings.TrimSpace(in.FSID))}
	if err := in.normalize(&c); err != nil {
		return nil, err
	}
	if c.FSID != "" && !models.ValidCephFSID(c.FSID) {
		return nil, refuseCeph("the fsid is the cluster's id as ceph fsid prints it; leave it empty to have the first node record it")
	}
	if taken, err := s.nameTaken(ctx, c.Name, c.ID); err != nil {
		return nil, err
	} else if taken {
		return nil, refuseCeph("a Ceph cluster named %q is already registered", c.Name)
	}
	if err := s.db.WithContext(ctx).Create(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// Update changes a cluster's name, monitors, client id or key (an empty key
// keeps the stored one). New access settings go to the nodes the cluster is
// enabled on: when the first node to answer refuses them (a wrong key, say),
// nothing is changed anywhere. A node that takes them later than the rest
// shows why on the cluster's card.
func (s *CephService) Update(ctx context.Context, id string, in CephClusterInput) (*models.CephCluster, error) {
	c, err := s.cluster(ctx, id)
	if err != nil {
		return nil, err
	}
	defer lockMembers("ceph:" + c.ID)()
	next := *c
	if err := in.normalize(&next); err != nil {
		return nil, err
	}
	if taken, err := s.nameTaken(ctx, next.Name, c.ID); err != nil {
		return nil, err
	} else if taken {
		return nil, refuseCeph("a Ceph cluster named %q is already registered", next.Name)
	}
	save := func() error {
		// A struct update, so the serializer seals the key.
		next.UpdatedAt = time.Now()
		return s.db.WithContext(ctx).Model(&next).Select("name", "monitors", "client_id", "client_key", "updated_at").Updates(&next).Error
	}
	if next.Monitors == c.Monitors && next.ClientID == c.ClientID && next.ClientKey == c.ClientKey {
		return &next, save()
	}
	rows, err := s.enabled(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	saved := false
	// Nodes that could not be asked are recorded only once the settings are
	// saved: a refusal from a later node changes nothing, them included.
	type missedNode struct {
		node *models.Node
		why  string
	}
	var missed []missedNode
	for _, r := range rows {
		node, err := s.node(ctx, r.NodeID)
		if err != nil {
			continue
		}
		fsid, err := s.setOnNode(ctx, &next, node, false)
		if err == nil && c.FSID != "" && fsid != c.FSID {
			err = fmt.Errorf("with these settings node %s reaches another cluster (fsid %s), not %s (fsid %s)", node.Name, fsid, c.Name, c.FSID)
		}
		switch {
		case errors.Is(err, errNoAnswer):
			missed = append(missed, missedNode{node, "The node has not received the cluster's new settings: " + err.Error()})
		case err != nil && !saved:
			return nil, refuseCeph("%v. Nothing was changed", err)
		case err != nil:
			s.recordNode(ctx, c, node, models.CephNodeUnreachable, err.Error())
		default:
			if !saved {
				if err := save(); err != nil {
					return nil, err
				}
				saved = true
			}
			s.recordNode(ctx, c, node, models.CephNodeOK, "")
		}
	}
	if !saved {
		if err := save(); err != nil {
			return nil, err
		}
	}
	for _, m := range missed {
		s.recordNode(ctx, &next, m.node, models.CephNodeUnreachable, m.why)
	}
	return &next, nil
}

// Delete unregisters a cluster. Refused while an RBD pool is on it (and so
// while any disk is) or a node still holds its keyring.
func (s *CephService) Delete(ctx context.Context, id string) error {
	c, err := s.cluster(ctx, id)
	if err != nil {
		return err
	}
	defer lockMembers("ceph:" + c.ID)()
	pools, err := s.pools(ctx, c.ID)
	if err != nil {
		return err
	}
	if len(pools) > 0 {
		return refuseCeph("%d RBD pools are on %s (%s); delete them first", len(pools), c.Name, pools[0].Name)
	}
	rows, err := s.enabled(ctx, c.ID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return refuseCeph("%s is still enabled on %d nodes; disable it there first, so they drop its keyring", c.Name, len(rows))
	}
	s.alerts.Drop(ctx, "ceph_health:"+c.ID, "Closed: the cluster was unregistered")
	// Not a soft delete: the sealed key goes with the row.
	return s.db.WithContext(ctx).Unscoped().Where("id = ?", c.ID).Delete(&models.CephCluster{}).Error
}

// setOnNode has a node's agent write the cluster's conf and keyring, define
// its libvirt secret and prove the access (or, with remove, drop all three).
// It returns the fsid of the cluster the node reached.
func (s *CephService) setOnNode(ctx context.Context, c *models.CephCluster, node *models.Node, remove bool) (string, error) {
	client, actx, err := s.agent(ctx, node)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoAnswer, err)
	}
	cctx, cancel := context.WithTimeout(actx, time.Minute)
	defer cancel()
	req := &pb.SetCephClusterRequest{ClusterId: c.ID, Remove: remove}
	if !remove {
		// With the fsid known, the node itself refuses another cluster
		// before it replaces anything.
		req.Monitors, req.ClientId, req.Key, req.Fsid = c.MonitorList(), c.ClientID, c.ClientKey, c.FSID
	}
	resp, err := client.SetCephCluster(cctx, req)
	switch {
	case unimplemented(err):
		return "", fmt.Errorf("node %s needs an agent update before it can use a Ceph cluster", node.Name)
	case err != nil:
		return "", fmt.Errorf("%w: node %s: %v", errNoAnswer, node.Name, err)
	case !resp.Success:
		return "", fmt.Errorf("node %s: %s", node.Name, agentMessage(resp.Error, "refused"))
	}
	return strings.ToLower(resp.Fsid), nil
}

// EnableNode gives a node access to the cluster. The node must reach the
// cluster with the registered id and key, and the cluster it reaches must be
// the one the other nodes reach: the first node's fsid is recorded, and a
// node reporting another is taken out again and refused.
func (s *CephService) EnableNode(ctx context.Context, clusterID, nodeID string) error {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return err
	}
	node, err := s.node(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.Status != models.NodeStatusActive {
		return refuseCeph("node %s is %s", node.Name, node.Status)
	}
	defer lockMembers("ceph:" + c.ID)()
	if c, err = s.cluster(ctx, clusterID); err != nil { // again, under the lock: the fsid may just have been recorded
		return err
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.CephClusterNode{}).Where("cluster_id = ? AND node_id = ?", c.ID, node.ID).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return refuseCeph("%s is already enabled on node %s", c.Name, node.Name)
	}
	// The node gets a pool row per RBD pool of the cluster, under the pool's name.
	pools, err := s.pools(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, p := range pools {
		if clash, err := poolNameOnNodes(ctx, s.db, p.Name, []string{node.ID}); err != nil {
			return err
		} else if clash != "" {
			return refuseCeph("node %s already has a storage pool named %q, the name of an RBD pool of this cluster; rename one of them first", node.Name, p.Name)
		}
	}

	fsid, err := s.setOnNode(ctx, c, node, false)
	if err != nil {
		return refuseCeph("%v", err)
	}
	if c.FSID != "" && fsid != c.FSID { // the node checks this too; an answer that slipped through is undone
		why := fmt.Sprintf("node %s reaches another cluster (fsid %s) than %s (fsid %s); check the monitor addresses and the node's network", node.Name, fsid, c.Name, c.FSID)
		if _, err := s.setOnNode(context.WithoutCancel(ctx), c, node, true); err != nil {
			return refuseCeph("%s (and the node could not be taken out again: %v)", why, err)
		}
		return refuseCeph("%s", why)
	}
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if c.FSID == "" {
			if err := tx.Model(&models.CephCluster{}).Where("id = ?", c.ID).Update("fsid", fsid).Error; err != nil {
				return err
			}
			c.FSID = fsid
		}
		return tx.Create(&models.CephClusterNode{ClusterID: c.ID, NodeID: node.ID, Status: models.CephNodeOK, CheckedAt: &now, CreatedAt: now}).Error
	})
	if err != nil {
		// Not recorded: the node must not keep a keyring the panel does not know of.
		_, _ = s.setOnNode(context.WithoutCancel(ctx), c, node, true)
		return err
	}
	// Health, capacity and the node's part of each RBD pool, without waiting
	// for the next poll.
	s.checkNode(ctx, c, node)
	return nil
}

// DisableNode takes a cluster off a node: its conf, keyring and libvirt
// secret are removed there. Refused while a VM on the node has a disk on the
// cluster, by the panel's records and by the node's own domains. A node that
// no longer exists is just forgotten.
func (s *CephService) DisableNode(ctx context.Context, clusterID, nodeID string) error {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return err
	}
	if _, err := uuid.Parse(nodeID); err != nil {
		return ErrCephNotFound
	}
	defer lockMembers("ceph:" + c.ID)()
	var row models.CephClusterNode
	if err := s.db.WithContext(ctx).Where("cluster_id = ? AND node_id = ?", c.ID, nodeID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCephNotFound
		}
		return err
	}
	node, err := s.node(ctx, nodeID)
	if err != nil && !errors.Is(err, ErrNodeNotFound) {
		return err
	}
	members := s.db.WithContext(ctx).Table("storage_pools").Select("id").
		Where("node_id = ? AND deleted_at IS NULL AND shared_pool_id IN (?)", nodeID,
			s.db.Table("shared_storage_pools").Select("id").Where("ceph_cluster_id = ? AND deleted_at IS NULL", c.ID))
	if node != nil {
		var vms int64
		if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM vms v WHERE v.deleted_at IS NULL AND CAST(v.node_id AS TEXT) = ?
			AND CAST(v.storage_pool_id AS TEXT) IN (SELECT CAST(sp.id AS TEXT) FROM storage_pools sp
				JOIN shared_storage_pools s ON CAST(s.id AS TEXT) = CAST(sp.shared_pool_id AS TEXT)
				WHERE CAST(s.ceph_cluster_id AS TEXT) = ? AND CAST(sp.node_id AS TEXT) = ? AND sp.deleted_at IS NULL AND s.deleted_at IS NULL)`,
			nodeID, c.ID, nodeID).Scan(&vms).Error; err != nil {
			return err
		}
		if vms > 0 {
			return refuseCeph("%d VMs on node %s have disks on %s; move or delete them first", vms, node.Name, c.Name)
		}
		// The node looks at its own domains before it removes anything.
		if _, err := s.setOnNode(ctx, c, node, true); err != nil {
			return refuseCeph("%v", err)
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id IN (?)", members).Delete(&models.StoragePool{}).Error; err != nil {
			return err
		}
		return tx.Where("cluster_id = ? AND node_id = ?", c.ID, nodeID).Delete(&models.CephClusterNode{}).Error
	})
	if err != nil {
		return err
	}
	s.alerts.Drop(ctx, cephNodeAlertKey(c.ID, nodeID), "Closed: the cluster was disabled on the node")
	return nil
}

// status asks a node how it sees the cluster and the given RBD pools. An
// errNoAnswer error is a node that could not be asked; any other is the node
// saying it cannot use the cluster.
func (s *CephService) status(ctx context.Context, c *models.CephCluster, node *models.Node, pools []models.SharedStoragePool) (*pb.CephStatusResponse, error) {
	client, actx, err := s.agent(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoAnswer, err)
	}
	// Bounded: the agent bounds each command too, and the caller never waits
	// on a cluster that is slow.
	cctx, cancel := context.WithTimeout(actx, 45*time.Second)
	defer cancel()
	req := &pb.CephStatusRequest{ClusterId: c.ID}
	for _, p := range pools {
		req.Pools = append(req.Pools, &pb.CephRBDPool{Pool: p.RBDPool, Namespace: p.RBDNamespace})
	}
	resp, err := client.CephStatus(cctx, req)
	switch {
	case unimplemented(err):
		return nil, fmt.Errorf("node %s needs an agent update before it can use a Ceph cluster", node.Name)
	case err != nil:
		return nil, fmt.Errorf("%w: node %s: %v", errNoAnswer, node.Name, err)
	case !resp.Success:
		return nil, fmt.Errorf("node %s: %s", node.Name, agentMessage(resp.Error, "cannot reach the cluster"))
	}
	resp.Fsid = strings.ToLower(resp.Fsid)
	return resp, nil
}

func cephNodeAlertKey(clusterID, nodeID string) string {
	return "ceph_node:" + clusterID + ":" + nodeID
}

// recordNode stores what a node said of a cluster, and raises or clears the
// alert of a node that cannot use it.
func (s *CephService) recordNode(ctx context.Context, c *models.CephCluster, node *models.Node, state, note string) {
	ctx = context.WithoutCancel(ctx)
	s.db.WithContext(ctx).Model(&models.CephClusterNode{}).Where("cluster_id = ? AND node_id = ?", c.ID, node.ID).
		Updates(map[string]interface{}{"status": state, "status_note": note, "checked_at": time.Now()})
	key := cephNodeAlertKey(c.ID, node.ID)
	if state == models.CephNodeOK {
		s.alerts.Clear(ctx, key)
		return
	}
	s.alerts.Raise(ctx, AlertInput{
		Key: key, Kind: models.AlertKindStoragePool, Severity: models.AlertSeverityCritical,
		Title:        fmt.Sprintf("Node %s cannot use Ceph cluster %s", node.Name, c.Name),
		Detail:       note + " VMs on this node with disks on the cluster cannot start, and no new disk is placed on it there, until this is put right.",
		ResourceType: "ceph_cluster", ResourceID: c.ID, ResourceName: c.Name,
	})
}

// checkNode asks one node about a cluster and records the answer. It returns
// what is wrong on the node, "" for nothing. The caller holds the cluster's
// lock; pollNode is for callers that do not.
func (s *CephService) checkNode(ctx context.Context, c *models.CephCluster, node *models.Node) string {
	pools, err := s.pools(ctx, c.ID)
	if err != nil {
		return err.Error()
	}
	resp, err := s.status(ctx, c, node, pools)
	return s.record(ctx, c, node, resp, err)
}

// pollNode is checkNode without the cluster's lock held: the node is asked
// first, which can take a while, and the answer is recorded under the lock
// only if the cluster is still enabled on the node. An answer that arrives
// after staff disabled it must not bring the node's rows and alerts back.
func (s *CephService) pollNode(ctx context.Context, c *models.CephCluster, node *models.Node) string {
	pools, err := s.pools(ctx, c.ID)
	if err != nil {
		return err.Error()
	}
	resp, err := s.status(ctx, c, node, pools)
	defer lockMembers("ceph:" + c.ID)()
	var n int64
	if cerr := s.db.WithContext(ctx).Model(&models.CephClusterNode{}).Where("cluster_id = ? AND node_id = ?", c.ID, node.ID).Count(&n).Error; cerr != nil || n == 0 {
		return fmt.Sprintf("the cluster is not enabled on node %s", node.Name)
	}
	if c, cerr := s.cluster(ctx, c.ID); cerr == nil { // as it is now
		return s.record(ctx, c, node, resp, err)
	}
	return "the cluster is no longer registered"
}

// record stores what a node answered about a cluster: whether the node
// reaches it, the cluster's health and capacity, and the node's part of each
// RBD pool. A node that could not be asked changes nothing: it has not said
// the cluster is gone.
func (s *CephService) record(ctx context.Context, c *models.CephCluster, node *models.Node, resp *pb.CephStatusResponse, err error) string {
	if errors.Is(err, errNoAnswer) {
		return err.Error()
	}
	state, problem := models.CephNodeOK, ""
	switch {
	case err != nil:
		state, problem = models.CephNodeUnreachable, err.Error()+"."
	case c.FSID != "" && resp.Fsid != c.FSID:
		state = models.CephNodeMismatch
		problem = fmt.Sprintf("Node %s reaches another cluster (fsid %s) than %s (fsid %s).", node.Name, resp.Fsid, c.Name, c.FSID)
	case resp.Problem != "":
		state, problem = models.CephNodeDegraded, fmt.Sprintf("Node %s: %s.", node.Name, resp.Problem)
	}
	s.recordNode(ctx, c, node, state, problem)

	if err == nil && state != models.CephNodeMismatch {
		s.db.WithContext(context.WithoutCancel(ctx)).Model(&models.CephCluster{}).Where("id = ?", c.ID).Updates(map[string]interface{}{
			"health": resp.Health, "health_summary": resp.HealthSummary, "checked_at": time.Now(),
			"total_bytes": resp.TotalBytes, "used_bytes": resp.UsedBytes, "available_bytes": resp.AvailableBytes,
		})
		s.alertHealth(ctx, c, resp)
	}

	seen := map[string]*pb.CephPoolStatus{}
	if resp != nil {
		for _, ps := range resp.Pools {
			seen[models.RBDPoolPath(ps.Pool, ps.Namespace)] = ps
		}
	}
	// The pools as they are now, which may not be the ones the node was asked
	// about: one added since is left for the next check.
	pools, perr := s.pools(ctx, c.ID)
	if perr != nil {
		return firstNonEmpty(problem, perr.Error())
	}
	for i := range pools {
		p := &pools[i]
		row, err := s.ensureMember(ctx, p, node.ID)
		if err != nil {
			continue // a pool of the node's own already has the name; EnableNode and Create refuse that
		}
		ps := seen[p.Path]
		why := problem
		switch {
		case why != "":
		case ps == nil:
			continue
		case ps.Error != "":
			why = fmt.Sprintf("Node %s cannot use RBD pool %s: %s.", node.Name, p.Path, ps.Error)
		}
		updates := map[string]interface{}{"status": "online", "status_note": "", "updated_at": time.Now()}
		if why != "" {
			updates["status"], updates["status_note"] = "degraded", why
		} else if ps.Measured {
			updates["total_space"], updates["used_space"], updates["available_space"] = ps.UsedBytes+ps.AvailableBytes, ps.UsedBytes, ps.AvailableBytes
		}
		s.db.WithContext(context.WithoutCancel(ctx)).Table("storage_pools").Where("id = ?", row.ID).Updates(updates)
		alertSharedMember(ctx, s.alerts, node, row, why)
	}
	return problem
}

// alertHealth raises the cluster's alert on HEALTH_ERR and clears it
// otherwise. HEALTH_WARN is shown on the Storage page, not alerted.
func (s *CephService) alertHealth(ctx context.Context, c *models.CephCluster, resp *pb.CephStatusResponse) {
	key := "ceph_health:" + c.ID
	if resp.Health != models.CephHealthErr {
		s.alerts.Clear(ctx, key)
		return
	}
	s.alerts.Raise(ctx, AlertInput{
		Key: key, Kind: models.AlertKindStoragePool, Severity: models.AlertSeverityCritical,
		Title:        fmt.Sprintf("Ceph cluster %s reports HEALTH_ERR", c.Name),
		Detail:       strings.TrimSpace(resp.HealthSummary + "\nVMs with disks on the cluster may stall or lose writes until the cluster is repaired. MaburVM does not repair it."),
		ResourceType: "ceph_cluster", ResourceID: c.ID, ResourceName: c.Name,
	})
}

// ensureMember returns a node's storage pool row for an RBD pool, making it
// (offline, until the node has listed the pool) when it is not there.
func (s *CephService) ensureMember(ctx context.Context, p *models.SharedStoragePool, nodeID string) (*models.StoragePool, error) {
	var row models.StoragePool
	if err := s.db.WithContext(ctx).Where("shared_pool_id = ? AND node_id = ?", p.ID, nodeID).Limit(1).Find(&row).Error; err != nil {
		return nil, err
	}
	if row.ID != "" {
		return &row, nil
	}
	row = models.StoragePool{Name: p.Name, Type: models.PoolTypeRBD, Status: "offline", Path: p.Path, FileFormat: "raw", NodeID: nodeID}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return tx.Table("storage_pools").Where("id = ?", row.ID).Update("shared_pool_id", p.ID).Error
	})
	if err != nil {
		return nil, err
	}
	row.SharedPoolID = &p.ID
	return &row, nil
}

// poolNameOnNodes returns the id of one of the nodes that already has a
// storage pool of this name, "" when none has.
func poolNameOnNodes(ctx context.Context, db *gorm.DB, name string, nodeIDs []string) (string, error) {
	if len(nodeIDs) == 0 {
		return "", nil
	}
	var ids []string
	err := db.WithContext(ctx).Model(&models.StoragePool{}).Where("name = ? AND node_id IN ?", name, nodeIDs).Limit(1).Pluck("node_id", &ids).Error
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[0], nil
}

// EnabledOn reports whether any cluster is enabled on a node. The collector
// asks before it starts a poll, so a fleet without Ceph pays one query.
func (s *CephService) EnabledOn(ctx context.Context, nodeID string) bool {
	var n int64
	s.db.WithContext(ctx).Model(&models.CephClusterNode{}).Where("node_id = ?", nodeID).Count(&n)
	return n > 0
}

// Poll checks every cluster enabled on a node. Each check is bounded, and
// the collector runs this beside the node's other work, never before it.
func (s *CephService) Poll(ctx context.Context, node *models.Node) {
	var rows []models.CephClusterNode
	if err := s.db.WithContext(ctx).Where("node_id = ?", node.ID).Find(&rows).Error; err != nil {
		return
	}
	for _, r := range rows {
		if c, err := s.cluster(ctx, r.ClusterID); err == nil {
			s.pollNode(ctx, c, node)
		}
	}
}

// Check asks every node the cluster is enabled on, now, and returns each
// node's verdict ("" passes). It sends the cluster's settings again first, so
// a node that lost its keyring or its libvirt secret gets them back.
func (s *CephService) Check(ctx context.Context, clusterID string) (map[string]string, error) {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	defer lockMembers("ceph:" + c.ID)()
	rows, err := s.enabled(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		node, err := s.node(ctx, r.NodeID)
		if err != nil {
			out[r.NodeID] = "the node no longer exists"
			continue
		}
		if _, err := s.setOnNode(ctx, c, node, false); err != nil && !errors.Is(err, errNoAnswer) {
			// The node answered that it cannot use the settings; checkNode
			// records what it can still do with the ones it has.
			out[r.NodeID] = err.Error()
		}
		if problem := s.checkNode(ctx, c, node); problem != "" {
			out[r.NodeID] = problem
		} else if _, failed := out[r.NodeID]; !failed {
			out[r.NodeID] = ""
		}
	}
	return out, nil
}

// createRBDPool checks and records a shared pool of kind rbd, then gives
// every node the cluster is enabled on its part of it. When nodes are enabled
// and none that answers can list the pool, it is refused with the reason: a
// mistyped pool name is the usual one.
func (s *CephService) createRBDPool(ctx context.Context, p *models.SharedStoragePool) error {
	if p.CephClusterID == nil {
		return refuseShared("choose the Ceph cluster the RBD pool is in")
	}
	c, err := s.cluster(ctx, *p.CephClusterID)
	if err != nil {
		return refuseShared("choose a registered Ceph cluster")
	}
	if !models.ValidRBDName(p.RBDPool) || (p.RBDNamespace != "" && !models.ValidRBDName(p.RBDNamespace)) {
		return refuseShared("an RBD pool or namespace name is 1 to 128 letters, digits, dots, dashes or underscores, starting with a letter or digit")
	}
	defer lockMembers("ceph:" + c.ID)()
	p.Path = models.RBDPoolPath(p.RBDPool, p.RBDNamespace)
	p.NFSServer, p.NFSExport, p.MountOptions = "", "", ""
	// One record per RBD pool, also across two records of one cluster (two
	// cephx users, say): the same images must not be two storage pools.
	var n int64
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM shared_storage_pools s
		JOIN ceph_clusters c ON CAST(c.id AS TEXT) = CAST(s.ceph_cluster_id AS TEXT) AND c.deleted_at IS NULL
		WHERE s.deleted_at IS NULL AND s.rbd_pool = ? AND s.rbd_namespace = ?
			AND (CAST(c.id AS TEXT) = ? OR (c.fsid <> '' AND c.fsid = ?))`,
		p.RBDPool, p.RBDNamespace, c.ID, c.FSID).Scan(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return refuseShared("RBD pool %s of %s is already a storage pool", p.Path, c.Name)
	}
	rows, err := s.enabled(ctx, c.ID)
	if err != nil {
		return err
	}
	nodeIDs := make([]string, len(rows))
	for i, r := range rows {
		nodeIDs[i] = r.NodeID
	}
	if clash, err := poolNameOnNodes(ctx, s.db, p.Name, nodeIDs); err != nil {
		return err
	} else if clash != "" {
		return refuseShared("a node the cluster is enabled on already has a storage pool named %q; choose another name", p.Name)
	}
	var nodes []*models.Node
	answered, usable, firstErr := 0, 0, ""
	for _, id := range nodeIDs {
		node, err := s.node(ctx, id)
		if err != nil {
			continue
		}
		nodes = append(nodes, node)
		resp, err := s.status(ctx, c, node, []models.SharedStoragePool{*p})
		if errors.Is(err, errNoAnswer) {
			continue
		}
		answered++
		switch {
		case err != nil:
			firstErr = firstNonEmpty(firstErr, err.Error())
		case len(resp.Pools) == 1 && resp.Pools[0].Error == "":
			usable++
		case len(resp.Pools) == 1:
			firstErr = firstNonEmpty(firstErr, fmt.Sprintf("node %s cannot use RBD pool %s: %s", node.Name, p.Path, resp.Pools[0].Error))
		}
	}
	if answered > 0 && usable == 0 {
		return refuseShared("%s", firstNonEmpty(firstErr, "no node could check the pool"))
	}
	if err := s.db.WithContext(ctx).Create(p).Error; err != nil {
		return err
	}
	for _, node := range nodes {
		s.checkNode(ctx, c, node)
	}
	return nil
}

// deleteRBDPool removes an RBD pool's record and its members' rows. Nothing
// on the cluster is removed. Refused while a VM or a volume is in it.
func (s *CephService) deleteRBDPool(ctx context.Context, p *models.SharedStoragePool) error {
	if p.CephClusterID != nil {
		// Not while a node's answer is being recorded: that makes member rows.
		defer lockMembers("ceph:" + *p.CephClusterID)()
	}
	var used int64
	if err := s.db.WithContext(ctx).Raw(`SELECT
		(SELECT COUNT(*) FROM vms v WHERE v.deleted_at IS NULL AND CAST(v.storage_pool_id AS TEXT) IN
			(SELECT CAST(id AS TEXT) FROM storage_pools WHERE CAST(shared_pool_id AS TEXT) = ? AND deleted_at IS NULL)) +
		(SELECT COUNT(*) FROM storage_volumes sv WHERE sv.deleted_at IS NULL AND CAST(sv.pool_id AS TEXT) IN
			(SELECT CAST(id AS TEXT) FROM storage_pools WHERE CAST(shared_pool_id AS TEXT) = ? AND deleted_at IS NULL))`,
		p.ID, p.ID).Scan(&used).Error; err != nil {
		return err
	}
	if used > 0 {
		return refuseShared("%d VMs or volumes have disks in %s; move or delete them first", used, p.Name)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("shared_pool_id = ?", p.ID).Delete(&models.StoragePool{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", p.ID).Delete(&models.SharedStoragePool{}).Error
	})
}

// rbdMemberProblem checks, now, that a node can use an RBD pool: it reaches
// the pool's cluster (the same fsid as every member) and lists the pool.
func (s *CephService) rbdMemberProblem(ctx context.Context, p *models.SharedStoragePool, node *models.Node) string {
	if p.CephClusterID == nil {
		return "the pool names no Ceph cluster"
	}
	c, err := s.cluster(ctx, *p.CephClusterID)
	if err != nil {
		return "the pool's Ceph cluster is no longer registered"
	}
	if problem := s.pollNode(ctx, c, node); problem != "" {
		return problem
	}
	var row models.StoragePool
	if err := s.db.WithContext(ctx).Where("shared_pool_id = ? AND node_id = ?", p.ID, node.ID).Limit(1).Find(&row).Error; err != nil {
		return err.Error()
	}
	switch {
	case row.ID == "":
		return fmt.Sprintf("the pool's Ceph cluster is not enabled on node %s", node.Name)
	case row.Status != "online":
		return firstNonEmpty(row.StatusNote, fmt.Sprintf("node %s has not checked the pool yet", node.Name))
	}
	return ""
}
