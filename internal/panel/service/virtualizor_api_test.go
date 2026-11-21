package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The listing each fixture holds its records under, as the admin API sends it.
var vzFixtureLists = []struct{ act, field string }{
	{"servers", "servs"}, {"users", "users"}, {"plans", "plans"}, {"ippool", "ippools"}, {"ips", "ips"}, {"vs", "vs"},
}

// vzFixtureMaster answers admin API calls from the recorded fixtures under
// testdata/virtualizor, paging them the way a master does.
type vzFixtureMaster struct {
	*httptest.Server
	key, pass string
	// capPage is the most records a page holds whatever reslen asks; 0 honours reslen.
	capPage int
	// ignorePaging sends every record on every page.
	ignorePaging bool

	mu    sync.Mutex
	calls []string // the query string of each call
}

func newVZFixtureMaster(t *testing.T) *vzFixtureMaster {
	t.Helper()
	m := &vzFixtureMaster{key: "fixture-key", pass: "fixture-pass-s3cret"}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func (m *vzFixtureMaster) writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (m *vzFixtureMaster) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m.mu.Lock()
	m.calls = append(m.calls, r.URL.RawQuery)
	m.mu.Unlock()
	if r.URL.Path != "/index.php" || q.Get("api") != "json" {
		http.NotFound(w, r)
		return
	}
	if q.Get("adminapikey") != m.key || q.Get("adminapipass") != m.pass {
		// The shape a master answers bad credentials with.
		m.writeJSON(w, map[string]interface{}{"title": "Login", "error": map[string]string{"0": "The API key or pass is not valid"}})
		return
	}
	field := ""
	for _, l := range vzFixtureLists {
		if l.act == q.Get("act") {
			field = l.field
		}
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "virtualizor", q.Get("act")+".json"))
	if err != nil || field == "" {
		m.writeJSON(w, map[string]interface{}{"error": []string{"Unknown action"}})
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("reslen"))
	if page > 0 && size > 0 && !m.ignorePaging {
		var recs map[string]json.RawMessage
		_ = json.Unmarshal(doc[field], &recs)
		keys := make([]string, 0, len(recs))
		for k := range recs {
			keys = append(keys, k)
		}
		sortNumeric(keys)
		if m.capPage > 0 && size > m.capPage {
			size = m.capPage
		}
		lo, hi := min((page-1)*size, len(keys)), min(page*size, len(keys))
		if lo == hi {
			doc[field] = json.RawMessage("[]") // PHP sends an empty listing as an array
		} else {
			out := map[string]json.RawMessage{}
			for _, k := range keys[lo:hi] {
				out[k] = recs[k]
			}
			doc[field], _ = json.Marshal(out)
		}
	}
	m.writeJSON(w, doc)
}

func (m *vzFixtureMaster) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func withPageSize(t *testing.T, n int) {
	old := vzPageSize
	vzPageSize = n
	t.Cleanup(func() { vzPageSize = old })
}

func TestVirtualizorAPIReadsTheFixtures(t *testing.T) {
	withPageSize(t, 2)
	m := newVZFixtureMaster(t)
	a, err := NewVirtualizorAPI(m.URL+"/index.php", m.key, m.pass, "")
	require.NoError(t, err)

	n, err := a.TestConnection(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	snap, err := a.Fetch(context.Background())
	require.NoError(t, err)
	for _, c := range m.calls {
		q, err := url.ParseQuery(c)
		require.NoError(t, err)
		assert.False(t, q.Has("apikey") || q.Has("apipass"), "the end-user API's parameter names are not sent: %s", c)
	}

	require.Len(t, snap.Servers, 3)
	assert.Equal(t, SourceServer{ID: "0", Name: "localhost", IP: "192.0.2.10"}, snap.Servers[0], "a numeric serid reads as text")

	ids := func(n int, id func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = id(i)
		}
		return out
	}
	assert.Equal(t, []string{"55", "56", "57", "58", "59", "60", "61"}, ids(len(snap.Users), func(i int) string { return snap.Users[i].ID }),
		"every page is read, and the owner missing from the listing comes from its VM")
	assert.Equal(t, SourceUser{ID: "55", Email: "ana@example.com", Name: "Ana Putri"}, snap.Users[0])
	assert.True(t, snap.Users[5].Admin)
	assert.Equal(t, SourceUser{ID: "61", Email: "dewi@example.com"}, snap.Users[6])

	require.Len(t, snap.Plans, 4)
	assert.Equal(t, SourcePlan{ID: "1", Name: "KVM 1G", Virt: "kvm", CPU: 1, RAMMB: 1024, DiskGB: 20, BandwidthGB: 1000, SpeedKBs: 12800}, snap.Plans[0])

	require.Len(t, snap.Pools, 4)
	p := snap.Pools[0]
	assert.Equal(t, []string{"9.9.9.9", "149.112.112.112"}, p.DNS)
	assert.Equal(t, []string{"0"}, p.ServerIDs, "the pool's server is ipp_serid")
	assert.Equal(t, []SourceAddress{{IP: "203.0.113.10", VMID: "37"}, {IP: "203.0.113.11"}, {IP: "203.0.113.12", VMID: "38"},
		{IP: "203.0.113.13", VMID: "41"}, {IP: "203.0.113.14", VMID: "45"}, {IP: "198.18.0.5"}}, p.Addresses)
	assert.True(t, snap.Pools[1].IPv6)
	assert.Equal(t, []string{"1"}, snap.Pools[3].ServerIDs)

	require.Len(t, snap.VMs, 9)
	v := snap.VMs[0]
	assert.Equal(t, "37", v.ID)
	assert.Equal(t, "v1001", v.Name)
	assert.Equal(t, "0", v.ServerID)
	assert.Equal(t, []string{"203.0.113.10", "2001:db8:30:1::10"}, v.IPs, "addresses come from the VM and from the IP listing, once each")
	assert.True(t, snap.VMs[1].Suspended)
	assert.Equal(t, []string{"203.0.113.12"}, snap.VMs[1].IPs, "an address list sent as an array reads too")
	assert.Equal(t, "openvz", snap.VMs[6].Virt)
}

