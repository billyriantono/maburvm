package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

func TestTemplateChecksumRules(t *testing.T) {
	const first = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	const second = "486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7"
	var listed atomic.Value
	listed.Store(first)
	var lists atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SHA256SUMS" {
			lists.Add(1)
			fmt.Fprintf(w, "%s *other.img\n%s *noble.img\n", second, listed.Load())
			return
		}
		w.Header().Set("Content-Length", "1234")
	}))
	defer srv.Close()

	ctx := context.Background()
	db := setupStorageTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE os_templates (id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL, image_path TEXT NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0,
		os_type TEXT NOT NULL DEFAULT 'linux', is_active BOOLEAN, description TEXT, licensed BOOLEAN NOT NULL DEFAULT 0, plan_ids TEXT, kind TEXT NOT NULL DEFAULT 'disk',
		checksum_sha256 TEXT NOT NULL DEFAULT '', checksum_url TEXT NOT NULL DEFAULT '', checksum_checked_at DATETIME, allow_http BOOLEAN NOT NULL DEFAULT 0,
		auto_refresh BOOLEAN NOT NULL DEFAULT 0, firmware TEXT NOT NULL DEFAULT 'bios', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	s := NewTemplateService(db, repository.NewTemplateRepository(db), repository.NewNodeRepository(db), nil)
	s.httpClient = srv.Client()
	img := srv.URL + "/images/noble.img"

	_, err := s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "a", Version: "1", FileURL: img})
	require.ErrorIs(t, err, ErrInvalidTemplate, "an https image needs a checksum")
	_, err = s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "b", Version: "1", FileURL: "http://mirror.example/x.img", SkipChecksum: true})
	require.ErrorIs(t, err, ErrInvalidTemplate, "plain http needs allowing")
	_, err = s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "c", Version: "1", FileURL: img, ChecksumSHA256: "abc"})
	require.ErrorIs(t, err, ErrInvalidTemplate, "not a sha256")
	_, err = s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "d", Version: "1", FileURL: img, Firmware: "efi", SkipChecksum: true})
	require.ErrorIs(t, err, ErrInvalidTemplate)
	_, err = s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "e", Version: "1", FileURL: img, AutoRefresh: true, ChecksumSHA256: first})
	require.ErrorIs(t, err, ErrInvalidTemplate, "refreshing needs a list")

	local, err := s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "win", Version: "1", FileURL: "/var/lib/libvirt/templates/w.qcow2", OSType: "windows"})
	require.NoError(t, err, "a path on the nodes needs no checksum")
	require.Equal(t, models.TemplateKindDisk, local.Template.Kind)

	iso, err := s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "inst", Version: "1", FileURL: srv.URL + "/x/Installer.ISO", SkipChecksum: true})
	require.NoError(t, err)
	require.Equal(t, models.TemplateKindISO, iso.Template.Kind)

	res, err := s.CreateTemplate(ctx, &CreateTemplateRequest{Name: "noble", Version: "24.04", FileURL: img, ChecksumURL: srv.URL + "/SHA256SUMS", AutoRefresh: true})
	require.NoError(t, err)
	require.Equal(t, first, res.Template.ChecksumSHA256)
	require.EqualValues(t, 1234, res.Template.SizeBytes)
	require.EqualValues(t, 1, lists.Load())

	// An edit of something else leaves the list alone.
	sum, list, name := first, srv.URL+"/SHA256SUMS", "Ubuntu"
	_, err = s.UpdateTemplate(ctx, res.Template.ID, &UpdateTemplateRequest{Name: name, ChecksumSHA256: &sum, ChecksumURL: &list})
	require.NoError(t, err)
	require.EqualValues(t, 1, lists.Load())

	// A refresh notices a new image, and records it.
	unchanged, err := s.RefreshTemplate(ctx, res.Template.ID, false)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	listed.Store(second)
	changed, err := s.RefreshTemplate(ctx, res.Template.ID, false)
	require.NoError(t, err)
	require.True(t, changed.Changed)
	require.Equal(t, first, changed.OldChecksum)
	require.Equal(t, second, changed.Checksum)
	got, err := s.GetTemplate(ctx, res.Template.ID, false)
	require.NoError(t, err)
	require.Equal(t, second, got.ChecksumSHA256)
	require.NotNil(t, got.ChecksumCheckedAt)

	_, err = s.RefreshTemplate(ctx, local.Template.ID, false)
	require.ErrorIs(t, err, ErrInvalidTemplate, "nothing to refresh from")
}
