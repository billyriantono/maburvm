package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// cloudflareAPI is the Cloudflare API. Fixed, so a saved token can only ever
// be sent there.
const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// cloudflareProvider writes records through the Cloudflare DNS API with an API
// token scoped to Zone:DNS:Edit. Zones, reverse ones included, must already be
// on the account: creating one needs account rights a DNS token should not
// have, and deleting one would take everything else in it along.
type cloudflareProvider struct {
	baseURL string
	token   string
	http    *http.Client
}

func newCloudflareProvider(token string) *cloudflareProvider {
	return &cloudflareProvider{baseURL: cloudflareAPI, token: token, http: &http.Client{Timeout: 10 * time.Second}}
}

func (p *cloudflareProvider) Configured() bool { return true }
func (p *cloudflareProvider) Name() string     { return "Cloudflare" }

type cfRecord struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Content  string         `json:"content,omitempty"`
	TTL      int            `json:"ttl"`
	Priority *int           `json:"priority,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int
		Message string
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
	Info   struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// do sends one request and decodes the result into out (when not nil).
func (p *cloudflareProvider) do(ctx context.Context, method, path string, body, out any) (*cfEnvelope, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env cfEnvelope
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("cloudflare answered %s", resp.Status)
	}
	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, resp.Status)
		}
		return &env, fmt.Errorf("cloudflare %s failed: %s", method, strings.Join(msgs, "; "))
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return &env, fmt.Errorf("decode cloudflare answer: %w", err)
		}
	}
	return &env, nil
}

// zoneID finds a zone on the account by name.
func (p *cloudflareProvider) zoneID(ctx context.Context, zone string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(zone), ".")
	var zones []struct{ ID string }
	if _, err := p.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(name), nil, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("the zone %s is not on this Cloudflare account; add it there first", name)
	}
	return zones[0].ID, nil
}

// records lists a zone's records, optionally of one type and name.
func (p *cloudflareProvider) records(ctx context.Context, zoneID, typ, name string) ([]cfRecord, error) {
	var all []cfRecord
	for page := 1; ; page++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if typ != "" {
			q.Set("type", typ)
		}
		if name != "" {
			q.Set("name", name)
		}
		var batch []cfRecord
		env, err := p.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if env.Info.TotalPages <= page {
			return all, nil
		}
	}
}

// SetPTR replaces the PTR rec with hostname.
func (p *cloudflareProvider) SetPTR(ctx context.Context, rec PTRRecord, hostname string) error {
	zid, err := p.zoneID(ctx, rec.Zone)
	if err != nil {
		return err
	}
	existing, err := p.records(ctx, zid, "PTR", rec.Name)
	if err != nil {
		return err
	}
	want := cfRecord{Type: "PTR", Name: rec.Name, Content: strings.TrimSuffix(hostname, "."), TTL: 3600}
	if len(existing) == 0 {
		_, err = p.do(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", want, nil)
		return err
	}
	if _, err := p.do(ctx, http.MethodPut, "/zones/"+zid+"/dns_records/"+existing[0].ID, want, nil); err != nil {
		return err
	}
	for _, extra := range existing[1:] {
		if _, err := p.do(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+extra.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// ClearPTR removes the PTR rec.
func (p *cloudflareProvider) ClearPTR(ctx context.Context, rec PTRRecord) error {
	zid, err := p.zoneID(ctx, rec.Zone)
	if err != nil {
		return err
	}
	existing, err := p.records(ctx, zid, "PTR", rec.Name)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if _, err := p.do(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+r.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// ListPTRs returns the zone's PTR records as fqdn (with a trailing dot) to
// hostname, like the other provider. A zone not on the account has none.
func (p *cloudflareProvider) ListPTRs(ctx context.Context, reverseZone string) (map[string]string, error) {
	out := map[string]string{}
	zid, err := p.zoneID(ctx, reverseZone)
	if err != nil {
		return out, nil
	}
	recs, err := p.records(ctx, zid, "PTR", "")
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		out[ensureDot(r.Name)] = ensureDot(r.Content)
	}
	return out, nil
}

// cfManagedTypes are the record types the panel owns in a synced zone; others
// (and the apex NS, which Cloudflare runs) are left alone.
var cfManagedTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "NS": true, "SRV": true, "PTR": true}

// cfDesired turns a panel record into the Cloudflare shape.
func cfDesired(zone *models.DNSZone, r models.DNSRecord) (cfRecord, error) {
	out := cfRecord{Type: strings.ToUpper(r.Type), Name: strings.TrimSuffix(recordFQDN(zone.Name, r.Name), "."),
		Content: strings.TrimSuffix(r.Content, "."), TTL: orDefaultTTL(r.TTL)}
	switch out.Type {
	case "MX":
		prio := r.Priority
		out.Priority = &prio
	case "TXT":
		out.Content = r.Content
	case "SRV":
		f := strings.Fields(r.Content)
		if len(f) != 3 {
			return out, fmt.Errorf("SRV %s: content must be \"weight port target\"", r.Name)
		}
		weight, werr := strconv.Atoi(f[0])
		port, perr := strconv.Atoi(f[1])
		if werr != nil || perr != nil {
			return out, fmt.Errorf("SRV %s: weight and port must be numbers", r.Name)
		}
		out.Content = ""
		out.Data = map[string]any{"priority": r.Priority, "weight": weight, "port": port, "target": strings.TrimSuffix(f[2], ".")}
	}
	return out, nil
}

// cfKey identifies a record by what it says, so an unchanged one is kept.
func cfKey(r cfRecord) string {
	content := r.Content
	if r.Type == "SRV" {
		if r.Data != nil {
			content = fmt.Sprintf("%v %v %v %v", r.Data["priority"], r.Data["weight"], r.Data["port"], r.Data["target"])
		}
	} else if r.Type == "MX" && r.Priority != nil {
		content = fmt.Sprintf("%d %s", *r.Priority, content)
	}
	return strings.ToLower(r.Name) + "|" + r.Type + "|" + strings.TrimSuffix(content, ".")
}

// SyncZone makes the zone's records of the types the panel manages match
// ours: changed ones are added and stale ones deleted.
func (p *cloudflareProvider) SyncZone(ctx context.Context, zone *models.DNSZone, records []models.DNSRecord) error {
	zid, err := p.zoneID(ctx, zone.Name)
	if err != nil {
		return err
	}
	apex := strings.TrimSuffix(strings.ToLower(zone.Name), ".")
	desired := map[string]cfRecord{}
	var order []string
	for _, r := range records {
		d, err := cfDesired(zone, r)
		if err != nil {
			return err
		}
		if d.Type == "NS" && strings.EqualFold(d.Name, apex) {
			continue // Cloudflare runs the apex NS and refuses to add one, which would fail every sync
		}
		k := cfKey(d)
		if _, dup := desired[k]; !dup {
			order = append(order, k)
		}
		desired[k] = d
	}
	current, err := p.records(ctx, zid, "", "")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, r := range current {
		if !cfManagedTypes[r.Type] || (r.Type == "NS" && strings.EqualFold(r.Name, apex)) {
			continue
		}
		k := cfKey(r)
		if _, keep := desired[k]; keep && !have[k] {
			have[k] = true
			continue
		}
		if _, err := p.do(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+r.ID, nil, nil); err != nil {
			return err
		}
	}
	for _, k := range order {
		if have[k] {
			continue
		}
		if _, err := p.do(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", desired[k], nil); err != nil {
			return err
		}
	}
	return nil
}

// DeleteZone leaves the zone on the account: it is the account owner's, and
// may hold more than the panel ever wrote. The caller logs this.
func (p *cloudflareProvider) DeleteZone(ctx context.Context, zoneName string) error {
	return fmt.Errorf("the zone %s stays on the Cloudflare account; remove it there if it is no longer needed", zoneName)
}

// verify checks the token is live.
func (p *cloudflareProvider) verify(ctx context.Context) error {
	var res struct{ Status string }
	if _, err := p.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &res); err != nil {
		return errors.New("Cloudflare rejected the API token")
	}
	if res.Status != "active" {
		return fmt.Errorf("the API token is %s, not active", res.Status)
	}
	return nil
}
