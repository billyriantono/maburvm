//go:build pg

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

const (
	cephKey   = "bWFidXJ2bS10ZXN0LWtleS1ub3QtcmVhbA=="
	cephFSID  = "aaaaaaaa-1111-2222-3333-444444444444"
	otherFSID = "bbbbbbbb-1111-2222-3333-444444444444"
	cephAlpha = "aaaaaaaa-0000-0000-0000-00000000000a"
	cephBeta  = "aaaaaaaa-0000-0000-0000-00000000000b"
)

// fakeCephNode is a node's agent as far as Ceph goes.
type fakeCephNode struct {
	pb.NodeAgentClient
	// fsid is the cluster the node's monitors lead to; "" is none.
	fsid   string
	health string
	// pools are the RBD pools the client can list there.
	pools map[string]bool
	// down: the agent does not answer. inUse: a VM here has a disk on the
	// cluster. secretLost: libvirt no longer holds the cluster's secret.
	down, inUse, secretLost bool
	// enabled holds the key the node was given, per cluster.
	enabled map[string]string
	calls   []string
	// onStatus runs while the node is being asked for the cluster's status.
	onStatus func()
}

func (n *fakeCephNode) SetCephCluster(_ context.Context, req *pb.SetCephClusterRequest, _ ...grpc.CallOption) (*pb.SetCephClusterResponse, error) {
	if n.down {
		return nil, errors.New("connection refused")
	}
	fail := func(msg string) (*pb.SetCephClusterResponse, error) {
		return &pb.SetCephClusterResponse{Error: &pb.ErrorResponse{Message: msg}}, nil
	}
	if req.Remove {
		n.calls = append(n.calls, "remove")
		if n.inUse {
			return fail("1 VMs on this node have disks on the cluster (vm-1); move or delete them first")
		}
		delete(n.enabled, req.ClusterId)
		return &pb.SetCephClusterResponse{Success: true}, nil
	}
	n.calls = append(n.calls, "set")
	if n.fsid == "" || req.Key != cephKey {
		return fail("the cluster did not answer ceph within 12s")
	}
	if req.Fsid != "" && req.Fsid != n.fsid {
		return fail("these monitors lead to another cluster (fsid " + n.fsid + "), not the one registered (fsid " + req.Fsid + ")")
	}
	n.enabled[req.ClusterId] = req.Key
	n.secretLost = false
	return &pb.SetCephClusterResponse{Success: true, Fsid: n.fsid}, nil
}

func (n *fakeCephNode) CephStatus(_ context.Context, req *pb.CephStatusRequest, _ ...grpc.CallOption) (*pb.CephStatusResponse, error) {
	if hook := n.onStatus; hook != nil {
		n.onStatus = nil
		hook()
	}
	if n.down {
		return nil, errors.New("connection refused")
	}
	fail := func(msg string) (*pb.CephStatusResponse, error) {
		return &pb.CephStatusResponse{Error: &pb.ErrorResponse{Message: msg}}, nil
	}
	if _, ok := n.enabled[req.ClusterId]; !ok {
		return fail("the Ceph cluster is not enabled on this node")
	}
	if n.fsid == "" {
		return fail("the cluster did not answer ceph within 12s")
	}
	resp := &pb.CephStatusResponse{Success: true, Fsid: n.fsid, Health: n.health, TotalBytes: 3000, UsedBytes: 1000, AvailableBytes: 2000}
	if n.health != models.CephHealthOK {
		resp.HealthSummary = "OSD_DOWN: 1 osds down"
	}
	if n.secretLost {
		resp.Problem = "libvirt on this node no longer holds the cluster's secret"
	}
	for _, p := range req.Pools {
		ps := &pb.CephPoolStatus{Pool: p.Pool, Namespace: p.Namespace}
		if n.pools[models.RBDPoolPath(p.Pool, p.Namespace)] {
			ps.Measured, ps.UsedBytes, ps.AvailableBytes = true, 100, 600
		} else {
			ps.Error = "rbd: error opening pool '" + p.Pool + "': (2) No such file or directory"
		}
		resp.Pools = append(resp.Pools, ps)
	}
	return resp, nil
}

