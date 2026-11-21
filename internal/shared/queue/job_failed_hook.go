package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// JobFailedHook, when set, is called once for a job that failed for good, with
// the job's kind, its arguments as stored and the reason. The panel sets it to
// tell operators; this package cannot import the panel's services.
var JobFailedHook func(ctx context.Context, kind string, args []byte, reason string)

// jobFailedForGood reports that an attempt's error is the job's last word: the
// worker cancelled it as not worth another try, or no tries are left. A job
// whose context was cancelled was stopped from outside, by an operator or by
// the panel shutting down, and did not fail; one that ran out of time did.
// A snooze is the worker asking to be run again later, and uses no try.
func jobFailedForGood(ctxErr, err error, attempt, maxAttempts int) bool {
	var snoozed *rivertype.JobSnoozeError
	if err == nil || errors.Is(ctxErr, context.Canceled) || errors.As(err, &snoozed) {
		return false
	}
	var cancelled *rivertype.JobCancelError
	return errors.As(err, &cancelled) || attempt >= maxAttempts
}

// jobFailureReporter watches every job on its way out and reports the ones
// that failed for good to JobFailedHook.
type jobFailureReporter struct{ river.WorkerMiddlewareDefaults }

func (*jobFailureReporter) Work(ctx context.Context, job *rivertype.JobRow, doInner func(ctx context.Context) error) error {
	// A worker that panics on its last try is discarded like any other failure.
	defer func() {
		if r := recover(); r != nil {
			if hook := JobFailedHook; hook != nil && job.Attempt >= job.MaxAttempts {
				go hook(context.WithoutCancel(ctx), job.Kind, job.EncodedArgs, fmt.Sprintf("the worker crashed: %v", r))
			}
			panic(r) // River records it and schedules the retry
		}
	}()
	err := doInner(ctx)
	if hook := JobFailedHook; hook != nil && jobFailedForGood(ctx.Err(), err, job.Attempt, job.MaxAttempts) {
		// The job's context is often what just ran out.
		go hook(context.WithoutCancel(ctx), job.Kind, job.EncodedArgs, err.Error())
	}
	return err
}
