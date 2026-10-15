package service

import (
	"testing"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestConfiguredResources(t *testing.T) {
	stored := models.Resources{CPU: 4, RAM: 8196, Disk: 20}

	got, changed := configuredResources(stored, &pb.VMResources{Vcpus: 16, MemoryMb: 8196, DiskGb: 50})
	if !changed || got.CPU != 16 || got.RAM != 8196 || got.Disk != 50 {
		t.Fatalf("drift not adopted: %+v changed=%v", got, changed)
	}

	// Zero fields (unmeasured / old agent) keep the stored value.
	if got, changed := configuredResources(stored, &pb.VMResources{DiskGb: 50}); !changed || got.CPU != 4 || got.RAM != 8196 || got.Disk != 50 {
		t.Fatalf("partial report mishandled: %+v changed=%v", got, changed)
	}
	if _, changed := configuredResources(stored, nil); changed {
		t.Fatal("nil report must not change anything")
	}
	if _, changed := configuredResources(stored, &pb.VMResources{Vcpus: 4, MemoryMb: 8196, DiskGb: 20}); changed {
		t.Fatal("matching spec reported as changed")
	}
}
