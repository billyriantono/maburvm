package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newIdempotencyTestHandler builds a BillingHandler backed by an in-memory
// SQLite DB with the billing_idempotency table created. It bypasses
// NewBillingHandler so the background cleanup goroutine isn't started.
func newIdempotencyTestHandler(t *testing.T) *BillingHandler {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:billing-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// The production column type is jsonb (Postgres); SQLite stores the same JSON
	// bytes as BLOB. Column names match the model so GORM CRUD works unchanged.
	if err := db.Exec(`CREATE TABLE billing_idempotency (
		idempotency_key TEXT PRIMARY KEY,
		request_id      TEXT NOT NULL,
		event           TEXT NOT NULL DEFAULT '',
		response        BLOB NOT NULL,
		processed_at    DATETIME NOT NULL,
		expires_at      DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	return &BillingHandler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		db:     db,
	}
}

func TestIdempotency_StoreThenCheckReturnsSameResponse(t *testing.T) {
	h := newIdempotencyTestHandler(t)
	ctx := context.Background()
	now := time.Now().UTC()

	want := WebhookResponse{Success: true, Message: "VM created successfully", VMID: "vm-123", RequestID: "req-1"}
	h.storeIdempotency(ctx, "key-1", "vm.create", &IdempotencyRecord{RequestID: "req-1", Response: want, ProcessedAt: now})

	got, ok := h.checkIdempotency(ctx, "key-1")
	if !ok {
		t.Fatal("expected idempotency record to be found")
	}
	if got.Response.VMID != want.VMID || got.Response.Message != want.Message || !got.Response.Success {
		t.Fatalf("stored response not returned faithfully: %+v", got.Response)
	}
	if got.RequestID != "req-1" {
		t.Fatalf("expected request id req-1, got %s", got.RequestID)
	}
}

func TestIdempotency_UnknownKeyNotFound(t *testing.T) {
	h := newIdempotencyTestHandler(t)
	if _, ok := h.checkIdempotency(context.Background(), "nope"); ok {
		t.Fatal("expected unknown key to be absent")
	}
}

func TestIdempotency_ExpiredRecordNotReturnedAndCleaned(t *testing.T) {
	h := newIdempotencyTestHandler(t)
	ctx := context.Background()
	// Store a record whose processed time is older than the TTL so it is expired.
	old := time.Now().UTC().Add(-IdempotencyCacheTTL - time.Hour)
	h.storeIdempotency(ctx, "old-key", "vm.create", &IdempotencyRecord{RequestID: "req-old", Response: WebhookResponse{Success: true}, ProcessedAt: old})

	if _, ok := h.checkIdempotency(ctx, "old-key"); ok {
		t.Fatal("expired record must not be returned")
	}

	// Cleanup should physically remove it.
	h.cleanupOldIdempotency(ctx)
	var count int64
	h.db.Model(&billingIdempotencyRow{}).Where("idempotency_key = ?", "old-key").Count(&count)
	if count != 0 {
		t.Fatalf("expected expired row to be deleted, found %d", count)
	}
}

func TestIdempotency_DuplicateStoreKeepsFirst(t *testing.T) {
	h := newIdempotencyTestHandler(t)
	ctx := context.Background()
	now := time.Now().UTC()

	h.storeIdempotency(ctx, "dup", "vm.create", &IdempotencyRecord{RequestID: "first", Response: WebhookResponse{VMID: "vm-first"}, ProcessedAt: now})
	// A racing/retried store with the same key must not overwrite the first result.
	h.storeIdempotency(ctx, "dup", "vm.create", &IdempotencyRecord{RequestID: "second", Response: WebhookResponse{VMID: "vm-second"}, ProcessedAt: now})

	got, ok := h.checkIdempotency(ctx, "dup")
	if !ok {
		t.Fatal("expected record")
	}
	if got.Response.VMID != "vm-first" {
		t.Fatalf("expected first write to win, got %s", got.Response.VMID)
	}
}
