package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The admin API of a Virtualizor master, read to import its customers, plans,
// IP pools and VMs. Only reads: every call is a listing.

var (
	// ErrSourceUnreachable is a source API that did not answer, or not with JSON.
	ErrSourceUnreachable = errors.New("the source panel did not answer")
	// ErrSourceRefused is an answer that refuses the call or cannot be read.
	ErrSourceRefused = errors.New("the source panel refused the request")
)

// vzPageSize is records asked for per page (a var so tests can page small).
var vzPageSize = 200

const (
	vzMaxPages = 500
	// vzMaxBody bounds one response; a listing of thousands of VMs is a few MB.
	vzMaxBody = 64 << 20
)

// VirtualizorAPI calls one master's admin API.
type VirtualizorAPI struct {
	base      *url.URL
	key, pass string
	http      *http.Client
}

// NewVirtualizorAPI checks the address and prepares a client. fingerprint, when
// set, is the SHA-256 of the one certificate to trust in place of the usual
// chain check: masters commonly serve a self-signed one on port 4085.
func NewVirtualizorAPI(baseURL, key, pass, fingerprint string) (*VirtualizorAPI, error) {
	u, err := parseSourceURL(baseURL)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if fp := NormalizeFingerprint(fingerprint); fp != "" {
		tr.TLSClientConfig = &tls.Config{
			// Verification is not skipped: VerifyConnection pins the certificate.
			InsecureSkipVerify: true, //nolint:gosec
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || certFingerprint(cs.PeerCertificates[0].Raw) != fp {
					return errors.New("the certificate is not the one whose fingerprint was saved")
				}
				return nil
			},
		}
	}
	client := &http.Client{Timeout: 90 * time.Second, Transport: tr,
		// The credentials ride in the query string: never follow them to another host.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Host != u.Host {
				return errors.New("the source panel redirected to another address; save that address instead")
			}
			return nil
		}}
	return &VirtualizorAPI{base: u, key: key, pass: pass, http: client}, nil
}

func parseSourceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("the address must look like https://master.example.com:4085")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/index.php"), "/")
	return u, nil
}

// NormalizeFingerprint puts a pasted SHA-256 fingerprint in one form (lower
// case hex, no separators); "" when it is not one.
func NormalizeFingerprint(fp string) string {
	fp = strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(fp)))
	if len(fp) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(fp); err != nil {
		return ""
	}
	return fp
}

func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// PresentedFingerprint connects to the source's TLS port and returns the
// SHA-256 of the certificate it presents, so an operator can compare it with
// the one on their master before trusting it.
func PresentedFingerprint(ctx context.Context, baseURL string) (string, error) {
	u, err := parseSourceURL(baseURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", errors.New("the address is not https")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()}} //nolint:gosec // only reads the certificate
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", host)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no certificate was presented")
	}
	return certFingerprint(certs[0].Raw), nil
}

// IsUntrustedCertificate reports a TLS failure an operator can resolve by
// pinning the master's certificate.
func IsUntrustedCertificate(err error) bool {
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	return errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci)
}

// vzStr reads a JSON scalar as text: the API sends numbers as strings, or not.
type vzStr string

func (s *vzStr) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case nil:
		*s = ""
	case string:
		*s = vzStr(strings.TrimSpace(t))
	case float64:
		*s = vzStr(strconv.FormatFloat(t, 'f', -1, 64))
	case bool:
		*s = vzStr(strconv.FormatBool(t))
	default:
		*s = ""
	}
	return nil
}

func (s vzStr) String() string { return string(s) }

