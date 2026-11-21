package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

func TestNextDeleteLock(t *testing.T) {
	cases := []struct {
		name           string
		current        string
		lock, byStaff  bool
		want           string
		refusedByStaff bool
	}{
		{"owner locks", "", true, false, models.VMDeleteLockOwner, false},
		{"owner unlocks own", models.VMDeleteLockOwner, false, false, "", false},
		{"owner cannot lift staff's", models.VMDeleteLockStaff, false, false, models.VMDeleteLockStaff, true},
		{"owner locking a staff lock keeps it", models.VMDeleteLockStaff, true, false, models.VMDeleteLockStaff, false},
		{"staff lock replaces owner's", models.VMDeleteLockOwner, true, true, models.VMDeleteLockStaff, false},
		{"staff lifts owner's", models.VMDeleteLockOwner, false, true, "", false},
		{"staff lifts own", models.VMDeleteLockStaff, false, true, "", false},
	}
	for _, c := range cases {
		got, err := nextDeleteLock(c.current, c.lock, c.byStaff)
		assert.Equal(t, c.want, got, c.name)
		assert.Equal(t, c.refusedByStaff, errors.Is(err, ErrVMDeleteLocked), c.name)
	}
}

// An owner's lock stops everyone but the billing system; a staff lock stops it too.
func TestDeleteLockErrBilling(t *testing.T) {
	ctx, billing := context.Background(), AsBillingSystem(context.Background())
	owner := &models.VM{DeleteLock: models.VMDeleteLockOwner}
	staff := &models.VM{DeleteLock: models.VMDeleteLockStaff}
	assert.ErrorIs(t, deleteLockErr(ctx, owner), ErrVMDeleteLocked)
	assert.NoError(t, deleteLockErr(billing, owner))
	assert.ErrorIs(t, deleteLockErr(billing, staff), ErrVMDeleteLocked)
	assert.NoError(t, deleteLockErr(ctx, &models.VM{}))
}

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{" Web ", "db", "web", "", "env:prod"})
	require.NoError(t, err)
	assert.Equal(t, []string{"db", "env:prod", "web"}, got)

	for _, bad := range [][]string{{"has space"}, {"-dash-first"}, {strings.Repeat("a", 33)}, {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}} {
		_, err := NormalizeTags(bad)
		assert.ErrorIs(t, err, ErrInvalidTags, "%v", bad)
	}
	empty, err := NormalizeTags(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{}, empty, "no tags is an empty list, not null")
}

func TestNodeGroups(t *testing.T) {
	g, err := NormalizeNodeGroup(" NVMe ")
	require.NoError(t, err)
	assert.Equal(t, "nvme", g)
	_, err = NormalizeNodeGroup("bad group")
	assert.ErrorIs(t, err, ErrInvalidNodeGroup)

	nodes := []models.Node{{ID: "a", NodeGroup: "nvme"}, {ID: "b"}, {ID: "c", NodeGroup: "nvme"}}
	all, err := inNodeGroup(nodes, "")
	require.NoError(t, err)
	assert.Len(t, all, 3)
	in, err := inNodeGroup(nodes, "nvme")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c"}, []string{in[0].ID, in[1].ID})
	_, err = inNodeGroup(nodes, "hdd")
	assert.ErrorIs(t, err, ErrNoAvailableNodes, "a group with no node refuses rather than placing elsewhere")
}

func TestVersions(t *testing.T) {
	cases := []struct {
		in string
		ok bool
		v  [3]int
	}{
		{"v1.2.3", true, [3]int{1, 2, 3}},
		{"1.10.0-rc1", true, [3]int{1, 10, 0}},
		{"v1.2.3-4-gabcdef", true, [3]int{1, 2, 3}},
		{"dev", false, [3]int{}},
	}
	for _, c := range cases {
		v, ok := parseVersion(c.in)
		assert.Equal(t, c.ok, ok, c.in)
		assert.Equal(t, c.v, v, c.in)
	}
	assert.Equal(t, 1, compareVersions([3]int{1, 10, 0}, [3]int{1, 9, 9}))
	assert.Equal(t, 0, compareVersions([3]int{1, 2, 3}, [3]int{1, 2, 3}))
	assert.Equal(t, -1, compareVersions([3]int{0, 9, 0}, [3]int{1, 0, 0}))
}

