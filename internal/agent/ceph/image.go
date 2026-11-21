package ceph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"libvirt.org/go/libvirtxml"
)

// Image is a VM disk on RBD, reached through a cluster enabled on this node.
// Everything is done with the rbd tool and qemu-img, as the cluster's client.
type Image struct {
	Cluster
	models.RBDImage
}

// Time limits for work whose length follows the image's size.
const (
	removeTimeout   = 30 * time.Minute
	transferTimeout = 8 * time.Hour
)

// ErrNoImage is an image that does not exist.
var ErrNoImage = errors.New("the RBD image does not exist")

// OpenImage names the image at a disk path (models.RBDImage.Path). It fails
// when the path's cluster is not enabled on this node.
func OpenImage(path string) (Image, error) {
	img, err := models.ParseRBDPath(path)
	if err != nil {
		return Image{}, err
	}
	c, err := Load(img.ClusterID)
	if err != nil {
		return Image{}, err
	}
	return Image{Cluster: c, RBDImage: img}, nil
}

// PoolImage names the image called name in the pool at poolRef (models.RBDPoolRef).
func PoolImage(poolRef, name string) (Image, error) {
	if _, _, _, err := models.ParseRBDPoolRef(poolRef); err != nil {
		return Image{}, err
	}
	return OpenImage(poolRef + "/" + name)
}

// IsPath reports a disk path on RBD.
func IsPath(p string) bool { return models.IsRBDPath(p) }

func (i Image) at(snap string) string {
	if snap == "" {
		return i.Spec()
	}
	return i.Spec() + "@" + snap
}

// QemuURL is the image, or one of its snapshots, as qemu-img opens it. It
// names the keyring file, never the key.
func (i Image) QemuURL(snap string) string {
	esc := strings.NewReplacer(`\`, `\\`, ":", `\:`).Replace
	return "rbd:" + i.at(snap) + ":id=" + i.ClientID + ":conf=" + esc(i.Conf()) + ":keyring=" + esc(i.Keyring())
}

func (i Image) rbd(ctx context.Context, limit time.Duration, args ...string) ([]byte, error) {
	out, err := i.runFor(ctx, limit, "rbd", args...)
	if err != nil && strings.Contains(err.Error(), "(2) No such file or directory") {
		return nil, fmt.Errorf("%w: %s", ErrNoImage, i.Spec())
	}
	return out, err
}

// Size is the image's size in bytes; ErrNoImage when it does not exist.
func (i Image) Size(ctx context.Context) (int64, error) {
	out, err := i.rbd(ctx, cmdTimeout, "info", "--format", "json", i.Spec())
	if err != nil {
		return 0, err
	}
	var doc struct {
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || doc.Size <= 0 {
		return 0, fmt.Errorf("rbd info of %s gave an answer that could not be read", i.Spec())
	}
	return doc.Size, nil
}

// Exists reports whether the image exists.
func (i Image) Exists(ctx context.Context) (bool, error) {
	_, err := i.Size(ctx)
	if errors.Is(err, ErrNoImage) {
		return false, nil
	}
	return err == nil, err
}

// mib is a size as rbd takes it by default: whole MiB, rounded up.
func mib(bytes int64) string { return fmt.Sprint((bytes + 1<<20 - 1) >> 20) }

// Create makes an empty image of size bytes, with the pool's default features.
func (i Image) Create(ctx context.Context, size int64) error {
	if size <= 0 {
		return fmt.Errorf("an image needs a size")
	}
	_, err := i.rbd(ctx, time.Minute, "create", "--size", mib(size), i.Spec())
	return err
}

// Grow makes the image at least size bytes. It never shrinks one.
func (i Image) Grow(ctx context.Context, size int64) error {
	have, err := i.Size(ctx)
	if err != nil || size <= have {
		return err
	}
	_, err = i.rbd(ctx, time.Minute, "resize", "--no-progress", "--size", mib(size), i.Spec())
	return err
}

// Watchers lists the addresses of the clients that have the image open right
// now: a running VM's QEMU, on this node or any other.
func (i Image) Watchers(ctx context.Context) ([]string, error) {
	out, err := i.rbd(ctx, cmdTimeout, "status", "--format", "json", i.Spec())
	if err != nil {
		return nil, err
	}
	var doc struct {
		Watchers []struct {
			Address string `json:"address"`
		} `json:"watchers"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("rbd status of %s gave an answer that could not be read", i.Spec())
	}
	addrs := make([]string, 0, len(doc.Watchers))
	for _, w := range doc.Watchers {
		addrs = append(addrs, w.Address)
	}
	return addrs, nil
}

