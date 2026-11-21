package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

var chainT0 = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)

func sp(s string) *string { return &s }

// chainBackup is a completed scheduled backup made on day d; parent "" is a
// full one.
func chainBackup(id string, day int, parent string) models.Backup {
	b := models.Backup{ID: id, Status: models.BackupStatusCompleted, BackupType: models.BackupTypeSchedule, Mode: models.BackupModeFull,
		CreatedAt: chainT0.AddDate(0, 0, day), Checkpoint: "maburvm-" + id, DestinationID: sp("dest"), NodeID: sp("node"),
		Disks: []models.BackupDisk{{Target: "vda", Key: "backups/vm/" + id + ".qcow2", Checksum: "sum-" + id}}}
	b.ChainID = sp(id)
	if parent != "" {
		b.Mode, b.ParentID = models.BackupModeIncremental, sp(parent)
	}
	return b
}

func chainOn(in chainInput, backups ...models.Backup) chainInput {
	in.Backups = backups
	if in.DestinationID == "" {
		in.DestinationID = "dest"
	}
	if in.NodeID == "" {
		in.NodeID = "node"
	}
	return in
}

func TestDecideChain(t *testing.T) {
	f, i1, i2 := chainBackup("f", 0, ""), chainBackup("i1", 1, "f"), chainBackup("i2", 2, "i1")
	day3 := chainT0.AddDate(0, 0, 3)

	got := decideChain(chainOn(chainInput{Now: day3, FullEveryDays: 7}, f, i1, i2))
	require.Equal(t, chainDecision{ParentID: "i2", From: "maburvm-i2", ChainID: "f"}, got)

	for _, c := range []struct {
		name string
		in   chainInput
		why  string
	}{
		{"incremental off", chainOn(chainInput{Now: day3}, f, i1, i2), ""},
		{"nothing yet", chainOn(chainInput{Now: day3, FullEveryDays: 7}), "no earlier backup"},
		{"full due", chainOn(chainInput{Now: chainT0.AddDate(0, 0, 7), FullEveryDays: 7}, f, i1, i2), "every 7 days"},
		{"moved", chainOn(chainInput{Now: day3, FullEveryDays: 7, NodeID: "other"}, f, i1, i2), "another node"},
		{"new destination", chainOn(chainInput{Now: day3, FullEveryDays: 7, DestinationID: "sftp"}, f, i1, i2), "destination changed"},
		{"disks replaced", chainOn(chainInput{Now: day3, FullEveryDays: 7, ResetAt: &i2.CreatedAt}, f, i1, i2), "replaced"},
		{"parent deleted", chainOn(chainInput{Now: day3, FullEveryDays: 7}, f, i2), "deleted"},
		{"plan allowance", chainOn(chainInput{Now: day3, FullEveryDays: 7, Allowance: func() *int { n := 4; return &n }(), ManualKept: 1}, f, i1, i2), "keeps 4"},
	} {
		got := decideChain(c.in)
		require.Empty(t, got.ParentID, c.name)
		require.Contains(t, got.Why, c.why, c.name)
	}

	// A failed backup since does not hide the newest checkpoint.
	failed := models.Backup{ID: "x", Status: models.BackupStatusFailed, CreatedAt: chainT0.AddDate(0, 0, 3)}
	require.Equal(t, "i2", decideChain(chainOn(chainInput{Now: day3, FullEveryDays: 7}, f, i1, i2, failed)).ParentID)

	// A full backup made with the VM off has no checkpoint: the chain goes on.
	off := chainBackup("off", 3, "")
	off.Checkpoint = ""
	require.Equal(t, "i2", decideChain(chainOn(chainInput{Now: chainT0.AddDate(0, 0, 4), FullEveryDays: 7}, f, i1, i2, off)).ParentID)
}

