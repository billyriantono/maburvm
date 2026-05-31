package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestValidateDNSRecord(t *testing.T) {
	valid := []models.DNSRecord{
		{Type: "A", Content: "203.0.113.5"},
		{Type: "AAAA", Content: "2001:db8::1"},
		{Type: "CNAME", Content: "target.example.com"},
		{Type: "NS", Content: "ns1.example.com"},
		{Type: "MX", Content: "mail.example.com", Priority: 10},
		{Type: "TXT", Content: "v=spf1 -all"},
	}
	for _, r := range valid {
		rec := r
		require.NoError(t, validateDNSRecord(&rec), "%s %s", r.Type, r.Content)
	}

	invalid := []models.DNSRecord{
		{Type: "A", Content: "2001:db8::1"},    // v6 in A
		{Type: "AAAA", Content: "203.0.113.5"}, // v4 in AAAA
		{Type: "A", Content: "not-an-ip"},
		{Type: "CNAME", Content: "no-dot"},   // not a hostname
		{Type: "MX", Content: ""},            // empty
		{Type: "WAT", Content: "x"},          // unsupported type
		{Type: "MX", Content: "m.e.com", Priority: -1},
	}
	for _, r := range invalid {
		rec := r
		require.ErrorIs(t, validateDNSRecord(&rec), ErrInvalidDNSRecord, "%s %q", r.Type, r.Content)
	}
}

func TestBuildZoneFile(t *testing.T) {
	zone := &models.DNSZone{Name: "example.com", TTL: 3600, PrimaryNS: "ns1.example.com", AdminEmail: "hostmaster@example.com"}
	records := []models.DNSRecord{
		{Name: "@", Type: "A", Content: "203.0.113.5", TTL: 3600},
		{Name: "www", Type: "CNAME", Content: "example.com", TTL: 300},
		{Name: "@", Type: "MX", Content: "mail.example.com", Priority: 10, TTL: 3600},
		{Name: "@", Type: "TXT", Content: "v=spf1 -all", TTL: 3600},
	}
	out := buildZoneFile(zone, records, "2026053001")

	require.Contains(t, out, "$ORIGIN example.com.")
	require.Contains(t, out, "IN\tSOA\tns1.example.com. hostmaster.example.com. (")
	require.Contains(t, out, "2026053001 ; serial")
	require.Contains(t, out, "@\tIN\tNS\tns1.example.com.")
	require.Contains(t, out, "@\t3600\tIN\tA\t203.0.113.5")
	require.Contains(t, out, "www\t300\tIN\tCNAME\texample.com.") // trailing dot added
	require.Contains(t, out, "@\t3600\tIN\tMX\t10 mail.example.com.")
	require.Contains(t, out, "IN\tTXT\t\"v=spf1 -all\"") // quoted
}

func setupDNSTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:dns-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE dns_zones (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, ttl INTEGER DEFAULT 3600,
		primary_ns TEXT DEFAULT '', admin_email TEXT DEFAULT '', description TEXT,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE dns_records (
		id TEXT PRIMARY KEY, zone_id TEXT NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL,
		content TEXT NOT NULL, ttl INTEGER DEFAULT 3600, priority INTEGER DEFAULT 0,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
	)`).Error)
	return db
}

func TestDNSServiceCRUD(t *testing.T) {
	db := setupDNSTestDB(t)
	svc := NewDNSService(db)
	ctx := context.Background()

	zone, err := svc.CreateZone(ctx, &ZoneRequest{Name: "Example.com.", PrimaryNS: "ns1.example.com", AdminEmail: "hostmaster@example.com"})
	require.NoError(t, err)
	require.Equal(t, "example.com", zone.Name, "name normalized (lowercased, trailing dot stripped)")

	// Duplicate zone rejected.
	_, err = svc.CreateZone(ctx, &ZoneRequest{Name: "example.com"})
	require.ErrorIs(t, err, ErrDNSZoneExists)

	// Invalid record rejected (no record created).
	_, err = svc.CreateRecord(ctx, zone.ID, &RecordRequest{Type: "A", Content: "nope"})
	require.ErrorIs(t, err, ErrInvalidDNSRecord)

	// Valid record created.
	rec, err := svc.CreateRecord(ctx, zone.ID, &RecordRequest{Name: "www", Type: "A", Content: "203.0.113.7"})
	require.NoError(t, err)
	require.Equal(t, "www", rec.Name)

	records, err := svc.ListRecords(ctx, zone.ID)
	require.NoError(t, err)
	require.Len(t, records, 1)

	// Update the record.
	updated, err := svc.UpdateRecord(ctx, rec.ID, &RecordRequest{Name: "www", Type: "A", Content: "203.0.113.8"})
	require.NoError(t, err)
	require.Equal(t, "203.0.113.8", updated.Content)

	// Export contains the record.
	out, err := svc.ExportZone(ctx, zone.ID)
	require.NoError(t, err)
	require.Contains(t, out, "www")
	require.Contains(t, out, "203.0.113.8")

	// Delete the record, then the zone.
	require.NoError(t, svc.DeleteRecord(ctx, rec.ID))
	left, err := svc.ListRecords(ctx, zone.ID)
	require.NoError(t, err)
	require.Empty(t, left)

	require.NoError(t, svc.DeleteZone(ctx, zone.ID))
	_, err = svc.GetZone(ctx, zone.ID)
	require.ErrorIs(t, err, ErrDNSZoneNotFound)
}

func TestDNSZoneNameValidation(t *testing.T) {
	db := setupDNSTestDB(t)
	svc := NewDNSService(db)
	_, err := svc.CreateZone(context.Background(), &ZoneRequest{Name: "not a domain"})
	require.ErrorIs(t, err, ErrInvalidDNSRecord)
	require.True(t, strings.Contains(err.Error(), "valid domain"))
}
