package models

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// SharedPoolRBD is a shared pool that is one RBD pool (and namespace) of a
// registered Ceph cluster. Its members are the nodes the cluster is enabled
// on; each has a storage_pools row of type PoolTypeRBD whose path is
// RBDPoolPath.
const (
	SharedPoolRBD = "rbd"
	PoolTypeRBD   = "rbd"
)

// rbdScheme starts the path of every disk and pool on RBD, as the panel and
// the agents write them:
//
//	rbd://<cluster id>/<pool>[/<namespace>]            a pool (RBDPoolRef)
//	rbd://<cluster id>/<pool>[/<namespace>]/<image>    a disk (RBDImage.Path)
//
// The cluster id is the id of the registered cluster, which is also the UUID
// of its libvirt secret on every node.
const rbdScheme = "rbd://"

// IsRBDPath reports a disk or pool path on RBD.
func IsRBDPath(p string) bool { return strings.HasPrefix(p, rbdScheme) }

// RBDPoolRef names an RBD pool to an agent, as the pool_path of pool type rbd.
func RBDPoolRef(clusterID, pool, namespace string) string {
	return rbdScheme + clusterID + "/" + RBDPoolPath(pool, namespace)
}

// ParseRBDPoolRef reads an RBDPoolRef.
func ParseRBDPoolRef(ref string) (clusterID, pool, namespace string, err error) {
	rest, ok := strings.CutPrefix(ref, rbdScheme)
	parts := strings.Split(rest, "/")
	if !ok || len(parts) < 2 || len(parts) > 3 || !uuidRe.MatchString(parts[0]) {
		return "", "", "", fmt.Errorf("%q does not name an RBD pool", ref)
	}
	for _, n := range parts[1:] {
		if !ValidRBDName(n) {
			return "", "", "", fmt.Errorf("%q does not name an RBD pool", ref)
		}
	}
	if len(parts) == 3 {
		namespace = parts[2]
	}
	return parts[0], parts[1], namespace, nil
}

// RBDImage is one image in an RBD pool: a VM's disk.
type RBDImage struct {
	ClusterID, Pool, Namespace, Name string
}

// ParseRBDPath reads a disk path on RBD.
func ParseRBDPath(p string) (RBDImage, error) {
	i := strings.LastIndex(p, "/")
	if !IsRBDPath(p) || i < len(rbdScheme) {
		return RBDImage{}, fmt.Errorf("%q is not a disk on RBD", p)
	}
	cluster, pool, ns, err := ParseRBDPoolRef(p[:i])
	if err != nil || !ValidRBDName(p[i+1:]) {
		return RBDImage{}, fmt.Errorf("%q is not a disk on RBD", p)
	}
	return RBDImage{ClusterID: cluster, Pool: pool, Namespace: ns, Name: p[i+1:]}, nil
}

// Spec is the image as rbd and libvirt name it: pool[/namespace]/image.
func (i RBDImage) Spec() string { return RBDPoolPath(i.Pool, i.Namespace) + "/" + i.Name }

// Path is the disk's path in the panel's records and between panel and agent.
func (i RBDImage) Path() string { return rbdScheme + i.ClusterID + "/" + i.Spec() }

// RBDPoolHolds reports whether the disk at diskPath is an image of the RBD
// pool whose storage_pools path is poolPath ("pool" or "pool/namespace").
// ponytail: the cluster is not compared, a storage_pools row does not carry
// it. Two clusters with the same pool name enabled on one node would both
// match; compare shared_storage_pools.ceph_cluster_id if that ever matters.
func RBDPoolHolds(poolPath, diskPath string) bool {
	img, err := ParseRBDPath(diskPath)
	return err == nil && RBDPoolPath(img.Pool, img.Namespace) == poolPath
}

// RBDSnapshotPrefix starts the name of every RBD snapshot that is a VM
// snapshot taken from the panel: the snapshot "before-upgrade" is the RBD
// snapshot "mvm-before-upgrade" of each of the VM's images.
const RBDSnapshotPrefix = "mvm-"

// ValidRBDSnapshotName reports a snapshot name a VM on RBD can use.
func ValidRBDSnapshotName(name string) bool { return ValidRBDName(RBDSnapshotPrefix + name) }

// RBDDiskName names a VM's disk image: maburvm-<vm id>-<suffix>, the suffix
// being the target (vda) or a short unique tag. OwnsRBDName is its inverse.
func RBDDiskName(vmID, suffix string) string { return LVMDiskName(vmID, suffix) }

// OwnsRBDName reports whether image is a name RBDDiskName gives the VM's disks.
func OwnsRBDName(vmID, image string) bool { return OwnsLVMName(vmID, image) }

// What a node last said about a cluster it is enabled on.
const (
	CephNodeOK = "ok"
	// CephNodeUnreachable: the node cannot reach the cluster with its id and key.
	CephNodeUnreachable = "unreachable"
	// CephNodeMismatch: the node reaches a cluster with another fsid.
	CephNodeMismatch = "mismatch"
	// CephNodeDegraded: the node reaches the cluster, but something on the
	// node is wrong (libvirt lost the cluster's secret).
	CephNodeDegraded = "degraded"
)

// Cluster health as Ceph words it.
const (
	CephHealthOK   = "HEALTH_OK"
	CephHealthWarn = "HEALTH_WARN"
	CephHealthErr  = "HEALTH_ERR"
)

