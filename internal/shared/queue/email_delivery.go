package queue

import (
	"context"
	"errors"

	"github.com/riverqueue/river"
)

// EmailDeliveryMaxAttempts bounds the tries for one email. Spaced attempt^4
// seconds apart, eight tries span a little over an hour: enough for a mail
// server's restart, short enough that "your server is ready" is still news.
const EmailDeliveryMaxAttempts = 8

// EmailDeliveryJob sends one email stored in the panel's email log.
type EmailDeliveryJob struct {
	EmailID string `json:"email_id"`
}

func (EmailDeliveryJob) Kind() string { return "email_delivery" }

func (EmailDeliveryJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, Priority: PriorityNormal, MaxAttempts: EmailDeliveryMaxAttempts}
}

// EmailDeliverHook makes one delivery attempt; final says no try is left after
// this one. The panel sets it: the mail settings and the log live in its
// services, which this package cannot import.
var EmailDeliverHook func(ctx context.Context, emailID string, final bool) error

type emailDeliveryWorker struct {
	river.WorkerDefaults[EmailDeliveryJob]
}

func (*emailDeliveryWorker) Work(ctx context.Context, job *river.Job[EmailDeliveryJob]) error {
	hook := EmailDeliverHook
	if hook == nil {
		return errors.New("email delivery is not wired in this process")
	}
	return hook(ctx, job.Args.EmailID, job.Attempt >= job.MaxAttempts)
}
