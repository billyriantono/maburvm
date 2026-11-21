package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/maburvm/panel/internal/agent/ceph"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

func init() {
	ceph.Dir = filepath.Join(agentDataDir(), "ceph")
}

// cephSetMu lets one change of a cluster's files run at a time.
var cephSetMu sync.Mutex

// SetCephCluster gives this node access to an existing Ceph cluster, or takes
// it away. Only the panel calls it, when staff enable or disable the cluster
// on the node. The key is never logged.
func (s *NodeAgentService) SetCephCluster(ctx context.Context, req *pb.SetCephClusterRequest) (*pb.SetCephClusterResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(code pb.ErrorCode, err error) (*pb.SetCephClusterResponse, error) {
		return &pb.SetCephClusterResponse{Error: migErr(code, err)}, nil
	}
	if !migration.ValidUUID(req.ClusterId) {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, fmt.Errorf("a Ceph cluster id is required"))
	}
	cephSetMu.Lock()
	defer cephSetMu.Unlock()
	if req.Remove {
		// The conf, keyring and secret go only when no VM here needs them.
		users, err := libvirt.SecretUsers(req.ClusterId)
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, fmt.Errorf("could not tell which VMs here use the cluster: %w", err))
		}
		if len(users) > 0 {
			return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, fmt.Errorf("%d VMs on this node have disks on the cluster (%s); move or delete them first",
				len(users), strings.Join(users[:min(len(users), 5)], ", ")))
		}
		if err := libvirt.UndefineSecret(req.ClusterId); err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, err)
		}
		if err := ceph.Remove(req.ClusterId); err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, err)
		}
		log.Printf("[Ceph] cluster %s disabled on this node", req.ClusterId)
		return &pb.SetCephClusterResponse{Success: true}, nil
	}

	// New settings are proved from staged files: a wrong key or monitor list
	// never replaces settings VMs here are running on.
	staged, err := ceph.Stage(req.ClusterId, req.Monitors, req.ClientId, req.Key)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)
	}
	st, err := staged.Status(ctx)
	if err != nil {
		ceph.Discard(staged)
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)
	}
	if req.Fsid != "" && !strings.EqualFold(req.Fsid, st.FSID) {
		ceph.Discard(staged)
		return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, fmt.Errorf("these monitors lead to another cluster (fsid %s), not the one registered (fsid %s); nothing was changed on this node", st.FSID, req.Fsid))
	}
	raw, _ := base64.StdEncoding.DecodeString(req.Key) // Stage checked it
	if err := libvirt.DefineCephSecret(req.ClusterId, raw); err != nil {
		ceph.Discard(staged)
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)
	}
	if _, err := ceph.Commit(staged); err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)
	}
	log.Printf("[Ceph] cluster %s (fsid %s) enabled on this node as client.%s", req.ClusterId, st.FSID, req.ClientId)
	return &pb.SetCephClusterResponse{Success: true, Fsid: st.FSID}, nil
}

// CephStatus reads a cluster's health and capacity as this node sees it, and
// checks the named RBD pools. Every command is bounded, so a cluster that
// does not answer costs seconds, not the caller.
func (s *NodeAgentService) CephStatus(ctx context.Context, req *pb.CephStatusRequest) (*pb.CephStatusResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	c, err := ceph.Load(req.ClusterId)
	if err != nil {
		code := pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		if errors.Is(err, ceph.ErrNotEnabled) {
			code = pb.ErrorCode_ERROR_CODE_NOT_FOUND
		}
		return &pb.CephStatusResponse{Error: migErr(code, err)}, nil
	}
	st, err := c.Status(ctx)
	if err != nil {
		return &pb.CephStatusResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)}, nil
	}
	resp := &pb.CephStatusResponse{Success: true, Fsid: st.FSID, Health: st.Health, HealthSummary: st.Summary}
	if !libvirt.HasSecret(req.ClusterId) {
		resp.Problem = "libvirt on this node no longer holds the cluster's secret, so VMs with disks on the cluster cannot start; check the cluster from the panel to define it again"
	}
	checks := make([]ceph.PoolCheck, len(req.Pools))
	for i, p := range req.Pools {
		checks[i] = ceph.PoolCheck{Pool: p.GetPool(), Namespace: p.GetNamespace()}
	}
	// A client that may not read usage is still a working client.
	usage, uerr := c.Survey(ctx, checks)
	if uerr == nil {
		resp.TotalBytes, resp.UsedBytes, resp.AvailableBytes = usage.Total, usage.Used, usage.Available
	}
	for _, chk := range checks {
		ps := &pb.CephPoolStatus{Pool: chk.Pool, Namespace: chk.Namespace}
		if chk.Err != nil {
			ps.Error = chk.Err.Error()
		} else if u, ok := usage.Pools[ps.Pool]; ok && uerr == nil {
			ps.Measured, ps.UsedBytes, ps.AvailableBytes = true, u.Used, u.Available
		}
		resp.Pools = append(resp.Pools, ps)
	}
	return resp, nil
}
