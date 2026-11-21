package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// DataDisks are the backup's disks after the primary one, attached to the
	// new VM before its first boot.
	DataDisks []models.BackupDisk
}

// apply adds what the create worker and the node need to know to the job.
func (r *RestoreSource) apply(params map[string]interface{}) {
	params["metadata"] = map[string]string{models.ImageOnceKey: "1", models.ImageSHA256Key: r.SHA256}
	params["object_store"] = r.InDestination
	if len(r.DataDisks) > 0 {
		params["restore_disks"] = r.DataDisks
	}
}

// featureRestoreDisks is what an agent reports when it can create a VM with
// every disk of a backup.
const featureRestoreDisks = "restore_disks"

// checkRestoresDisks refuses a node whose agent would silently leave out a
// backup's data disks.
func (s *BackupService) checkRestoresDisks(ctx context.Context, node *models.Node) error {
	client, authCtx, err := s.vmService.AgentFor(ctx, node.ID)
	if err != nil {
		return err
	}
	infoCtx, cancel := context.WithTimeout(authCtx, 30*time.Second)
	defer cancel()
	info, err := client.GetNodeInfo(infoCtx, &pb.GetNodeInfoRequest{})
	if err != nil && status.Code(err) != codes.Unimplemented {
		return fmt.Errorf("could not ask node %s what its agent can do: %w", node.Name, err)
	}
	for _, f := range info.GetFeatures() {
		if f == featureRestoreDisks {
			return nil
		}
	}
	return fmt.Errorf("this backup has more than one disk, and the agent on node %s is too old to restore them all; update it first", node.Name)
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
	if len(backup.DataDisks()) > 0 {
		if err := s.checkRestoresDisks(ctx, node); err != nil {
			return nil, err
		}
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
		Restore:        &RestoreSource{SHA256: backup.Checksum, InDestination: inDestination, DataDisks: backup.DataDisks()},
		// The owner already pays for this server. Their quota still counts the
		// original, and must not be what stops it being brought back.
		Billed: true,
	}
}

// copyFirewallRules gives the restored VM its original's firewall, and what it
// does with unmatched traffic, in place of the defaults every new VM is seeded
// with: a server that had other ports open would come back unreachable on
// them. Best-effort, like the seed.
func (s *BackupService) copyFirewallRules(ctx context.Context, srcID, dstID string) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var defaults []string
		if err := tx.Table("vms").Where("id = ?", srcID).Pluck("firewall_default", &defaults).Error; err != nil {
			return err
		}
		if len(defaults) == 1 && defaults[0] != "" {
			if err := tx.Table("vms").Where("id = ?", dstID).Update("firewall_default", defaults[0]).Error; err != nil {
				return err
			}
		}
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
				Action: r.Action, Direction: r.Direction, SourceIP: r.SourceIP, Priority: r.Priority,
				ICMPType: r.ICMPType, Log: r.Log, Family: r.Family}
		}
		return tx.Create(&copies).Error
	})
	if err != nil {
		s.logger.WarnContext(ctx, "could not copy the original VM's firewall rules to the restored VM; it has the defaults",
			"source_vm_id", srcID, "vm_id", dstID, "error", err)
	}
}
