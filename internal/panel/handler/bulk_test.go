package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/service"
)

func TestBulkIDs(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()
	got, err := bulkIDs([]string{a, b, a})
	require.NoError(t, err)
	assert.Equal(t, []string{a, b}, got, "duplicates dropped, order kept")

	_, err = bulkIDs(nil)
	assert.Error(t, err)
	_, err = bulkIDs([]string{"web-01"})
	assert.Error(t, err)
	many := make([]string, MaxBulkVMs+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	_, err = bulkIDs(many)
	assert.Error(t, err)
}

func TestMoveResult(t *testing.T) {
	ok := moveResult(service.DrainPlanItem{VMID: "v", Hostname: "web", CanMove: true, DestNodeName: "node-b", Mode: "live", Warnings: []string{"slow link"}}, nil, true)
	assert.Equal(t, BulkResult{VMID: "v", Hostname: "web", OK: true, Message: "To node-b (live). slow link"}, ok)
	queued := moveResult(service.DrainPlanItem{VMID: "v", CanMove: true, DestNodeName: "node-b", Mode: "offline"}, nil, false)
	assert.Equal(t, "Queued: To node-b (offline)", queued.Message)
	blocked := moveResult(service.DrainPlanItem{VMID: "v", Blockers: []string{"no room", "no link"}}, service.ErrMigrationBlocked, false)
	assert.False(t, blocked.OK)
	assert.Equal(t, "no room; no link", blocked.Message)
}
