package models

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Kinds of shared storage pool. An nfs pool is mounted by each member's agent
// at SharedPoolMountPath; a mounted pool is a directory the operator mounts at
// the same path on every member (NFS, CephFS, GlusterFS, any cluster file
// system).
const (
	SharedPoolNFS     = "nfs"
	SharedPoolMounted = "mounted"
)

// FeatureSharedStorage is the agent feature for shared pools: SetSharedPool,
// SharedPoolToken, and FinishMigrationSourceRequest.keep_paths.
const FeatureSharedStorage = "shared_storage"

// SharedPoolMarker is the file in a shared pool's directory that names the
// pool. A member whose path lacks it is not looking at the shared storage
// (not mounted: the local disk underneath), and writes nothing there.
const SharedPoolMarker = ".maburvm-shared-pool"

// SharedPoolTokenPrefix starts the name of every file the shared-pool token
// call may touch.
const SharedPoolTokenPrefix = ".maburvm-"

// sharedPoolMountRoot holds the mount points of nfs pools, one per pool id.
const sharedPoolMountRoot = "/var/lib/maburvm/pools"

// SharedStoragePool is storage several nodes see at the same path. Each
// member node has its own storage_pools row (StoragePool.SharedPoolID), so
// everything that works on a node's pool works on a member.
type SharedStoragePool struct {
	ID           string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name         string         `json:"name" gorm:"type:varchar(100);not null"`
	Kind         string         `json:"kind" gorm:"type:varchar(16);not null;default:mounted"`
	Path         string         `json:"path" gorm:"type:varchar(255);not null"`
	NFSServer    string         `json:"nfs_server" gorm:"column:nfs_server;type:varchar(255);not null;default:''"`
	NFSExport    string         `json:"nfs_export" gorm:"column:nfs_export;type:varchar(255);not null;default:''"`
	MountOptions string         `json:"mount_options" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

func (SharedStoragePool) TableName() string { return "shared_storage_pools" }

// SharedPoolMountPath is where every member mounts an nfs pool.
func SharedPoolMountPath(poolID string) string { return sharedPoolMountRoot + "/" + poolID }

var (
	uuidRe         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	nfsServerRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]{0,251}[A-Za-z0-9])?$`)
	nfsExportRe    = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,253}$`)
	mountOptionsRe = regexp.MustCompile(`^[A-Za-z0-9_=.,:-]{0,255}$`)
)

// ValidateSharedPool checks a shared pool as both the panel and the agent
// accept it, and returns the path its members use. The agent writes these
// values into a systemd unit, so nothing outside a plain host name, path and
// option list passes.
func ValidateSharedPool(id, kind, dir, server, export, options string) (string, error) {
	if !uuidRe.MatchString(id) {
		return "", fmt.Errorf("the shared pool has no usable id")
	}
	switch kind {
	case SharedPoolMounted:
		if !path.IsAbs(dir) || path.Clean(dir) != dir || dir == "/" || strings.ContainsAny(dir, " \t\n\\\"'") {
			return "", fmt.Errorf("the path must be an absolute directory that every member node mounts, such as /mnt/vmstore")
		}
		return dir, nil
	case SharedPoolNFS:
		if !nfsServerRe.MatchString(server) {
			return "", fmt.Errorf("the NFS server must be a host name or an address")
		}
		if !nfsExportRe.MatchString(export) || path.Clean(export) != export {
			return "", fmt.Errorf("the NFS export must be an absolute path of letters, digits, dots, dashes and underscores, such as /export/vms")
		}
		if !mountOptionsRe.MatchString(options) {
			return "", fmt.Errorf("mount options are a comma-separated list, such as vers=4.2,noatime")
		}
		for _, o := range strings.Split(options, ",") {
			switch o {
			case "nolock", "local_lock=all", "local_lock=posix":
				return "", fmt.Errorf("the %s option is refused: without locks the other nodes see, two nodes could write one disk image at once", o)
			case "soft", "softerr":
				return "", fmt.Errorf("the %s option is refused: a soft mount can silently lose writes to a disk image", o)
			case "ro":
				return "", fmt.Errorf("the ro option is refused: VM disks need a writable mount")
			}
		}
		return SharedPoolMountPath(id), nil
	}
	return "", fmt.Errorf("a shared pool is of kind %q or %q", SharedPoolNFS, SharedPoolMounted)
}
