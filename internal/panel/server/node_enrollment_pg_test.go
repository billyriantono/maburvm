//go:build pg

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
)

// 078 applies on a database built from every earlier migration, again on top
// of itself, down and up; an install token is used once however many
// installers race for it, and a node that has reported refuses one.
func TestNodeEnrollmentOnPostgres(t *testing.T) {
	db, raw, _ := pgMigratedDB(t)
	ctx := context.Background()
	up, down := migrationSQL(t, "078_node_enrollment_preflight.up.sql"), migrationSQL(t, "078_node_enrollment_preflight.down.sql")
	_, err := raw.Exec(up)
	require.NoError(t, err, "078 run twice")
	_, err = raw.Exec(down)
	require.NoError(t, err, "078 down")
	_, err = raw.Exec(up)
	require.NoError(t, err, "078 up again")

	fresh, live := uuid.NewString(), uuid.NewString()
	_, err = raw.Exec(`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ($1, 'node-new', '192.0.2.20', 'offline', 'old-token')`, fresh)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO nodes (id, name, ip_address, status, token, last_report_at) VALUES ($1, 'node-live', '192.0.2.21', 'active', 'live-token', NOW())`, live)
	require.NoError(t, err)

	_, err = service.IssueNodeEnrollment(ctx, db, live, "")
	assert.True(t, errors.Is(err, service.ErrNodeHasAgent), "a node that has reported gets no install token: %v", err)

	enr, err := service.IssueNodeEnrollment(ctx, db, fresh, uuid.NewString())
	require.NoError(t, err)
	pending, err := service.CheckNodeEnrollment(ctx, db, enr.Token)
	require.NoError(t, err)
	assert.Equal(t, "node-new", pending.NodeName)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []*service.EnrolledNode
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := service.RedeemNodeEnrollment(ctx, db, enr.Token, "198.51.100.9"); err == nil {
				mu.Lock()
				winners = append(winners, got)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Len(t, winners, 1, "one install token, one credential")
	node, err := repository.NewNodeRepository(db).GetByID(ctx, fresh)
	require.NoError(t, err)
	assert.Equal(t, winners[0].AgentToken, node.Token, "the node now authenticates with the credential handed out")

	// The preflight report column round-trips through the service.
	svc := service.NewNodeService(repository.NewNodeRepository(db), db)
	rep, err := svc.StoredPreflight(ctx, fresh)
	require.NoError(t, err)
	assert.Nil(t, rep.CheckedAt)
	_, err = raw.Exec(`UPDATE nodes SET preflight = $1, preflight_at = $2 WHERE id = $3`,
		`{"supported":true,"checks":[{"id":"kvm_device","name":"KVM device","status":"pass","detail":"/dev/kvm is present."}]}`, time.Now(), fresh)
	require.NoError(t, err)
	rep, err = svc.StoredPreflight(ctx, fresh)
	require.NoError(t, err)
	require.Len(t, rep.Checks, 1)
	assert.NotNil(t, rep.CheckedAt)

	ok, err := svc.AcceptsInstaller(ctx, live)
	require.NoError(t, err)
	assert.False(t, ok)

	// A token that checked out while the node was new stops checking out
	// once an agent answers for the node.
	enr, err = service.IssueNodeEnrollment(ctx, db, fresh, "")
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET cert_fingerprint = 'pinned' WHERE id = $1`, fresh)
	require.NoError(t, err)
	_, err = service.CheckNodeEnrollment(ctx, db, enr.Token)
	assert.True(t, errors.Is(err, service.ErrNodeHasAgent), "check after an agent answered: %v", err)

	// The pools and bridges the panel asks the node to check, on the real schema.
	for _, q := range []string{
		`INSERT INTO storage_pools (name, type, path, node_id) VALUES ('vm', 'dir', '/vm', '` + fresh + `'), ('lv', 'lvm', 'vg0', '` + fresh + `')`,
		`INSERT INTO ip_pools (name, cidr, gateway, bridge, node_id) VALUES ('own', '192.0.2.0/24', '192.0.2.1', 'br0', '` + fresh + `')`,
		`INSERT INTO ip_pools (name, cidr, gateway, bridge) VALUES ('global', '198.51.100.0/24', '198.51.100.1', 'brglobal')`,
	} {
		_, err = raw.Exec(q)
		require.NoError(t, err, q)
	}
	pools, bridges := service.PreflightTargets(ctx, db, fresh)
	assert.Equal(t, []string{"/vm"}, pools)
	assert.Equal(t, []string{"br0", "brglobal"}, bridges)
}
