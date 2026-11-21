package queue

import (
	"context"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// featureAgent is a backupAgent that says which features it has.
type featureAgent struct {
	backupAgent
	features []string
}

func (a *featureAgent) GetNodeInfo(context.Context, *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	return &pb.GetNodeInfoResponse{Success: true, Features: a.features}, nil
}

func chainJob() *river.Job[BackupJob] {
	j := backupJob(1, 3)
	j.Args.Checkpoint = "maburvm-" + testBackupID
	j.Args.ParentID, j.Args.IncrementalFrom, j.Args.ChainID = "parent-1", "maburvm-parent-1", "full-1"
	return j
}

func TestBackupRecordsTheChainTheNodeMade(t *testing.T) {
	store := &pb.ObjectStore{Endpoint: "s3.example.test", Bucket: "b", AccessKey: "ak", SecretKey: "sk"}
	done := func(mode, checkpoint, note string) *pb.BackupDiskResponse {
		return &pb.BackupDiskResponse{Success: true, UsedRequestStore: true, Mode: mode, Checkpoint: checkpoint, ModeNote: note,
			Disks: []*pb.BackupDiskFile{{Target: "vda", Key: "backups/x.qcow2", SizeBytes: 9, Checksum: "c"}}}
	}

	t.Run("incremental as asked", func(t *testing.T) {
		agent := &backupAgent{resp: done("incremental", "maburvm-"+testBackupID, "")}
		w, db := backupWorkerFixture(t, agent, store)
		if err := w.Work(context.Background(), chainJob()); err != nil {
			t.Fatal(err)
		}
		if got := agent.asked[0]; got.GetCheckpoint() != "maburvm-"+testBackupID || got.GetIncrementalFrom() != "maburvm-parent-1" {
			t.Fatalf("asked %+v", got)
		}
		b := loadBackup(t, db)
		if b.Mode != models.BackupModeIncremental || *b.ParentID != "parent-1" || *b.ChainID != "full-1" || b.Checkpoint != "maburvm-"+testBackupID ||
			*b.DestinationID != "dest-1" || *b.NodeID != testNodeID {
			t.Fatalf("row = %+v", b)
		}
	})

	// The checkpoint was gone: the node took a full backup, which starts its own chain.
	t.Run("node fell back to full", func(t *testing.T) {
		agent := &backupAgent{resp: done("full", "maburvm-"+testBackupID, "Full backup: the checkpoint is gone.")}
		w, db := backupWorkerFixture(t, agent, store)
		if err := w.Work(context.Background(), chainJob()); err != nil {
			t.Fatal(err)
		}
		b := loadBackup(t, db)
		if b.Mode != models.BackupModeFull || b.ParentID != nil || *b.ChainID != testBackupID || !strings.Contains(b.ModeNote, "checkpoint is gone") {
			t.Fatalf("row = %+v", b)
		}
	})

	// An agent from before chains ignores the request and copies everything.
	t.Run("older agent", func(t *testing.T) {
		agent := &backupAgent{resp: done("", "", "")}
		w, db := backupWorkerFixture(t, agent, store)
		if err := w.Work(context.Background(), chainJob()); err != nil {
			t.Fatal(err)
		}
		b := loadBackup(t, db)
		if b.Mode != models.BackupModeFull || b.Checkpoint != "" || !strings.Contains(b.ModeNote, "older than the panel") {
			t.Fatalf("row = %+v", b)
		}
	})
}

// An agent older than SFTP and mounted directories would ignore the kind and
// write to its own storage: the backup is refused instead.
func TestBackupToANewKindNeedsANewAgent(t *testing.T) {
	sftp := &pb.ObjectStore{Kind: "sftp", Sftp: &pb.SFTPStore{Host: "backup.example.test", User: "u", Password: "p", HostKey: "ssh-ed25519 AAAA"}}

	t.Run("older agent", func(t *testing.T) {
		old := &featureAgent{features: []string{"restore_disks"}}
		w, db := backupWorkerFixture(t, old, sftp)
		err := w.Work(context.Background(), backupJob(1, 3))
		if !cancelled(err) {
			t.Fatalf("a backup the node would put elsewhere was not refused: %v", err)
		}
		if len(old.asked) != 0 {
			t.Fatal("the old agent was asked to back up")
		}
		if b := loadBackup(t, db); b.Status != models.BackupStatusFailed || !strings.Contains(b.ErrorMessage, "older than the panel") {
			t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
		}
	})

	t.Run("current agent", func(t *testing.T) {
		current := &featureAgent{features: []string{featureBackupTargets}, backupAgent: backupAgent{resp: &pb.BackupDiskResponse{Success: true, UsedRequestStore: true,
			Disks: []*pb.BackupDiskFile{{Target: "vda", Key: "backups/x.qcow2", SizeBytes: 1, Checksum: "c"}}}}}
		w, db := backupWorkerFixture(t, current, sftp)
		if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
			t.Fatal(err)
		}
		if got := current.asked[0].GetObjectStore(); got.GetKind() != "sftp" || got.GetSftp().GetPassword() != "p" {
			t.Fatalf("sent %v", got)
		}
		if b := loadBackup(t, db); b.StorageProvider != "sftp" || *b.DestinationID != "dest-1" {
			t.Fatalf("row = %+v", b)
		}
	})
}

func TestRestoreRequestCarriesTheChain(t *testing.T) {
	job := RestoreJob{SourceKey: "backups/c.qcow2", Checksum: "cc", Disks: []models.BackupDisk{{Target: "vda", Key: "backups/c.qcow2", Checksum: "cc"}},
		Layers: map[string][]models.BackupDisk{"vda": {{Key: "backups/a.qcow2", Checksum: "aa"}, {Key: "backups/b.qcow2", Checksum: "bb"}}}}
	req := restoreDiskRequest("vm", job, nil)
	// Sent without source_key, so an agent too old for chains cannot restore the top alone.
	if req.GetSourceKey() != "" || len(req.GetDisks()) != 1 {
		t.Fatalf("req = %+v", req)
	}
	layers := req.GetDisks()[0].GetLayers()
	if len(layers) != 2 || layers[0].GetKey() != "backups/a.qcow2" || layers[1].GetChecksum() != "bb" {
		t.Fatalf("layers = %+v", layers)
	}
	// A full backup of one disk goes the way every agent understands.
	if req := restoreDiskRequest("vm", RestoreJob{SourceKey: "k", Checksum: "c", Disks: job.Disks}, nil); req.GetSourceKey() != "k" || len(req.GetDisks()) != 0 {
		t.Fatalf("req = %+v", req)
	}
}

// The key a node encrypted a backup with is recorded on it; a node that
// stored it in the clear (or is too old to say) leaves it empty.
func TestCompletedBackupRecordsTheKey(t *testing.T) {
	enc := completedBackup(&pb.BackupDiskResponse{Disks: []*pb.BackupDiskFile{
		{Target: "vda", Key: "a", SizeBytes: 3, EncryptionKeyId: "00000000-0000-4000-8000-000000000001"},
		{Target: "vdb", Key: "b", SizeBytes: 4, EncryptionKeyId: "00000000-0000-4000-8000-000000000001"},
	}})
	if enc.EncryptionKeyID != "00000000-0000-4000-8000-000000000001" || enc.Size != 7 {
		t.Fatalf("got key %q size %d", enc.EncryptionKeyID, enc.Size)
	}
	if plain := completedBackup(&pb.BackupDiskResponse{SizeBytes: 5, Checksum: "c"}); plain.EncryptionKeyID != "" {
		t.Fatalf("an older agent's backup is recorded as encrypted: %q", plain.EncryptionKeyID)
	}
}
