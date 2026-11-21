package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// What staff and owners keep on a VM besides its machine: a lock against
// deletion, the owner's tags, and staff notes.

// ErrVMDeleteLocked refuses removing or reinstalling a locked VM.
var ErrVMDeleteLocked = errors.New("the VM is locked against deletion and reinstall")

// ErrInvalidTags refuses tags that are not short labels.
var ErrInvalidTags = errors.New("invalid tags")

type billingActorKey struct{}

// AsBillingSystem marks ctx as the billing system acting: an owner's delete
// lock does not stop it, a staff lock does.
func AsBillingSystem(ctx context.Context) context.Context {
	return context.WithValue(ctx, billingActorKey{}, true)
}

func isBillingSystem(ctx context.Context) bool {
	v, _ := ctx.Value(billingActorKey{}).(bool)
	return v
}

// deleteLockErr says whether vm's lock refuses removing or reinstalling it now.
func deleteLockErr(ctx context.Context, vm *models.VM) error {
	switch vm.DeleteLock {
	case models.VMDeleteLockStaff:
		return fmt.Errorf("%w: staff locked it; staff must unlock it first", ErrVMDeleteLocked)
	case models.VMDeleteLockOwner:
		if isBillingSystem(ctx) {
			return nil
		}
		return fmt.Errorf("%w: its owner locked it; unlock it first", ErrVMDeleteLocked)
	}
	return nil
}

// nextDeleteLock is the lock after a lock or unlock request. Staff set and
// clear either lock (their lock replaces the owner's); an owner sets and
// clears only their own and cannot lift staff's.
func nextDeleteLock(current string, lock, byStaff bool) (string, error) {
	switch {
	case byStaff && lock:
		return models.VMDeleteLockStaff, nil
	case byStaff:
		return models.VMDeleteLockNone, nil
	case current == models.VMDeleteLockStaff && !lock:
		return current, fmt.Errorf("%w: staff locked it; contact support to unlock it", ErrVMDeleteLocked)
	case current == models.VMDeleteLockStaff:
		return current, nil
	case lock:
		return models.VMDeleteLockOwner, nil
	}
	return models.VMDeleteLockNone, nil
}

// SetDeleteLock locks or unlocks a VM against deletion and reinstall and
// returns the lock now in force.
func (s *VMService) SetDeleteLock(ctx context.Context, vmID string, lock, byStaff bool) (string, error) {
	// Written only over the lock it was decided from, so an owner's unlock
	// racing a staff lock cannot lift the staff lock; a lost race decides again.
	for try := 0; try < 3; try++ {
		vm, err := s.GetVMRecord(ctx, vmID)
		if err != nil {
			return "", err
		}
		next, err := nextDeleteLock(vm.DeleteLock, lock, byStaff)
		if err != nil || next == vm.DeleteLock {
			return next, err
		}
		res := s.db.WithContext(ctx).Table("vms").Where("id = ? AND delete_lock = ?", vmID, vm.DeleteLock).Update("delete_lock", next)
		if res.Error != nil {
			return "", fmt.Errorf("failed to save the lock: %w", res.Error)
		}
		if res.RowsAffected == 1 {
			return next, nil
		}
	}
	return "", errors.New("the lock changed meanwhile; try again")
}

// MaxVMTags is how many tags a VM carries.
const MaxVMTags = 10

var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,31}$`)

// NormalizeTags lower-cases, trims, de-duplicates and sorts tags, refusing
// anything that is not a short label.
func NormalizeTags(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if !tagPattern.MatchString(t) {
			return nil, fmt.Errorf("%w: %q: a tag is 1 to 32 letters, digits, dots, dashes, underscores or colons, starting with a letter or digit", ErrInvalidTags, t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > MaxVMTags {
		return nil, fmt.Errorf("%w: at most %d tags", ErrInvalidTags, MaxVMTags)
	}
	sort.Strings(out)
	return out, nil
}

// SetTags replaces a VM's tags and returns them as saved.
func (s *VMService) SetTags(ctx context.Context, vmID string, tags []string) ([]string, error) {
	clean, err := NormalizeTags(tags)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetVMRecord(ctx, vmID); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(clean)
	if err := s.db.WithContext(ctx).Table("vms").Where("id = ?", vmID).
		Update("tags", gorm.Expr("CAST(? AS jsonb)", string(raw))).Error; err != nil {
		return nil, fmt.Errorf("failed to save the tags: %w", err)
	}
	return clean, nil
}

// StaffNote is a note staff keep on a VM or an account.
type StaffNote struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Body        string    `json:"body"`
	AuthorID    *string   `json:"author_id"`
	AuthorEmail string    `json:"author_email"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (StaffNote) TableName() string { return "staff_notes" }

// Subjects a staff note is kept on.
const (
	NoteOnVM   = "vm"
	NoteOnUser = "user"
)

// ErrNoteNotFound names a note that is not on the subject asked about.
var ErrNoteNotFound = errors.New("note not found")

// maxNoteLength bounds one note.
const maxNoteLength = 4000

func cleanNote(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || len([]rune(body)) > maxNoteLength {
		return "", fmt.Errorf("a note is 1 to %d characters", maxNoteLength)
	}
	return body, nil
}

// ListStaffNotes returns the notes on a subject, newest first.
func ListStaffNotes(ctx context.Context, db *gorm.DB, subjectType, subjectID string) ([]StaffNote, error) {
	out := []StaffNote{}
	err := db.WithContext(ctx).Where("subject_type = ? AND subject_id = ?", subjectType, subjectID).
		Order("created_at DESC").Find(&out).Error
	return out, err
}

// AddStaffNote keeps a note on a subject.
func AddStaffNote(ctx context.Context, db *gorm.DB, subjectType, subjectID, body, authorID, authorEmail string) (*StaffNote, error) {
	body, err := cleanNote(body)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	n := &StaffNote{ID: uuid.New().String(), SubjectType: subjectType, SubjectID: subjectID, Body: body,
		AuthorID: strPtrOrNil(authorID), AuthorEmail: authorEmail, CreatedAt: now, UpdatedAt: now}
	if err := db.WithContext(ctx).Create(n).Error; err != nil {
		return nil, fmt.Errorf("failed to save the note: %w", err)
	}
	return n, nil
}

// UpdateStaffNote rewrites a note on a subject.
func UpdateStaffNote(ctx context.Context, db *gorm.DB, subjectType, subjectID, noteID, body string) (*StaffNote, error) {
	body, err := cleanNote(body)
	if err != nil {
		return nil, err
	}
	res := db.WithContext(ctx).Model(&StaffNote{}).
		Where("id = ? AND subject_type = ? AND subject_id = ?", noteID, subjectType, subjectID).
		Updates(map[string]any{"body": body, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNoteNotFound
	}
	var n StaffNote
	err = db.WithContext(ctx).First(&n, "id = ?", noteID).Error
	return &n, err
}

// DeleteStaffNote removes a note from a subject.
func DeleteStaffNote(ctx context.Context, db *gorm.DB, subjectType, subjectID, noteID string) error {
	res := db.WithContext(ctx).Where("id = ? AND subject_type = ? AND subject_id = ?", noteID, subjectType, subjectID).
		Delete(&StaffNote{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNoteNotFound
	}
	return nil
}