func (s vzStr) Int() int64 {
	f, err := strconv.ParseFloat(string(s), 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// set is "1" or "true".
func (s vzStr) set() bool { return s == "1" || strings.EqualFold(string(s), "true") }

// vzAddrs is a VM's address list: an object of id to address (or to a record
// with an "ip"), or an array.
type vzAddrs []string

func (a *vzAddrs) UnmarshalJSON(b []byte) error {
	var raw interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	pick := func(v interface{}) string {
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		case map[string]interface{}:
			s, _ := t["ip"].(string)
			return strings.TrimSpace(s)
		}
		return ""
	}
	var out []string
	switch t := raw.(type) {
	case []interface{}:
		for _, v := range t {
			if ip := pick(v); ip != "" {
				out = append(out, ip)
			}
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortNumeric(keys)
		for _, k := range keys {
			if ip := pick(t[k]); ip != "" {
				out = append(out, ip)
			}
		}
	}
	*a = out
	return nil
}

type vzServerRec struct {
	ID   vzStr `json:"serid"`
	Name vzStr `json:"server_name"`
	IP   vzStr `json:"ip"`
}

type vzUserRec struct {
	ID    vzStr `json:"uid"`
	Email vzStr `json:"email"`
	First vzStr `json:"fname"`
	Last  vzStr `json:"lname"`
	Type  vzStr `json:"type"` // 0 user, 1 admin, 2 cloud
}

type vzPlanRec struct {
	ID           vzStr `json:"plid"`
	Name         vzStr `json:"plan_name"`
	Virt         vzStr `json:"virt"`
	Space        vzStr `json:"space"`
	RAM          vzStr `json:"ram"`
	Cores        vzStr `json:"cores"`
	Bandwidth    vzStr `json:"bandwidth"`
	NetworkSpeed vzStr `json:"network_speed"`
}

type vzPoolRec struct {
	ID       vzStr `json:"ippid"`
	Name     vzStr `json:"ippool_name"`
	Gateway  vzStr `json:"gateway"`
	Netmask  vzStr `json:"netmask"`
	NS1      vzStr `json:"ns1"`
	NS2      vzStr `json:"ns2"`
	IPv6     vzStr `json:"ipv6"`
	ServerID vzStr `json:"ipp_serid"`
	SerID    vzStr `json:"serid"` // older masters
}

type vzIPRec struct {
	PoolID vzStr `json:"ippid"`
	IP     vzStr `json:"ip"`
	VPSID  vzStr `json:"vpsid"`
}

type vzVPSRec struct {
	ID           vzStr   `json:"vpsid"`
	Name         vzStr   `json:"vps_name"`
	UUID         vzStr   `json:"uuid"`
	ServerID     vzStr   `json:"serid"`
	UserID       vzStr   `json:"uid"`
	Email        vzStr   `json:"email"` // the owner's, on masters that join it in
	PlanID       vzStr   `json:"plid"`
	Hostname     vzStr   `json:"hostname"`
	OSName       vzStr   `json:"os_name"`
	Virt         vzStr   `json:"virt"`
	Space        vzStr   `json:"space"`
	RAM          vzStr   `json:"ram"`
	Cores        vzStr   `json:"cores"`
	Bandwidth    vzStr   `json:"bandwidth"`
	NetworkSpeed vzStr   `json:"network_speed"`
	Suspended    vzStr   `json:"suspended"`
	IPs          vzAddrs `json:"ips"`
	IPv6         vzAddrs `json:"ips6"`
}

// get makes one call and returns the decoded top-level object.
func (a *VirtualizorAPI) get(ctx context.Context, act string, page int) (map[string]json.RawMessage, error) {
	u := *a.base
	u.Path += "/index.php"
	q := url.Values{}
	q.Set("act", act)
	q.Set("api", "json")
	// The documented form of an admin API call: the key pair as parameters
	// (apikey and apipass are the end-user API's, on another port).
	q.Set("adminapikey", a.key)
	q.Set("adminapipass", a.pass)
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
		q.Set("reslen", strconv.Itoa(vzPageSize))
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, fmt.Errorf("%w: no answer within %s", ErrSourceUnreachable, a.http.Timeout)
		}
		// The error carries the URL, and with it the credentials: keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: %w", ErrSourceUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, vzMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	if len(body) > vzMaxBody {
		return nil, fmt.Errorf("%w: the answer to %s is larger than %d MB", ErrSourceUnreachable, act, vzMaxBody>>20)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d for %s", ErrSourceUnreachable, resp.StatusCode, act)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: the answer to %s is not JSON; check the address (the admin port is usually 4085) and the API key and pass", ErrSourceUnreachable, act)
	}
	if msg := vzErrorText(out["error"]); msg != "" {
		return nil, fmt.Errorf("%w (%s): %s", ErrSourceRefused, act, msg)
	}
	return out, nil
}

// vzErrorText flattens the API's error field (a string, a list or an object
// of messages); "" when there is none.
func vzErrorText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var parts []string
	var walk func(interface{})
	walk = func(v interface{}) {
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				parts = append(parts, s)
			}
		case []interface{}:
			for _, e := range t {
				walk(e)
			}
		case map[string]interface{}:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(v)
	return strings.Join(parts, "; ")
}

