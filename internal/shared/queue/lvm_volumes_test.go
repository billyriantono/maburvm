package queue

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func keptVolumesDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:kept-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE storage_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT, status TEXT, total_space INTEGER DEFAULT 0, used_space INTEGER DEFAULT 0, available_space INTEGER DEFAULT 0, path TEXT NOT NULL, file_format TEXT, alert_threshold INTEGER DEFAULT 90, overcommit INTEGER DEFAULT 0, is_primary BOOLEAN DEFAULT 0, status_note TEXT NOT NULL DEFAULT '', metadata_percent REAL, node_id TEXT NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE storage_volumes (id TEXT PRIMARY KEY, name TEXT NOT NULL, pool_id TEXT NOT NULL, vm_id TEXT, size INTEGER NOT NULL DEFAULT 0, format TEXT, path TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`INSERT INTO storage_pools (id, name, type, path, node_id) VALUES ('p-thin', 'fast', 'lvmthin', 'vg0/thin', 'n1'), ('p-vg', 'slow', 'lvm', 'vg1', 'n1'), ('p-dir', 'data', 'dir', '/data', 'n1'), ('p-far', 'far', 'lvm', 'vg9', 'n2')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestNodeLVMPools(t *testing.T) {
	db := keptVolumesDB(t)
	got := nodeLVMPools(context.Background(), db, "n1")
	var names []string
	for _, p := range got {
		names = append(names, p.GetVg()+"/"+p.GetThinPool())
	}
	if strings.Join(names, ",") != "vg0/thin,vg1/" {
		t.Fatalf("pools = %v", names)
	}
}

func TestRecordKeptVolumes(t *testing.T) {
	ctx := context.Background()
	db := keptVolumesDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vm := &models.VM{ID: "vm-1", Hostname: "web01"}
	kept := []*pb.KeptVolume{
		{Path: "/dev/vg0/legacy1-0", Target: "vda", SizeBytes: 10 << 30, Vg: "vg0", ThinPool: "thin", Reason: "not created by MaburVM"},
		{Path: "/dev/vg1/legacy1-1", Target: "vdb", SizeBytes: 5 << 30, Vg: "vg1"},
		{Path: "/dev/sdb", Target: "vdc", Reason: "not a logical volume"},
	}
	note := recordKeptVolumes(ctx, db, vm, "n1", kept, log)
	if !strings.Contains(note, "2 volume(s) kept") {
		t.Fatalf("note = %q", note)
	}
	var rows []models.StorageVolume
	if err := db.Order("pool_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].PoolID != "p-thin" || rows[0].Name != "web01 vda (kept)" || rows[0].Format != "raw" || rows[0].VMID != nil || rows[1].PoolID != "p-vg" {
		t.Fatalf("rows = %+v", rows)
	}
	// A delete tried again lists nothing twice.
	recordKeptVolumes(ctx, db, vm, "n1", kept, log)
	var n int64
	db.Model(&models.StorageVolume{}).Count(&n)
	if n != 2 {
		t.Fatalf("%d rows after a retry", n)
	}
	if note := recordKeptVolumes(ctx, db, vm, "n1", kept[2:], log); !strings.Contains(note, "outside any storage pool") {
		t.Fatalf("note = %q", note)
	}
	if recordKeptVolumes(ctx, db, vm, "n1", nil, log) != "" {
		t.Fatal("nothing kept, nothing said")
	}
}
