package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newEnrollmentDB is a SQLite stand-in for the tables enrollment touches.
func newEnrollmentDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL,
			cert_fingerprint TEXT NOT NULL DEFAULT '', last_report_at DATETIME, preflight TEXT, preflight_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, node_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE node_enrollments (id TEXT PRIMARY KEY, node_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_by TEXT,
			created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, used_at DATETIME, used_from TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('11111111-1111-1111-1111-111111111111', 'node-new', '192.0.2.10', 'offline', 'old-token')`,
		`INSERT INTO nodes (id, name, ip_address, status, token, cert_fingerprint) VALUES ('22222222-2222-2222-2222-222222222222', 'node-live', '192.0.2.11', 'active', 'live-token', 'abc')`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('33333333-3333-3333-3333-333333333333', 'node-vms', '192.0.2.12', 'offline', 'vms-token')`,
		`INSERT INTO vms (id, node_id) VALUES ('v1', '33333333-3333-3333-3333-333333333333')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	return db
}

const (
	newNode  = "11111111-1111-1111-1111-111111111111"
	liveNode = "22222222-2222-2222-2222-222222222222"
	vmsNode  = "33333333-3333-3333-3333-333333333333"
)

func nodeToken(t *testing.T, db *gorm.DB, id string) string {
	var tok string
	if err := db.Raw(`SELECT token FROM nodes WHERE id = ?`, id).Scan(&tok).Error; err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestEnrollmentLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newEnrollmentDB(t)

	enr, err := IssueNodeEnrollment(ctx, db, newNode, "not-a-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enr.Token, enrollmentTokenPrefix) || len(enr.Token) < 40 {
		t.Fatalf("token %q", enr.Token)
	}
	if d := time.Until(enr.ExpiresAt); d < 50*time.Minute || d > NodeEnrollmentTTL {
		t.Fatalf("expires in %s", d)
	}
	var stored int64
	db.Raw(`SELECT COUNT(*) FROM node_enrollments WHERE token_hash = ?`, enr.Token).Scan(&stored)
	if stored != 0 {
		t.Fatal("the token itself must never be stored")
	}

	pending, err := CheckNodeEnrollment(ctx, db, enr.Token)
	if err != nil || pending.NodeID != newNode || pending.NodeName != "node-new" {
		t.Fatalf("check: %+v %v", pending, err)
	}
	// Checking does not use the token up.
	if _, err := CheckNodeEnrollment(ctx, db, enr.Token); err != nil {
		t.Fatalf("second check: %v", err)
	}

	got, err := RedeemNodeEnrollment(ctx, db, enr.Token, "198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != newNode || len(got.AgentToken) != 2*TokenLength || got.AgentToken == "old-token" {
		t.Fatalf("redeem: %+v", got)
	}
	if nodeToken(t, db, newNode) != got.AgentToken {
		t.Fatal("the node's agent token must be the one handed out (no AES key in tests, so stored as is)")
	}
	var from string
	db.Raw(`SELECT used_from FROM node_enrollments WHERE node_id = ?`, newNode).Scan(&from)
	if from != "198.51.100.7" {
		t.Fatalf("used_from = %q", from)
	}

	if _, err := RedeemNodeEnrollment(ctx, db, enr.Token, "x"); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a used token must be refused, got %v", err)
	}
	if _, err := CheckNodeEnrollment(ctx, db, enr.Token); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a used token must not check out, got %v", err)
	}
}

func TestEnrollmentRefusesNodesWithAnAgentOrVMs(t *testing.T) {
	ctx := context.Background()
	db := newEnrollmentDB(t)
	for _, id := range []string{liveNode, vmsNode} {
		if _, err := IssueNodeEnrollment(ctx, db, id, ""); !errors.Is(err, ErrNodeHasAgent) {
			t.Errorf("%s: want ErrNodeHasAgent, got %v", id, err)
		}
	}
	if _, err := IssueNodeEnrollment(ctx, db, "44444444-4444-4444-4444-444444444444", ""); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("unknown node: %v", err)
	}

	// A token issued while the node was new is refused once an agent answers
	// for it, and the node's credential is left alone.
	enr, err := IssueNodeEnrollment(ctx, db, newNode, "")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE nodes SET last_report_at = ? WHERE id = ?`, time.Now(), newNode)
	if _, err := RedeemNodeEnrollment(ctx, db, enr.Token, "x"); !errors.Is(err, ErrNodeHasAgent) {
		t.Fatalf("want ErrNodeHasAgent, got %v", err)
	}
	if nodeToken(t, db, newNode) != "old-token" {
		t.Fatal("a refused enrollment must not rotate the agent token")
	}
	if nodeToken(t, db, liveNode) != "live-token" {
		t.Fatal("the live node's token must be untouched")
	}
}

