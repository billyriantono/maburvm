package queue

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// nodeLVMPools are the LVM pools registered on a node, as the node is told
// them: the only places it may grow or remove a logical volume.
func nodeLVMPools(ctx context.Context, db *gorm.DB, nodeID string) []*pb.LVMPool {
	if db == nil {
		return nil
	}
	var pools []models.StoragePool
	if err := db.WithContext(ctx).Where("node_id = ? AND type IN ?", nodeID, []string{models.PoolTypeLVM, models.PoolTypeLVMThin}).
		Find(&pools).Error; err != nil {
		return nil
	}
	var out []*pb.LVMPool
	for _, p := range pools {
		if vg, thin, err := models.SplitLVMPoolPath(p.Type, p.Path); err == nil {
			out = append(out, &pb.LVMPool{Vg: vg, ThinPool: thin})
		}
	}
	return out
}

// recordKeptVolumes lists the volumes a deleted VM left on its node under the
// pool that holds them, where an operator can delete them on purpose, and
// returns a note for the delete's record. A volume outside every registered
// pool is only logged: nothing in the panel may remove it.
func recordKeptVolumes(ctx context.Context, db *gorm.DB, vm *models.VM, nodeID string, kept []*pb.KeptVolume, logger *slog.Logger) string {
	if len(kept) == 0 {
		return ""
	}
	listed := 0
	for _, k := range kept {
		poolID := ""
		if db != nil && k.GetVg() != "" {
			typ, path := models.PoolTypeLVM, k.GetVg()
			if k.GetThinPool() != "" {
				typ, path = models.PoolTypeLVMThin, k.GetVg()+"/"+k.GetThinPool()
			}
			var pool models.StoragePool
			if db.WithContext(ctx).Where("node_id = ? AND type = ? AND path = ?", nodeID, typ, path).First(&pool).Error == nil {
				poolID = pool.ID
			}
		}
		if poolID == "" {
			logger.WarnContext(ctx, "a deleted VM's block device was kept outside any storage pool; remove it on the node by hand if it is no longer needed",
				"vm_id", vm.ID, "path", k.GetPath(), "reason", k.GetReason())
			continue
		}
		var n int64
		if db.WithContext(ctx).Model(&models.StorageVolume{}).Where("pool_id = ? AND path = ?", poolID, k.GetPath()).Count(&n); n > 0 {
			listed++ // a delete tried again: already listed
			continue
		}
		row := &models.StorageVolume{Name: keptVolumeName(vm.Hostname, k.GetTarget()), PoolID: poolID, Size: k.GetSizeBytes(), Format: "raw", Path: k.GetPath()}
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			logger.ErrorContext(ctx, "a deleted VM's volume was kept but could not be listed", "vm_id", vm.ID, "path", k.GetPath(), "error", err)
			continue
		}
		listed++
	}
	if listed == 0 {
		return fmt.Sprintf("; %d block device(s) kept on the node outside any storage pool", len(kept))
	}
	return fmt.Sprintf("; %d volume(s) kept, listed under their storage pool", listed)
}

// keptVolumeName names a kept volume after the VM it belonged to.
func keptVolumeName(hostname, target string) string {
	name := strings.TrimSpace(hostname + " " + target + " (kept)")
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

// NodeLVMPools is nodeLVMPools for the panel's own calls to a node.
func NodeLVMPools(ctx context.Context, db *gorm.DB, nodeID string) []*pb.LVMPool {
	return nodeLVMPools(ctx, db, nodeID)
}
