package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ImageSettingsSection is the system_settings row naming the drivers ISO.
const ImageSettingsSection = "images"

// ImageSettings are the image choices staff make once for the panel.
type ImageSettings struct {
	// DriversISOTemplateID is the ISO put in a Windows install's second drive
	// (the virtio drivers disc), when the order asks for it.
	DriversISOTemplateID string `json:"drivers_iso_template_id"`
}

// ErrNoDriversISO is an order for the drivers disc when staff chose none.
var ErrNoDriversISO = errors.New("no drivers ISO is set: choose one on the ISOs page first")

// LoadImageSettings reads the image settings; none set is the zero value.
func LoadImageSettings(ctx context.Context, db *gorm.DB) ImageSettings {
	var raw string
	var out ImageSettings
	if err := db.WithContext(ctx).Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", ImageSettingsSection).Scan(&raw).Error; err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// DriversISOTemplate is the drivers ISO staff chose.
func DriversISOTemplate(ctx context.Context, db *gorm.DB) (*models.OSTemplate, error) {
	id := LoadImageSettings(ctx, db).DriversISOTemplateID
	if id == "" {
		return nil, ErrNoDriversISO
	}
	var t models.OSTemplate
	if err := db.WithContext(ctx).Where("id = ?", id).First(&t).Error; err != nil || !t.IsISO() {
		return nil, ErrNoDriversISO
	}
	return &t, nil
}

// SetDriversISO makes an ISO template the drivers disc; "" clears it.
func SetDriversISO(ctx context.Context, db *gorm.DB, templateID string) error {
	if templateID != "" {
		var t models.OSTemplate
		if err := db.WithContext(ctx).Where("id = ?", templateID).First(&t).Error; err != nil {
			return ErrTemplateNotFound
		}
		if !t.IsISO() {
			return fmt.Errorf("%w: %s is not an ISO", ErrInvalidTemplate, t.Name)
		}
	}
	data, _ := json.Marshal(ImageSettings{DriversISOTemplateID: templateID})
	return db.WithContext(ctx).Exec(`INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW())
		ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()`, ImageSettingsSection, string(data)).Error
}

// checkImageAddress applies the rules for a template's image address: a URL
// is https, or http when staff allowed it (the checksum still guards it); an
// uploaded image is checked by its own name; anything else is a path on the
// nodes, which staff put there.
func checkImageAddress(ref string, allowHTTP bool) error {
	if _, _, ok := models.SplitUploadImageRef(ref); ok {
		return nil
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" {
		if strings.HasPrefix(ref, "/") {
			return nil
		}
		return fmt.Errorf("%w: the image address must be an https URL or an absolute path on the nodes", ErrInvalidTemplate)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowHTTP {
			return nil
		}
		return fmt.Errorf("%w: the image address is plain http, which anyone on the way can change; use https, or allow http for this template (its checksum still protects it)", ErrInvalidTemplate)
	}
	return fmt.Errorf("%w: the image address must be an https URL or an absolute path on the nodes", ErrInvalidTemplate)
}

var hexSHA256 = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

// ParseChecksumList finds the sha256 of a file in a checksum list: a
// SHA256SUMS file ("<hash>  <name>", or "<hash> *<name>"), a BSD-style list
// ("SHA256 (<name>) = <hash>"), or a file holding a single hash.
func ParseChecksumList(body, fileName string) (string, error) {
	var only []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "SHA256 ("); ok {
			if name, sum, ok := strings.Cut(rest, ") = "); ok && path.Base(name) == fileName && hexSHA256.MatchString(sum) {
				return strings.ToLower(strings.TrimSpace(sum)), nil
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && len(f[0]) == 64 && hexSHA256.MatchString(f[0]) {
			if path.Base(strings.TrimPrefix(f[len(f)-1], "*")) == fileName {
				return strings.ToLower(f[0]), nil
			}
			continue
		}
		if len(f) == 1 && len(f[0]) == 64 && hexSHA256.MatchString(f[0]) {
			only = append(only, strings.ToLower(f[0]))
		}
	}
	if len(only) == 1 {
		return only[0], nil
	}
	return "", fmt.Errorf("%w: the checksum list has no sha256 for %s", ErrInvalidTemplate, fileName)
}

// imageFileName is the file name an image address ends in.
func imageFileName(ref string) string {
	if u, err := url.Parse(ref); err == nil && u.Path != "" {
		return path.Base(u.Path)
	}
	return path.Base(ref)
}

// fetchChecksum reads the sha256 of the image at imageRef from a checksum list.
func (s *TemplateService) fetchChecksum(ctx context.Context, checksumURL, imageRef string) (string, error) {
	u, err := url.Parse(checksumURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%w: the checksum list must be an http(s) URL", ErrInvalidTemplate)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: the checksum list could not be fetched: %v", ErrInvalidTemplate, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: the checksum list answered HTTP %d", ErrInvalidTemplate, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return ParseChecksumList(string(body), imageFileName(imageRef))
}

// resolveChecksum settles a template's checksum from what staff gave: a
// sha256, or a checksum list to read it from (which wins when both are given
// and must then agree).
func (s *TemplateService) resolveChecksum(ctx context.Context, sum, checksumURL, imageRef string) (string, error) {
	sum = strings.ToLower(strings.TrimSpace(sum))
	if sum != "" && !models.ValidSHA256(sum) {
		return "", fmt.Errorf("%w: the checksum must be a sha256 (64 hex characters)", ErrInvalidTemplate)
	}
	if checksumURL == "" {
		return sum, nil
	}
	listed, err := s.fetchChecksum(ctx, checksumURL, imageRef)
	if err != nil {
		return "", err
	}
	if sum != "" && sum != listed {
		return "", fmt.Errorf("%w: the checksum given (%s) is not the one the checksum list has (%s)", ErrInvalidTemplate, sum, listed)
	}
	return listed, nil
}

// TemplateRefreshResult says what a refresh found.
type TemplateRefreshResult struct {
	TemplateID  string   `json:"template_id"`
	Changed     bool     `json:"changed"`
	OldChecksum string   `json:"old_checksum"`
	Checksum    string   `json:"checksum"`
	NodeIDs     []string `json:"node_ids"`
}

// RefreshTemplate reads a template's checksum list again and, when the image
// changed, records the new checksum and has every active node fetch it. VMs
// built from the old image are copies of it and are not touched; each node
// drops its old copy once the new one is in and nothing uses the old.
// force fetches onto the nodes even when nothing changed.
func (s *TemplateService) RefreshTemplate(ctx context.Context, id string, force bool) (*TemplateRefreshResult, error) {
	t, err := s.templateRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, err
	}
	res := &TemplateRefreshResult{TemplateID: t.ID, OldChecksum: t.ChecksumSHA256, Checksum: t.ChecksumSHA256}
	if t.ChecksumURL != "" {
		sum, err := s.fetchChecksum(ctx, t.ChecksumURL, t.ImagePath)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		cols := map[string]any{"checksum_checked_at": now, "updated_at": now}
		if sum != t.ChecksumSHA256 {
			cols["checksum_sha256"], res.Checksum, res.Changed = sum, sum, true
		}
		if err := s.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("id = ?", t.ID).Updates(cols).Error; err != nil {
			return nil, err
		}
	} else if !force {
		return nil, fmt.Errorf("%w: this template has no checksum list to refresh from; give it one, or sync it to the nodes", ErrInvalidTemplate)
	}
	if !res.Changed && !force {
		return res, nil
	}
	if s.riverClient == nil {
		return res, nil
	}
	sync, err := s.SyncTemplate(ctx, t.ID, &SyncTemplateRequest{Force: true})
	if err != nil {
		return nil, err
	}
	res.NodeIDs = sync.NodeIDs
	return res, nil
}

// AutoRefresh refreshes every template that asks for it, once.
func (s *TemplateService) AutoRefresh(ctx context.Context, logger *slog.Logger) {
	var ids []string
	if err := s.db.WithContext(ctx).Model(&models.OSTemplate{}).
		Where("auto_refresh = ? AND checksum_url <> '' AND is_active = ?", true, true).Pluck("id", &ids).Error; err != nil {
		logger.WarnContext(ctx, "template auto-refresh skipped", "error", err)
		return
	}
	for _, id := range ids {
		res, err := s.RefreshTemplate(ctx, id, false)
		if err != nil {
			logger.WarnContext(ctx, "template auto-refresh failed", "template_id", id, "error", err)
			continue
		}
		if res.Changed {
			logger.InfoContext(ctx, "template image changed; nodes are fetching it", "template_id", id, "checksum", res.Checksum)
		}
	}
}

// AutoRefreshLoop runs AutoRefresh once a day until ctx ends.
func (s *TemplateService) AutoRefreshLoop(ctx context.Context, logger *slog.Logger) {
	timer := time.NewTimer(15 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.AutoRefresh(ctx, logger)
		timer.Reset(24 * time.Hour)
	}
}

// Uploading an ISO to the panel goes in chunks small enough for every proxy
// on the way: start, send the chunks in order, then finish, which checks the
// file and adds it as an ISO.

// MaxImageUpload bounds an image uploaded to the panel.
const MaxImageUpload = 64 << 30

// ErrUploadNotFound is an upload id with no upload behind it.
var ErrUploadNotFound = errors.New("upload not found; start it again")

type imageUpload struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"` // what staff expect, when they gave it
	OSType    string `json:"os_type"`
	CreatedBy string `json:"created_by"`
	// HashState is the sha256 of the bytes received so far, so finishing
	// does not read a large file again inside one request.
	HashState []byte `json:"hash_state,omitempty"`
}

