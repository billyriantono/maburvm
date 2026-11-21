//go:build pg

package service

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What a job that failed for good leaves behind, on the real schema: an alert
// for staff always, and a notice for a customer only about their own VM and
// only for the failures they would notice.
func TestOnJobFailedTellsStaffAndTheCustomer(t *testing.T) {
	db := migrationDB(t)
	const (
		customer = "b0000000-0000-0000-0000-000000000001"
		staff    = "b0000000-0000-0000-0000-000000000002"
		node     = "b0000000-0000-0000-0000-000000000003"
		tmpl     = "b0000000-0000-0000-0000-000000000004"
		theirs   = "b0000000-0000-0000-0000-000000000005"
		ours     = "b0000000-0000-0000-0000-000000000006"
	)
	for _, q := range []string{
		`INSERT INTO users (id, email, password_hash, role) VALUES ('` + customer + `', 'customer@example.test', 'x', 'client'), ('` + staff + `', 'staff@example.test', 'x', 'admin')`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('` + node + `', 'node-1', '192.0.2.10', 'active', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path) VALUES ('` + tmpl + `', 'Debian', '12', '/t.qcow2')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, status) VALUES
			('` + theirs + `', '` + customer + `', '` + node + `', 'shop', '` + tmpl + `', 'error'),
			('` + ours + `', '` + staff + `', '` + node + `', 'monitor', '` + tmpl + `', 'error')`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	hook := OnJobFailed(db, NewAlertService(db, slog.New(slog.NewTextHandler(io.Discard, nil)), strings.Repeat("k", 32)), nil)
	ctx := context.Background()

	hook(ctx, "vm_operation", []byte(`{"vm_id":"`+theirs+`","operation":"create","node_id":"`+node+`"}`), "template image not found")
	hook(ctx, "backup", []byte(`{"vm_id":"`+theirs+`","backup_id":"x","scheduled":true}`), "upload failed: AccessDenied")
	hook(ctx, "vm_operation", []byte(`{"vm_id":"`+theirs+`","operation":"start","node_id":"`+node+`"}`), "node unreachable")
	hook(ctx, "vm_operation", []byte(`{"vm_id":"`+ours+`","operation":"create","node_id":"`+node+`"}`), "no space left on device")

	var alerts []struct{ AlertKey, Title, Detail string }
	require.NoError(t, db.Raw(`SELECT alert_key, title, detail FROM alerts ORDER BY alert_key`).Scan(&alerts).Error)
	var keys []string
	for _, a := range alerts {
		keys = append(keys, a.AlertKey)
	}
	assert.Equal(t, []string{
		"backup_failed:" + theirs,
		"job_failed:vm_operation:create:" + theirs,
		"job_failed:vm_operation:create:" + ours,
		"job_failed:vm_operation:start:" + theirs,
	}, keys, "staff hear of every job that failed for good")
	for _, a := range alerts {
		if a.AlertKey == "job_failed:vm_operation:create:"+theirs {
			assert.Equal(t, "VM create failed for shop", a.Title)
			assert.Equal(t, "template image not found", a.Detail)
		}
	}

	var notices []struct{ UserID, Kind, Title string }
	require.NoError(t, db.Raw(`SELECT user_id::text, kind, title FROM user_notifications ORDER BY kind`).Scan(&notices).Error)
	require.Len(t, notices, 2, "the customer hears of the create and the backup, staff of nothing: %+v", notices)
	assert.Equal(t, customer, notices[0].UserID)
	assert.Equal(t, "backup_failed", notices[0].Kind)
	assert.Equal(t, "vm_create_failed", notices[1].Kind)
	assert.Contains(t, notices[1].Title, "shop could not be created")
	for _, n := range notices {
		assert.NotContains(t, n.Title, "AccessDenied", "the reason, which names paths and keys, is for staff")
	}
}
