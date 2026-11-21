package queue

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// ObjectStoreHook returns a backup destination saved in the panel (by ID; ""
// is the default) and its ID, for sending to a node with a backup or a
// restore. It returns nil when none is saved, and the node then uses the store
// in its own environment file. The panel sets it at start-up; this package
// cannot import the panel's services.
var ObjectStoreHook func(ctx context.Context, destinationID string) (*pb.ObjectStore, string, error)

func panelObjectStore(ctx context.Context, destinationID string) (*pb.ObjectStore, string, error) {
	if ObjectStoreHook == nil {
		return nil, "", nil
	}
	return ObjectStoreHook(ctx, destinationID)
}

// Agent features a request may depend on.
const (
	featureBackupTargets     = "backup_targets"
	featureIncrementalBackup = "incremental_backup"
)

// needsFeature is the agent feature a node must have to be sent store; "" when
// every agent understands it.
func needsFeature(store *pb.ObjectStore) string {
	if k := store.GetKind(); k != "" && k != "s3" {
		return featureBackupTargets
	}
	return ""
}

// agentHas asks a node's agent whether it has a feature. An agent too old to
// say has none.
func agentHas(ctx context.Context, client pb.NodeAgentClient, node *models.Node, feature string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := client.GetNodeInfo(agentAuthContext(ctx, node), &pb.GetNodeInfoRequest{})
	if err != nil && status.Code(err) != codes.Unimplemented {
		return false, err
	}
	for _, f := range info.GetFeatures() {
		if f == feature {
			return true, nil
		}
	}
	return false, nil
}

// resetBackupChain records that a VM's disks were replaced: the next scheduled
// backup is a full one.
func resetBackupChain(ctx context.Context, vmID string) {
	if globalWorkerContext == nil || globalWorkerContext.DB == nil {
		return
	}
	_ = globalWorkerContext.DB.WithContext(ctx).Model(&models.BackupSchedule{}).
		Where("vm_id = ?", vmID).Update("chain_reset_at", time.Now()).Error
}

// permanentVMFailure reports an agent's answer to a VM command that another try
// would get again. Agents report every VM command failure as INTERNAL, so
// besides the codes this goes by the few messages that name something only an
// operator can supply.
func permanentVMFailure(agentErr *pb.ErrorResponse, detail string) bool {
	if agentErr.GetRetryable() {
		return false
	}
	switch agentErr.GetCode() {
	case pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, pb.ErrorCode_ERROR_CODE_NOT_FOUND, pb.ErrorCode_ERROR_CODE_ALREADY_EXISTS,
		pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED:
		return true
	}
	d := strings.ToLower(detail)
	return isDomainNotFound(d) || strings.Contains(d, "template image not found") ||
		strings.Contains(d, "template not found") || strings.Contains(d, "no space left on device")
}

// permanentDiskFailure reports whether a failed backup, restore or capture will
// fail the same way if it is tried again. A retry moves the whole disk again,
// so only a failure that can clear on its own is worth one.
//
// rpcErr is the call failing; agentErr is the agent answering that the work
// failed. Exactly one is set.
func permanentDiskFailure(rpcErr error, agentErr *pb.ErrorResponse) bool {
	if rpcErr != nil {
		st, ok := status.FromError(rpcErr)
		if !ok {
			// Not an answer from the agent: the connection could not be made.
			return false
		}
		switch st.Code() {
		case codes.Unavailable, codes.Canceled, codes.Aborted, codes.Internal, codes.Unknown:
			return false
		}
		// Unimplemented, bad arguments, not found, not allowed. A deadline is
		// here too: it is the job's own time limit, and the disk has not got
		// smaller since.
		return true
	}
	if agentErr.GetRetryable() {
		return false
	}
	if agentErr.GetCode() == pb.ErrorCode_ERROR_CODE_UNAVAILABLE {
		return false
	}
	// An agent from before failures carried a code calls every one of them
	// INTERNAL. Only a transfer the store did not refuse is worth repeating.
	if agentErr.GetCode() == pb.ErrorCode_ERROR_CODE_INTERNAL {
		msg := strings.ToLower(agentErr.GetMessage())
		transfer := strings.HasPrefix(msg, "upload failed") || strings.HasPrefix(msg, "download failed")
		return !transfer || storeRefused(msg)
	}
	return true
}

// storeRefused recognises, in an agent's message, an object store that
// answered and said no. The wording is the S3 error codes, which every
// compatible store uses.
func storeRefused(msg string) bool {
	for _, marker := range []string{
		"accessdenied", "access denied", "invalidaccesskeyid", "signaturedoesnotmatch",
		"nosuchbucket", "nosuchkey", "statuscode: 400", "statuscode: 401", "statuscode: 403", "statuscode: 404",
		"invalid storage endpoint", "missing object storage configuration",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// restoresBackup reports that a create builds its disk from a backup. Every new
// try downloads the whole disk, so its failures are not left to River's 25.
func restoresBackup(cfg *pb.VMConfig) bool {
	return cfg.GetMetadata()[models.ImageOnceKey] != ""
}

// createRestoresBackup is restoresBackup for a job whose config is not built
// yet: the job's time limit is asked for before its work starts.
func createRestoresBackup(job VMOperationJob) bool {
	var p struct {
		Metadata map[string]string `json:"metadata"`
	}
	return job.Operation == VMOpCreate && json.Unmarshal(job.Params, &p) == nil && p.Metadata[models.ImageOnceKey] != ""
}

// diskTransferTimeout is how long a create that downloads a backup may take:
// what a restore in place is given. The ordinary create's hour is not enough
// for a large disk.
const diskTransferTimeout = 8 * time.Hour

// createStillRunning recognises a node's answer that an earlier create of the
// same VM has not finished. The node keeps working after the panel's call ends.
func createStillRunning(detail string) bool {
	return strings.Contains(detail, "is already running on this node")
}

// agentFailureMessage is the reason an agent gave for failing, or fallback.
func agentFailureMessage(e *pb.ErrorResponse, fallback string) string {
	if msg := e.GetMessage(); msg != "" {
		return msg
	}
	return fallback
}

// lastDiskAttempt reports that a job has no tries left. Checked against our own
// limit as well as the job's: one queued before the limit existed still
// carries River's 25.
func lastDiskAttempt(attempt, maxAttempts int) bool {
	return attempt >= DiskJobMaxAttempts || attempt >= maxAttempts
}

// recordBackupOutcome writes a backup's failure on its row: failed, when it is
// over, or pending with the reason while another try is still coming. A row
// left failed between tries would let a second backup of the same VM start.
func recordBackupOutcome(ctx context.Context, backupID string, final bool, reason string) {
	if backupID == "" || globalWorkerContext == nil || globalWorkerContext.DB == nil {
		return
	}
	state := models.BackupStatusFailed
	if !final {
		state = models.BackupStatusPending
		reason = "Will be tried again. Last attempt: " + reason
	}
	// The job's context is often the thing that failed (a timeout, a shutdown),
	// and the reason must be written all the same.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = globalWorkerContext.DB.WithContext(ctx).
		Model(&models.Backup{}).
		Where("id = ?", backupID).
		Updates(map[string]interface{}{"status": state, "error_message": reason}).Error
}
