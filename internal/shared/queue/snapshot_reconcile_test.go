package queue

import (
	"testing"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// Deleting "monday" on a node with an older agent takes "tuesday" and
// "wednesday" with it. Their records must go; the one still on the node stays.
func TestSnapshotsLostOnNode(t *testing.T) {
	records := []models.Snapshot{
		{ID: "id-sun", Name: "sunday"},
		{ID: "id-tue", Name: "tuesday"},
		{ID: "id-wed", Name: "wednesday"},
		{ID: "id-old", Name: "stored-under-its-id"},
	}
	onNode := []*pb.SnapshotInfo{
		{SnapshotId: "sunday", Name: "sunday"},
		{SnapshotId: "id-old", Name: "id-old"},
		{SnapshotId: "backup-123", Name: "backup-123"}, // not one of the panel's snapshots
	}
	lost := snapshotsLostOnNode(records, onNode)
	if len(lost) != 2 || lost[0].Name != "tuesday" || lost[1].Name != "wednesday" {
		t.Fatalf("lost = %+v, want tuesday and wednesday", lost)
	}
	if lost := snapshotsLostOnNode(records[:1], onNode); lost != nil {
		t.Fatalf("a snapshot that is on the node was reported lost: %+v", lost)
	}
	// A node with no snapshots at all is a real answer, not an unknown one.
	if lost := snapshotsLostOnNode(records, nil); len(lost) != len(records) {
		t.Fatalf("lost = %d of %d", len(lost), len(records))
	}
}
