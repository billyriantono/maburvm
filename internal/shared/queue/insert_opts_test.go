package queue

import (
	"testing"

	"github.com/riverqueue/river"
)

// River reads a job's options only through JobArgsWithInsertOpts. A method of
// any other shape compiles and is ignored: every job then lands in the default
// queue with 25 tries, which is how these options once went unused.
func TestJobInsertOptsReachRiver(t *testing.T) {
	queues := map[string]bool{QueueCritical: true, QueueDefault: true, QueueBatch: true, QueueAudit: true}
	for _, args := range []river.JobArgs{
		VMOperationJob{Operation: VMOpCreate}, VMOperationJob{Operation: VMOpConfigureNetwork}, TemplateSyncJob{},
		BackupJob{}, ImageJob{}, RestoreJob{}, ImportJob{}, SnapshotJob{}, AuditJob{}, NetworkConfigJob{},
		WebhookDeliveryJob{}, EmailDeliveryJob{},
	} {
		withOpts, ok := args.(river.JobArgsWithInsertOpts)
		if !ok {
			t.Errorf("%s: River does not see its InsertOpts", args.Kind())
			continue
		}
		opts := withOpts.InsertOpts()
		if !queues[opts.Queue] {
			t.Errorf("%s: queue %q is not one the client runs", args.Kind(), opts.Queue)
		}
		if opts.Priority < 1 || opts.Priority > 4 {
			t.Errorf("%s: priority %d is outside River's 1 to 4", args.Kind(), opts.Priority)
		}
		if args.Kind() != (AuditJob{}).Kind() && opts.MaxAttempts == 0 {
			t.Errorf("%s: no bounded number of tries", args.Kind())
		}
	}
}
