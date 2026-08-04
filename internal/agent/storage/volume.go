package storage

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gibibyte = 1024 * 1024 * 1024

// VolumeManager provisions and removes storage volumes across pool backends:
// directory/file (qemu-img), LVM (lvcreate), and ZFS (zfs create -V).
type VolumeManager struct {
	qemu    *QCOW2Manager
	timeout time.Duration
}

// NewVolumeManager creates a VolumeManager.
func NewVolumeManager() *VolumeManager {
	return &VolumeManager{qemu: NewQCOW2Manager(), timeout: 300 * time.Second}
}

// normalizePoolType maps a pool type to one of the
// supported provisioning backends: "dir", "lvm", or "zfs". Thin/compressed
// variants map to their base backend (the volume is real; the thin/compression
// nuance is a pool-level property, not applied per-volume here).
func normalizePoolType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "dir", "file", "directory":
		return "dir"
	case "lvm", "lvmthin", "thin-lvm", "thinlvm":
		return "lvm"
	case "zfs", "zfsthin", "zfscompressed", "zfsthincompressed":
		return "zfs"
	default:
		return strings.ToLower(strings.TrimSpace(t)) // unsupported -> errors below
	}
}

// lvCreateArgs builds args for `lvcreate -L <size>G -n <name> <vg>`.
func lvCreateArgs(vg, name string, sizeGB int) []string {
	return []string{"-L", fmt.Sprintf("%dG", sizeGB), "-n", name, vg}
}

// lvDevicePath returns the device path of a logical volume.
func lvDevicePath(vg, name string) string { return filepath.Join("/dev", vg, name) }

// zfsCreateArgs builds args for `zfs create -V <size>G <dataset>/<name>`.
func zfsCreateArgs(dataset, name string, sizeGB int) []string {
	return []string{"create", "-V", fmt.Sprintf("%dG", sizeGB), dataset + "/" + name}
}

// zfsDevicePath returns the zvol device path.
func zfsDevicePath(dataset, name string) string { return "/dev/zvol/" + dataset + "/" + name }

// CreateVolume provisions a volume in the pool, returning its path/device and
// on-disk (or allocated) size in bytes.
func (m *VolumeManager) CreateVolume(poolType, poolPath, name, format string, sizeGB int) (string, int64, error) {
	if sizeGB <= 0 {
		return "", 0, fmt.Errorf("size must be positive, got %d", sizeGB)
	}
	if strings.TrimSpace(poolPath) == "" || strings.TrimSpace(name) == "" {
		return "", 0, fmt.Errorf("pool path and name are required")
	}

	switch normalizePoolType(poolType) {
	case "dir":
		if format == "" {
			format = "qcow2"
		}
		fname := filepath.Base(name)
		if !hasVolumeExt(fname) {
			if format == "raw" {
				fname += ".img"
			} else {
				fname += ".qcow2"
			}
		}
		full := filepath.Join(poolPath, fname)
		size, err := m.qemu.CreateVolume(full, format, sizeGB)
		return full, size, err

	case "lvm":
		if _, err := m.run("lvcreate", lvCreateArgs(poolPath, filepath.Base(name), sizeGB)...); err != nil {
			return "", 0, err
		}
		return lvDevicePath(poolPath, filepath.Base(name)), int64(sizeGB) * gibibyte, nil

	case "zfs":
		if _, err := m.run("zfs", zfsCreateArgs(poolPath, filepath.Base(name), sizeGB)...); err != nil {
			return "", 0, err
		}
		return zfsDevicePath(poolPath, filepath.Base(name)), int64(sizeGB) * gibibyte, nil

	default:
		return "", 0, fmt.Errorf("volume provisioning for pool type %q is not supported", poolType)
	}
}

// DeleteVolume removes a previously provisioned volume. Each backend validates
// the path shape to avoid removing unrelated files/devices.
func (m *VolumeManager) DeleteVolume(poolType, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("volume path is required")
	}
	switch normalizePoolType(poolType) {
	case "dir":
		if !hasVolumeExt(path) {
			return fmt.Errorf("refusing to delete non-volume path: %s", path)
		}
		return m.qemu.DeleteImage(path)

	case "lvm":
		if !strings.HasPrefix(path, "/dev/") {
			return fmt.Errorf("refusing to lvremove non-device path: %s", path)
		}
		_, err := m.run("lvremove", "-f", path)
		return err

	case "zfs":
		const prefix = "/dev/zvol/"
		if !strings.HasPrefix(path, prefix) {
			return fmt.Errorf("refusing to destroy non-zvol path: %s", path)
		}
		_, err := m.run("zfs", "destroy", strings.TrimPrefix(path, prefix))
		return err

	default:
		return fmt.Errorf("volume deletion for pool type %q is not supported", poolType)
	}
}

// hasVolumeExt reports whether path looks like a provisioned file-based volume.
func hasVolumeExt(path string) bool {
	for _, ext := range []string{".qcow2", ".img", ".raw"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func (m *VolumeManager) run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