type cephFixture struct {
	db          *gorm.DB
	svc         *CephService
	shared      *SharedPoolService
	alpha, beta *fakeCephNode
}

func newCephFixture(t *testing.T) *cephFixture {
	models.SetSecretKeys("0123456789abcdef0123456789abcdef")
	t.Cleanup(func() { models.SetSecretKeys("") })
	db := migrationDB(t)
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, ip_address, token, status, cpu_count, memory_total_bytes) VALUES
		(?, 'alpha', '192.0.2.10', 'ta', 'active', 16, 68719476736), (?, 'beta', '192.0.2.20', 'tb', 'active', 16, 68719476736)`, cephAlpha, cephBeta).Error)
	f := &cephFixture{db: db}
	node := func() *fakeCephNode {
		return &fakeCephNode{fsid: cephFSID, health: models.CephHealthOK, pools: map[string]bool{"vms": true, "vms/tenant-a": true}, enabled: map[string]string{}}
	}
	f.alpha, f.beta = node(), node()
	agent := func(ctx context.Context, n *models.Node) (pb.NodeAgentClient, context.Context, error) {
		if n.ID == cephAlpha {
			return f.alpha, ctx, nil
		}
		return f.beta, ctx, nil
	}
	alerts := NewAlertService(db, nil, alertTestKey)
	f.svc = &CephService{db: db, agent: agent, alerts: alerts}
	f.shared = &SharedPoolService{db: db, agent: agent, alerts: alerts}
	return f
}

func (f *cephFixture) register(t *testing.T) *models.CephCluster {
	c, err := f.svc.Create(context.Background(), CephClusterInput{Name: "ceph-jkt", Monitors: "10.0.0.1, 10.0.0.2:6789", ClientID: "client.maburvm", ClientKey: cephKey})
	require.NoError(t, err)
	return c
}

func (f *cephFixture) rbdPool(t *testing.T, c *models.CephCluster, name, pool, ns string) *models.SharedStoragePool {
	p, err := f.shared.Create(context.Background(), models.SharedStoragePool{Name: name, Kind: models.SharedPoolRBD, CephClusterID: &c.ID, RBDPool: pool, RBDNamespace: ns})
	require.NoError(t, err)
	return p
}

func (f *cephFixture) firing(t *testing.T, key string) bool {
	var n int64
	require.NoError(t, f.db.Model(&models.Alert{}).Where("alert_key = ? AND state = ? AND clear_since IS NULL", key, models.AlertStateFiring).Count(&n).Error)
	return n > 0
}

func TestCephKeyIsSealedAndNeverReturned(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)
	assert.Equal(t, "maburvm", c.ClientID, "client. is dropped")
	assert.Equal(t, "10.0.0.1,10.0.0.2:6789", c.Monitors)

	var stored string
	require.NoError(t, f.db.Raw(`SELECT client_key FROM ceph_clusters WHERE id = ?`, c.ID).Scan(&stored).Error)
	assert.True(t, models.IsSealed(stored), "the key is encrypted at rest")
	assert.NotContains(t, stored, cephKey)

	views, err := f.svc.List(ctx)
	require.NoError(t, err)
	for _, v := range []interface{}{c, views} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		assert.NotContains(t, string(b), cephKey, "no answer carries the key")
		assert.NotContains(t, string(b), "client_key")
	}

	// A change without a key keeps the stored one, still sealed.
	_, err = f.svc.Update(ctx, c.ID, CephClusterInput{Name: "ceph-jakarta", Monitors: "10.0.0.1", ClientID: "maburvm"})
	require.NoError(t, err)
	got, err := f.svc.cluster(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, cephKey, got.ClientKey)
	assert.Equal(t, "ceph-jakarta", got.Name)
	require.NoError(t, f.db.Raw(`SELECT client_key FROM ceph_clusters WHERE id = ?`, c.ID).Scan(&stored).Error)
	assert.True(t, models.IsSealed(stored))

	for name, in := range map[string]CephClusterInput{
		"a key that is not base64":   {Name: "x", Monitors: "10.0.0.1", ClientID: "maburvm", ClientKey: "not a key"},
		"a monitor that is a path":   {Name: "x", Monitors: "/etc/ceph", ClientID: "maburvm", ClientKey: cephKey},
		"a client id with a space":   {Name: "x", Monitors: "10.0.0.1", ClientID: "mabur vm", ClientKey: cephKey},
		"an fsid that is not a UUID": {Name: "x", Monitors: "10.0.0.1", ClientID: "maburvm", ClientKey: cephKey, FSID: "ceph"},
		"a name already registered":  {Name: "ceph-jakarta", Monitors: "10.0.0.1", ClientID: "maburvm", ClientKey: cephKey},
	} {
		_, err := f.svc.Create(ctx, in)
		assert.ErrorIs(t, err, ErrCeph, name)
	}
}

func TestCephEnableRecordsTheFsidAndRefusesAnotherCluster(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)

	// A node id that is not one is not found, not a database error.
	assert.ErrorIs(t, f.svc.EnableNode(ctx, c.ID, "alpha"), ErrNodeNotFound)
	assert.ErrorIs(t, f.svc.DisableNode(ctx, c.ID, "alpha"), ErrCephNotFound)

	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephAlpha))
	assert.Equal(t, cephKey, f.alpha.enabled[c.ID], "the node is given the opened key")
	got, _ := f.svc.cluster(ctx, c.ID)
	assert.Equal(t, cephFSID, got.FSID, "the first node's fsid is recorded")
	assert.Equal(t, models.CephHealthOK, got.Health)
	assert.EqualValues(t, 3000, got.TotalBytes)

	// beta's monitors lead to another cluster.
	f.beta.fsid = otherFSID
	err := f.svc.EnableNode(ctx, c.ID, cephBeta)
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), otherFSID)
	assert.Contains(t, err.Error(), cephFSID)
	assert.Equal(t, []string{"set"}, f.beta.calls, "the node is told which cluster to expect, and refuses another itself")
	assert.Empty(t, f.beta.enabled, "nothing was written on it")
	rows, _ := f.svc.enabled(ctx, c.ID)
	assert.Len(t, rows, 1)

	// A node that cannot reach the cluster at all is refused with the reason.
	f.beta.fsid = ""
	err = f.svc.EnableNode(ctx, c.ID, cephBeta)
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), "did not answer")

	f.beta.fsid = cephFSID
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephBeta))
	assert.ErrorIs(t, f.svc.EnableNode(ctx, c.ID, cephBeta), ErrCeph, "twice is refused")

	// An fsid given at registration holds the first node to it too.
	c2, err := f.svc.Create(ctx, CephClusterInput{Name: "other", Monitors: "10.9.0.1", ClientID: "maburvm", ClientKey: cephKey, FSID: otherFSID})
	require.NoError(t, err)
	assert.ErrorIs(t, f.svc.EnableNode(ctx, c2.ID, cephAlpha), ErrCeph)
}

func TestRBDPoolMembersAreTheClustersNodesAndCountOnce(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephAlpha))

	// A pool no node can list is refused with what rbd said.
	_, err := f.shared.Create(ctx, models.SharedStoragePool{Name: "typo", Kind: models.SharedPoolRBD, CephClusterID: &c.ID, RBDPool: "vmz"})
	require.ErrorIs(t, err, ErrSharedPool)
	assert.Contains(t, err.Error(), "error opening pool 'vmz'")
	_, err = f.shared.Create(ctx, models.SharedStoragePool{Name: "bad", Kind: models.SharedPoolRBD, CephClusterID: &c.ID, RBDPool: "--pool=x"})
	assert.ErrorIs(t, err, ErrSharedPool)

	p := f.rbdPool(t, c, "ceph-vms", "vms", "tenant-a")
	assert.Equal(t, "vms/tenant-a", p.Path)
	_, err = f.shared.Create(ctx, models.SharedStoragePool{Name: "again", Kind: models.SharedPoolRBD, CephClusterID: &c.ID, RBDPool: "vms", RBDNamespace: "tenant-a"})
	assert.ErrorIs(t, err, ErrSharedPool, "one record per RBD pool")
	// Also through a second record of the same cluster (another cephx user).
	c2, err := f.svc.Create(ctx, CephClusterInput{Name: "ceph-jkt-2", Monitors: "10.0.0.1", ClientID: "maburvm2", ClientKey: cephKey, FSID: cephFSID})
	require.NoError(t, err)
	_, err = f.shared.Create(ctx, models.SharedStoragePool{Name: "twice", Kind: models.SharedPoolRBD, CephClusterID: &c2.ID, RBDPool: "vms", RBDNamespace: "tenant-a"})
	assert.ErrorIs(t, err, ErrSharedPool, "the same images are not two storage pools")
	require.NoError(t, f.svc.Delete(ctx, c2.ID))

	// beta joins the pool by being enabled on the cluster, not by hand.
	assert.ErrorIs(t, f.shared.AddMember(ctx, p.ID, cephBeta), ErrSharedPool)
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephBeta))
	var members []models.StoragePool
	require.NoError(t, f.db.Where("shared_pool_id = ?", p.ID).Find(&members).Error)
	require.Len(t, members, 2)
	for _, m := range members {
		assert.Equal(t, models.PoolTypeRBD, m.Type)
		assert.Equal(t, "online", m.Status)
		assert.Equal(t, "vms/tenant-a", m.Path)
		assert.EqualValues(t, 700, m.TotalSpace)
		assert.EqualValues(t, 600, m.AvailableSpace)
	}

	views, err := f.shared.List(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Len(t, views[0].Members, 2)
	assert.EqualValues(t, 700, views[0].TotalSpace, "two nodes, one pool: counted once")
	clusters, err := f.svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	require.Len(t, clusters[0].Pools, 1)
	assert.EqualValues(t, 100, clusters[0].Pools[0].UsedSpace)
	assert.Len(t, clusters[0].Nodes, 2)

	// The pool takes VM disks, but only by choice: being online and the
	// roomiest does not make it the pool for new VMs; staff naming it, or
	// making it the node's pool for new VMs, does.
	require.NoError(t, f.db.Exec(`UPDATE storage_pools SET available_space = 1 << 50 WHERE shared_pool_id = ?`, p.ID).Error)
	assert.Nil(t, PrimaryPool(ctx, f.db, cephAlpha))
	chosen, err := chosenPool(ctx, f.db, members[0].ID, members[0].NodeID)
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(`UPDATE storage_pools SET is_primary = true WHERE id = ?`, members[0].ID).Error)
	if got := PrimaryPool(ctx, f.db, members[0].NodeID); assert.NotNil(t, got) {
		assert.Equal(t, members[0].ID, got.ID)
	}
	// A node is told the pool with its cluster, which the pool's row does not carry.
	poolType, poolPath, err := AgentPoolRef(ctx, f.db, chosen)
	require.NoError(t, err)
	assert.Equal(t, models.PoolTypeRBD, poolType)
	assert.Equal(t, "rbd://"+c.ID+"/vms/tenant-a", poolPath)
	disk := poolPath + "/" + models.RBDDiskName("7b0c9d0e-1111-2222-3333-444444444444", "vda")
	assert.True(t, PoolHoldsDisk(*chosen, disk))
	img, err := models.ParseRBDPath(disk)
	require.NoError(t, err)
	assert.Equal(t, c.ID, img.ClusterID, "the secret every node holds for the cluster has this UUID")
	require.NoError(t, f.db.Exec(`UPDATE storage_pools SET is_primary = false WHERE id = ?`, members[0].ID).Error)

	// A node that loses the pool is not usable there, and says why.
	delete(f.beta.pools, "vms/tenant-a")
	res, err := f.shared.Check(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "", res[cephAlpha])
	assert.Contains(t, res[cephBeta], "cannot use RBD pool vms/tenant-a")
	var beta models.StoragePool
	require.NoError(t, f.db.Where("shared_pool_id = ? AND node_id = ?", p.ID, cephBeta).First(&beta).Error)
	assert.Equal(t, "degraded", beta.Status)
	assert.True(t, f.firing(t, "pool_shared:"+beta.ID))
	alphaNode, _ := f.svc.node(ctx, cephAlpha)
	betaNode, _ := f.svc.node(ctx, cephBeta)
	assert.Error(t, f.shared.ProveBetween(ctx, p.ID, alphaNode, betaNode))
	f.beta.pools["vms/tenant-a"] = true
	assert.NoError(t, f.shared.ProveBetween(ctx, p.ID, alphaNode, betaNode))
}

func TestCephDisableAndUnregisterAreRefusedWhileInUse(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephAlpha))
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephBeta))
	p := f.rbdPool(t, c, "ceph-vms", "vms", "")
	var row string
	require.NoError(t, f.db.Raw(`SELECT id FROM storage_pools WHERE shared_pool_id = ? AND node_id = ?`, p.ID, cephAlpha).Scan(&row).Error)

	const user, tmpl, vm = "11111111-0000-0000-0000-000000000001", "22222222-0000-0000-0000-000000000001", "99999999-0000-0000-0000-000000000001"
	require.NoError(t, f.db.Exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'owner@example.com', 'x')`, user).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES (?, 'debian', '12', '/x', 'c')`, tmpl).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status, storage_pool_id)
		VALUES (?, ?, ?, 'on-ceph', ?, '{"cpu":1,"ram":1024,"disk":10}', 'running', ?)`, vm, user, cephAlpha, tmpl, row).Error)

	err := f.svc.DisableNode(ctx, c.ID, cephAlpha)
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), "1 VMs on node alpha")
	assert.NotContains(t, f.alpha.calls, "remove", "the node is not asked to drop a keyring its VMs need")
	assert.ErrorIs(t, f.shared.Delete(ctx, p.ID), ErrSharedPool, "nor is the pool removed under a VM")
	err = f.svc.Delete(ctx, c.ID)
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), "RBD pools")

	// The panel knows of no VM there any more, but the node still has one.
	require.NoError(t, f.db.Exec(`DELETE FROM vms WHERE id = ?`, vm).Error)
	f.alpha.inUse = true
	err = f.svc.DisableNode(ctx, c.ID, cephAlpha)
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), "vm-1")
	assert.Contains(t, f.alpha.enabled, c.ID)

	f.alpha.inUse = false
	require.NoError(t, f.svc.DisableNode(ctx, c.ID, cephAlpha))
	assert.NotContains(t, f.alpha.enabled, c.ID)
	var left int64
	require.NoError(t, f.db.Model(&models.StoragePool{}).Where("shared_pool_id = ?", p.ID).Count(&left).Error)
	assert.EqualValues(t, 1, left, "alpha's part of the pool went with it; beta's stays")
	assert.ErrorIs(t, f.svc.DisableNode(ctx, c.ID, cephAlpha), ErrCephNotFound)

	require.NoError(t, f.shared.Delete(ctx, p.ID))
	require.NoError(t, f.db.Model(&models.StoragePool{}).Where("shared_pool_id = ?", p.ID).Count(&left).Error)
	assert.Zero(t, left)
	err = f.svc.Delete(ctx, c.ID)
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), "still enabled on 1 nodes")
	require.NoError(t, f.svc.DisableNode(ctx, c.ID, cephBeta))
	require.NoError(t, f.svc.Delete(ctx, c.ID))
	var rows int64
	require.NoError(t, f.db.Raw(`SELECT COUNT(*) FROM ceph_clusters`).Scan(&rows).Error)
	assert.Zero(t, rows, "the sealed key goes with the record")
}

