package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// RestoreSource says that a new VM's disk (CloneSourceRef, an "s3://" key) is a
// backup rather than an image to keep.
type RestoreSource struct {
	// SHA256 is the checksum recorded when the backup was taken.
	SHA256 string
	// InDestination is true when the file is in the panel's backup destination,
	// which the node is then sent. False leaves the node to its own storage.
	InDestination bool
}

// apply adds what the create worker and the node need to know to the job.
func (r *RestoreSource) apply(params map[string]interface{}) {
	params["metadata"] = map[string]string{models.ImageOnceKey: "1", models.ImageSHA256Key: r.SHA256}
	params["object_store"] = r.InDestination
}

// AgentFor returns a client for a node's agent and the context that
// authenticates to it.
func (s *VMService) AgentFor(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, error) {
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	authCtx, err := s.agentAuthContext(ctx, nodeID)
	return client, authCtx, err
}

// SetVMService gives the backup service the create pipeline, for restoring a
// backup as a new VM.
func (s *BackupService) SetVMService(vm *VMService) { s.vmService = vm }

// RestoreAsNewRequest asks for a backup to become a new VM on a chosen node.
type RestoreAsNewRequest struct {
	BackupID string
	NodeID   string
	// Hostname of the new VM. Empty means "restored-<original hostname>".
	Hostname string
	// IPPoolID picks the pool its address comes from. Empty means the first
	// pool on the node with a free address.
	IPPoolID string
}

// RestoreAsNewVM creates a VM from a backup on any active node: the case a
// backup exists for, where the original VM's node is gone. The new VM belongs
// to the original's owner and has its size, speed cap, transfer quota and
// firewall rules, and a new address. The original VM and the backup are left
// as they are.
func (s *BackupService) RestoreAsNewVM(ctx context.Context, req *RestoreAsNewRequest) (*CreateVMResponse, error) {
	if s.vmService == nil || s.dest == nil {
		return nil, errors.New("restoring as a new VM is not available")
	}
	backup, err := s.backupRepo.GetByID(ctx, req.BackupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBackupNotFound
		}
		return nil, fmt.Errorf("failed to get backup: %w", err)
	}
	if backup.Status != models.BackupStatusCompleted {
		return nil, fmt.Errorf("backup is not completed (status: %s)", backup.Status)
	}
	if backup.Checksum == "" {
		return nil, fmt.Errorf("backup has no recorded checksum; cannot safely restore")
	}
	src, err := s.vmRepo.GetByID(ctx, backup.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	node, err := s.nodeRepo.GetByID(ctx, req.NodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Where the file is. A node's own storage is often the destination's
	// bucket: when the file is found there any node can be sent it, and the
	// checksum guards against a different file under the same name.
	inDestination := backup.StorageProvider != models.BackupStoreNode
	if store, serr := s.destinationStore(ctx); serr == nil && store != nil {
		lookCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		found, lerr := store.Exists(lookCtx, backup.StoragePath)
		cancel()
		switch {
		case lerr != nil:
			// The panel not reaching the store says nothing about the node.
		case found:
			inDestination = true
		case inDestination:
			return nil, errors.New("this backup's file is not in the backup destination; if the bucket or endpoint was changed since it was made, the file is still in the old one")
		}
	}
	// Ask the node to prove it can read that storage before anything is created
	// on it: an agent too old to check a download, or a node that cannot reach
	// the store, would otherwise leave a VM that never gets its disk.
	if err := s.dest.CheckNode(ctx, node, inDestination); err != nil {
		return nil, err
	}

	create := restoreCreateRequest(backup, src, req, inDestination)
	if len(create.Hostname) > 100 {
		return nil, fmt.Errorf("hostname %q is too long; give the new VM a shorter one", create.Hostname)
	}
	s.vmService.inheritNetworkLimits(ctx, src.ID, create)
	resp, err := s.vmService.CreateVM(ctx, create)
	if err != nil {
		return nil, err
	}
	s.copyFirewallRules(ctx, src.ID, resp.VM.ID)

	s.logger.InfoContext(ctx, "restoring backup as a new VM",
		"backup_id", backup.ID, "source_vm_id", src.ID, "vm_id", resp.VM.ID, "node_id", node.ID)
	return resp, nil
}

// restoreCreateRequest is the order for the new VM.
func restoreCreateRequest(backup *models.Backup, src *models.VM, req *RestoreAsNewRequest, inDestination bool) *CreateVMRequest {
	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		hostname = "restored-" + src.Hostname
	}
	return &CreateVMRequest{
		UserID:         src.UserID,
		Hostname:       hostname,
		OSTemplateID:   src.OSTemplateID,
		Resources:      src.Resources,
		NodeID:         req.NodeID,
		IPPoolID:       req.IPPoolID,
		AutoAssignIP:   req.IPPoolID == "",
		CloneSourceRef: "s3://" + backup.StoragePath,
		Restore:        &RestoreSource{SHA256: backup.Checksum, InDestination: inDestination},
		// The owner already pays for this server. Their quota still counts the
		// original, and must not be what stops it being brought back.
		Billed: true,
	}
}

// copyFirewallRules gives the restored VM its original's firewall in place of
// the defaults every new VM is seeded with: a server that had other ports open
// would come back unreachable on them. Best-effort, like the seed.
func (s *BackupService) copyFirewallRules(ctx context.Context, srcID, dstID string) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rules []models.FirewallRule
		if err := tx.Where("vm_id = ?", srcID).Find(&rules).Error; err != nil {
			return err
		}
		if err := tx.Where("vm_id = ?", dstID).Delete(&models.FirewallRule{}).Error; err != nil {
			return err
		}
		if len(rules) == 0 {
			return nil
		}
		copies := make([]models.FirewallRule, len(rules))
		for i, r := range rules {
			copies[i] = models.FirewallRule{VMID: dstID, Protocol: r.Protocol, PortRange: r.PortRange,
				Action: r.Action, Direction: r.Direction, SourceIP: r.SourceIP, Priority: r.Priority}
		}
		return tx.Create(&copies).Error
	})
	if err != nil {
		s.logger.WarnContext(ctx, "could not copy the original VM's firewall rules to the restored VM; it has the defaults",
			"source_vm_id", srcID, "vm_id", dstID, "error", err)
	}
}
