package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// JobFailures counts failed tries by job kind: outcome "retry" when the queue
// will try again, "failed" when the job failed for good.
var JobFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "maburvm_queue_job_failures_total",
	Help: "Failed job tries by kind. outcome is retry (it will be tried again) or failed (failed for good).",
}, []string{"kind", "outcome"})

// JobFailedHook, when set, is called once for a job that failed for good, with
// the job's kind, its arguments as stored and the reason. The panel sets it to
// tell operators; this package cannot import the panel's services.
var JobFailedHook func(ctx context.Context, kind string, args []byte, reason string)

// JobRetryingHook, when set, is called each time a try fails and the queue
// will try the job again: with many tries spaced ever further apart, "failed
// for good" can be weeks after the first sign of trouble.
var JobRetryingHook func(ctx context.Context, kind string, args []byte, reason string)

// tryFailed reports that this try ended in a failure of the job's own: not a
// success, not a stop from outside and not a snooze.
func tryFailed(ctxErr, err error) bool {
	var snoozed *rivertype.JobSnoozeError
	return err != nil && !errors.Is(ctxErr, context.Canceled) && !errors.As(err, &snoozed)
}

// jobFailedForGood reports that an attempt's error is the job's last word: the
// worker cancelled it as not worth another try, or no tries are left. A job
// whose context was cancelled was stopped from outside, by an operator or by
// the panel shutting down, and did not fail; one that ran out of time did.
// A snooze is the worker asking to be run again later, and uses no try.
func jobFailedForGood(ctxErr, err error, attempt, maxAttempts int) bool {
	if !tryFailed(ctxErr, err) {
		return false
	}
	return isJobCancel(err) || attempt >= maxAttempts
}

func isJobCancel(err error) bool {
	var cancelled *rivertype.JobCancelError
	return errors.As(err, &cancelled)
}

// failureReason is an error as an operator should read it, without River's
// "JobCancelError:" prefix.
func failureReason(err error) string {
	var cancelled *rivertype.JobCancelError
	if errors.As(err, &cancelled) && cancelled.Unwrap() != nil {
		return cancelled.Unwrap().Error()
	}
	return err.Error()
}

// attemptLimit is how many tries a job gets: the limit it was queued with,
// capped for the kinds that drive a node, so a job queued before those limits
// existed (with River's default of 25) stops at the same point as a new one.
func attemptLimit(kind string, args []byte, maxAttempts int) int {
	limit := 0
	switch kind {
	case (VMOperationJob{}).Kind():
		limit = NodeJobMaxAttempts
		var p struct {
			Operation VMOperationType `json:"operation"`
		}
		if json.Unmarshal(args, &p) == nil && p.Operation == VMOpConfigureNetwork {
			limit = NetworkJobMaxAttempts
		}
	case (NetworkConfigJob{}).Kind():
		limit = NetworkJobMaxAttempts
	case (SnapshotJob{}).Kind(), (TemplateSyncJob{}).Kind(), (ImportJob{}).Kind():
		limit = NodeJobMaxAttempts
	case (BackupJob{}).Kind(), (RestoreJob{}).Kind(), (ImageJob{}).Kind():
		limit = DiskJobMaxAttempts
	}
	if limit > 0 && limit < maxAttempts {
		return limit
	}
	return maxAttempts
}

// lastTry reports that a failure of this try leaves the job no other.
func lastTry(job *rivertype.JobRow) bool {
	return job.Attempt >= attemptLimit(job.Kind, job.EncodedArgs, job.MaxAttempts)
}

// jobFailureReporter watches every job on its way out, ends a job that has used
// its tries even when it was queued with more, and reports the ones that
// failed for good to JobFailedHook.
type jobFailureReporter struct{ river.WorkerMiddlewareDefaults }

func (*jobFailureReporter) Work(ctx context.Context, job *rivertype.JobRow, doInner func(ctx context.Context) error) (err error) {
	limit := attemptLimit(job.Kind, job.EncodedArgs, job.MaxAttempts)
	// A worker that panics on its last try is discarded like any other failure.
	defer func() {
		if r := recover(); r != nil {
			if job.Attempt < limit {
				panic(r) // River records it and schedules the retry
			}
			reason := fmt.Sprintf("the worker crashed: %v", r)
			JobFailures.WithLabelValues(job.Kind, "failed").Inc()
			if hook := JobFailedHook; hook != nil {
				go hook(context.WithoutCancel(ctx), job.Kind, job.EncodedArgs, reason)
			}
			err = river.JobCancel(errors.New(reason))
		}
	}()
	err = doInner(ctx)
	if limit < job.MaxAttempts && job.Attempt >= limit && tryFailed(ctx.Err(), err) && !isJobCancel(err) {
		err = river.JobCancel(fmt.Errorf("gave up after %d tries: %w", job.Attempt, err))
	}
	forGood := jobFailedForGood(ctx.Err(), err, job.Attempt, limit)
	if forGood {
		JobFailures.WithLabelValues(job.Kind, "failed").Inc()
	} else if tryFailed(ctx.Err(), err) {
		JobFailures.WithLabelValues(job.Kind, "retry").Inc()
	}
	if hook := JobFailedHook; hook != nil && forGood {
		// The job's context is often what just ran out.
		go hook(context.WithoutCancel(ctx), job.Kind, job.EncodedArgs, failureReason(err))
	}
	if hook := JobRetryingHook; hook != nil && !forGood && tryFailed(ctx.Err(), err) {
		go hook(context.WithoutCancel(ctx), job.Kind, job.EncodedArgs, failureReason(err))
	}
	return err
}