func TestCephPollRecordsHealthAndAlertsOnErrOrUnreachable(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephAlpha))
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephBeta))
	p := f.rbdPool(t, c, "ceph-vms", "vms", "")
	alpha, _ := f.svc.node(ctx, cephAlpha)
	beta, _ := f.svc.node(ctx, cephBeta)
	nodeRow := func(id string) models.CephClusterNode {
		var r models.CephClusterNode
		require.NoError(t, f.db.Where("cluster_id = ? AND node_id = ?", c.ID, id).First(&r).Error)
		return r
	}
	member := func(id string) models.StoragePool {
		var r models.StoragePool
		require.NoError(t, f.db.Where("shared_pool_id = ? AND node_id = ?", p.ID, id).First(&r).Error)
		return r
	}
	assert.True(t, f.svc.EnabledOn(ctx, cephAlpha))

	// A warning is shown, not alerted.
	f.alpha.health = models.CephHealthWarn
	f.svc.Poll(ctx, alpha)
	got, _ := f.svc.cluster(ctx, c.ID)
	assert.Equal(t, models.CephHealthWarn, got.Health)
	assert.Equal(t, "OSD_DOWN: 1 osds down", got.HealthSummary)
	assert.False(t, f.firing(t, "ceph_health:"+c.ID))

	f.alpha.health = models.CephHealthErr
	f.svc.Poll(ctx, alpha)
	assert.True(t, f.firing(t, "ceph_health:"+c.ID))
	f.alpha.health = models.CephHealthOK
	f.svc.Poll(ctx, alpha)
	assert.False(t, f.firing(t, "ceph_health:"+c.ID), "it clears when the cluster is no longer in error")

	// beta loses its way to the cluster: that node only, with the reason.
	f.beta.fsid = ""
	f.svc.Poll(ctx, beta)
	assert.Equal(t, models.CephNodeUnreachable, nodeRow(cephBeta).Status)
	assert.Contains(t, nodeRow(cephBeta).StatusNote, "did not answer")
	assert.Equal(t, "degraded", member(cephBeta).Status)
	assert.True(t, f.firing(t, cephNodeAlertKey(c.ID, cephBeta)))
	assert.Equal(t, models.CephNodeOK, nodeRow(cephAlpha).Status)
	assert.Equal(t, "online", member(cephAlpha).Status)
	got, _ = f.svc.cluster(ctx, c.ID)
	assert.Equal(t, models.CephHealthOK, got.Health, "a node that cannot reach the cluster says nothing of its health")

	// An agent that does not answer has not said the cluster is gone.
	f.beta.fsid, f.beta.down = cephFSID, true
	f.svc.Poll(ctx, beta)
	assert.Equal(t, models.CephNodeUnreachable, nodeRow(cephBeta).Status, "unchanged")
	f.beta.down = false
	f.svc.Poll(ctx, beta)
	assert.Equal(t, models.CephNodeOK, nodeRow(cephBeta).Status)
	assert.Equal(t, "online", member(cephBeta).Status)
	assert.False(t, f.firing(t, cephNodeAlertKey(c.ID, cephBeta)))

	// A node whose monitors now lead elsewhere is a mismatch, and its answer
	// is not taken as this cluster's health.
	f.beta.fsid, f.beta.health = otherFSID, models.CephHealthErr
	f.svc.Poll(ctx, beta)
	assert.Equal(t, models.CephNodeMismatch, nodeRow(cephBeta).Status)
	assert.Equal(t, "degraded", member(cephBeta).Status)
	assert.False(t, f.firing(t, "ceph_health:"+c.ID))
	f.beta.fsid, f.beta.health = cephFSID, models.CephHealthOK

	// A lost libvirt secret is reported, and a check from the panel restores it.
	f.beta.secretLost = true
	f.svc.Poll(ctx, beta)
	assert.Equal(t, models.CephNodeDegraded, nodeRow(cephBeta).Status)
	res, err := f.svc.Check(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{cephAlpha: "", cephBeta: ""}, res)
	assert.Equal(t, models.CephNodeOK, nodeRow(cephBeta).Status)

	// New settings a node refuses change nothing.
	_, err = f.svc.Update(ctx, c.ID, CephClusterInput{Name: "ceph-jkt", Monitors: "10.0.0.1", ClientID: "maburvm", ClientKey: "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="})
	require.ErrorIs(t, err, ErrCeph)
	assert.Contains(t, err.Error(), "Nothing was changed")
	got, _ = f.svc.cluster(ctx, c.ID)
	assert.Equal(t, cephKey, got.ClientKey)
	assert.Equal(t, "10.0.0.1,10.0.0.2:6789", got.Monitors)
}

