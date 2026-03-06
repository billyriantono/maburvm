package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultTemplatesDir is the default directory for cached templates
	DefaultTemplatesDir = "/var/lib/mabur/templates"
	// TemplateMetadataFile is the filename for template metadata
	TemplateMetadataFile = "templates.json"
)

// TemplateManager handles template downloading and caching
type TemplateManager struct {
	templatesDir string
	httpClient   *http.Client
}

// TemplateInfo holds metadata about a cached template
type TemplateInfo struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	SourceURL   string    `json:"source_url"`
	Size        int64     `json:"size"`
	Checksum    string    `json:"checksum"`
	CreatedAt   time.Time `json:"created_at"`
	ContentType string    `json:"content_type,omitempty"`
}

// templateMetadata holds the list of cached templates
type templateMetadata struct {
	Templates []TemplateInfo `json:"templates"`
}

// NewTemplateManager creates a new template manager
func NewTemplateManager() *TemplateManager {
	return &TemplateManager{
		templatesDir: DefaultTemplatesDir,
		httpClient: &http.Client{
			Timeout: 30 * time.Minute,
		},
	}
}

// NewTemplateManagerWithDir creates a template manager with a custom directory
func NewTemplateManagerWithDir(dir string) *TemplateManager {
	tm := NewTemplateManager()
	tm.templatesDir = dir
	return tm
}

// SetHTTPClient allows overriding the default HTTP client
func (tm *TemplateManager) SetHTTPClient(client *http.Client) {
	tm.httpClient = client
}

// DownloadTemplate downloads an OS template from URL to dest
// Downloads to a temporary location first, then moves to final destination
func (tm *TemplateManager) DownloadTemplate(url string, dest string) error {
	// Create temp file in same directory as destination for atomic move
	tempFile, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Ensure cleanup on error
	cleanup := func() {
		tempFile.Close()
		os.Remove(tempPath)
	}

	// Download the file
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		cleanup()
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		cleanup()
		return fmt.Errorf("failed to download template: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cleanup()
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Copy to temp file with progress tracking
	_, err = io.Copy(tempFile, resp.Body)
	if err != nil {
		cleanup()
		return fmt.Errorf("failed to write downloaded content: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		cleanup()
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Atomic move from temp to final destination
	if err := os.Rename(tempPath, dest); err != nil {
		cleanup()
		return fmt.Errorf("failed to move file to destination: %w", err)
	}

	return nil
}

// VerifyChecksum verifies the SHA256 checksum of a file
func (tm *TemplateManager) VerifyChecksum(path string, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer file.Close()

	// Decode expected checksum from hex
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil {
		return fmt.Errorf("invalid expected checksum format: %w", err)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to hash file: %w", err)
	}

	computed := hasher.Sum(nil)

	if !hmacEqual(computed, expectedBytes) {
		return fmt.Errorf("checksum mismatch: computed %s, expected %s",
			hex.EncodeToString(computed), expected)
	}

	return nil
}

// hmacEqual compares two byte slices in constant time to prevent timing attacks
func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// ComputeChecksum computes the SHA256 checksum of a file
func (tm *TemplateManager) ComputeChecksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("failed to hash file: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ListTemplates returns a list of cached templates
func (tm *TemplateManager) ListTemplates() ([]TemplateInfo, error) {
	// Ensure templates directory exists
	if err := os.MkdirAll(tm.templatesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create templates directory: %w", err)
	}

	// Read metadata file
	metaPath := filepath.Join(tm.templatesDir, TemplateMetadataFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No metadata file yet, return empty list
			return []TemplateInfo{}, nil
		}
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta templateMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	// Verify templates still exist and update sizes
	var validTemplates []TemplateInfo
	for _, tmpl := range meta.Templates {
		info, err := os.Stat(tmpl.Path)
		if err != nil {
			// Template file doesn't exist, skip
			continue
		}
		tmpl.Size = info.Size()
		validTemplates = append(validTemplates, tmpl)
	}

	return validTemplates, nil
}

// AddTemplate adds a template to the cache metadata
func (tm *TemplateManager) AddTemplate(info TemplateInfo) error {
	// Ensure templates directory exists
	if err := os.MkdirAll(tm.templatesDir, 0755); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}

	// Get file size
	fileInfo, err := os.Stat(info.Path)
	if err != nil {
		return fmt.Errorf("failed to stat template file: %w", err)
	}
	info.Size = fileInfo.Size()
	info.CreatedAt = time.Now()

	// Read existing metadata
	metaPath := filepath.Join(tm.templatesDir, TemplateMetadataFile)
	var meta templateMetadata

	data, err := os.ReadFile(metaPath)
	if err == nil {
		if err := json.Unmarshal(data, &meta); err != nil {
			return fmt.Errorf("failed to parse metadata: %w", err)
		}
	}

	// Check if template already exists and update
	found := false
	for i, t := range meta.Templates {
		if t.Name == info.Name {
			meta.Templates[i] = info
			found = true
			break
		}
	}

	if !found {
		meta.Templates = append(meta.Templates, info)
	}

	// Write metadata back
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// RemoveTemplate removes a template from the cache
func (tm *TemplateManager) RemoveTemplate(name string) error {
	metaPath := filepath.Join(tm.templatesDir, TemplateMetadataFile)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta templateMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("failed to parse metadata: %w", err)
	}

	// Find and remove template
	var newTemplates []TemplateInfo
	for _, t := range meta.Templates {
		if t.Name != name {
			newTemplates = append(newTemplates, t)
		} else {
			// Also delete the file
			os.Remove(t.Path)
		}
	}

	meta.Templates = newTemplates

	// Write metadata back
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// hashWriter wraps a writer and computes hash simultaneously
type hashWriter struct {
	writer io.Writer
	hasher hash.Hash
}

func (hw *hashWriter) Write(p []byte) (n int, err error) {
	hw.hasher.Write(p)
	return hw.writer.Write(p)
}
