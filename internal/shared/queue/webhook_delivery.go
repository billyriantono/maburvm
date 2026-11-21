package queue

import (
	"context"
	"errors"

	"github.com/riverqueue/river"
)

// WebhookDeliveryMaxAttempts bounds the tries for one outbound billing event.
// River spaces them attempt^4 seconds apart, so ten tries span about four
// hours: long enough to ride out a billing server's maintenance window.
const WebhookDeliveryMaxAttempts = 10

// WebhookDeliveryJob sends one stored outbound event to the billing webhook.
type WebhookDeliveryJob struct {
	DeliveryID string `json:"delivery_id"`
}

func (WebhookDeliveryJob) Kind() string { return "webhook_delivery" }

func (WebhookDeliveryJob) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: QueueDefault, Priority: PriorityNormal, MaxAttempts: WebhookDeliveryMaxAttempts}
}

// WebhookDeliverHook makes one delivery attempt. final says no try is left
// after this one. The panel sets it: the delivery log, the URL and the signing
// secret live in its services, which this package cannot import.
var WebhookDeliverHook func(ctx context.Context, deliveryID string, final bool) error

type webhookDeliveryWorker struct {
	river.WorkerDefaults[WebhookDeliveryJob]
}

func (*webhookDeliveryWorker) Work(ctx context.Context, job *river.Job[WebhookDeliveryJob]) error {
	hook := WebhookDeliverHook
	if hook == nil {
		return errors.New("webhook delivery is not wired in this process")
	}
	return hook(ctx, job.Args.DeliveryID, job.Attempt >= job.MaxAttempts)
}
