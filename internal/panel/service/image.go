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
