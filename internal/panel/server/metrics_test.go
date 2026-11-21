package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMetricsEndpoint(t *testing.T) {
	get := func(s *Server, path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		return rec
	}

	off := NewServer(nil, &config.Config{})
	off.setupHealthRoutes()
	if rec := get(off, "/metrics", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no token configured: want 404, got %d", rec.Code)
	}

	cfg := &config.Config{}
	cfg.Server.MetricsToken = "s3cret"
	s := NewServer(nil, cfg)
	s.setupHealthRoutes()
	get(s, "/health", "")
	if rec := get(s, "/metrics", "Bearer wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}
	rec := get(s, "/metrics", "Bearer s3cret")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `maburvm_http_request_duration_seconds_count{method="GET",route="/health",status="200"}`) {
		t.Fatalf("scrape: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestFleetCollector(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT, status TEXT, deleted_at DATETIME, failed_polls INTEGER NOT NULL DEFAULT 0, last_report_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, node_id TEXT, status TEXT, deleted_at DATETIME)`,
		`CREATE TABLE node_metrics (node_id TEXT, cpu_usage REAL, memory_usage REAL, disk_usage REAL,
			network_rx_bytes_per_sec REAL, network_tx_bytes_per_sec REAL, conntrack_count INTEGER, conntrack_max INTEGER, recorded_at DATETIME, status TEXT NOT NULL DEFAULT 'online')`,
		`INSERT INTO nodes (id, name, status, deleted_at) VALUES ('n1','node1','active',NULL), ('n2','gone','offline','2026-01-01'), ('n3','node2','active',NULL), ('n4','fresh','offline',NULL)`,
		`INSERT INTO vms VALUES ('a','n1','running',NULL), ('b','n1','running',NULL), ('c','n1','stopped',NULL), ('d','n1','running','2026-01-01')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	db.Exec(`INSERT INTO node_metrics VALUES ('n1',10,50,20,1,2,100,1000,?,'online'), ('n1',14,85,21,1,2,200,1000,?,'online')`,
		now.Add(-2*time.Minute), now.Add(-time.Minute))
	// node2 went quiet ten minutes ago: its last reading is real but stale, and
	// a newer row of zeros is what an older panel stored for it while it was down.
	quiet := now.Add(-10 * time.Minute)
	db.Exec(`INSERT INTO node_metrics VALUES ('n3',30,60,40,1,2,300,1000,?,'online'), ('n3',0,0,0,0,0,0,0,?,'offline')`, quiet, now)
	db.Exec(`UPDATE nodes SET last_report_at = ? WHERE id = 'n1'`, now)
	db.Exec(`UPDATE nodes SET last_report_at = ?, failed_polls = 2 WHERE id = 'n3'`, quiet)

	cfg := &config.Config{}
	cfg.Server.MetricsToken = "t"
	s := NewServer(db, cfg)
	s.setupHealthRoutes()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{
		`maburvm_fleet_collect_success 1`,
		`maburvm_node_status{node="node1",status="active"} 1`,
		`maburvm_vms{node="node1",status="running"} 2`,
		`maburvm_vms{node="node1",status="stopped"} 1`,
		`maburvm_node_memory_usage_percent{node="node1"} 85`,
		`maburvm_node_conntrack_entries{node="node1"} 200`,
		`maburvm_node_up{node="node1"} 1`,
		// A node that missed a check is down, and its last report stays where it was.
		`maburvm_node_up{node="node2"} 0`,
		`maburvm_node_last_report_timestamp_seconds{node="node2"} ` + strconv.FormatFloat(float64(quiet.Unix()), 'g', -1, 64),
		// A node that has never reported is not up.
		`maburvm_node_up{node="fresh"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, not := range []string{
		`maburvm_node_cpu_usage_percent{node="node2"}`, // neither the stale reading nor the zeros
		`maburvm_node_last_report_timestamp_seconds{node="fresh"}`,
	} {
		if strings.Contains(body, not) {
			t.Errorf("exported %q", not)
		}
	}
	if strings.Contains(body, `node="gone"`) {
		t.Error("deleted node exported")
	}
}