// records splits a listing into its records, keyed by id. PHP sends an empty
// listing as [] and a full one as an object; some lists come as arrays.
func records(raw json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "false" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		out := make(map[string]json.RawMessage, len(arr))
		for i, r := range arr {
			out[fmt.Sprintf("#%09d", i)] = r // sorts in array order
		}
		return out, nil
	}
	var obj map[string]json.RawMessage
	return obj, json.Unmarshal(raw, &obj)
}

// list reads every page of a listing. A master that ignores paging sends the
// same records again, which ends the loop as a short page would.
func (a *VirtualizorAPI) list(ctx context.Context, act string, fields ...string) ([]json.RawMessage, error) {
	seen := map[string]bool{}
	var out []json.RawMessage
	largest := 0
	for page := 1; page <= vzMaxPages; page++ {
		resp, err := a.get(ctx, act, page)
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		for _, f := range fields {
			if r, ok := resp[f]; ok {
				raw = r
				break
			}
		}
		recs, err := records(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: its %s listing could not be read: %v", ErrSourceRefused, act, err)
		}
		keys := make([]string, 0, len(recs))
		for k := range recs {
			keys = append(keys, k)
		}
		sortNumeric(keys) // a snapshot, and the preview planned from it, read the same every time
		added := 0
		for _, k := range keys {
			r := recs[k]
			id := k
			if strings.HasPrefix(k, "#") {
				id = string(r) // an array has no ids; the record itself tells pages apart
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, r)
			added++
		}
		// A short page is the last one. Short against the largest page seen,
		// not the size asked for: a master may cap the page size lower.
		if added == 0 || len(recs) < largest {
			return out, nil
		}
		largest = max(largest, len(recs))
	}
	return nil, fmt.Errorf("%w: its %s listing has more than %d pages", ErrSourceRefused, act, vzMaxPages)
}

