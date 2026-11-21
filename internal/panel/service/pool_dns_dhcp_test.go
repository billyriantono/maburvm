package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// A pool's resolvers are checked and stored canonical; DHCP needs an IPv4
// pool with a gateway, and only switching it tells the nodes.
func TestPoolNameserversAndDHCP(t *testing.T) {
	ctx := context.Background()
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	var told []string
	svc.SetPoolGuestsChanged(func(_ context.Context, id string) { told = append(told, id) })

	_, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "bad", CIDR: "192.0.2.0/28", Gateway: "192.0.2.1", Nameservers: "ns.example"})
	require.Error(t, err)
	_, err = svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "nogw", CIDR: "192.0.2.0/28", DHCP: true})
	require.ErrorContains(t, err, "gateway")
	_, err = svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "v6", Family: models.IPFamilyIPv6, CIDR: "2001:db8::/120", Gateway: "2001:db8::1", DHCP: true})
	require.ErrorContains(t, err, "IPv4")

	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "p", CIDR: "192.0.2.0/28", Gateway: "192.0.2.1", Nameservers: "9.9.9.9 149.112.112.112"})
	require.NoError(t, err)
	require.Equal(t, "9.9.9.9,149.112.112.112", pool.Nameservers)

	ns := "1.1.1.1"
	_, err = svc.UpdatePool(ctx, pool.ID, &UpdateIPPoolRequest{Nameservers: &ns})
	require.NoError(t, err)
	require.Empty(t, told, "new resolvers reach guests at their next setup, not by a push")
	on := true
	updated, err := svc.UpdatePool(ctx, pool.ID, &UpdateIPPoolRequest{DHCP: &on})
	require.NoError(t, err)
	require.True(t, updated.DHCP)
	require.Equal(t, []string{pool.ID}, told)
	_, err = svc.UpdatePool(ctx, pool.ID, &UpdateIPPoolRequest{DHCP: &on})
	require.NoError(t, err)
	require.Len(t, told, 1, "an unchanged switch tells nobody")
}

// Further IPv6 addresses live inside the VM's prefix, need an agent that can
// configure them, and are kept on the prefix.
func TestSetIPv6Addresses(t *testing.T) {
	ctx := context.Background()
	f := newNICFixture(t)
	v6pool := &models.IPPool{Name: "v6", Family: models.IPFamilyIPv6, CIDR: "2001:db8:20::/48", Gateway: models.IPv6LinkLocalGateway}
	require.NoError(t, f.db.Create(v6pool).Error)

	_, err := f.svc.SetIPv6Addresses(ctx, f.vm.ID, []string{"2001:db8:20:5::25"})
	require.ErrorIs(t, err, ErrNoRoutedIPv6)

	require.NoError(t, f.db.Create(&models.VMIPv6Prefix{PoolID: v6pool.ID, VMID: f.vm.ID, Prefix: "2001:db8:20:5::/64", Idx: 5}).Error)
	_, err = f.svc.SetIPv6Addresses(ctx, f.vm.ID, []string{"2001:db8:20:6::25"})
	require.ErrorIs(t, err, ErrInvalidPolicy)
	_, err = f.svc.SetIPv6Addresses(ctx, f.vm.ID, []string{"2001:db8:20:5::25"})
	require.ErrorIs(t, err, ErrIPv6ExtraUnsupported, "the node's agent lacks the feature")

	f.agent.features = append(f.agent.features, models.FeatureIPv6Extra)
	_, _ = f.svc.SetIPv6Addresses(ctx, f.vm.ID, []string{"2001:db8:20:5::25"}) // no job queue here: the push fails, the list is kept
	got, err := f.svc.GetIPv6Addresses(ctx, f.vm.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"2001:db8:20:5::25"}, got.Extra)
	require.Equal(t, "2001:db8:20:5::1/64", got.Address)
}