func TestEnrollmentExpiryReissueAndGarbage(t *testing.T) {
	ctx := context.Background()
	db := newEnrollmentDB(t)

	first, err := IssueNodeEnrollment(ctx, db, newNode, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := IssueNodeEnrollment(ctx, db, newNode, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckNodeEnrollment(ctx, db, first.Token); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a new install command must retire the old one, got %v", err)
	}

	db.Exec(`UPDATE node_enrollments SET expires_at = ?`, time.Now().UTC().Add(-time.Minute))
	if _, err := RedeemNodeEnrollment(ctx, db, second.Token, "x"); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("an expired token must be refused, got %v", err)
	}

	for _, bad := range []string{"", "mbe_", "mbe_00", "Bearer " + second.Token, "a-node-agent-token"} {
		if _, err := CheckNodeEnrollment(ctx, db, bad); !errors.Is(err, ErrEnrollmentInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
		if _, err := RedeemNodeEnrollment(ctx, db, bad, "x"); !errors.Is(err, ErrEnrollmentInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// Two installers racing with one token: exactly one gets the credential.
func TestEnrollmentIsSingleUseUnderRace(t *testing.T) {
	ctx := context.Background()
	db := newEnrollmentDB(t)
	enr, err := IssueNodeEnrollment(ctx, db, newNode, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := RedeemNodeEnrollment(ctx, db, enr.Token, "x"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d redeems succeeded", wins)
	}
}

func TestPreflightStoreAndRead(t *testing.T) {
	ctx := context.Background()
	db := newEnrollmentDB(t)
	svc := &NodeService{db: db}

	rep, err := svc.StoredPreflight(ctx, newNode)
	if err != nil || rep.CheckedAt != nil || !rep.Supported || len(rep.Checks) != 0 {
		t.Fatalf("never checked: %+v %v", rep, err)
	}
	if _, err := svc.StoredPreflight(ctx, "44444444-4444-4444-4444-444444444444"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("unknown node: %v", err)
	}

	at := time.Now().UTC().Truncate(time.Second)
	in := &NodePreflight{Supported: true, Checks: []PreflightCheck{{ID: "kvm_device", Name: "KVM device", Status: "fail", Detail: "missing", Fix: "modprobe"}}}
	if err := storePreflight(ctx, db, newNode, in, at); err != nil {
		t.Fatal(err)
	}
	rep, err = svc.StoredPreflight(ctx, newNode)
	if err != nil || rep.CheckedAt == nil || len(rep.Checks) != 1 || rep.Checks[0].Fix != "modprobe" {
		t.Fatalf("stored: %+v %v", rep, err)
	}

	if err := storePreflight(ctx, db, newNode, &NodePreflight{Supported: false}, at); err != nil {
		t.Fatal(err)
	}
	if rep, _ = svc.StoredPreflight(ctx, newNode); rep.Supported || len(rep.Checks) != 0 {
		t.Fatalf("an old agent's answer must read as unsupported: %+v", rep)
	}
}

func TestPreflightTargets(t *testing.T) {
	db := newEnrollmentDB(t)
	for _, stmt := range []string{
		`CREATE TABLE storage_pools (id TEXT PRIMARY KEY, node_id TEXT, type TEXT, path TEXT, deleted_at DATETIME)`,
		`CREATE TABLE ip_pools (id TEXT PRIMARY KEY, node_id TEXT, bridge TEXT, deleted_at DATETIME)`,
		`CREATE TABLE ip_pool_nodes (pool_id TEXT, node_id TEXT)`,
		`INSERT INTO storage_pools VALUES ('s1', '` + newNode + `', 'dir', '/vm', NULL), ('s2', '` + newNode + `', 'lvm', 'vg0', NULL),
			('s3', '` + newNode + `', 'Directory', '/data', NULL), ('s4', '` + newNode + `', 'dir', '/gone', '2024-01-01'), ('s5', '` + liveNode + `', 'dir', '/other', NULL)`,
		`INSERT INTO ip_pools VALUES ('p1', '` + newNode + `', 'br0', NULL), ('p2', NULL, 'br1', NULL), ('p3', NULL, '', NULL), ('p4', '` + liveNode + `', 'br9', NULL), ('p5', NULL, 'br0', NULL),
			('p6', NULL, 'brglobal', NULL), ('p7', NULL, 'brother', NULL)`,
		`INSERT INTO ip_pool_nodes VALUES ('p2', '` + newNode + `'), ('p3', '` + newNode + `'), ('p5', '` + newNode + `'), ('p7', '` + liveNode + `')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	pools, bridges := PreflightTargets(context.Background(), db, newNode)
	if strings.Join(pools, ",") != "/data,/vm" {
		t.Errorf("pools = %v", pools)
	}
	// p6 is global (no node, none assigned), so its bridge is this node's too.
	if strings.Join(bridges, ",") != "br0,br1,brglobal" {
		t.Errorf("bridges = %v", bridges)
	}
}