// CephCluster is an existing Ceph cluster staff registered. MaburVM is only a
// client of it: it does not install, grow or upgrade the cluster.
type CephCluster struct {
	ID   string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name string `json:"name" gorm:"type:varchar(100);not null"`
	// Monitors is a comma-separated list of host or host:port.
	Monitors string `json:"monitors" gorm:"type:text;not null"`
	// ClientID is the cephx user without "client."; ClientKey its key, sealed
	// at rest and never sent to a browser.
	ClientID  string `json:"client_id" gorm:"type:varchar(64);not null"`
	ClientKey string `json:"-" gorm:"column:client_key;type:text;not null;serializer:secret"`
	// FSID is the cluster's id: given at registration, or recorded from the
	// first node that reaches the cluster. Every node must report this one.
	FSID string `json:"fsid" gorm:"column:fsid;type:varchar(36);not null;default:''"`
	// Health and capacity as the last node to answer reported them.
	Health         string         `json:"health" gorm:"type:varchar(16);not null;default:''"`
	HealthSummary  string         `json:"health_summary" gorm:"type:text;not null;default:''"`
	TotalBytes     int64          `json:"total_bytes" gorm:"not null;default:0"`
	UsedBytes      int64          `json:"used_bytes" gorm:"not null;default:0"`
	AvailableBytes int64          `json:"available_bytes" gorm:"not null;default:0"`
	CheckedAt      *time.Time     `json:"checked_at"`
	CreatedAt      time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt      time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
}

func (CephCluster) TableName() string { return "ceph_clusters" }

// MonitorList is Monitors as a list.
func (c CephCluster) MonitorList() []string { return strings.Split(c.Monitors, ",") }

// CephClusterNode is a cluster enabled on a node: the node's agent holds the
// cluster's conf and keyring, and libvirt there its secret.
type CephClusterNode struct {
	ClusterID  string     `json:"cluster_id" gorm:"type:uuid;primaryKey"`
	NodeID     string     `json:"node_id" gorm:"type:uuid;primaryKey"`
	Status     string     `json:"status" gorm:"type:varchar(16);not null;default:ok"`
	StatusNote string     `json:"status_note" gorm:"type:text;not null;default:''"`
	CheckedAt  *time.Time `json:"checked_at"`
	CreatedAt  time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
}

func (CephClusterNode) TableName() string { return "ceph_cluster_nodes" }

var (
	cephMonitorRe = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]{2,45}\]|[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?)(:[0-9]{1,5})?$`)
	cephClientRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	rbdNameRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ParseCephMonitors splits a list of monitor addresses (commas or spaces)
// and checks each is a host, host:port, [ipv6] or [ipv6]:port.
func ParseCephMonitors(s string) ([]string, error) {
	mons := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';' })
	if len(mons) == 0 || len(mons) > 16 {
		return nil, fmt.Errorf("give 1 to 16 monitor addresses, such as 10.0.0.1,10.0.0.2,10.0.0.3")
	}
	seen := map[string]bool{}
	for _, m := range mons {
		if !cephMonitorRe.MatchString(m) {
			return nil, fmt.Errorf("%q is not a monitor address: use a host name or an address, with :port if it is not the default (an IPv6 address goes in brackets)", m)
		}
		if seen[m] {
			return nil, fmt.Errorf("monitor %s is listed twice", m)
		}
		seen[m] = true
	}
	return mons, nil
}

// NormalizeCephClientID drops a leading "client." and checks the rest.
func NormalizeCephClientID(s string) (string, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "client.")
	if !cephClientRe.MatchString(s) {
		return "", fmt.Errorf("the client id is the cephx user's name without \"client.\", such as maburvm")
	}
	return s, nil
}

// ValidCephKey reports a cephx key as `ceph auth get-key` prints it: base64
// on one line. The agent writes it into a keyring file, so nothing else passes.
func ValidCephKey(key string) bool {
	// The decoder skips line breaks; a key with one is refused all the same.
	raw, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(raw) >= 16 && len(raw) <= 256 && !strings.ContainsAny(key, "\r\n")
}

// ValidCephFSID reports a cluster fsid (a lower-case UUID).
func ValidCephFSID(s string) bool { return uuidRe.MatchString(s) }

// ValidateCephAccess checks what a node is given to reach a cluster, as both
// the panel and the agent accept it. The agent writes these into a conf file,
// a keyring and command arguments.
func ValidateCephAccess(clusterID string, monitors []string, clientID, key string) error {
	if !uuidRe.MatchString(clusterID) {
		return fmt.Errorf("the Ceph cluster has no usable id")
	}
	if _, err := ParseCephMonitors(strings.Join(monitors, ",")); err != nil {
		return err
	}
	if !cephClientRe.MatchString(clientID) || strings.HasPrefix(clientID, "client.") {
		return fmt.Errorf("the client id is the cephx user's name without \"client.\", such as maburvm")
	}
	if !ValidCephKey(key) {
		return fmt.Errorf("the key is not a cephx key (the output of ceph auth get-key)")
	}
	return nil
}

// ValidRBDName reports a usable RBD pool or namespace name. Ceph allows more;
// these are the names that are safe in a command line and in a disk source.
func ValidRBDName(s string) bool { return rbdNameRe.MatchString(s) }

// RBDPoolPath is the path of an RBD pool's storage_pools rows: "pool", or
// "pool/namespace".
func RBDPoolPath(pool, namespace string) string {
	if namespace == "" {
		return pool
	}
	return pool + "/" + namespace
}
