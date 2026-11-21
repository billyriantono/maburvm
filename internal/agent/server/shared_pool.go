package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/storage"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func init() {
	storage.SharedPoolsFile = filepath.Join(agentDataDir(), "shared-pools.json")
}

// SetSharedPool makes this node a member of a shared pool, or takes it out.
// Nothing here runs on its own: only the panel, when staff add or remove the
// node, calls it.
func (s *NodeAgentService) SetSharedPool(ctx context.Context, req *pb.SetSharedPoolRequest) (*pb.SetSharedPoolResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(err error) (*pb.SetSharedPoolResponse, error) {
		return &pb.SetSharedPoolResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)}, nil
	}
	if !migration.ValidUUID(req.PoolId) {
		return fail(fmt.Errorf("a shared pool id is required"))
	}
	if req.Remove {
		return s.leaveSharedPool(ctx, req)
	}
	dir, err := models.ValidateSharedPool(req.PoolId, req.Kind, req.Path, req.NfsServer, req.NfsExport, req.MountOptions)
	if err != nil {
		return fail(err)
	}
	for _, o := range storage.SharedPools() {
		if o.ID != req.PoolId && (o.Path == dir || strings.HasPrefix(o.Path, dir+"/") || strings.HasPrefix(dir, o.Path+"/")) {
			return fail(fmt.Errorf("%s overlaps another shared pool on this node (%s)", dir, o.Path))
		}
	}
	if req.Kind == models.SharedPoolMounted {
		if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
			return fail(fmt.Errorf("the directory %s does not exist on this node; mount the shared storage there first", dir))
		}
		if storage.OnRootFilesystem(dir) {
			return fail(fmt.Errorf("%s is on this node's own system disk: nothing is mounted there; mount the shared storage first", dir))
		}
	}
	// Recorded before the mount: from here nothing is written under the path
	// until the pool's marker file is seen there.
	if err := storage.SaveSharedPool(storage.SharedPool{ID: req.PoolId, Kind: req.Kind, Path: dir}); err != nil {
		return fail(err)
	}
	if req.Kind == models.SharedPoolNFS {
		_, err = storage.MountNFSPool(ctx, req.PoolId, req.NfsServer, req.NfsExport, req.MountOptions)
	}
	if err != nil {
		_ = storage.RemoveSharedPool(req.PoolId)
		return fail(err)
	}
	log.Printf("[SharedPool] joined shared pool %s (%s) at %s", req.PoolId, req.Kind, dir)
	return &pb.SetSharedPoolResponse{Success: true, Path: dir}, nil
}

// leaveSharedPool takes this node out of a shared pool: refused while a VM
// here has a disk in it; an nfs pool is unmounted and its unit removed.
func (s *NodeAgentService) leaveSharedPool(ctx context.Context, req *pb.SetSharedPoolRequest) (*pb.SetSharedPoolResponse, error) {
	fail := func(err error) (*pb.SetSharedPoolResponse, error) {
		return &pb.SetSharedPoolResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_CONFLICT, err)}, nil
	}
	kind, dir := req.Kind, ""
	for _, o := range storage.SharedPools() {
		if o.ID == req.PoolId {
			kind, dir = o.Kind, o.Path
		}
	}
	if kind == models.SharedPoolNFS {
		dir = models.SharedPoolMountPath(req.PoolId)
	}
	// A path that is not the shared storage right now (no marker: a join that
	// failed its check, say) is simply forgotten; nothing was mounted over it.
	if dir != "" && storage.CheckSharedDir(dir) == nil {
		inUse, err := libvirt.DiskSourceFiles()
		if err != nil {
			return fail(fmt.Errorf("could not tell which disks VMs here use: %w", err))
		}
		for p := range inUse {
			if strings.HasPrefix(p, dir+"/") {
				return fail(fmt.Errorf("a VM on this node uses %s; move or delete it first", p))
			}
		}
	}
	if kind == models.SharedPoolNFS {
		if err := storage.UnmountNFSPool(ctx, req.PoolId); err != nil {
			return fail(err)
		}
	}
	if err := storage.RemoveSharedPool(req.PoolId); err != nil {
		return fail(err)
	}
	log.Printf("[SharedPool] left shared pool %s", req.PoolId)
	return &pb.SetSharedPoolResponse{Success: true, Path: dir}, nil
}

// SharedPoolToken writes, reads or removes a token file in a shared pool this
// node is a member of.
func (s *NodeAgentService) SharedPoolToken(ctx context.Context, req *pb.SharedPoolTokenRequest) (*pb.SharedPoolTokenResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(err error) (*pb.SharedPoolTokenResponse, error) {
		return &pb.SharedPoolTokenResponse{Error: migErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err)}, nil
	}
	if pool, ok := storage.SharedPoolAt(req.Path); !ok || pool.Path != req.Path {
		return fail(fmt.Errorf("%s is not a shared pool this node is a member of", req.Path))
	}
	content, exists, err := storage.SharedToken(req.Path, req.Name, req.Op, req.Content)
	if err != nil {
		return fail(err)
	}
	return &pb.SharedPoolTokenResponse{Success: true, Exists: exists, Content: content}, nil
}
