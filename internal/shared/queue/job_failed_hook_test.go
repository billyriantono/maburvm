package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
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
