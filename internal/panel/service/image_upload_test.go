package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/stretchr/testify/require"
)

func TestImageUploadInChunks(t *testing.T) {
	t.Setenv("MABURVM_DATA_DIR", t.TempDir())
	db := setupStorageTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE os_templates (id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL, image_path TEXT NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0,
		os_type TEXT NOT NULL DEFAULT 'linux', is_active BOOLEAN, description TEXT, licensed BOOLEAN NOT NULL DEFAULT 0, plan_ids TEXT, kind TEXT NOT NULL DEFAULT 'disk',
		checksum_sha256 TEXT NOT NULL DEFAULT '', checksum_url TEXT NOT NULL DEFAULT '', checksum_checked_at DATETIME, allow_http BOOLEAN NOT NULL DEFAULT 0,
		auto_refresh BOOLEAN NOT NULL DEFAULT 0, firmware TEXT NOT NULL DEFAULT 'bios', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	s := NewTemplateService(db, repository.NewTemplateRepository(db), repository.NewNodeRepository(db), nil)
	const body = "hello world, this is an iso"
	const sum = "1d7b2d75f2bc5d2b0c4bc0e6cd04a5c96e1d4b3b2ab64cd61c2d43e7c20e7a2f" // not the real one

	_, err := s.StartImageUpload("../evil.iso", int64(len(body)), "", "linux", "")
	require.ErrorIs(t, err, ErrInvalidTemplate)

	// A checksum that does not match is refused at the end, and the upload goes.
	bad, err := s.StartImageUpload("x.iso", int64(len(body)), sum, "linux", "")
	require.NoError(t, err)
	_, err = s.WriteImageChunk(bad, 0, strings.NewReader(body))
	require.NoError(t, err)
	_, err = s.FinishImageUpload(context.Background(), bad)
	require.ErrorIs(t, err, ErrInvalidTemplate)
	_, err = s.WriteImageChunk(bad, 0, strings.NewReader(body))
	require.ErrorIs(t, err, ErrUploadNotFound)

	id, err := s.StartImageUpload("debian.iso", int64(len(body)), "", "linux", "")
	require.NoError(t, err)
	next, err := s.WriteImageChunk(id, 0, strings.NewReader(body[:10]))
	require.NoError(t, err)
	require.EqualValues(t, 10, next)
	// The same chunk again is refused, saying where the upload is.
	at, err := s.WriteImageChunk(id, 0, strings.NewReader(body[:10]))
	require.ErrorIs(t, err, ErrInvalidTemplate)
	require.EqualValues(t, 10, at)
	_, err = s.FinishImageUpload(context.Background(), id)
	require.ErrorContains(t, err, "10 of")
	_, err = s.WriteImageChunk(id, 10, strings.NewReader(body[10:]+"overflow"))
	require.ErrorIs(t, err, ErrInvalidTemplate)
	_, err = s.WriteImageChunk(id, 10, strings.NewReader(body[10:]))
	require.NoError(t, err)

	tmpl, err := s.FinishImageUpload(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, models.TemplateKindISO, tmpl.Kind)
	ref, name, ok := models.SplitUploadImageRef(tmpl.ImagePath)
	require.True(t, ok)
	require.Equal(t, "debian.iso", name)
	require.Equal(t, tmpl.ChecksumSHA256, ref)
	got, err := os.ReadFile(queue.UploadedImagePath(ref, name))
	require.NoError(t, err)
	require.Equal(t, body, string(got))
	whole := sha256.Sum256([]byte(body))
	require.Equal(t, hex.EncodeToString(whole[:]), tmpl.ChecksumSHA256, "the incremental checksum is the file's")
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(queue.UploadedImagePath(ref, name)), "incoming", "*"))
	require.Empty(t, left)
}
