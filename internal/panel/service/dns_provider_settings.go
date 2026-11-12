package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// DNSProviderSection is the system_settings row holding the nameserver
// connection. It is managed only through SettingsDNSProvider: the generic
// settings endpoints neither return nor accept it.
const DNSProviderSection = "dns_provider"

var (
	// ErrDNSAPIKeyRequired is returned when a save would leave the provider
	// without a key, or would send the stored key to a different host.
	ErrDNSAPIKeyRequired = errors.New("the API key is required")
	// ErrDNSInvalidURL is returned for anything but an http(s) URL with a host.
	ErrDNSInvalidURL = errors.New("the API URL must be an http or https URL")
)

// DNSProviderStatus is what an administrator may see about the nameserver
// connection. The API key is never part of it, only whether one is stored.
type DNSProviderStatus struct {
	// Source is "settings", "environment" or "none".
	Source    string `json:"source"`
	URL       string `json:"url"`
	ServerID  string `json:"server_id"`
	HasAPIKey bool   `json:"has_api_key"`
}

type storedDNSProvider struct {
	URL       string `json:"url"`
	ServerID  string `json:"server_id"`
	APIKeyEnc string `json:"api_key_enc"`
}

// SettingsDNSProvider is the nameserver connection as configured by an
// administrator in the panel, falling back to PDNS_API_URL / PDNS_API_KEY from
// the environment when nothing is saved.
//
// The API key is stored encrypted with the panel's AES key, which lives outside
// the database, so a database dump alone does not yield a credential that can
// rewrite DNS. It is decrypted only here, to sign requests, and is never
// returned to a browser.
type SettingsDNSProvider struct {
	db       *gorm.DB
	key      []byte
	prevKeys [][]byte

	mu       sync.Mutex
	cached   DNSProvider
	cachedAt time.Time
}

// NewSettingsDNSProvider builds the provider. previousAESKeys are retired keys
// still accepted for decryption, so a key rotation does not lose the setting.
func NewSettingsDNSProvider(db *gorm.DB, aesKey string, previousAESKeys ...string) *SettingsDNSProvider {
	p := &SettingsDNSProvider{db: db, key: []byte(aesKey)}
	for _, k := range previousAESKeys {
		p.prevKeys = append(p.prevKeys, []byte(k))
	}
	return p
}

func encryptWithKey(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (p *SettingsDNSProvider) decrypt(enc string) (string, error) {
	plain, err := decryptWithKey(p.key, enc)
	for _, k := range p.prevKeys {
		if err == nil {
			break
		}
		plain, err = decryptWithKey(k, enc)
	}
	return plain, err
}

// load reads the saved connection. ok is false when nothing is saved.
func (p *SettingsDNSProvider) load(ctx context.Context) (storedDNSProvider, bool) {
	var raw string
	err := p.db.WithContext(ctx).
		Raw("SELECT data::text FROM system_settings WHERE section = ?", DNSProviderSection).
		Scan(&raw).Error
	var s storedDNSProvider
	if err != nil || raw == "" || json.Unmarshal([]byte(raw), &s) != nil || s.URL == "" {
		return storedDNSProvider{}, false
	}
	return s, true
}

// current returns the provider to use right now. Cached briefly so a burst of
// DNS writes does not read and decrypt the setting each time; Save and Clear
// drop the cache, so a change applies immediately.
func (p *SettingsDNSProvider) current(ctx context.Context) DNSProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && time.Since(p.cachedAt) < 30*time.Second {
		return p.cached
	}
	var prov DNSProvider
	if s, ok := p.load(ctx); ok {
		if key, err := p.decrypt(s.APIKeyEnc); err == nil && key != "" {
			prov = newPDNSProvider(s.URL, key, s.ServerID)
		} else {
			// A key we cannot decrypt (the AES key changed with no previous key
			// configured) must not silently fall back to another nameserver.
			prov = noopDNSProvider{}
		}
	} else {
		prov = NewDNSProviderFromEnv()
	}
	p.cached, p.cachedAt = prov, time.Now()
	return prov
}

func (p *SettingsDNSProvider) invalidate() {
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
}

