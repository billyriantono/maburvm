package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
)

func TestNodeVLANBridges(t *testing.T) {
	ctx := context.Background()
	db := setupVMIPAMTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE nodes ADD COLUMN vlan_bridges TEXT`).Error)
	_, node, _ := seedVMIPAMDependencies(t, db)
	svc := NewNodeService(repository.NewNodeRepository(db), db)

	got, err := svc.GetVLANBridges(ctx, node.ID)
	require.NoError(t, err)
	require.True(t, got.UseEnvironment, "nothing set: the agent's environment decides")

	require.NoError(t, db.Exec(`UPDATE nodes SET vlan_bridges = 'br1:bond0,br2:eth3' WHERE id = ?`, node.ID).Error)
	got, err = svc.GetVLANBridges(ctx, node.ID)
	require.NoError(t, err)
	require.False(t, got.UseEnvironment)
	require.Equal(t, []VLANBridge{{Bridge: "br1", Uplink: "bond0"}, {Bridge: "br2", Uplink: "eth3"}}, got.Bridges)

	for name, bridges := range map[string][]VLANBridge{
		"bad name":     {{Bridge: "br1;reboot", Uplink: "bond0"}},
		"own uplink":   {{Bridge: "br1", Uplink: "br1"}},
		"named twice":  {{Bridge: "br1", Uplink: "bond0"}, {Bridge: "br1", Uplink: "eth3"}},
		"empty uplink": {{Bridge: "br1"}},
	} {
		_, err := svc.SetVLANBridges(ctx, node.ID, &NodeVLANBridges{Bridges: bridges})
		require.ErrorIs(t, err, ErrInvalidPolicy, name)
	}
	_, err = svc.GetVLANBridges(ctx, "00000000-0000-0000-0000-000000000000")
	require.ErrorIs(t, err, ErrNodeNotFound)
}
