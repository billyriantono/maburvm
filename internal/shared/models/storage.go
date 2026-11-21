package models

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StoragePool represents a storage pool on a node
type StoragePool struct {
	ID             string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name           string `json:"name" gorm:"type:varchar(100);not null;uniqueIndex:idx_storage_pool_name_node" validate:"required,max=100"`
	Type           string `json:"type" gorm:"type:varchar(50);not null"`                      // dir, lvm, zfs, etc.
	Status         string `json:"status" gorm:"type:varchar(20);not null;default:'offline'"`  // online, offline, degraded
	TotalSpace     int64  `json:"total_space" gorm:"not null;default:0"`                      // bytes
	UsedSpace      int64  `json:"used_space" gorm:"not null;default:0"`                       // bytes
	AvailableSpace int64  `json:"available_space" gorm:"not null;default:0"`                  // bytes
	Path           string `json:"path" gorm:"type:varchar(255);not null"`                     // filesystem path
	FileFormat     string `json:"file_format" gorm:"type:varchar(20);not null;default:'raw'"` // raw, qcow2
	AlertThreshold int    `json:"alert_threshold" gorm:"not null;default:90"`                 // 0-100 percent
	Overcommit     int64  `json:"overcommit" gorm:"not null;default:0"`                       // bytes, 0 = disabled
	IsPrimary      bool   `json:"is_primary" gorm:"not null;default:false"`
	// StatusNote says why the node cannot use the pool; MetadataPercent is a
	// thin pool's metadata use. Both are measured, not set.
	StatusNote      string         `json:"status_note" gorm:"type:text;not null;default:''"`
	MetadataPercent *float64       `json:"metadata_percent,omitempty"`
	NodeID          string         `json:"node_id" gorm:"type:uuid;not null;uniqueIndex:idx_storage_pool_name_node;index"`
	CreatedAt       time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt       time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`

	// AllocatedSpace is the virtual disk size promised on this pool, in bytes;
	// nil when it could not be worked out. AllocationLimit is the most the
	// operator's ratio allows, 0 when no ratio is set. Both are computed on read.
	AllocatedSpace  *int64 `json:"allocated_space,omitempty" gorm:"-"`
	AllocationLimit int64  `json:"allocation_limit" gorm:"-"`

	// Relations
	Node *Node `json:"node,omitempty" gorm:"foreignKey:NodeID"`
}

// Pool types that hold VM disks. A dir pool keeps qcow2 files in a directory;
// an lvm pool raw logical volumes in a volume group (Path is the group); an
// lvmthin pool thin volumes in a thin pool (Path is "vg/thinpool").
const (
	PoolTypeDir     = "dir"
	PoolTypeLVM     = "lvm"
	PoolTypeLVMThin = "lvmthin"
)

// IsLVMPoolType reports a pool type whose disks are logical volumes.
func IsLVMPoolType(t string) bool { return t == PoolTypeLVM || t == PoolTypeLVMThin }

// lvmNameRe is LVM's own rule for volume group and logical volume names.
var lvmNameRe = regexp.MustCompile(`^[A-Za-z0-9+_.][A-Za-z0-9+_.-]{0,126}$`)

// ValidLVMName reports a usable volume group, thin pool or volume name.
func ValidLVMName(s string) bool { return s != "." && s != ".." && lvmNameRe.MatchString(s) }

// SplitLVMPoolPath returns the volume group and, for a thin pool, the thin
// pool named by an LVM pool's path.
func SplitLVMPoolPath(poolType, path string) (vg, thin string, err error) {
	path = strings.TrimSpace(path)
	switch poolType {
	case PoolTypeLVM:
		vg = path
	case PoolTypeLVMThin:
		var ok bool
		if vg, thin, ok = strings.Cut(path, "/"); !ok || !ValidLVMName(thin) {
			return "", "", fmt.Errorf("a thin pool is named as volume-group/thin-pool, such as vg0/thin")
		}
	default:
		return "", "", fmt.Errorf("pool type %q is not LVM", poolType)
	}
	if !ValidLVMName(vg) {
		return "", "", fmt.Errorf("%q is not a volume group name", vg)
	}
	return vg, thin, nil
}

// IsBlockDevicePath reports a disk path that is a device, not a file.
func IsBlockDevicePath(p string) bool { return strings.HasPrefix(p, "/dev/") }

// LVMPoolHolds reports whether the device path is a logical volume in the
// pool's volume group. Which thin pool holds it only the node can tell.
func LVMPoolHolds(poolType, poolPath, dev string) bool {
	vg, _, err := SplitLVMPoolPath(poolType, poolPath)
	if err != nil {
		return false
	}
	lv, ok := strings.CutPrefix(path.Clean(dev), "/dev/"+vg+"/")
	return ok && ValidLVMName(lv)
}

// lvmDiskPrefix starts the name of every logical volume MaburVM creates.
const lvmDiskPrefix = "maburvm-"

var lvmDiskSuffixRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// LVMDiskName names a VM's disk volume: maburvm-<vm id>-<suffix>, the suffix
// being the target (vda) or a short unique tag.
func LVMDiskName(vmID, suffix string) string { return lvmDiskPrefix + vmID + "-" + suffix }

// OwnsLVMName reports whether lv is a name LVMDiskName gives the VM's disks.
func OwnsLVMName(vmID, lv string) bool {
	rest, ok := strings.CutPrefix(lv, lvmDiskPrefix+vmID+"-")
	return ok && vmID != "" && lvmDiskSuffixRe.MatchString(rest)
}

// MaburVMVolumePath reports a device path whose volume name MaburVM gave it.
func MaburVMVolumePath(dev string) bool {
	return IsBlockDevicePath(dev) && strings.HasPrefix(path.Base(dev), lvmDiskPrefix)
}

// TableName specifies the table name for StoragePool
func (StoragePool) TableName() string {
	return "storage_pools"
}

// BeforeCreate hook for StoragePool
func (p *StoragePool) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return nil
}

// StorageVolume represents a storage volume (disk image) in a pool
type StorageVolume struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	PoolID    string         `json:"pool_id" gorm:"type:uuid;not null;index"`
	VMID      *string        `json:"vm_id,omitempty" gorm:"type:uuid;index"`
	Size      int64          `json:"size" gorm:"not null"` // bytes
	Format    string         `json:"format" gorm:"type:varchar(20);not null;default:'qcow2'"`
	Path      string         `json:"path" gorm:"type:varchar(255);not null"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`

	// Relations
	Pool *StoragePool `json:"pool,omitempty" gorm:"foreignKey:PoolID"`
}

// TableName specifies the table name for StorageVolume
func (StorageVolume) TableName() string {
	return "storage_volumes"
}

// BeforeCreate hook for StorageVolume
func (v *StorageVolume) BeforeCreate(tx *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	return nil
}
