package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// Out of the box the panel watches every reading and acts on none.
func TestDefaultAbusePolicyOnlyAlerts(t *testing.T) {
	p := DefaultAbusePolicy()
	require.NoError(t, p.Validate())
	for _, kind := range models.AbuseKinds {
		r := p.Rule(kind)
		require.True(t, r.Enabled, kind)
		require.Equal(t, models.AbuseActionAlert, r.Action, kind)
	}
	require.Equal(t, models.AbuseSYNRateThreshold, p.OutboundSYN.Threshold, "the outbound threshold is the node's own limit")
}

func TestAbusePolicyRefusesActionsForTheWrongDirection(t *testing.T) {
	p := DefaultAbusePolicy()
	p.InboundPPS.Action = models.AbuseActionQuarantine // cutting off the victim of a flood
	require.Error(t, p.Validate())

	p = DefaultAbusePolicy()
	p.OutboundPPS.Action = models.AbuseActionNullRoute
	require.Error(t, p.Validate())

	p = DefaultAbusePolicy()
	p.OutboundSYN.Threshold = 0
	require.Error(t, p.Validate())

	p = DefaultAbusePolicy()
	p.InboundPPS.Action = models.AbuseActionNullRoute
	p.OutboundPPS.Action = models.AbuseActionRateLimit
	require.NoError(t, p.Validate())
}

func TestNextAbuseStep(t *testing.T) {
	quarantine := AbuseRule{Enabled: true, Threshold: 100, Action: models.AbuseActionQuarantine}
	alert := AbuseRule{Enabled: true, Threshold: 100, Action: models.AbuseActionAlert}
	open := func(samples int, state string) *models.AbuseEvent {
		return &models.AbuseEvent{Samples: samples, ActionState: state}
	}
	for _, tc := range []struct {
		name    string
		open    *models.AbuseEvent
		rule    AbuseRule
		value   float64
		sustain int
		want    abuseStep
	}{
		{"quiet guest", nil, quarantine, 10, 2, stepNothing},
		{"first reading over opens, does not act", nil, quarantine, 150, 2, stepOpen},
		{"second reading over acts", open(1, models.AbuseStateNone), quarantine, 150, 2, stepAct},
		{"a single-reading policy acts at once", nil, quarantine, 150, 1, stepAct},
		{"alert-only never acts", open(5, models.AbuseStateNone), alert, 150, 2, stepContinue},
		{"acted on once only", open(5, models.AbuseStateActive), quarantine, 150, 2, stepContinue},
		{"released by staff is left alone", open(5, models.AbuseStateReleased), quarantine, 150, 2, stepContinue},
		{"a failed action is not retried every minute", open(5, models.AbuseStateFailed), quarantine, 150, 2, stepContinue},
		{"back under ends the episode", open(3, models.AbuseStateNone), quarantine, 10, 2, stepEnd},
		{"an action in force keeps it open", open(3, models.AbuseStateActive), quarantine, 0, 2, stepNothing},
		{"a disabled rule never opens", nil, AbuseRule{Threshold: 1, Action: models.AbuseActionAlert}, 1e9, 1, stepNothing},
	} {
		require.Equal(t, tc.want, nextAbuseStep(tc.open, tc.rule, tc.value, tc.sustain), tc.name)
	}
}

func TestAbuseOwnerTextNamesTheAction(t *testing.T) {
	ev := &models.AbuseEvent{Kind: models.AbuseInboundPPS, Peak: 412000, Action: models.AbuseActionNullRoute, Targets: "203.0.113.5,203.0.113.6"}
	text := abuseOwnerText(ev, false)
	require.Contains(t, text, "203.0.113.5, 203.0.113.6")
	require.Contains(t, text, "412000 packets/s")
	require.Contains(t, abuseOwnerText(ev, true), "reachable again")
}

// Staff hear that the policy acted even though the episode's warning went out
// a reading earlier, inside the alert settle time.
func TestAbuseActionAlertIsNotSwallowedByTheWarning(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	require.NoError(t, h.db.Exec(`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT)`).Error)
	ctx := context.Background()
	s := NewAbuseService(h.db, nil, nil)
	s.SetAlerts(h.AlertService)
	ev := &models.AbuseEvent{NodeID: "n1", MAC: "52:54:00:aa:bb:cc", Kind: models.AbuseInboundPPS, Threshold: 200_000,
		Action: models.AbuseActionAlert, ActionState: models.AbuseStateNone}
	s.alertStaff(ctx, ev, 300_000)
	h.advance(2 * time.Minute)
	ev.Action, ev.ActionState = models.AbuseActionNullRoute, models.AbuseStateActive
	s.alertStaff(ctx, ev, 300_000)
	rows := h.rows(t)
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.NotContains(t, r.Delivery, "Not sent", r.Title)
	}
}
