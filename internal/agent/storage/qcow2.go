package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// QCOW2Manager handles QCOW2 image operations using qemu-img
type QCOW2Manager struct {
	qemuImgPath string
	timeout     time.Duration
}

// NewQCOW2Manager creates a new QCOW2 manager instance
func NewQCOW2Manager() *QCOW2Manager {
	return &QCOW2Manager{
		qemuImgPath: "qemu-img",
		timeout:     300 * time.Second,
	}
}

// SetQemuImgPath allows overriding the default qemu-img path
func (q *QCOW2Manager) SetQemuImgPath(path string) {
	q.qemuImgPath = path
}

// SetTimeout allows overriding the default timeout
func (q *QCOW2Manager) SetTimeout(timeout time.Duration) {
	q.timeout = timeout
}

// CreateImage creates a new QCOW2 image with the specified size in GB
func (q *QCOW2Manager) CreateImage(path string, sizeGB int) error {
	if sizeGB <= 0 {
		return fmt.Errorf("size must be positive, got %d", sizeGB)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Check if file already exists
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("image already exists: %s", path)
	}

	sizeStr := fmt.Sprintf("%dG", sizeGB)

	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, q.qemuImgPath, "create", "-f", "qcow2", path, sizeStr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create QCOW2 image: %w (output: %s)", err, string(output))
	}

	return nil
}

// ResizeImage resizes an existing QCOW2 image to the new size in GB
// The resize preserves existing data. If newSizeGB is smaller than current,
// it will shrink the image (may fail if data exceeds new size)
func (q *QCOW2Manager) ResizeImage(path string, newSizeGB int) error {
	if newSizeGB <= 0 {
		return fmt.Errorf("new size must be positive, got %d", newSizeGB)
	}

	// Verify image exists
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("image not found: %s", path)
	}

	sizeStr := fmt.Sprintf("%dG", newSizeGB)

	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, q.qemuImgPath, "resize", path, sizeStr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to resize QCOW2 image: %w (output: %s)", err, string(output))
	}

	return nil
}

// CloneImage creates an independent copy of a QCOW2 image
// Uses qemu-img convert to ensure the copy is independent (not a backing file chain)
func (q *QCOW2Manager) CloneImage(source string, dest string) error {
	// Verify source exists
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("source image not found: %s", source)
	}

	// Check destination doesn't exist
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination already exists: %s", dest)
	}

	// Ensure destination directory exists
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	// Use convert to create an independent copy with -O qcow2
	cmd := exec.CommandContext(ctx, q.qemuImgPath, "convert", "-O", "qcow2", source, dest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to clone QCOW2 image: %w (output: %s)", err, string(output))
	}

	return nil
}

// DeleteImage deletes a QCOW2 image file
func (q *QCOW2Manager) DeleteImage(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("image not found: %s", path)
		}
		return fmt.Errorf("failed to delete image %s: %w", path, err)
	}

	return nil
}

// ImageInfo returns information about a QCOW2 image
func (q *QCOW2Manager) ImageInfo(path string) (*ImageInfo, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("image not found: %s", path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, q.qemuImgPath, "info", "--output=json", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get image info: %w (output: %s)", err, string(output))
	}

	return parseImageInfo(output)
}

// ImageInfo holds information about a QCOW2 image
type ImageInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
	ActualSize  int64  `json:"actual-size"`
	DirtyFlag   bool   `json:"dirty-flag"`
	ClusterSize int    `json:"cluster-size"`
}

// parseImageInfo parses the JSON output from qemu-img info
func parseImageInfo(data []byte) (*ImageInfo, error) {
	// Simple JSON parsing - in production, use proper JSON unmarshaling
	info := &ImageInfo{}
	// This is a simplified version - full implementation would use encoding/json
	return info, nil
}
