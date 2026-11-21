package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/agentsign"
)

const (
	provNewNode  = "11111111-1111-1111-1111-111111111111"
	provLiveNode = "22222222-2222-2222-2222-222222222222"
)

var provisionDBs atomic.Int64

type provisionFixture struct {
	e   *echo.Echo
	h   *ProvisionHandler
	db  *gorm.DB
	dir string
}

// newProvisionFixture serves the provision routes from a temp binary dir
// holding files, against a SQLite stand-in for the tables they touch.
func newProvisionFixture(t *testing.T, files map[string][]byte) *provisionFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:provision%d?mode=memory&cache=shared", provisionDBs.Add(1))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL,
			cert_fingerprint TEXT NOT NULL DEFAULT '', last_report_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, node_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE node_enrollments (id TEXT PRIMARY KEY, node_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_by TEXT,
			created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, used_at DATETIME, used_from TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT, resource_type TEXT, resource_id TEXT, ip_address TEXT,
			user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME)`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('` + provNewNode + `', 'node-new', '192.0.2.10', 'offline', 'old-token')`,
		`INSERT INTO nodes (id, name, ip_address, status, token, cert_fingerprint) VALUES ('` + provLiveNode + `', 'node-live', '192.0.2.11', 'active', 'live-token', 'ab')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := NewProvisionHandler(db, dir, "https://panel.example")
	e := echo.New()
	RegisterProvisionRoutes(e, h)
	return &provisionFixture{e: e, h: h, db: db, dir: dir}
}

func (f *provisionFixture) do(t *testing.T, method, url, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(`{"hostname":"kvm-new","arch":"amd64"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if token != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

func (f *provisionFixture) issue(t *testing.T, nodeID string) string {
	t.Helper()
	enr, err := service.IssueNodeEnrollment(t.Context(), f.db, nodeID, "")
	if err != nil {
		t.Fatal(err)
	}
	return enr.Token
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Success {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	return body.Data
}

// The script is public: it carries the panel URL and both digests, nothing
// secret, and verifies the binary before making it executable.
func TestProvision_InstallScriptIsPublicAndComplete(t *testing.T) {
	amd, arm := []byte("\x7fELF-amd64"), []byte("\x7fELF-arm64")
	f := newProvisionFixture(t, map[string][]byte{"agent-amd64": amd, "agent-arm64": arm})
	rec := f.do(t, http.MethodGet, "/install-agent.sh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	script := rec.Body.String()
	for _, marker := range []string{"__PANEL_URL__", "__AGENT_SHA256_AMD64__", "__AGENT_SHA256_ARM64__"} {
		if strings.Contains(script, marker) {
			t.Fatalf("unresolved placeholder %q", marker)
		}
	}
	if !strings.Contains(script, `PANEL_URL="${PANEL_URL:-https://panel.example}"`) ||
		!strings.Contains(script, sha256Hex(amd)) || !strings.Contains(script, sha256Hex(arm)) {
		t.Fatal("script must embed the panel URL and both digests")
	}
	if regexp.MustCompile(`mbe_[0-9a-f]{8,}`).MatchString(script) {
		t.Fatal("the public script must not carry an install token")
	}
	verify, chmod := strings.Index(script, "sha256sum"), strings.Index(script, "chmod 0755")
	if verify == -1 || chmod == -1 || verify > chmod {
		t.Fatalf("the binary must be verified before it is made executable: verify=%d chmod=%d", verify, chmod)
	}
	// The packages the agent shells out to.
	for _, pkg := range []string{"libvirt-daemon-system", "qemu-utils", "iputils-arping", "ebtables", "libguestfs-tools", "genisoimage", "chrony", "bridge-utils"} {
		if !strings.Contains(script, pkg) {
			t.Errorf("installer does not install %s", pkg)
		}
	}
}

func TestProvision_InstallScriptMissingBinaryAborts(t *testing.T) {
	f := newProvisionFixture(t, map[string][]byte{"agent-amd64": []byte("\x7fELF-amd64")})
	script := f.do(t, http.MethodGet, "/install-agent.sh", "").Body.String()
	if !strings.Contains(script, `AGENT_SHA256_ARM64="${AGENT_SHA256_ARM64:-}"`) {
		t.Fatal("arm64 digest must be templated empty")
	}
	if !strings.Contains(script, "no SHA-256 published") {
		t.Fatal("installer lacks the empty-digest abort guard")
	}
}

// The binary needs a good install token, never one in the URL.
func TestProvision_AgentBinaryNeedsInstallToken(t *testing.T) {
	blob := []byte("\x7fELF-fake-agent-binary")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig := agentsign.Sign(priv, sha256Hex(blob))
	f := newProvisionFixture(t, map[string][]byte{
		"agent-amd64":     blob,
		"agent-amd64.sig": []byte(base64.StdEncoding.EncodeToString(sig) + "\n"),
	})

	for token, want := range map[string]int{"": http.StatusUnauthorized, "mbe_0000": http.StatusForbidden, "a-node-token": http.StatusForbidden} {
		rec := f.do(t, http.MethodGet, "/api/v1/nodes/agent-binary?arch=amd64", token)
		if rec.Code != want || rec.Header().Get("Content-Disposition") != "" || strings.Contains(rec.Body.String(), "ELF") {
			t.Fatalf("token %q: status %d, want %d, body %q", token, rec.Code, want, rec.Body.String())
		}
	}
	if rec := f.do(t, http.MethodGet, "/api/v1/nodes/agent-binary?arch=mips", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad arch: %d", rec.Code)
	}

	token := f.issue(t, provNewNode)
	rec := f.do(t, http.MethodGet, "/api/v1/nodes/agent-binary?arch=amd64", token)
	if rec.Code != http.StatusOK || rec.Body.String() != string(blob) {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Checksum-Sha256") != sha256Hex(blob) {
		t.Fatalf("checksum header %q", rec.Header().Get("X-Checksum-Sha256"))
	}
	gotSig, err := base64.StdEncoding.DecodeString(rec.Header().Get("X-Agent-Signature"))
	if err != nil || !agentsign.Verify(pub, sha256Hex(blob), gotSig) {
		t.Fatalf("signature header does not verify: %v", err)
	}
	// Downloading does not use the token up.
	if rec := f.do(t, http.MethodGet, "/api/v1/nodes/enroll", token); rec.Code != http.StatusOK {
		t.Fatalf("token gone after a download: %d", rec.Code)
	}
}

func TestProvision_ChecksumEndpoint(t *testing.T) {
	blob := []byte("\x7fELF-arm64-agent")
	f := newProvisionFixture(t, map[string][]byte{"agent-arm64": blob})
	rec := f.do(t, http.MethodGet, "/api/v1/nodes/agent-binary/checksum?arch=arm64", "")
	var body struct{ Arch, Algorithm, Digest string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if body.Arch != "arm64" || body.Algorithm != "sha256" || body.Digest != sha256Hex(blob) {
		t.Fatalf("checksum body = %+v", body)
	}
	if rec := f.do(t, http.MethodGet, "/api/v1/nodes/agent-binary/checksum?arch=amd64", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing arch: %d", rec.Code)
	}
}

// The token names its node, trades once for the agent credential, and the
// trade lands in the audit log against the node.
func TestProvision_EnrollFlow(t *testing.T) {
	f := newProvisionFixture(t, nil)
	token := f.issue(t, provNewNode)

	if data := decodeData(t, f.do(t, http.MethodGet, "/api/v1/nodes/enroll", token)); data["node_name"] != "node-new" {
		t.Fatalf("check: %v", data)
	}
	rec := f.do(t, http.MethodPost, "/api/v1/nodes/enroll", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec)
	agentToken, _ := data["agent_token"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(agentToken) || data["node_id"] != provNewNode || data["grpc_port"] != float64(50051) {
		t.Fatalf("enroll data: %v", data)
	}
	var stored string
	f.db.Raw(`SELECT token FROM nodes WHERE id = ?`, provNewNode).Scan(&stored)
	if stored != agentToken {
		t.Fatal("the node must authenticate with the credential handed out")
	}
	var audit struct {
		Action, ResourceType, ResourceID, Details string
	}
	f.db.Raw(`SELECT action, resource_type, resource_id, details FROM audit_logs`).Scan(&audit)
	if audit.Action != "ENROLL" || audit.ResourceID != provNewNode || !strings.Contains(audit.Details, "kvm-new") || strings.Contains(audit.Details, agentToken) {
		t.Fatalf("audit row: %+v", audit)
	}

	if rec := f.do(t, http.MethodPost, "/api/v1/nodes/enroll", token); rec.Code != http.StatusForbidden {
		t.Fatalf("second enroll: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodPost, "/api/v1/nodes/enroll", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
}

// A node an agent already answers for is never taken over, token or not.
func TestProvision_EnrollRefusesNodeWithAgent(t *testing.T) {
	f := newProvisionFixture(t, nil)
	token := f.issue(t, provNewNode)
	f.db.Exec(`UPDATE nodes SET cert_fingerprint = 'pinned' WHERE id = ?`, provNewNode)
	// The installer's first question and its download already say so, before
	// it installs anything on the host.
	for _, url := range []string{"/api/v1/nodes/enroll", "/api/v1/nodes/agent-binary?arch=amd64"} {
		if rec := f.do(t, http.MethodGet, url, token); rec.Code != http.StatusConflict {
			t.Fatalf("GET %s: status %d %s", url, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do(t, http.MethodPost, "/api/v1/nodes/enroll", token); rec.Code != http.StatusConflict {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var stored string
	f.db.Raw(`SELECT token FROM nodes WHERE id = ?`, provNewNode).Scan(&stored)
	if stored != "old-token" {
		t.Fatal("the credential must be untouched")
	}
}

func TestProvision_InstallCommand(t *testing.T) {
	f := newProvisionFixture(t, nil)
	call := func(nodeID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/install-command", nil)
		rec := httptest.NewRecorder()
		c := f.e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(nodeID)
		c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.New()})
		if err := f.h.InstallCommand(c); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	rec := call(provNewNode)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec)
	token, _ := data["token"].(string)
	cmd, _ := data["command"].(string)
	if cmd != "curl -fsSL https://panel.example/install-agent.sh | sudo bash -s -- --token "+token || data["expires_at"] == nil {
		t.Fatalf("command %q", cmd)
	}
	var createdBy *string
	f.db.Raw(`SELECT created_by FROM node_enrollments`).Scan(&createdBy)
	if createdBy == nil {
		t.Fatal("who created the token is recorded")
	}

	if rec := call(provLiveNode); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "only for new machines") {
		t.Fatalf("live node: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("44444444-4444-4444-4444-444444444444"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown node: %d", rec.Code)
	}
}