// A change refused by one node leaves a node that could not be asked as it
// was: no "has not received the new settings" note or alert for settings that
// were never saved.
func TestCephRefusedUpdateLeavesUnaskedNodesAlone(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephAlpha))
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephBeta))

	f.alpha.down = true // asked first, does not answer; beta then refuses the key
	_, err := f.svc.Update(ctx, c.ID, CephClusterInput{Name: "ceph-jkt", Monitors: "10.0.0.1", ClientID: "maburvm", ClientKey: "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="})
	require.ErrorIs(t, err, ErrCeph)
	var row models.CephClusterNode
	require.NoError(t, f.db.Where("cluster_id = ? AND node_id = ?", c.ID, cephAlpha).First(&row).Error)
	assert.Equal(t, models.CephNodeOK, row.Status)
	assert.Empty(t, row.StatusNote)
	assert.False(t, f.firing(t, cephNodeAlertKey(c.ID, cephAlpha)))

	// Saved through beta: alpha is told it is behind.
	_, err = f.svc.Update(ctx, c.ID, CephClusterInput{Name: "ceph-jkt", Monitors: "10.0.0.1", ClientID: "maburvm"})
	require.NoError(t, err)
	require.NoError(t, f.db.Where("cluster_id = ? AND node_id = ?", c.ID, cephAlpha).First(&row).Error)
	assert.Equal(t, models.CephNodeUnreachable, row.Status)
	assert.Contains(t, row.StatusNote, "has not received")
	assert.True(t, f.firing(t, cephNodeAlertKey(c.ID, cephAlpha)))
}

// A poll asks the node without holding the cluster's lock. Staff disabling
// the cluster on that node meanwhile must win: the late answer brings back
// neither the node's part of the pool nor an alert nobody would clear.
func TestCephPollAnswerAfterDisableChangesNothing(t *testing.T) {
	f := newCephFixture(t)
	ctx := context.Background()
	c := f.register(t)
	require.NoError(t, f.svc.EnableNode(ctx, c.ID, cephAlpha))
	p := f.rbdPool(t, c, "ceph-vms", "vms", "")
	alpha, _ := f.svc.node(ctx, cephAlpha)

	f.alpha.onStatus = func() { require.NoError(t, f.svc.DisableNode(ctx, c.ID, cephAlpha)) }
	f.svc.Poll(ctx, alpha)

	var rows, members int64
	require.NoError(t, f.db.Model(&models.CephClusterNode{}).Where("cluster_id = ?", c.ID).Count(&rows).Error)
	require.NoError(t, f.db.Model(&models.StoragePool{}).Where("shared_pool_id = ?", p.ID).Count(&members).Error)
	assert.Zero(t, rows)
	assert.Zero(t, members)
	assert.False(t, f.firing(t, cephNodeAlertKey(c.ID, cephAlpha)))
}
