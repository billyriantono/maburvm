package service

import (
	"fmt"
	"sort"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// maxChainLinks bounds an incremental chain: restoring its newest backup
// downloads every one of them.
const maxChainLinks = 40

// chainInput is what deciding the next scheduled backup's mode looks at.
type chainInput struct {
	Now           time.Time
	FullEveryDays int
	DestinationID string // where the backup goes
	NodeID        string // the VM's node now
	ResetAt       *time.Time
	// Allowance is the plan's backup count, nil for none; ManualKept the
	// completed manual backups that count against it and are never pruned.
	Allowance  *int
	ManualKept int
	// Backups are the VM's backups, any order, deleted ones left out.
	Backups []models.Backup
}

// chainDecision is an incremental backup's parent, or a full one (ParentID
// empty) and why.
type chainDecision struct {
	ParentID string
	From     string // the parent's checkpoint
	ChainID  string
	Why      string
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// decideChain picks what the next scheduled backup builds on: the newest
// backup holding a checkpoint (the agent keeps only the newest), when its
// whole chain is still there, on this node and destination, younger than the
// full-backup interval and short enough. Anything else starts a new chain.
func decideChain(in chainInput) chainDecision {
	if in.FullEveryDays <= 0 {
		return chainDecision{}
	}
	byID := map[string]*models.Backup{}
	var leaf *models.Backup
	for i := range in.Backups {
		b := &in.Backups[i]
		byID[b.ID] = b
		if b.Status == models.BackupStatusCompleted && b.Checkpoint != "" && (leaf == nil || b.CreatedAt.After(leaf.CreatedAt)) {
			leaf = b
		}
	}
	full := func(why string) chainDecision { return chainDecision{Why: why} }
	if leaf == nil {
		return full("no earlier backup to build on")
	}
	if strVal(leaf.DestinationID) != in.DestinationID {
		return full("the backup destination changed")
	}
	if strVal(leaf.NodeID) != in.NodeID {
		return full("the VM moved to another node")
	}
	if in.ResetAt != nil && !leaf.CreatedAt.After(*in.ResetAt) {
		return full("the VM's disks were replaced since the last backup")
	}
	chain, err := chainOf(leaf, byID)
	if err != nil {
		return full(err.Error())
	}
	if in.Now.Sub(chain[0].CreatedAt) >= time.Duration(in.FullEveryDays)*24*time.Hour {
		return full(fmt.Sprintf("a full backup is due every %d days", in.FullEveryDays))
	}
	if len(chain) >= maxChainLinks {
		return full("the chain of incremental backups is at its longest")
	}
	if in.Allowance != nil && *in.Allowance > 0 && in.ManualKept+len(chain)+1 > *in.Allowance {
		// The whole chain is needed by its newest backup; only a new full one
		// lets the old chain go within the plan's allowance.
		return full(fmt.Sprintf("the VM's plan keeps %d backups", *in.Allowance))
	}
	return chainDecision{ParentID: leaf.ID, From: leaf.Checkpoint, ChainID: chain[0].ID}
}

// chainOf is the backups b is built from, the full one first and b last. Every
// one must be there and completed.
func chainOf(b *models.Backup, byID map[string]*models.Backup) ([]models.Backup, error) {
	var rev []models.Backup
	for cur := b; ; {
		if cur.Status != models.BackupStatusCompleted {
			return nil, fmt.Errorf("an earlier backup of its chain is %s", cur.Status)
		}
		rev = append(rev, *cur)
		if cur.Mode != models.BackupModeIncremental {
			break
		}
		if len(rev) > maxChainLinks {
			return nil, fmt.Errorf("its chain is longer than %d backups", maxChainLinks)
		}
		parent, ok := byID[strVal(cur.ParentID)]
		if !ok {
			return nil, fmt.Errorf("an earlier backup of its chain was deleted")
		}
		cur = parent
	}
	out := make([]models.Backup, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out, nil
}

// ancestors marks every backup some backup in from is built on.
func ancestors(from []models.Backup, byID map[string]*models.Backup, into map[string]bool) {
	for _, b := range from {
		for cur := byID[strVal(b.ParentID)]; cur != nil && !into[cur.ID]; cur = byID[strVal(cur.ParentID)] {
			into[cur.ID] = true
		}
	}
}

// alive are the backups that hold or will hold files: everything not failed.
func alive(all []models.Backup) []models.Backup {
	var out []models.Backup
	for _, b := range all {
		if b.Status != models.BackupStatusFailed {
			out = append(out, b)
		}
	}
	return out
}

// pruneChains deletes the candidates no kept backup is built on, newest
// first, so a chain goes from its end. A delete that fails keeps that backup,
// and with it everything it is built on.
func pruneChains(all, candidates []models.Backup, del func(models.Backup) error) (deleted []string) {
	byID := map[string]*models.Backup{}
	for i := range all {
		byID[all[i].ID] = &all[i]
	}
	cand := map[string]bool{}
	for _, c := range candidates {
		cand[c.ID] = true
	}
	var kept []models.Backup
	for _, b := range alive(all) {
		if !cand[b.ID] {
			kept = append(kept, b)
		}
	}
	required := map[string]bool{}
	ancestors(kept, byID, required)
	order := append([]models.Backup(nil), candidates...)
	sort.SliceStable(order, func(i, j int) bool { return order[i].CreatedAt.After(order[j].CreatedAt) })
	for _, b := range order {
		if required[b.ID] {
			continue
		}
		if err := del(b); err != nil {
			ancestors([]models.Backup{b}, byID, required)
			continue
		}
		deleted = append(deleted, b.ID)
	}
	return deleted
}

// nextOverAllowance is the oldest completed scheduled backup that may go to
// bring a VM back within its plan: not the newest one, none another backup is
// built on, none in skip. nil when none may.
func nextOverAllowance(all []models.Backup, skip map[string]bool) *models.Backup {
	byID := map[string]*models.Backup{}
	for i := range all {
		byID[all[i].ID] = &all[i]
	}
	live := alive(all)
	required := map[string]bool{}
	ancestors(live, byID, required)
	var scheduled []*models.Backup
	for i := range all {
		b := &all[i]
		if b.Status == models.BackupStatusCompleted && b.BackupType == models.BackupTypeSchedule {
			scheduled = append(scheduled, b)
		}
	}
	sort.SliceStable(scheduled, func(i, j int) bool { return scheduled[i].CreatedAt.Before(scheduled[j].CreatedAt) })
	for i := 0; i < len(scheduled)-1; i++ {
		if !required[scheduled[i].ID] && !skip[scheduled[i].ID] {
			return scheduled[i]
		}
	}
	return nil
}

// dependents counts the backups built directly or indirectly on id.
func dependents(all []models.Backup, id string) int {
	byID := map[string]*models.Backup{}
	for i := range all {
		byID[all[i].ID] = &all[i]
	}
	n := 0
	for _, b := range alive(all) {
		for cur := byID[strVal(b.ParentID)]; cur != nil; cur = byID[strVal(cur.ParentID)] {
			if cur.ID == id {
				n++
				break
			}
		}
	}
	return n
}

// restoreLayers maps each disk of the newest backup of chain to the images
// under it, the full backup's first. Every backup of the chain must hold the
// same disks.
func restoreLayers(chain []models.Backup) (map[string][]models.BackupDisk, error) {
	if len(chain) < 2 {
		return nil, nil
	}
	top := chain[len(chain)-1]
	if len(top.Disks) == 0 {
		return nil, fmt.Errorf("the backup lists no disks")
	}
	out := map[string][]models.BackupDisk{}
	for _, link := range chain[:len(chain)-1] {
		have := map[string]models.BackupDisk{}
		for _, d := range link.Disks {
			have[d.Target] = d
		}
		for _, d := range top.Disks {
			l, ok := have[d.Target]
			if !ok || l.Key == "" || l.Checksum == "" {
				return nil, fmt.Errorf("the backup of %s it is built on has no disk %s", link.CreatedAt.Format(time.RFC3339), d.Target)
			}
			out[d.Target] = append(out[d.Target], models.BackupDisk{Target: l.Target, Key: l.Key, Checksum: l.Checksum})
		}
	}
	return out, nil
}
