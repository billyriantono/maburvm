package queue

import (
	"context"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// snapshotsLostOnNode returns the records whose snapshot the node no longer
// has. A snapshot is stored on the node under the record's name.
func snapshotsLostOnNode(records []models.Snapshot, onNode []*pb.SnapshotInfo) []models.Snapshot {
	have := map[string]bool{}
	for _, snap := range onNode {
		have[snap.GetName()] = true
		have[snap.GetSnapshotId()] = true
	}
	var lost []models.Snapshot
	for _, record := range records {
		if !have[record.Name] && !have[record.ID] {
			lost = append(lost, record)
		}
	}
	return lost
}

// reconcileSnapshots drops the records of a VM's snapshots that are gone from
// its node, so the panel stops offering restores that cannot happen.
//
// Deleting a snapshot used to delete every snapshot taken after it as well, and
// a node whose agent has not been updated still does. If the node cannot be
// asked, the records stay: not knowing is not the same as gone.
func (w *SnapshotWorker) reconcileSnapshots(ctx context.Context, client pb.NodeAgentClient, node *models.Node, vmID string) {
	db := globalWorkerContext.DB
	if db == nil {
		return
	}
	// Records first, node second: a snapshot finished in between is then on the
	// node's list without being among the records, never the other way round.
	var records []models.Snapshot
	if err := db.WithContext(ctx).
		Where("vm_id = ? AND status = ?", vmID, models.SnapshotStatusComplete).
		Find(&records).Error; err != nil || len(records) == 0 {
		return
	}
	resp, err := client.CreateSnapshot(agentAuthContext(ctx, node), &pb.SnapshotRequest{
		VmId:      vmID,
		Operation: pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST,
	})
	if err != nil || !resp.GetSuccess() {
		w.logger.WarnContext(ctx, "could not compare snapshot records with the node; left as they are",
			"vm_id", vmID, "error", err, "message", resp.GetError().GetMessage())
		return
	}
	for _, record := range snapshotsLostOnNode(records, resp.GetSnapshots()) {
		if err := db.WithContext(ctx).Delete(&models.Snapshot{}, "id = ?", record.ID).Error; err != nil {
			w.logger.ErrorContext(ctx, "failed to drop the record of a snapshot the node no longer has",
				"vm_id", vmID, "snapshot_id", record.ID, "error", err)
			continue
		}
		w.logger.WarnContext(ctx, "snapshot is gone from the node; its record was removed",
			"vm_id", vmID, "snapshot_id", record.ID, "name", record.Name, "taken_at", record.CreatedAt)
	}
}
