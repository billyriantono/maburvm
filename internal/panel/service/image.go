package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/maburvm/panel/internal/shared/storage"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// ErrImageNotFound is returned when an image does not exist or is not visible to
// the caller.
var ErrImageNotFound = errors.New("image not found")

// ErrImageNotReady is returned when an image is used as a create source before
// its export finished.
var ErrImageNotReady = errors.New("image is not available yet")

// ImageService manages user-owned standalone images (capture from a VM, list,
// delete) and resolves them as a create-VM source. It reuses the agent
// BackupDisk export via an ImageJob.
type ImageService struct {
	db          *gorm.DB
	riverClient *river.Client[pgx.Tx]
	logger      *slog.Logger
}

// NewImageService creates a new ImageService.
func NewImageService(db *gorm.DB, riverClient *river.Client[pgx.Tx], logger *slog.Logger) *ImageService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ImageService{db: db, riverClient: riverClient, logger: logger}
}

// CreateImageFromVM captures a VM's disk to a standalone image. The caller must
// own the VM (or be admin). The heavy lifting runs asynchronously; the returned
// image is in the pending state.
func (s *ImageService) CreateImageFromVM(ctx context.Context, userID uuid.UUID, isAdmin bool, vmID, name string) (*models.Image, error) {
	var vm models.VM
	if err := s.db.WithContext(ctx).First(&vm, "id = ?", vmID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, err
	}
	if !isAdmin && vm.UserID != userID.String() {
		return nil, ErrVMNotFound // don't reveal existence to non-owners
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("image-%s", time.Now().Format("2006-01-02-1504"))
	}

	srcVMID, err := uuid.Parse(vm.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid vm id: %w", err)
	}
	var tmplID *uuid.UUID
	if t, perr := uuid.Parse(vm.OSTemplateID); perr == nil {
		tmplID = &t
	}

	img := &models.Image{
		ID:           uuid.New(),
		UserID:       userID,
		Name:         name,
		SourceVMID:   &srcVMID,
		OSTemplateID: tmplID,
		Status:       models.ImageStatusPending,
	}
	img.ObjectKey = fmt.Sprintf("images/%s.qcow2", img.ID.String())

	if err := s.db.WithContext(ctx).Create(img).Error; err != nil {
		return nil, fmt.Errorf("failed to create image record: %w", err)
	}

	if _, err := s.riverClient.Insert(ctx, queue.ImageJob{
		ImageID:     img.ID.String(),
		VMID:        vm.ID,
		Destination: img.ObjectKey,
	}, nil); err != nil {
		// Roll back the row so a stuck-pending image isn't left behind.
		_ = s.db.WithContext(ctx).Delete(img).Error
		return nil, fmt.Errorf("failed to enqueue image capture: %w", err)
	}
	return img, nil
}

// ListImages returns the caller's images (all images for admins), newest first.
func (s *ImageService) ListImages(ctx context.Context, userID uuid.UUID, isAdmin bool) ([]models.Image, error) {
	var images []models.Image
	q := s.db.WithContext(ctx).Order("created_at DESC")
	if !isAdmin {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.Find(&images).Error; err != nil {
		return nil, err
	}
	return images, nil
}

// getOwned loads an image the caller may act on.
func (s *ImageService) getOwned(ctx context.Context, imageID string, userID uuid.UUID, isAdmin bool) (*models.Image, error) {
	var img models.Image
	if err := s.db.WithContext(ctx).First(&img, "id = ?", imageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrImageNotFound
		}
		return nil, err
	}
	if !isAdmin && img.UserID != userID {
		return nil, ErrImageNotFound
	}
	return &img, nil
}

// DeleteImage removes an image row (soft delete) and best-effort deletes the
// backing object-storage file.
func (s *ImageService) DeleteImage(ctx context.Context, imageID string, userID uuid.UUID, isAdmin bool) error {
	img, err := s.getOwned(ctx, imageID, userID, isAdmin)
	if err != nil {
		return err
	}
	// Best-effort object cleanup. The panel process may not carry storage creds
	// (nodes do), so a missing client is not fatal — an orphaned object is a minor
	// cost, and the row is what gates listing/create-from-image.
	// ponytail: orphaned R2 object on delete when panel has no creds; add a
	// server-side reaper only if storage cost becomes a real problem.
	if img.ObjectKey != "" {
		if client := storageClientFromEnv(); client != nil {
			if derr := client.Delete(ctx, img.ObjectKey); derr != nil {
				s.logger.WarnContext(ctx, "failed to delete image object", "key", img.ObjectKey, "error", derr)
			}
		}
	}
	return s.db.WithContext(ctx).Delete(img).Error
}