func TestVirtualizorAPIPaging(t *testing.T) {
	cases := []struct {
		name         string
		pageSize     int
		capPage      int
		ignorePaging bool
		maxCalls     int
	}{
		{"master honours the page size", 4, 0, false, 3},
		{"master caps pages below the size asked", 200, 3, false, 4},
		{"master ignores paging", 2, 0, true, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPageSize(t, tc.pageSize)
			m := newVZFixtureMaster(t)
			m.capPage, m.ignorePaging = tc.capPage, tc.ignorePaging
			a, err := NewVirtualizorAPI(m.URL, m.key, m.pass, "")
			require.NoError(t, err)
			raws, err := a.list(context.Background(), "vs", "vs")
			require.NoError(t, err)
			assert.Len(t, raws, 9, "every VM once")
			assert.LessOrEqual(t, m.callCount(), tc.maxCalls)
		})
	}
}

func TestVirtualizorAPIFailures(t *testing.T) {
	m := newVZFixtureMaster(t)

	a, err := NewVirtualizorAPI(m.URL, m.key, "wrong-pass", "")
	require.NoError(t, err)
	_, err = a.TestConnection(context.Background())
	require.ErrorIs(t, err, ErrSourceRefused)
	assert.Contains(t, err.Error(), "The API key or pass is not valid")
	assert.NotContains(t, err.Error(), "wrong-pass")

	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>Login</body></html>"))
	}))
	defer html.Close()
	a, err = NewVirtualizorAPI(html.URL, "k", "secret-pass", "")
	require.NoError(t, err)
	_, err = a.TestConnection(context.Background())
	require.ErrorIs(t, err, ErrSourceUnreachable)
	assert.Contains(t, err.Error(), "4085")

	// A redirect to another host would carry the credentials there.
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer redirect.Close()
	a, err = NewVirtualizorAPI(redirect.URL, "k", "secret-pass", "")
	require.NoError(t, err)
	_, err = a.TestConnection(context.Background())
	require.ErrorIs(t, err, ErrSourceUnreachable)
	assert.NotContains(t, err.Error(), "secret-pass")
	assert.Zero(t, elsewhere)

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	a, err = NewVirtualizorAPI(down.URL, "k", "secret-pass", "")
	require.NoError(t, err)
	_, err = a.TestConnection(context.Background())
	require.ErrorIs(t, err, ErrSourceUnreachable)
	assert.NotContains(t, err.Error(), "secret-pass", "the URL, and with it the credentials, is not in the error")

	for _, bad := range []string{"", "master.example.com", "ftp://master.example.com", "https://user:pw@master.example.com"} {
		_, err := NewVirtualizorAPI(bad, "k", "p", "")
		assert.Error(t, err, bad)
	}
}

func TestVirtualizorAPIPinnedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servs":{"0":{"serid":"0","server_name":"localhost","ip":"192.0.2.10"}}}`))
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	fp := hex.EncodeToString(sum[:])

	a, err := NewVirtualizorAPI(srv.URL, "k", "p", "")
	require.NoError(t, err)
	_, err = a.TestConnection(context.Background())
	require.Error(t, err)
	assert.True(t, IsUntrustedCertificate(err), "a self-signed certificate is reported as one to pin: %v", err)

	got, err := PresentedFingerprint(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, fp, got)

	colons := strings.ToUpper(strings.Join(splitEvery(fp, 2), ":"))
	a, err = NewVirtualizorAPI(srv.URL, "k", "p", colons)
	require.NoError(t, err)
	n, err := a.TestConnection(context.Background())
	require.NoError(t, err, "the pinned certificate is trusted")
	assert.Equal(t, 1, n)

	a, err = NewVirtualizorAPI(srv.URL, "k", "p", strings.Repeat("ab", 32))
	require.NoError(t, err)
	_, err = a.TestConnection(context.Background())
	require.Error(t, err, "another certificate is not")
	assert.False(t, errors.Is(err, ErrSourceRefused))
}

func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