// The newest published release wins over drafts, pre-releases and order in
// the feed; a security fix in any release newer than this build is reported.
func TestFetchReleases(t *testing.T) {
	var gotUA, gotPath string
	feed := `[
			{"tag_name":"v2.0.0-rc1","prerelease":true,"body":"x"},
			{"tag_name":"v1.4.0","name":"1.4","html_url":"https://example.test/r/1.4","body":"Faster backups","published_at":"2026-11-01T00:00:00Z"},
			{"tag_name":"v1.3.1","body":"Fixes a security problem in the console (CVE-2026-0001)"},
			{"tag_name":"v9.9.9","draft":true},
			{"tag_name":"v1.2.0","body":"security: old"}
		]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotPath = r.UserAgent(), r.URL.Path
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()
	old := releaseFeed
	releaseFeed = srv.URL + "/repos/%s/releases"
	defer func() { releaseFeed = old }()

	latest, security, err := fetchReleases(context.Background(), "owner/maburvm", "v1.3.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.4.0", latest.Tag)
	assert.Equal(t, "https://example.test/r/1.4", latest.URL)
	assert.True(t, security, "v1.3.1 is newer than v1.3.0 and fixes a security problem")
	assert.Equal(t, "maburvm-panel", gotUA)
	assert.Equal(t, "/repos/owner/maburvm/releases", gotPath)

	_, security, err = fetchReleases(context.Background(), "owner/maburvm", "v1.3.1")
	require.NoError(t, err)
	assert.False(t, security, "the security release is already installed")

	feed = `[{"tag_name":"v1.5.0","html_url":"javascript:alert(1)"}]`
	latest, _, err = fetchReleases(context.Background(), "owner/maburvm", "v1.3.0")
	require.NoError(t, err)
	assert.Empty(t, latest.URL, "only an https link is kept")
}

// Two sends of one announcement at once would both email the same people.
func TestEmailAnnouncementOneRunAtATime(t *testing.T) {
	id := "33333333-3333-3333-3333-333333333333"
	announcementSending.Store(id, true)
	t.Cleanup(func() { announcementSending.Delete(id) })
	_, err := EmailAnnouncement(context.Background(), nil, id, 60)
	assert.ErrorIs(t, err, ErrAnnouncementSending)
}

func TestAnnouncementInput(t *testing.T) {
	ok := AnnouncementInput{Title: "Maintenance on node A", Body: "We reboot it on Sunday."}
	require.NoError(t, ok.normalize())
	assert.Equal(t, AnnouncementService, ok.Kind)
	assert.Equal(t, AudienceAll, ok.Audience.Type)

	start := time.Now()
	end := start.Add(-time.Hour)
	for _, c := range []struct {
		name string
		in   AnnouncementInput
	}{
		{"no title", AnnouncementInput{Body: "x"}},
		{"two-line title", AnnouncementInput{Title: "a\nb", Body: "x"}},
		{"no body", AnnouncementInput{Title: "t"}},
		{"bad kind", AnnouncementInput{Title: "t", Body: "x", Kind: "spam"}},
		{"no ids", AnnouncementInput{Title: "t", Body: "x", Audience: Audience{Type: AudienceNodes}}},
		{"bad id", AnnouncementInput{Title: "t", Body: "x", Audience: Audience{Type: AudienceVMs, IDs: []string{"nope"}}}},
		{"bad audience", AnnouncementInput{Title: "t", Body: "x", Audience: Audience{Type: "friends"}}},
		{"ends first", AnnouncementInput{Title: "t", Body: "x", StartsAt: &start, EndsAt: &end}},
	} {
		assert.ErrorIs(t, c.in.normalize(), ErrInvalidAnnouncement, c.name)
	}
}

func TestUnsubscribeToken(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	id := "11111111-1111-1111-1111-111111111111"
	token := UnsubscribeToken(id)
	got, err := unsubscribeUser(token)
	require.NoError(t, err)
	assert.Equal(t, id, got)
	for _, bad := range []string{"", id, id + ".", "22222222-2222-2222-2222-222222222222." + strings.SplitN(token, ".", 2)[1], token + "x"} {
		_, err := unsubscribeUser(bad)
		assert.ErrorIs(t, err, ErrBadUnsubscribeToken, bad)
	}
}