// ResolveSource validates an image the caller may create a VM from and returns
// the disk source ref (s3://<key>) plus the base OS template id to record on the
// new VM. It enforces ownership and readiness.
func (s *ImageService) ResolveSource(ctx context.Context, imageID string, userID uuid.UUID, isAdmin bool) (sourceRef, osTemplateID string, err error) {
	img, err := s.getOwned(ctx, imageID, userID, isAdmin)
	if err != nil {
		return "", "", err
	}
	if img.Status != models.ImageStatusAvailable || img.ObjectKey == "" {
		return "", "", ErrImageNotReady
	}
	if img.OSTemplateID == nil {
		// The base template was deleted; we have no OS metadata to attach to the
		// new VM (the vms.os_template_id FK is NOT NULL).
		return "", "", fmt.Errorf("image has no base OS template; cannot create a VM from it")
	}
	return "s3://" + img.ObjectKey, img.OSTemplateID.String(), nil
}

// storageClientFromEnv builds an object-storage client from STORAGE_*/S3_* env
// vars, or returns nil if none are configured. Mirrors the agent's env resolver
// so image object deletion works when the panel shares the same credentials.
func storageClientFromEnv() *storage.Client {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	endpoint := first("STORAGE_ENDPOINT", "S3_ENDPOINT")
	bucket := first("STORAGE_BUCKET", "S3_BUCKET")
	access := first("STORAGE_ACCESS_KEY", "S3_ACCESS_KEY")
	secret := first("STORAGE_SECRET_KEY", "S3_SECRET_KEY")
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		return nil
	}
	region := first("STORAGE_REGION", "S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	client, err := storage.NewClient(&storage.Config{
		Endpoint: endpoint, AccessKey: access, SecretKey: secret,
		Bucket: bucket, Region: region, UsePathStyle: true,
	})
	if err != nil {
		return nil
	}
	return client
}

// ErrImageNotRetryable is returned when an image is not in a state a retry can
// help with.
var ErrImageNotRetryable = errors.New("only a failed or interrupted capture can be retried")

// RetryCapture re-runs a capture that did not finish.
//
// Needed because a capture can end without ever reporting a failure: a panel
// restart cancels the job that was running it, and the image row is left saying
// "pending" with nothing behind it. Eight were in that state after a routine
// deploy, indistinguishable from work still in progress, with no way to ask for
// it again short of deleting the row and starting over.
//
// The same image row is reused rather than a new one created, so the name and
// the object key stay stable and a retry does not multiply rows in the list.
func (s *ImageService) RetryCapture(ctx context.Context, imageID string, userID uuid.UUID, isAdmin bool) (*models.Image, error) {
	img, err := s.getOwned(ctx, imageID, userID, isAdmin)
	if err != nil {
		return nil, err
	}

	// A capture that is genuinely still running must not be started twice: two
	// exports of the same disk would compete for the node's CPU and both take
	// longer than one would have.
	if img.Status == models.ImageStatusAvailable {
		return nil, ErrImageNotRetryable
	}
	if img.Status == models.ImageStatusPending && s.hasActiveJob(ctx, img.ID.String()) {
		return nil, fmt.Errorf("this capture is still running")
	}
	if img.SourceVMID == nil {
		return nil, fmt.Errorf("this image has no source VM to capture from")
	}

	if err := s.db.WithContext(ctx).Model(img).Updates(map[string]any{
		"status":        models.ImageStatusPending,
		"error_message": "",
	}).Error; err != nil {
		return nil, err
	}
	img.Status = models.ImageStatusPending
	img.ErrorMessage = ""

	if _, err := s.riverClient.Insert(ctx, queue.ImageJob{
		ImageID:     img.ID.String(),
		VMID:        img.SourceVMID.String(),
		Destination: img.ObjectKey,
	}, nil); err != nil {
		return nil, fmt.Errorf("failed to enqueue the capture: %w", err)
	}
	return img, nil
}

// hasActiveJob reports whether a capture job for this image is still queued or
// running, so a retry cannot start a second export of the same disk.
func (s *ImageService) hasActiveJob(ctx context.Context, imageID string) bool {
	var count int64
	if err := s.db.WithContext(ctx).Raw(`
		SELECT count(*) FROM river_job
		WHERE kind = 'image'
		  AND state IN ('available', 'running', 'retryable', 'scheduled')
		  AND args->>'image_id' = ?`, imageID).Scan(&count).Error; err != nil {
		// Unable to tell: assume it is running. Refusing a retry is recoverable;
		// starting a duplicate export of a 90 GB disk is not.
		return true
	}
	return count > 0
}

// ReconcileStuckCaptures marks as failed any capture whose job is gone.
//
// Called at startup, because that is exactly when the previous process's
// in-flight jobs were cancelled. Without it those rows say "pending" forever —
// which reads identically to work in progress and hides the fact that nothing is
// happening.
func (s *ImageService) ReconcileStuckCaptures(ctx context.Context) int64 {
	res := s.db.WithContext(ctx).Exec(`
		UPDATE images SET
			status = 'failed',
			error_message = 'The capture was interrupted before it finished, most likely by a panel restart. Retry to run it again.',
			updated_at = NOW()
		WHERE status = 'pending'
		  AND deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM river_job j
			WHERE j.kind = 'image'
			  AND j.state IN ('available', 'running', 'retryable', 'scheduled')
			  AND j.args->>'image_id' = images.id::text
		  )`)
	return res.RowsAffected
}