func newPDNSProvider(apiURL, apiKey, serverID string) *pdnsProvider {
	if strings.TrimSpace(serverID) == "" {
		serverID = "localhost"
	}
	return &pdnsProvider{
		baseURL:  strings.TrimSuffix(strings.TrimSpace(apiURL), "/"),
		apiKey:   apiKey,
		serverID: strings.TrimSpace(serverID),
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Status reports where the connection comes from, without the key.
func (p *SettingsDNSProvider) Status(ctx context.Context) DNSProviderStatus {
	if s, ok := p.load(ctx); ok {
		return DNSProviderStatus{Source: "settings", URL: s.URL, ServerID: s.ServerID, HasAPIKey: s.APIKeyEnc != ""}
	}
	envURL := strings.TrimSpace(os.Getenv("PDNS_API_URL"))
	if envURL != "" && strings.TrimSpace(os.Getenv("PDNS_API_KEY")) != "" {
		serverID := strings.TrimSpace(os.Getenv("PDNS_SERVER_ID"))
		if serverID == "" {
			serverID = "localhost"
		}
		return DNSProviderStatus{Source: "environment", URL: envURL, ServerID: serverID, HasAPIKey: true}
	}
	return DNSProviderStatus{Source: "none"}
}

func normalizeDNSAPIURL(raw string) (string, *url.URL, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", nil, ErrDNSInvalidURL
	}
	return raw, u, nil
}

// resolveKey decides which API key a save or test uses. A typed key always
// wins. An empty one means "keep the saved key", which is only allowed while
// the URL still points at the same host: otherwise anyone able to edit the URL
// could have the panel deliver the saved key to a server of their choosing.
func (p *SettingsDNSProvider) resolveKey(ctx context.Context, target *url.URL, apiKey string) (string, error) {
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		return apiKey, nil
	}
	s, ok := p.load(ctx)
	if !ok || s.APIKeyEnc == "" {
		return "", ErrDNSAPIKeyRequired
	}
	saved, err := url.Parse(s.URL)
	if err != nil || saved.Scheme != target.Scheme || saved.Host != target.Host {
		return "", fmt.Errorf("%w when the server address changes", ErrDNSAPIKeyRequired)
	}
	key, err := p.decrypt(s.APIKeyEnc)
	if err != nil || key == "" {
		return "", fmt.Errorf("%w: the saved key can no longer be read", ErrDNSAPIKeyRequired)
	}
	return key, nil
}

// Save stores the connection. An empty apiKey keeps the saved one (see resolveKey).
func (p *SettingsDNSProvider) Save(ctx context.Context, apiURL, serverID, apiKey string) error {
	apiURL, u, err := normalizeDNSAPIURL(apiURL)
	if err != nil {
		return err
	}
	key, err := p.resolveKey(ctx, u, apiKey)
	if err != nil {
		return err
	}
	enc, err := encryptWithKey(p.key, key)
	if err != nil {
		return fmt.Errorf("could not encrypt the API key: %w", err)
	}
	if serverID = strings.TrimSpace(serverID); serverID == "" {
		serverID = "localhost"
	}
	data, _ := json.Marshal(storedDNSProvider{URL: apiURL, ServerID: serverID, APIKeyEnc: enc})
	if err := p.db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		DNSProviderSection, string(data),
	).Error; err != nil {
		return err
	}
	p.invalidate()
	return nil
}

// Clear removes the saved connection; the environment, if set, applies again.
func (p *SettingsDNSProvider) Clear(ctx context.Context) error {
	err := p.db.WithContext(ctx).Exec("DELETE FROM system_settings WHERE section = ?", DNSProviderSection).Error
	p.invalidate()
	return err
}

// Test checks that the nameserver answers with these details, without saving.
func (p *SettingsDNSProvider) Test(ctx context.Context, apiURL, serverID, apiKey string) error {
	apiURL, u, err := normalizeDNSAPIURL(apiURL)
	if err != nil {
		return err
	}
	key, err := p.resolveKey(ctx, u, apiKey)
	if err != nil {
		return err
	}
	prov := newPDNSProvider(apiURL, key, serverID)
	req, err := prov.newRequest(ctx, http.MethodGet, "", nil)
	if err != nil {
		return err
	}
	resp, err := prov.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the server: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("the server rejected the API key")
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("the server answered, but has no server id %q", prov.serverID)
	default:
		return fmt.Errorf("the server answered %s", resp.Status)
	}
}

// DNSProvider, delegating to whatever is configured at the time of the call.

func (p *SettingsDNSProvider) Configured() bool {
	return p.current(context.Background()).Configured()
}
func (p *SettingsDNSProvider) Name() string { return p.current(context.Background()).Name() }
func (p *SettingsDNSProvider) SyncZone(ctx context.Context, zone *models.DNSZone, records []models.DNSRecord) error {
	return p.current(ctx).SyncZone(ctx, zone, records)
}
func (p *SettingsDNSProvider) DeleteZone(ctx context.Context, zoneName string) error {
	return p.current(ctx).DeleteZone(ctx, zoneName)
}
func (p *SettingsDNSProvider) SetPTR(ctx context.Context, ip, hostname string) error {
	return p.current(ctx).SetPTR(ctx, ip, hostname)
}
func (p *SettingsDNSProvider) ClearPTR(ctx context.Context, ip string) error {
	return p.current(ctx).ClearPTR(ctx, ip)
}
func (p *SettingsDNSProvider) ListPTRs(ctx context.Context, reverseZone string) (map[string]string, error) {
	return p.current(ctx).ListPTRs(ctx, reverseZone)
}
