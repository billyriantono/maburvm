package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The security centre must not call a VM firewall enforced on a guess: a node
// that says "off" is a failure, and one that says nothing is a warning.
func TestSecurityCheckVMFirewallEnforcement(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:opsfw?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT, status TEXT, firewall_enforcement TEXT NOT NULL DEFAULT '', deleted_at DATETIME)`).Error)

	check := func() *SecurityCheck {
		for _, c := range securityChecks(context.Background(), db) {
			if c.ID == "vm_firewall" {
				return &c
			}
		}
		return nil
	}

	require.Nil(t, check(), "with no nodes there is nothing to measure")

	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, status, firewall_enforcement) VALUES ('1','alpha','active','enforced'), ('2','bravo','active','')`).Error)
	c := check()
	require.Equal(t, "warn", c.Status, "a node that has not reported is not confirmed")
	require.Contains(t, c.Detail, "bravo")
	require.NotContains(t, c.Detail, "alpha")

	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, status, firewall_enforcement) VALUES ('3','charlie','active','off')`).Error)
	c = check()
	require.Equal(t, "fail", c.Status)
	require.Contains(t, c.Detail, "charlie")

	require.NoError(t, db.Exec(`UPDATE nodes SET firewall_enforcement = 'enforced'`).Error)
	c = check()
	require.Equal(t, "ok", c.Status)

	// A removed node no longer counts.
	require.NoError(t, db.Exec(`UPDATE nodes SET firewall_enforcement = 'off', deleted_at = CURRENT_TIMESTAMP WHERE id = '3'`).Error)
	require.Equal(t, "ok", check().Status)
}
