//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/agentsign"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// maxAgentBinary bounds what a self-update may write to disk. The agent is ~36 MB.
const maxAgentBinary = 256 << 20

// swapScript runs in a transient systemd unit, outside this service's cgroup, so
// it survives the restart it performs. It mirrors scripts/deploy-agent.sh:
// back up, swap, restart, and roll back unless the new agent is still up — with
// no extra restarts — after 30s. A crash-looping binary flickers through
// "active" between Restart= attempts, so being active once proves nothing.
// One swap at a time: a second update that arrives during the first's health
// check gives up instead of backing up a binary that is still on trial.
//
// $1 = installed binary, $2 = verified new binary in the same directory.
const swapScript = `set -u
EXE="$1"; NEW="$2"; UNIT=maburvm-agent
exec 9>/run/maburvm-agent-update.lock
flock -n 9 || { echo "another agent update is still running, this one is dropped"; rm -f "$NEW"; exit 1; }
BAK="/root/maburvm-agent.bak-$(date +%Y%m%d-%H%M%S)"
sleep 2  # let the RPC response reach the panel first
cp -a "$EXE" "$BAK" || { echo "backup failed, update aborted"; rm -f "$NEW"; exit 1; }
mv -f "$NEW" "$EXE"
systemctl restart "$UNIT"
R0=$(systemctl show -p NRestarts --value "$UNIT")
sleep 30
if [ "$(systemctl is-active "$UNIT")" = active ] && [ "$(systemctl show -p NRestarts --value "$UNIT")" = "$R0" ]; then
  echo "agent update OK; backup at $BAK"
  ls -1t /root/maburvm-agent.bak-* 2>/dev/null | tail -n +6 | xargs -r rm -f
  exit 0
fi
echo "agent unhealthy after update; rolling back to $BAK"
journalctl -u "$UNIT" -n 20 --no-pager || true
install -m0755 "$BAK" "$EXE"
systemctl restart "$UNIT"
exit 1
`

// UpdateAgent receives a new agent binary from the panel and schedules the swap.
//
// Everything that can be checked before the swap is: the digest, the size, and
// that the binary actually executes on this host and reports the commit the
// panel says it is. That last check catches a libc or libvirt mismatch here,
// with the running agent untouched, rather than in a restart loop.
func (s *NodeAgentService) UpdateAgent(stream pb.NodeAgent_UpdateAgentServer) error {
	fail := func(format string, a ...any) error {
		msg := fmt.Sprintf(format, a...)
		log.Printf("[UpdateAgent] refused: %s", msg)
		return stream.SendAndClose(&pb.AgentUpdateResponse{
			Success: false,
			Message: msg,
			Error:   &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg},
		})
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.Arch != runtime.GOARCH {
		return fail("binary is %s, this node is %s", first.Arch, runtime.GOARCH)
	}
	if first.Size <= 0 || first.Size > maxAgentBinary || len(first.Sha256) != 64 {
		return fail("invalid size or digest in update header")
	}

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return fail("cannot locate own binary: %v", err)
	}
	// Same directory as the installed binary, so the swap is a rename. Named per
	// update, so a second upload cannot overwrite a file a swap is about to move.
	tmp := fmt.Sprintf("%s.new-%d", exe, time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fail("cannot write %s: %v", tmp, err)
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp)
		}
	}()

	h := sha256.New()
	w := io.MultiWriter(f, h)
	var n int64
	for chunk := first; ; {
		n += int64(len(chunk.Data))
		if n > first.Size {
			f.Close()
			return fail("received more than the announced %d bytes", first.Size)
		}
		if _, err := w.Write(chunk.Data); err != nil {
			f.Close()
			return fail("write failed: %v", err)
		}
		chunk, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return fail("write failed: %v", err)
	}
	if n != first.Size {
		return fail("received %d of %d bytes", n, first.Size)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest != strings.ToLower(first.Sha256) {
		return fail("sha256 mismatch: got %s", digest)
	}
	if msg := agentsign.CheckUpdate(os.Getenv("AGENT_UPDATE_PUBKEY"), digest, first.Signature, first.Arch); msg != "" {
		return fail("%s", msg)
	}

	ctx, cancel := context.WithTimeout(stream.Context(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp, "--version").CombinedOutput()
	if err != nil {
		return fail("new binary does not run on this node: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if first.Commit != "" && !strings.Contains(string(out), first.Commit) {
		return fail("new binary reports %q, expected commit %s", strings.TrimSpace(string(out)), first.Commit)
	}

	unit := fmt.Sprintf("maburvm-agent-update-%d", time.Now().Unix())
	cmd := exec.Command("systemd-run", "--unit", unit, "--collect", "--quiet",
		"/bin/bash", "-c", swapScript, "swap", exe, tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fail("could not schedule the swap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	keep = true
	log.Printf("[UpdateAgent] verified %d bytes (%s), swap scheduled as %s", n, first.Sha256[:12], unit)
	return stream.SendAndClose(&pb.AgentUpdateResponse{
		Success: true,
		Message: fmt.Sprintf("update scheduled; the agent restarts in a few seconds (journalctl -u %s)", unit),
	})
}
