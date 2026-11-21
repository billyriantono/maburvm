package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestJobFailedForGood(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name          string
		ctxErr, err   error
		attempt, max  int
		wantPermanent bool
	}{
		{"success", nil, nil, 1, 3, false},
		{"a try left", nil, boom, 2, 3, false},
		{"last try", nil, boom, 3, 3, true},
		{"worker gave up early", nil, river.JobCancel(boom), 1, 25, true},
		{"timed out on the last try", context.DeadlineExceeded, context.DeadlineExceeded, 3, 3, true},
		{"stopped by an operator or a shutdown", context.Canceled, boom, 3, 3, false},
		{"stopped while giving up", context.Canceled, river.JobCancel(boom), 1, 3, false},
		{"asked to be run again later on the last try", nil, river.JobSnooze(time.Minute), 3, 3, false},
	} {
		if got := jobFailedForGood(tc.ctxErr, tc.err, tc.attempt, tc.max); got != tc.wantPermanent {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.wantPermanent)
		}
		// A try that failed is every failure that is not a stop or a snooze.
		wantFailed := tc.err != nil && tc.name != "stopped by an operator or a shutdown" &&
			tc.name != "stopped while giving up" && tc.name != "asked to be run again later on the last try"
		if got := tryFailed(tc.ctxErr, tc.err); got != wantFailed {
			t.Errorf("%s: tryFailed got %v, want %v", tc.name, got, wantFailed)
		}
	}
}

func TestAttemptLimit(t *testing.T) {
	for _, tc := range []struct {
		kind string
		args string
		max  int
		want int
	}{
		{"vm_operation", `{"operation":"create"}`, 25, NodeJobMaxAttempts},
		{"vm_operation", `{"operation":"configure_network"}`, 25, NetworkJobMaxAttempts},
		{"vm_operation", `{"operation":"create"}`, 2, 2},
		{"network_config", `{}`, 25, NetworkJobMaxAttempts},
		{"snapshot_operation", `{}`, 25, NodeJobMaxAttempts},
		{"backup", `{}`, 25, DiskJobMaxAttempts},
		{"webhook_delivery", `{}`, 10, 10},
		{"audit", `{}`, 25, 25},
	} {
		if got := attemptLimit(tc.kind, []byte(tc.args), tc.max); got != tc.want {
			t.Errorf("%s %s max %d: got %d, want %d", tc.kind, tc.args, tc.max, got, tc.want)
		}
	}
}

// A VM job queued before its kind had a limit (River's default of 25) must end,
// and be reported, at the same try as a new one.
func TestJobFailureReporterEndsLegacyJobsAtTheLimit(t *testing.T) {
	reported := make(chan string, 1)
	JobFailedHook = func(_ context.Context, _ string, _ []byte, reason string) { reported <- reason }
	defer func() { JobFailedHook = nil }()

	job := &rivertype.JobRow{Kind: "vm_operation", EncodedArgs: []byte(`{"operation":"start"}`), Attempt: NodeJobMaxAttempts, MaxAttempts: 25}
	err := (&jobFailureReporter{}).Work(context.Background(), job, func(context.Context) error { return errors.New("node unreachable") })
	if !isJobCancel(err) {
		t.Fatalf("got %v, want the job cancelled", err)
	}
	select {
	case reason := <-reported:
		if reason != "gave up after 5 tries: node unreachable" {
			t.Errorf("reason %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("the final failure was not reported")
	}

	job.Attempt = NodeJobMaxAttempts - 1
	if err := (&jobFailureReporter{}).Work(context.Background(), job, func(context.Context) error { return errors.New("x") }); isJobCancel(err) {
		t.Error("a try short of the limit must be retried")
	}
}