// uploadLocks keeps two requests from writing one upload at once.
// ponytail: one small entry per upload until the panel restarts.
var uploadLocks sync.Map

func lockUpload(id string) func() {
	m, _ := uploadLocks.LoadOrStore(id, &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	return m.(*sync.Mutex).Unlock
}

func saveUpload(meta string, u imageUpload) error {
	b, _ := json.Marshal(u)
	tmp := meta + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, meta)
}

func uploadDir() string { return filepath.Dir(queue.UploadedImagePath(strings.Repeat("0", 64), "x")) }

func uploadPaths(id string) (meta, data string, err error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", "", ErrUploadNotFound
	}
	dir := filepath.Join(uploadDir(), "incoming")
	return filepath.Join(dir, id+".json"), filepath.Join(dir, id+".part"), nil
}

// StartImageUpload opens an upload of an ISO named name, size bytes.
func (s *TemplateService) StartImageUpload(name string, size int64, sum, osType, userID string) (string, error) {
	name = strings.TrimSpace(name)
	if !models.ValidUploadName(name) {
		return "", fmt.Errorf("%w: the file name may hold letters, digits, dots, dashes, underscores and plus signs only", ErrInvalidTemplate)
	}
	if size <= 0 || size > MaxImageUpload {
		return "", fmt.Errorf("%w: an uploaded image is at most %d GB", ErrInvalidTemplate, MaxImageUpload>>30)
	}
	sum = strings.ToLower(strings.TrimSpace(sum))
	if sum != "" && !models.ValidSHA256(sum) {
		return "", fmt.Errorf("%w: the checksum must be a sha256 (64 hex characters)", ErrInvalidTemplate)
	}
	if osType == "" {
		osType = models.OSTypeLinux
	}
	if !models.ValidOSType(osType) {
		return "", fmt.Errorf("%w: unknown system type %q", ErrInvalidTemplate, osType)
	}
	id := uuid.New().String()
	meta, data, _ := uploadPaths(id)
	if err := os.MkdirAll(filepath.Dir(meta), 0o700); err != nil {
		return "", err
	}
	// Uploads left unfinished for a day are given up on.
	if old, err := os.ReadDir(filepath.Dir(meta)); err == nil {
		for _, e := range old {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.Remove(filepath.Join(filepath.Dir(meta), e.Name()))
			}
		}
	}
	if err := saveUpload(meta, imageUpload{Name: name, Size: size, SHA256: sum, OSType: osType, CreatedBy: userID}); err != nil {
		return "", err
	}
	if err := os.WriteFile(data, nil, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func loadUpload(id string) (imageUpload, string, string, error) {
	var u imageUpload
	meta, data, err := uploadPaths(id)
	if err != nil {
		return u, "", "", err
	}
	b, err := os.ReadFile(meta)
	if err != nil || json.Unmarshal(b, &u) != nil {
		return u, "", "", ErrUploadNotFound
	}
	return u, meta, data, nil
}

// WriteImageChunk appends a chunk at offset, which must be where the upload
// has got to: chunks go in order, and a chunk sent twice is taken once.
func (s *TemplateService) WriteImageChunk(id string, offset int64, r io.Reader) (int64, error) {
	defer lockUpload(id)()
	u, meta, data, err := loadUpload(id)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	if len(u.HashState) > 0 {
		if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(u.HashState); err != nil {
			return 0, err
		}
	}
	f, err := os.OpenFile(data, os.O_WRONLY, 0)
	if err != nil {
		return 0, ErrUploadNotFound
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if offset != fi.Size() {
		return fi.Size(), fmt.Errorf("%w: the upload is at byte %d, not %d", ErrInvalidTemplate, fi.Size(), offset)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, u.Size-offset+1))
	if err == nil && offset+n > u.Size {
		err = fmt.Errorf("%w: the file is larger than the %d bytes announced", ErrInvalidTemplate, u.Size)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		u.HashState, err = h.(encoding.BinaryMarshaler).MarshalBinary()
	}
	if err == nil {
		err = saveUpload(meta, u)
	}
	if err != nil {
		// Back to where the last whole chunk ended, so the chunk can be sent again.
		_ = f.Truncate(offset)
		return offset, err
	}
	return offset + n, nil
}

// CancelImageUpload drops an unfinished upload.
func (s *TemplateService) CancelImageUpload(id string) error {
	defer lockUpload(id)()
	return dropUpload(id)
}

func dropUpload(id string) error {
	_, meta, data, err := loadUpload(id)
	if err != nil {
		return err
	}
	_ = os.Remove(data)
	return os.Remove(meta)
}

// FinishImageUpload checks a complete upload against its size and checksum,
// keeps it under its checksum and adds it as an ISO template, then has every
// active node copy it.
func (s *TemplateService) FinishImageUpload(ctx context.Context, id string) (*models.OSTemplate, error) {
	u, meta, data, err := loadUpload(id)
	if err != nil {
		return nil, err
	}
	defer lockUpload(id)()
	fi, err := os.Stat(data)
	if err != nil {
		return nil, ErrUploadNotFound
	}
	if n := fi.Size(); n != u.Size {
		return nil, fmt.Errorf("%w: %d of %d bytes have arrived", ErrInvalidTemplate, n, u.Size)
	}
	h := sha256.New()
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(u.HashState); err != nil {
		return nil, fmt.Errorf("%w: the upload's checksum was lost; upload it again", ErrInvalidTemplate)
	}
	n := fi.Size()
	sum := hex.EncodeToString(h.Sum(nil))
	if u.SHA256 != "" && u.SHA256 != sum {
		_ = dropUpload(id)
		return nil, fmt.Errorf("%w: the file arrived with sha256 %s, not the %s expected; upload it again", ErrInvalidTemplate, sum, u.SHA256)
	}
	final := queue.UploadedImagePath(sum, u.Name)
	if err := os.Rename(data, final); err != nil {
		return nil, err
	}
	_ = os.Remove(meta)
	t := &models.OSTemplate{
		Name: u.Name, Version: sum[:12], OSType: u.OSType, ImagePath: models.UploadImageRef(sum, u.Name),
		SizeBytes: n, IsActive: true, Kind: models.TemplateKindISO, ChecksumSHA256: sum, Firmware: models.FirmwareBIOS,
		Description: "Uploaded to the panel",
	}
	if exists, err := s.templateRepo.NameAndVersionExists(ctx, t.Name, t.Version); err == nil && exists {
		return nil, ErrTemplateAlreadyExists
	}
	if err := s.templateRepo.Create(ctx, t); err != nil {
		return nil, fmt.Errorf("failed to add the ISO: %w", err)
	}
	if s.riverClient != nil {
		if _, err := s.SyncTemplate(ctx, t.ID, &SyncTemplateRequest{}); err != nil {
			slog.Default().WarnContext(ctx, "uploaded ISO not sent to the nodes yet", "template_id", t.ID, "error", err)
		}
	}
	return t, nil
}

// removeUploadedImage deletes an uploaded image's file once no template
// refers to it.
func (s *TemplateService) removeUploadedImage(ctx context.Context, ref string) {
	sum, name, ok := models.SplitUploadImageRef(ref)
	if !ok {
		return
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("image_path = ?", ref).Count(&n).Error; err != nil || n > 0 {
		return
	}
	_ = os.Remove(queue.UploadedImagePath(sum, name))
}

// ISOTemplateFor is an ISO template a VM may take: active, an ISO, offered on
// the VM's plan to a customer, and, when uploaded to the panel, already on the
// VM's node.
func (s *VMService) ISOTemplateFor(ctx context.Context, vmID, templateID string, byCustomer bool) (*models.OSTemplate, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil, ErrVMNotFound
	}
	t, err := s.templateRepo.GetByID(ctx, templateID)
	if err != nil || !t.IsISO() || !t.IsActive {
		return nil, fmt.Errorf("that ISO is not available")
	}
	if byCustomer {
		plan := ""
		if vm.PlanID != nil {
			plan = *vm.PlanID
		}
		if !t.OfferedOn(plan) {
			return nil, fmt.Errorf("that ISO is not offered on this server's plan")
		}
	}
	if _, _, uploaded := models.SplitUploadImageRef(t.ImagePath); uploaded {
		var status string
		_ = s.db.WithContext(ctx).Raw("SELECT status FROM template_node_status WHERE template_id = ? AND node_id = ?", t.ID, vm.NodeID).Scan(&status).Error
		if status != string(SyncStatusAvailable) {
			return nil, fmt.Errorf("%s is still being copied to this server's node; try again in a few minutes", t.Name)
		}
	}
	return t, nil
}

// AttachedISO2 is the ISO in a VM's second drive as last confirmed by its node;
// nil when not known (or a database without the column).
func (s *VMService) AttachedISO2(ctx context.Context, vmID string) *string {
	var iso *string
	q := s.db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard})
	if err := q.Raw("SELECT attached_iso_2 FROM vms WHERE id = ?", vmID).Scan(&iso).Error; err != nil {
		return nil
	}
	return iso
}