func decodeAll[T any](raws []json.RawMessage, what string) ([]T, error) {
	out := make([]T, 0, len(raws))
	for _, r := range raws {
		var v T
		if err := json.Unmarshal(r, &v); err != nil {
			return nil, fmt.Errorf("%w: a %s record could not be read: %v", ErrSourceRefused, what, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// TestConnection reads the server list, the smallest call that needs valid
// credentials, and says how many servers the master has.
func (a *VirtualizorAPI) TestConnection(ctx context.Context) (int, error) {
	raws, err := a.list(ctx, "servers", "servs", "servers")
	if err != nil {
		return 0, err
	}
	return len(raws), nil
}

// Fetch reads everything an import needs into a snapshot. Plans and VMs of
// other virtualisation types are kept so the preview can say why they stay.
func (a *VirtualizorAPI) Fetch(ctx context.Context) (*VirtualizorSnapshot, error) {
	type call struct {
		act    string
		fields []string
		raws   *[]json.RawMessage
	}
	var servers, users, plans, pools, ips, vms []json.RawMessage
	for _, c := range []call{
		{"servers", []string{"servs", "servers"}, &servers},
		{"users", []string{"users"}, &users},
		{"plans", []string{"plans"}, &plans},
		{"ippool", []string{"ippools", "ippool"}, &pools},
		{"ips", []string{"ips"}, &ips},
		{"vs", []string{"vs"}, &vms},
	} {
		r, err := a.list(ctx, c.act, c.fields...)
		if err != nil {
			return nil, err
		}
		*c.raws = r
	}
	return buildVirtualizorSnapshot(servers, users, plans, pools, ips, vms)
}

func buildVirtualizorSnapshot(servers, users, plans, pools, ips, vms []json.RawMessage) (*VirtualizorSnapshot, error) {
	srvRecs, err := decodeAll[vzServerRec](servers, "server")
	if err != nil {
		return nil, err
	}
	userRecs, err := decodeAll[vzUserRec](users, "user")
	if err != nil {
		return nil, err
	}
	planRecs, err := decodeAll[vzPlanRec](plans, "plan")
	if err != nil {
		return nil, err
	}
	poolRecs, err := decodeAll[vzPoolRec](pools, "IP pool")
	if err != nil {
		return nil, err
	}
	ipRecs, err := decodeAll[vzIPRec](ips, "IP address")
	if err != nil {
		return nil, err
	}
	vmRecs, err := decodeAll[vzVPSRec](vms, "VM")
	if err != nil {
		return nil, err
	}

	snap := &VirtualizorSnapshot{FetchedAt: time.Now().UTC()}
	for _, r := range srvRecs {
		snap.Servers = append(snap.Servers, SourceServer{ID: r.ID.String(), Name: r.Name.String(), IP: r.IP.String()})
	}
	for _, r := range userRecs {
		if r.ID == "" {
			continue
		}
		snap.Users = append(snap.Users, SourceUser{ID: r.ID.String(), Email: strings.ToLower(r.Email.String()),
			Name: strings.TrimSpace(r.First.String() + " " + r.Last.String()), Admin: r.Type == "1"})
	}
	// An owner missing from the user listing is still known by the email its
	// VMs carry.
	knownUser := map[string]bool{}
	for _, u := range snap.Users {
		knownUser[u.ID] = true
	}
	for _, r := range vmRecs {
		if uid := r.UserID.String(); uid != "" && uid != "0" && !knownUser[uid] && r.Email != "" {
			knownUser[uid] = true
			snap.Users = append(snap.Users, SourceUser{ID: uid, Email: strings.ToLower(r.Email.String())})
		}
	}
	for _, r := range planRecs {
		if r.ID == "" {
			continue
		}
		snap.Plans = append(snap.Plans, SourcePlan{ID: r.ID.String(), Name: r.Name.String(), Virt: r.Virt.String(),
			CPU: r.Cores.Int(), RAMMB: r.RAM.Int(), DiskGB: r.Space.Int(), BandwidthGB: r.Bandwidth.Int(), SpeedKBs: r.NetworkSpeed.Int()})
	}
	held := map[string]string{}   // address -> VM id
	byVM := map[string][]string{} // VM id -> addresses, in listing order
	poolIPs := map[string][]string{}
	for _, r := range ipRecs {
		ip := strings.TrimSpace(r.IP.String())
		if net.ParseIP(ip) == nil {
			continue
		}
		poolIPs[r.PoolID.String()] = append(poolIPs[r.PoolID.String()], ip)
		if v := r.VPSID.String(); v != "" && v != "0" {
			held[ip] = v
			byVM[v] = append(byVM[v], ip)
		}
	}
	for _, r := range poolRecs {
		if r.ID == "" {
			continue
		}
		p := SourcePool{ID: r.ID.String(), Name: r.Name.String(), Gateway: r.Gateway.String(), Netmask: r.Netmask.String(), IPv6: r.IPv6.set()}
		for _, ns := range []vzStr{r.NS1, r.NS2} {
			if ns != "" {
				p.DNS = append(p.DNS, ns.String())
			}
		}
		if s := r.ServerID.String(); s != "" {
			p.ServerIDs = []string{s}
		} else if s := r.SerID.String(); s != "" {
			p.ServerIDs = []string{s}
		}
		for _, ip := range poolIPs[p.ID] {
			p.Addresses = append(p.Addresses, SourceAddress{IP: ip, VMID: held[ip]})
		}
		snap.Pools = append(snap.Pools, p)
	}
	for _, r := range vmRecs {
		if r.ID == "" {
			continue
		}
		v := SourceVM{ID: r.ID.String(), Name: r.Name.String(), UUID: strings.ToLower(r.UUID.String()), ServerID: r.ServerID.String(),
			UserID: r.UserID.String(), PlanID: r.PlanID.String(), Hostname: r.Hostname.String(), OSName: r.OSName.String(),
			Virt: r.Virt.String(), CPU: r.Cores.Int(), RAMMB: r.RAM.Int(), DiskGB: r.Space.Int(), BandwidthGB: r.Bandwidth.Int(),
			SpeedKBs: r.NetworkSpeed.Int(), Suspended: r.Suspended.set()}
		v.IPs = mergeAddrs(append(append([]string{}, r.IPs...), r.IPv6...), byVM[v.ID])
		snap.VMs = append(snap.VMs, v)
	}
	snap.sort()
	return snap, nil
}

// mergeAddrs returns the valid addresses of both lists once each, in order.
func mergeAddrs(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ip := range append(a, b...) {
		p := net.ParseIP(strings.SplitN(ip, "/", 2)[0])
		if p == nil || seen[p.String()] {
			continue
		}
		seen[p.String()] = true
		out = append(out, p.String())
	}
	return out
}

// sortNumeric orders ids as numbers where they are numbers.
func sortNumeric(keys []string) {
	sort.SliceStable(keys, func(i, j int) bool { return lessNumeric(keys[i], keys[j]) })
}

func lessNumeric(a, b string) bool {
	ai, errA := strconv.ParseInt(a, 10, 64)
	bi, errB := strconv.ParseInt(b, 10, 64)
	if errA == nil && errB == nil {
		return ai < bi
	}
	if (errA == nil) != (errB == nil) {
		return errA == nil
	}
	return a < b
}
