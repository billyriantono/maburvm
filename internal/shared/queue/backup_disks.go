package queue

import (
	"context"
	"log/slog"

	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// restoreDisksProto is a backup's data disks as the node is sent them.
func restoreDisksProto(disks []models.BackupDisk, layers map[string][]models.BackupDisk) []*pb.BackupDiskFile {
	var out []*pb.BackupDiskFile
	for _, d := range disks {
		out = append(out, &pb.BackupDiskFile{Target: d.Target, Key: d.Key, Checksum: d.Checksum, VirtualSizeBytes: d.VirtualSize,
			Layers: layersProto(layers[d.Target])})
	}
	return out
}

// layersProto is the images an incremental backup's disk is built on.
func layersProto(layers []models.BackupDisk) []*pb.BackupLayer {
	var out []*pb.BackupLayer
	for _, l := range layers {
		out = append(out, &pb.BackupLayer{Key: l.Key, Checksum: l.Checksum})
	}
	return out
}

// recordRestoredDisks records the data disks a restore as a new VM attached,
// so the panel lists, counts and can detach them like any other.
func recordRestoredDisks(ctx context.Context, db *gorm.DB, vmID string, disks []*pb.BackupDiskFile, logger *slog.Logger) {
	if db == nil {
		return
	}
	for _, d := range disks {
		var n int64
		if err := db.WithContext(ctx).Model(&models.VMDisk{}).Where("vm_id = ? AND device = ?", vmID, d.GetTarget()).Count(&n).Error; err != nil || n > 0 {
			continue // a create tried again: already recorded
		}
		row := &models.VMDisk{VMID: vmID, Device: d.GetTarget(), SizeGB: sizeGB(d.GetVirtualSizeBytes()), Path: d.GetPath()}
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			logger.ErrorContext(ctx, "a restored data disk is attached on the node but could not be recorded",
				"vm_id", vmID, "device", d.GetTarget(), "path", d.GetPath(), "error", err)
		}
	}
}

// sizeGB rounds a disk's size up to whole GB, at least 1.
func sizeGB(bytes int64) int {
	const gib = int64(1) << 30
	return int(max((bytes+gib-1)/gib, 1))
}
