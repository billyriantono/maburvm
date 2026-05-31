package storage

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateVolumeValidation(t *testing.T) {
	q := NewQCOW2Manager()

	if _, err := q.CreateVolume("/tmp/vol.qcow2", "qcow2", 0); err == nil {
		t.Fatal("expected error for non-positive size")
	}
	if _, err := q.CreateVolume("/tmp/vol.qcow2", "vhdx", 1); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestConvertImageValidation(t *testing.T) {
	q := NewQCOW2Manager()
	if err := q.ConvertImage("/nonexistent/src.qcow2", "/tmp/dst.qcow2", "vmdk"); err == nil {
		t.Error("expected error for unsupported target format")
	}
	if err := q.ConvertImage("/nonexistent/src.qcow2", "/tmp/dst.qcow2", "qcow2"); err == nil {
		t.Error("expected error for missing source")
	}
}

// TestCreateVolumeRoundTrip exercises real provisioning when qemu-img is present.
func TestCreateVolumeRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed; skipping real provisioning round-trip")
	}
	q := NewQCOW2Manager()
	path := filepath.Join(t.TempDir(), "data.qcow2")

	size, err := q.CreateVolume(path, "qcow2", 1)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if size <= 0 {
		t.Fatalf("expected positive on-disk size, got %d", size)
	}
	// Creating the same path again must fail (already exists).
	if _, err := q.CreateVolume(path, "qcow2", 1); err == nil {
		t.Fatal("expected error creating an existing volume")
	}
	if err := q.DeleteImage(path); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}
}