func TestPruneChainsNeverDeletesWhatAKeptBackupNeeds(t *testing.T) {
	f1, a, b := chainBackup("f1", 0, ""), chainBackup("a", 1, "f1"), chainBackup("b", 2, "a")
	f2, c := chainBackup("f2", 7, ""), chainBackup("c", 8, "f2")
	all := []models.Backup{f1, a, b, f2, c}

	// The policy keeps only c: the old chain goes from its end, f2 stays for c.
	var order []string
	deleted := pruneChains(all, []models.Backup{f1, a, b, f2}, func(x models.Backup) error { order = append(order, x.ID); return nil })
	require.Equal(t, []string{"b", "a", "f1"}, deleted)
	require.Equal(t, deleted, order)

	// The policy keeps a: f1 must stay; b may go.
	deleted = pruneChains(all, []models.Backup{f1, b}, func(models.Backup) error { return nil })
	require.Equal(t, []string{"b"}, deleted)

	// b could not be deleted: a and f1 stay with it.
	deleted = pruneChains(all, []models.Backup{f1, a, b}, func(x models.Backup) error {
		if x.ID == "b" {
			return errors.New("store unreachable")
		}
		return nil
	})
	require.Empty(t, deleted)

	// An incremental still running needs its parent.
	running := chainBackup("r", 9, "c")
	running.Status = models.BackupStatusInProgress
	deleted = pruneChains(append(all, running), []models.Backup{c}, func(models.Backup) error { return nil })
	require.Empty(t, deleted)
}

func TestNextOverAllowance(t *testing.T) {
	f1, a, b := chainBackup("f1", 0, ""), chainBackup("a", 1, "f1"), chainBackup("b", 2, "a")
	f2 := chainBackup("f2", 3, "")
	manual := models.Backup{ID: "m", Status: models.BackupStatusCompleted, BackupType: models.BackupTypeManual, Mode: models.BackupModeFull, CreatedAt: chainT0.AddDate(0, 0, -5)}

	// Within the old chain only its end may go first.
	require.Equal(t, "b", nextOverAllowance([]models.Backup{manual, f1, a, b, f2}, nil).ID)
	require.Equal(t, "a", nextOverAllowance([]models.Backup{manual, f1, a, f2}, nil).ID)
	require.Equal(t, "f1", nextOverAllowance([]models.Backup{manual, f1, f2}, nil).ID)
	// The newest is never taken, and nothing the newest needs.
	require.Nil(t, nextOverAllowance([]models.Backup{manual, f1, a, b}, nil))
	// Plain full backups rotate oldest first, as before.
	require.Equal(t, "x1", nextOverAllowance([]models.Backup{chainBackup("x2", 2, ""), chainBackup("x1", 1, ""), chainBackup("x3", 3, "")}, nil).ID)
}

func TestDependentsAndRestoreLayers(t *testing.T) {
	f, a, b := chainBackup("f", 0, ""), chainBackup("a", 1, "f"), chainBackup("b", 2, "a")
	f.Disks = append(f.Disks, models.BackupDisk{Target: "vdb", Key: "backups/vm/f-vdb.qcow2", Checksum: "sum-f-vdb"})
	a.Disks = append(a.Disks, models.BackupDisk{Target: "vdb", Key: "backups/vm/a-vdb.qcow2", Checksum: "sum-a-vdb"})
	b.Disks = append(b.Disks, models.BackupDisk{Target: "vdb", Key: "backups/vm/b-vdb.qcow2", Checksum: "sum-b-vdb"})
	all := []models.Backup{f, a, b}
	require.Equal(t, 2, dependents(all, "f"))
	require.Equal(t, 1, dependents(all, "a"))
	require.Equal(t, 0, dependents(all, "b"))

	byID := map[string]*models.Backup{"f": &all[0], "a": &all[1], "b": &all[2]}
	chain, err := chainOf(byID["b"], byID)
	require.NoError(t, err)
	ids := []string{}
	for _, c := range chain {
		ids = append(ids, c.ID)
	}
	require.Equal(t, "f,a,b", strings.Join(ids, ","))

	layers, err := restoreLayers(chain)
	require.NoError(t, err)
	require.Equal(t, []models.BackupDisk{{Target: "vda", Key: "backups/vm/f.qcow2", Checksum: "sum-f"}, {Target: "vda", Key: "backups/vm/a.qcow2", Checksum: "sum-a"}}, layers["vda"])
	require.Equal(t, "backups/vm/a-vdb.qcow2", layers["vdb"][1].Key)

	// A link without one of the disks cannot be restored.
	a.Disks = a.Disks[:1]
	_, err = restoreLayers([]models.Backup{f, a, b})
	require.ErrorContains(t, err, "no disk vdb")

	// A full backup restores alone.
	layers, err = restoreLayers([]models.Backup{f})
	require.NoError(t, err)
	require.Nil(t, layers)
}