// InUseError is an image another client still has open.
type InUseError struct {
	Spec     string
	Watchers []string
}

func (e *InUseError) Error() string {
	return fmt.Sprintf("the RBD image %s is open on %s, so a VM still runs on it (on this node or another); it was left as it is",
		e.Spec, strings.Join(e.Watchers, ", "))
}

// CheckUnused fails with an InUseError while any client has the image open.
// A client that was killed is listed for about 30 seconds more, so wait says
// how long to keep looking before believing it.
func (i Image) CheckUnused(ctx context.Context, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		w, err := i.Watchers(ctx)
		if err != nil || len(w) == 0 {
			return err
		}
		if time.Now().After(deadline) {
			return &InUseError{Spec: i.Spec(), Watchers: w}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// WatcherGrace outlasts the time Ceph keeps listing a client that was killed.
const WatcherGrace = 45 * time.Second

// Remove deletes the image and its snapshots, but only when no client has it
// open: a VM running on it on another node is never deleted under. An image
// that is not there is not an error.
func (i Image) Remove(ctx context.Context) error {
	if err := i.CheckUnused(ctx, WatcherGrace); err != nil {
		if errors.Is(err, ErrNoImage) {
			return nil
		}
		return err
	}
	if _, err := i.rbd(ctx, removeTimeout, "snap", "purge", "--no-progress", i.Spec()); err != nil {
		return fmt.Errorf("the snapshots of %s could not be removed: %w", i.Spec(), err)
	}
	_, err := i.rbd(ctx, removeTimeout, "rm", "--no-progress", i.Spec())
	return err
}

// Snapshot is one snapshot of an image.
type Snapshot struct {
	Name      string
	CreatedAt time.Time
}

// Snapshots lists the image's snapshots.
func (i Image) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := i.rbd(ctx, cmdTimeout, "snap", "ls", "--format", "json", i.Spec())
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name      string `json:"name"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("rbd snap ls of %s gave an answer that could not be read", i.Spec())
	}
	snaps := make([]Snapshot, 0, len(rows))
	for _, r := range rows {
		s := Snapshot{Name: r.Name}
		// "Mon Jan  2 15:04:05 2006", in the node's time zone.
		s.CreatedAt, _ = time.ParseInLocation(time.ANSIC, strings.TrimSpace(r.Timestamp), time.Local)
		snaps = append(snaps, s)
	}
	return snaps, nil
}

// SnapCreate takes a snapshot of the image as it is this moment.
func (i Image) SnapCreate(ctx context.Context, snap string) error {
	if !models.ValidRBDName(snap) {
		return fmt.Errorf("%q is not a usable snapshot name", snap)
	}
	_, err := i.rbd(ctx, time.Minute, "snap", "create", i.at(snap))
	return err
}

// SnapRemove deletes a snapshot; one that is not there is not an error.
func (i Image) SnapRemove(ctx context.Context, snap string) error {
	if !models.ValidRBDName(snap) {
		return fmt.Errorf("%q is not a usable snapshot name", snap)
	}
	_, err := i.rbd(ctx, removeTimeout, "snap", "rm", i.at(snap))
	if errors.Is(err, ErrNoImage) {
		return nil
	}
	return err
}

// SnapRollback puts the image back as it was at the snapshot. The caller
// makes sure no VM runs on it.
func (i Image) SnapRollback(ctx context.Context, snap string) error {
	if !models.ValidRBDName(snap) {
		return fmt.Errorf("%q is not a usable snapshot name", snap)
	}
	_, err := i.rbd(ctx, transferTimeout, "snap", "rollback", "--no-progress", i.at(snap))
	return err
}

type lock struct {
	ID      string `json:"id"`
	Locker  string `json:"locker"`
	Address string `json:"address"`
}

// parseLocks reads `rbd lock ls --format json`: a list since Nautilus, an
// object keyed by lock id before.
func parseLocks(out []byte) ([]lock, error) {
	var list []lock
	if json.Unmarshal(out, &list) == nil {
		return list, nil
	}
	var byID map[string]lock
	if err := json.Unmarshal(out, &byID); err != nil {
		return nil, err
	}
	for id, l := range byID {
		l.ID = id
		list = append(list, l)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].ID < list[b].ID })
	return list, nil
}

// Fence takes the image away from whoever holds it, for a VM being brought
// back on this node after its own node failed. Each lock is broken with
// `rbd lock rm`, which first puts the holder on the cluster's blocklist, so a
// QEMU still alive on the failed node can write nothing more. A client that
// has the image open without a lock (an image without exclusive-lock) is put
// on the blocklist directly. It returns the addresses it fenced.
//
// The cephx user needs mon 'profile rbd' for this; a refusal is an error, and
// the VM is not brought back on a disk another node may still write.
func (i Image) Fence(ctx context.Context) ([]string, error) {
	out, err := i.rbd(ctx, cmdTimeout, "lock", "ls", "--format", "json", i.Spec())
	if err != nil {
		return nil, err
	}
	locks, err := parseLocks(out)
	if err != nil {
		return nil, fmt.Errorf("rbd lock ls of %s gave an answer that could not be read", i.Spec())
	}
	fenced := map[string]bool{}
	for _, l := range locks {
		if _, err := i.rbd(ctx, time.Minute, "lock", "rm", i.Spec(), l.ID, l.Locker); err != nil && !errors.Is(err, ErrNoImage) {
			return nil, fmt.Errorf("the lock %s holds on %s could not be broken (the cephx user needs mon 'profile rbd'): %w", l.Address, i.Spec(), err)
		}
		fenced[l.Address] = true
	}
	watchers, err := i.Watchers(ctx)
	if err != nil {
		return nil, err
	}
	for _, addr := range watchers {
		if fenced[addr] {
			continue
		}
		// "blocklist" since Pacific, "blacklist" before.
		if _, err := i.run(ctx, "ceph", "--connect-timeout", "10", "osd", "blocklist", "add", addr); err != nil {
			if _, old := i.run(ctx, "ceph", "--connect-timeout", "10", "osd", "blacklist", "add", addr); old != nil {
				return nil, fmt.Errorf("%s still has %s open and could not be put on the cluster's blocklist (the cephx user needs mon 'profile rbd'): %w", addr, i.Spec(), err)
			}
		}
		fenced[addr] = true
	}
	addrs := make([]string, 0, len(fenced))
	for a := range fenced {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	return addrs, nil
}

// qemuImg runs qemu-img; its output is returned in the error.
func qemuImg(ctx context.Context, limit time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stdout, stderr, err := Run(ctx, "qemu-img", args...)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		return fmt.Errorf("qemu-img is not installed on this node")
	case ctx.Err() != nil:
		return fmt.Errorf("qemu-img did not finish within %s", limit)
	}
	return fmt.Errorf("qemu-img: %s", lastLine(string(stderr)+"\n"+string(stdout)))
}

// Import makes the image from a local disk image (format "" probes it, which
// only a trusted template may be). The image must not exist.
func (i Image) Import(ctx context.Context, src, format string) error {
	args := []string{"convert"}
	if format != "" {
		args = append(args, "-f", format)
	}
	return qemuImg(ctx, transferTimeout, append(args, "-O", "raw", src, i.QemuURL(""))...)
}

// ReplaceNew makes the image, which must not exist, from a local disk image,
// at least minSize bytes large. It is imported under a second name and renamed
// into place, so a run that dies leaves nothing under the image's own name.
func (i Image) ReplaceNew(ctx context.Context, src, format string, minSize int64) error {
	next := i
	next.Name = i.Name + ".new"
	if err := next.Remove(ctx); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = next.Remove(context.WithoutCancel(ctx))
		return err
	}
	if err := next.Import(ctx, src, format); err != nil {
		return fail(err)
	}
	if err := next.Grow(ctx, minSize); err != nil {
		return fail(err)
	}
	if _, err := i.rbd(ctx, time.Minute, "rename", next.Spec(), i.Spec()); err != nil {
		return fail(err)
	}
	return nil
}

// ReplaceWith gives the image new contents from a local disk image, at least
// minSize bytes large. The new contents are written to a second image first
// and the two swapped by renames, so a failure leaves the image as it was;
// the old contents and their snapshots are removed last. Refused while any
// client has the image open.
func (i Image) ReplaceWith(ctx context.Context, src, format string, minSize int64) error {
	next, old := i, i
	next.Name, old.Name = i.Name+".new", i.Name+".old"
	// Left by a run that stopped between the renames: the old image is the
	// whole one.
	if here, err := i.Exists(ctx); err != nil {
		return err
	} else if !here {
		if _, err := old.rbd(ctx, time.Minute, "rename", old.Spec(), i.Spec()); err != nil {
			return fmt.Errorf("%w: %s", ErrNoImage, i.Spec())
		}
	}
	if err := i.CheckUnused(ctx, WatcherGrace); err != nil {
		return err
	}
	if err := next.Remove(ctx); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = next.Remove(context.WithoutCancel(ctx))
		return err
	}
	if err := next.Import(ctx, src, format); err != nil {
		return fail(err)
	}
	if err := next.Grow(ctx, minSize); err != nil {
		return fail(err)
	}
	if err := old.Remove(ctx); err != nil {
		return fail(err)
	}
	if _, err := i.rbd(ctx, time.Minute, "rename", i.Spec(), old.Spec()); err != nil {
		return fail(err)
	}
	if _, err := i.rbd(ctx, time.Minute, "rename", next.Spec(), i.Spec()); err != nil {
		_, _ = i.rbd(context.WithoutCancel(ctx), time.Minute, "rename", old.Spec(), i.Spec())
		return fail(err)
	}
	if err := old.Remove(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("the disk has its new contents, but the old ones (%s) could not be removed: %w", old.Spec(), err)
	}
	return nil
}

// DiskSource is the image as a libvirt disk source: the pool and image, every
// monitor, and the cluster's libvirt secret (its UUID is the cluster's id).
// QEMU is given the monitors and the key this way because it runs as a user
// that cannot read the agent's conf and keyring.
func (i Image) DiskSource() *libvirtxml.DomainDiskSource {
	net := &libvirtxml.DomainDiskSourceNetwork{Protocol: "rbd", Name: i.Spec(),
		Auth: &libvirtxml.DomainDiskAuth{Username: i.ClientID, Secret: &libvirtxml.DomainDiskSecret{Type: "ceph", UUID: i.ClusterID}}}
	for _, m := range i.Monitors {
		net.Hosts = append(net.Hosts, monitorHost(m))
	}
	return &libvirtxml.DomainDiskSource{Network: net}
}

// monitorHost splits host, host:port, [v6] and [v6]:port.
func monitorHost(m string) libvirtxml.DomainDiskSourceHost {
	host, port := m, ""
	if strings.HasPrefix(m, "[") {
		if end := strings.Index(m, "]"); end > 0 {
			host, port = m[1:end], strings.TrimPrefix(m[end+1:], ":")
		}
	} else if h, p, ok := strings.Cut(m, ":"); ok {
		host, port = h, p
	}
	return libvirtxml.DomainDiskSourceHost{Name: host, Port: port}
}

// DiskPath is the disk path (models.RBDImage.Path) of a libvirt disk on RBD
// that names a cluster's secret, as DiskSource writes them; false for any
// other disk. libvirt accepts <auth> in the source or, older, beside it.
func DiskPath(d *libvirtxml.DomainDisk) (string, bool) {
	if d == nil || d.Source == nil || d.Source.Network == nil || d.Source.Network.Protocol != "rbd" {
		return "", false
	}
	auth := d.Source.Network.Auth
	if auth == nil {
		auth = d.Auth
	}
	if auth == nil || auth.Secret == nil || auth.Secret.UUID == "" {
		return "", false
	}
	p := "rbd://" + auth.Secret.UUID + "/" + d.Source.Network.Name
	if _, err := models.ParseRBDPath(p); err != nil {
		return "", false
	}
	return p, true
}
