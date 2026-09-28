package service

import (
	"context"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDelegatedPrefix(t *testing.T) {
	cases := []struct {
		cidr    string
		n, idx  int
		want    string
		wantErr bool
	}{
		{"2001:db8:20::/48", 64, 0, "2001:db8:20::/64", false},
		{"2001:db8:20::/48", 64, 1, "2001:db8:20:1::/64", false},
		{"2001:db8:20::/48", 64, 5, "2001:db8:20:5::/64", false},
		{"2001:db8:20::/48", 64, 0x1234, "2001:db8:20:1234::/64", false},
		{"2001:db8:20::/48", 64, 65535, "2001:db8:20:ffff::/64", false},
		{"2001:db8:20::/48", 64, 65536, "", true}, // past the /48
		{"2001:db8:20::/48", 48, 1, "", true},     // not inside the pool
		{"2001:db8::/32", 48, 0x1001, "2001:db8:1001::/48", false},
		{"2001:db8:ffff::/48", 64, 3, "2001:db8:ffff:3::/64", false},
		{"203.0.113.0/24", 64, 1, "", true},
	}
	for _, c := range cases {
		got, err := delegatedPrefix(c.cidr, c.n, c.idx)
		if c.wantErr {
			require.Error(t, err, "%+v", c)
			continue
		}
		require.NoError(t, err, "%+v", c)
		require.Equal(t, c.want, got)
	}
}

func TestIPv6PrefixAddress(t *testing.T) {
	require.Equal(t, "2001:db8:20:5::1/64", models.IPv6PrefixAddress("2001:db8:20:5::/64"))
	require.Equal(t, "", models.IPv6PrefixAddress("203.0.113.0/24"))
}

func TestLowestFreeIdx(t *testing.T) {
	require.Equal(t, 1, lowestFreeIdx(nil, 65536))
	require.Equal(t, 3, lowestFreeIdx([]int{1, 2}, 65536))
	require.Equal(t, 2, lowestFreeIdx([]int{1, 3, 4}, 65536)) // gap reused first
	require.Equal(t, 1, lowestFreeIdx([]int{2, 3}, 65536))
	require.Equal(t, -1, lowestFreeIdx([]int{1, 2, 3}, 4))
	require.Equal(t, -1, lowestFreeIdx(nil, 1))
}

func TestValidateDelegatedPrefixLen(t *testing.T) {
	require.NoError(t, validateDelegatedPrefixLen(models.IPFamilyIPv6, "2001:db8:20::/48", 64))
	require.Error(t, validateDelegatedPrefixLen(models.IPFamilyIPv4, "203.0.113.0/24", 64))
	require.Error(t, validateDelegatedPrefixLen(models.IPFamilyIPv6, "", 64))
	require.Error(t, validateDelegatedPrefixLen(models.IPFamilyIPv6, "2001:db8:20::/64", 64))
	require.Error(t, validateDelegatedPrefixLen(models.IPFamilyIPv6, "2001:db8:20::/48", 80))
}

func TestIPv6PrefixAllocationLowestFreeWithGaps(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()
	n := 64

	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{
		Name: "v6", Family: models.IPFamilyIPv6, CIDR: "2001:db8:20::/48", DelegatedPrefixLen: &n,
	})
	require.NoError(t, err)
	require.Equal(t, models.IPv6LinkLocalGateway, pool.Gateway, "delegated pools default to the link-local gateway")
	addrs, err := svc.ListAddresses(ctx, pool.ID, 0, 0)
	require.NoError(t, err)
	require.Empty(t, addrs, "a delegated pool must not pre-generate address rows")

	vm := func(i byte) string { return "00000000-0000-0000-0000-0000000000" + string('0'+i) + "0" }
	alloc := func(id string) *models.VMIPv6Prefix {
		var p *models.VMIPv6Prefix
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			p, err = svc.AllocateIPv6PrefixInTx(ctx, tx, pool.ID, id, "")
			return err
		}))
		return p
	}

	// idx 0 (the link network) is never delegated.
	require.Equal(t, "2001:db8:20:1::/64", alloc(vm(1)).Prefix)
	require.Equal(t, "2001:db8:20:2::/64", alloc(vm(2)).Prefix)
	require.Equal(t, "2001:db8:20:3::/64", alloc(vm(3)).Prefix)

	// A VM gets exactly one prefix.
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.AllocateIPv6PrefixInTx(ctx, tx, pool.ID, vm(2), "")
		return err
	})
	require.ErrorIs(t, err, ErrVMAlreadyHasIPv6Prefix)

	// Releasing the middle one opens a gap that the next allocation fills.
	require.NoError(t, svc.ReleaseIPv6PrefixByVMInTx(ctx, db, vm(2)))
	got, err := svc.GetVMIPv6Prefix(ctx, vm(2))
	require.NoError(t, err)
	require.Nil(t, got)
	require.Equal(t, "2001:db8:20:2::/64", alloc(vm(4)).Prefix)
	require.Equal(t, "2001:db8:20:4::/64", alloc(vm(5)).Prefix)

	p, err := svc.GetVMIPv6Prefix(ctx, vm(5))
	require.NoError(t, err)
	require.Equal(t, "2001:db8:20:4::1/64", p.Address())

	pool, err = svc.GetPool(ctx, pool.ID)
	require.NoError(t, err)
	require.EqualValues(t, 4, pool.DelegatedCount)

	// A plain pool cannot delegate.
	v4, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "v4", Family: models.IPFamilyIPv4})
	require.NoError(t, err)
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.AllocateIPv6PrefixInTx(ctx, tx, v4.ID, vm(6), "")
		return err
	})
	require.ErrorIs(t, err, ErrPoolNotDelegated)
}

func TestIPv6PrefixAllocationRespectsNodeBinding(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()
	n := 64
	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{
		Name: "v6", Family: models.IPFamilyIPv6, CIDR: "2001:db8:20::/48", DelegatedPrefixLen: &n,
		NodeIDs: []string{"node-a"},
	})
	require.NoError(t, err)
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.AllocateIPv6PrefixInTx(ctx, tx, pool.ID, "vm-1", "node-b")
		return err
	})
	require.ErrorIs(t, err, ErrPoolNotAvailableOnNode)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.AllocateIPv6PrefixInTx(ctx, tx, pool.ID, "vm-1", "node-a")
		return err
	}))
}

func TestPrefixIdxAndLinkSkip(t *testing.T) {
	idx, err := prefixIdx("2001:db8:21::/48", 64, "2001:db8:21:104::/64")
	if err != nil || idx != 0x104 {
		t.Fatalf("idx %d err %v", idx, err)
	}
	for _, bad := range []string{"2001:db8:22:104::/64", "2001:db8:21:104::/63", "2001:db8:21:104::1/64", "10.0.0.0/64"} {
		if _, err := prefixIdx("2001:db8:21::/48", 64, bad); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
	got, _ := delegatedPrefix("2001:db8:21::/48", 64, idx)
	if got != "2001:db8:21:104::/64" {
		t.Fatalf("round trip %s", got)
	}
	// the link network is skipped like a taken prefix
	used := insertSorted([]int{1, 2, 3}, 2)
	if len(used) != 3 {
		t.Fatalf("duplicate inserted: %v", used)
	}
	if n := lowestFreeIdx(insertSorted([]int{1, 2}, 3), 1<<16); n != 4 {
		t.Fatalf("want 4, got %d", n)
	}
}
