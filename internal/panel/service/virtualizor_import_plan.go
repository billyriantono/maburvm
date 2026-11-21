package service

import (
	"fmt"
	"net"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	vmimport "github.com/maburvm/panel/internal/agent/import"
)

// What a Virtualizor master holds, read once for a preview, and what an import
// of it will do here. Planning is a pure function of the snapshot, the
// operator's choices and the panel's current state, so the preview and the
// apply that follows it decide the same way.

// VirtualizorSnapshot is the source data an import works from.
type VirtualizorSnapshot struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Servers   []SourceServer `json:"servers"`
	Users     []SourceUser   `json:"users"`
	Plans     []SourcePlan   `json:"plans"`
	Pools     []SourcePool   `json:"pools"`
	VMs       []SourceVM     `json:"vms"`
	// Scans are the libvirt domains found on each panel node mapped to a
	// source server: where a source VM is actually running.
	Scans []NodeScan `json:"scans"`
}

type SourceServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type SourceUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Admin bool   `json:"admin,omitempty"`
}

type SourcePlan struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Virt        string `json:"virt"`
	CPU         int64  `json:"cpu"`
	RAMMB       int64  `json:"ram_mb"`
	DiskGB      int64  `json:"disk_gb"`
	BandwidthGB int64  `json:"bandwidth_gb"` // monthly transfer, 0 = unlimited
	SpeedKBs    int64  `json:"speed_kbs"`    // port speed in KB/s, 0 = unlimited
}

type SourcePool struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Gateway   string          `json:"gateway"`
	Netmask   string          `json:"netmask"`
	DNS       []string        `json:"dns,omitempty"`
	IPv6      bool            `json:"ipv6"`
	ServerIDs []string        `json:"server_ids,omitempty"`
	Addresses []SourceAddress `json:"addresses,omitempty"`
}

// SourceAddress is one address of a pool and the source VM holding it ("" = free).
type SourceAddress struct {
	IP   string `json:"ip"`
	VMID string `json:"vm_id,omitempty"`
}

type SourceVM struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"` // the libvirt domain name on its node
	UUID        string   `json:"uuid"`
	ServerID    string   `json:"server_id"`
	UserID      string   `json:"user_id"`
	PlanID      string   `json:"plan_id"`
	Hostname    string   `json:"hostname"`
	OSName      string   `json:"os_name"`
	Virt        string   `json:"virt"`
	CPU         int64    `json:"cpu"`
	RAMMB       int64    `json:"ram_mb"`
	DiskGB      int64    `json:"disk_gb"`
	BandwidthGB int64    `json:"bandwidth_gb"`
	SpeedKBs    int64    `json:"speed_kbs"`
	Suspended   bool     `json:"suspended"`
	IPs         []string `json:"ips,omitempty"`
}

// NodeScan is one node's libvirt domains, or why it could not be read. VNC
// passwords are kept only sealed.
type NodeScan struct {
	NodeID  string                     `json:"node_id"`
	Error   string                     `json:"error,omitempty"`
	Domains []vmimport.ImportCandidate `json:"domains,omitempty"`
}

func (s *VirtualizorSnapshot) sort() {
	sort.SliceStable(s.Servers, func(i, j int) bool { return lessNumeric(s.Servers[i].ID, s.Servers[j].ID) })
	sort.SliceStable(s.Users, func(i, j int) bool { return lessNumeric(s.Users[i].ID, s.Users[j].ID) })
	sort.SliceStable(s.Plans, func(i, j int) bool { return lessNumeric(s.Plans[i].ID, s.Plans[j].ID) })
	sort.SliceStable(s.Pools, func(i, j int) bool { return lessNumeric(s.Pools[i].ID, s.Pools[j].ID) })
	sort.SliceStable(s.VMs, func(i, j int) bool { return lessNumeric(s.VMs[i].ID, s.VMs[j].ID) })
}

func (s *VirtualizorSnapshot) scan(nodeID string) *NodeScan {
	for i := range s.Scans {
		if s.Scans[i].NodeID == nodeID {
			return &s.Scans[i]
		}
	}
	return nil
}

// VirtualizorImportOptions are the operator's choices for one source.
type VirtualizorImportOptions struct {
	// DefaultTemplateID is the template recorded for VMs whose system matches
	// none by name. A VM row needs one; nothing is installed from it.
	DefaultTemplateID string `json:"default_template_id"`
	// NodeMap sends a source server's VMs to a panel node; "-" leaves them
	// out. Servers not listed are matched by address, then name.
	NodeMap map[string]string `json:"node_map,omitempty"`
	// PlanMap links a source plan to a panel plan id, or "new" to create one.
	// Plans not listed link to a panel plan of the same name, else are created.
	PlanMap map[string]string `json:"plan_map,omitempty"`
	// BillingRefs is the billing system's service reference of each source VM
	// (by its id). A bare number is a WHMCS service id.
	BillingRefs map[string]string `json:"billing_refs,omitempty"`
}

const (
	importKindPool = "pool"
	importKindPlan = "plan"
	importKindUser = "user"
	importKindVM   = "vm"

	ImportActionCreate  = "create"  // will be made here
	ImportActionLink    = "link"    // an object already here will be used
	ImportActionUpdate  = "update"  // a VM already here gets its owner, plan or billing reference
	ImportActionExists  = "exists"  // imported before: nothing to do
	ImportActionBlocked = "blocked" // cannot be imported; Conflicts say why

	importPlanNew  = "new"
	importNodeSkip = "-"
)

// importKindOrder is the order an apply works in: a VM needs its owner and
// its addresses' pool first.
var importKindOrder = []string{importKindPool, importKindPlan, importKindUser, importKindVM}

func importKindRank(kind string) int {
	for i, k := range importKindOrder {
		if k == kind {
			return i
		}
	}
	return len(importKindOrder)
}

// ImportPlan is the preview: every source record and what will happen to it.
type ImportPlan struct {
	SourceID  string                   `json:"source_id"`
	FetchedAt time.Time                `json:"fetched_at"`
	Options   VirtualizorImportOptions `json:"options"`
	Servers   []ImportServerRow        `json:"servers"`
	Items     []ImportItem             `json:"items"`
	Choices   ImportChoices            `json:"choices"`
	Notes     []string                 `json:"notes,omitempty"`
	// PendingInvites counts accounts the import created that have not been
	// sent their invitation.
	PendingInvites int64 `json:"pending_invites"`
	// Ready is false until the master has been read once. Reading is a read
	// of the master or its nodes running now; ReadError is why the last one failed.
	Ready         bool       `json:"ready"`
	Reading       bool       `json:"reading"`
	ReadError     string     `json:"read_error,omitempty"`
	ReadStartedAt *time.Time `json:"read_started_at,omitempty"`
}

// ImportServerRow is a source server and the node its VMs go to.
type ImportServerRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	NodeID    string `json:"node_id,omitempty"`
	NodeName  string `json:"node_name,omitempty"`
	Matched   string `json:"matched"` // auto, chosen, skipped or "" (no node)
	VMs       int    `json:"vms"`
	ScanError string `json:"scan_error,omitempty"`
}

// ImportChoices are what the mapping selects offer.
type ImportChoices struct {
	Nodes     []ImportChoice `json:"nodes"`
	Templates []ImportChoice `json:"templates"`
	Plans     []ImportChoice `json:"plans"`
}

type ImportChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ImportItem is one source record in the preview.
type ImportItem struct {
	Kind        string            `json:"kind"`
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Detail      string            `json:"detail,omitempty"`
	Action      string            `json:"action"`
	TargetID    string            `json:"target_id,omitempty"`
	TargetLabel string            `json:"target_label,omitempty"`
	Conflicts   []string          `json:"conflicts,omitempty"`
	Notes       []string          `json:"notes,omitempty"`
	VM          *ImportVMDetail   `json:"vm,omitempty"`
	Pool        *ImportPoolDetail `json:"pool,omitempty"`
	Plan        *ImportPlanDetail `json:"plan,omitempty"`
}

type ImportVMDetail struct {
	OwnerKey      string   `json:"owner_key"`
	OwnerLabel    string   `json:"owner_label"`
	PlanKey       string   `json:"plan_key,omitempty"`
	PlanLabel     string   `json:"plan_label,omitempty"`
	NodeID        string   `json:"node_id,omitempty"`
	NodeName      string   `json:"node_name,omitempty"`
	DomainUUID    string   `json:"domain_uuid,omitempty"`
	Hostname      string   `json:"hostname"`
	TemplateID    string   `json:"template_id,omitempty"`
	TemplateLabel string   `json:"template_label,omitempty"`
	IPs           []string `json:"ips,omitempty"`
	BillingRef    string   `json:"billing_ref,omitempty"`
	Suspended     bool     `json:"suspended,omitempty"`
}

type ImportPoolDetail struct {
	CIDR    string   `json:"cidr"`
	Family  string   `json:"family"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	NodeIDs []string `json:"node_ids,omitempty"`
	Total   int      `json:"total"`
	InUse   int      `json:"in_use"`
}

type ImportPlanDetail struct {
	Name          string `json:"name"`
	CPU           int    `json:"cpu"`
	RAMMB         int    `json:"ram_mb"`
	DiskGB        int    `json:"disk_gb"`
	BandwidthMbps int    `json:"bandwidth_mbps"`
	DataQuotaGB   int64  `json:"data_quota_gb"`
}

// importState is what the panel already holds, as planning needs it.
type importState struct {
	nodes     []stateNode
	templates []stateTemplate
	// users by lower-case email, deleted accounts included: their email
	// stays taken.
	users    map[string]stateUser
	userByID map[string]stateUser
	plans    []statePlan
	pools    []statePool
	// assigned addresses: address -> the VM holding it (no VM: an unattached
	// floating IP, still a customer's).
	assigned  map[string]stateHolder
	vms       map[string]stateVM
	hostnames map[string]string // owner id + "\x00" + lower(hostname) -> VM id
	links     map[string]importLink
}

type stateNode struct{ ID, Name, IP string }
type stateTemplate struct{ ID, Name, Version string }
type stateUser struct {
	ID, Email string
	Staff     bool
	Deleted   bool
}
type statePlan struct {
	ID, Name       string
	CPU, RAM, Disk int
}
type statePool struct{ ID, Name, Family, CIDR string }
type stateHolder struct{ VMID, Hostname string }
type stateVM struct{ ID, UserID, Hostname, PlanID, BillingRef, Source, NodeID string }

func linkKey(kind, key string) string { return kind + "|" + key }

func hostKey(owner, hostname string) string { return owner + "\x00" + strings.ToLower(hostname) }

func isKVM(virt string) bool {
	v := strings.ToLower(strings.TrimSpace(virt))
	return v == "" || strings.HasPrefix(v, "kvm")
}

func validImportEmail(email string) bool {
	addr, err := mail.ParseAddress(email)
	return err == nil && addr.Address == email && len(email) <= 255
}

// importSpeedMbps turns a port speed in KB/s into Mbit/s (12800 KB/s is 100).
func importSpeedMbps(kbs int64) int {
	if kbs <= 0 {
		return 0
	}
	return int((kbs*8 + 512) / 1024)
}

// NormalizeBillingRef turns what an operator pasted into the reference the
// billing module sends: a bare WHMCS service id becomes "whmcs-<id>".
func NormalizeBillingRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref != "" && strings.IndexFunc(ref, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		return "whmcs-" + ref
	}
	return ref
}

// sourcePoolNetwork works out a pool's network from its gateway and netmask
// (a dotted mask, or a prefix length as IPv6 pools use).
func sourcePoolNetwork(p SourcePool) (cidr, family string, err error) {
	anchor := net.ParseIP(p.Gateway)
	if anchor == nil && len(p.Addresses) > 0 {
		anchor = net.ParseIP(p.Addresses[0].IP)
	}
	if anchor == nil {
		return "", "", fmt.Errorf("it has no gateway and no addresses, so its network cannot be worked out")
	}
	bits := 32
	family = "ipv4"
	if anchor.To4() == nil {
		bits, family = 128, "ipv6"
	} else {
		anchor = anchor.To4()
	}
	mask := strings.TrimPrefix(strings.TrimSpace(p.Netmask), "/")
	var ones int
	if n, perr := strconv.Atoi(mask); perr == nil {
		ones = n
	} else if m := net.ParseIP(mask); m != nil && m.To4() != nil && family == "ipv4" {
		var size int
		ones, size = net.IPMask(m.To4()).Size()
		if size == 0 {
			return "", "", fmt.Errorf("its netmask %s is not a valid mask", p.Netmask)
		}
	} else {
		return "", "", fmt.Errorf("its netmask %q cannot be read", p.Netmask)
	}
	if ones <= 0 || ones > bits {
		return "", "", fmt.Errorf("its netmask %q is out of range", p.Netmask)
	}
	network := &net.IPNet{IP: anchor.Mask(net.CIDRMask(ones, bits)), Mask: net.CIDRMask(ones, bits)}
	return network.String(), family, nil
}

// poolAddressesIn are the addresses of p inside network cidr.
func poolAddressesIn(p SourcePool, cidr string) []SourceAddress {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	var out []SourceAddress
	for _, a := range p.Addresses {
		if ip := net.ParseIP(a.IP); ip != nil && network.Contains(ip) {
			out = append(out, a)
		}
	}
	return out
}

func cidrsOverlap(a, b string) bool {
	_, na, err1 := net.ParseCIDR(a)
	_, nb, err2 := net.ParseCIDR(b)
	return err1 == nil && err2 == nil && (na.Contains(nb.IP) || nb.Contains(na.IP))
}

func sameCIDR(a, b string) bool {
	_, na, err1 := net.ParseCIDR(a)
	_, nb, err2 := net.ParseCIDR(b)
	return err1 == nil && err2 == nil && na.String() == nb.String()
}

func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchTemplate finds the template a source system name ("ubuntu-22.04-x86_64")
// belongs to: the longest template name-and-version the system name starts
// with. A name alone counts only when it carries its version ("Debian 12").
func matchTemplate(osName string, templates []stateTemplate) *stateTemplate {
	norm := alnumLower(osName)
	if norm == "" {
		return nil
	}
	var best *stateTemplate
	bestLen := 0
	for i := range templates {
		t := &templates[i]
		keys := []string{alnumLower(t.Name + t.Version)}
		if strings.IndexFunc(t.Name, unicode.IsDigit) >= 0 {
			keys = append(keys, alnumLower(t.Name))
		}
		for _, k := range keys {
			if k != "" && strings.HasPrefix(norm, k) && len(k) > bestLen {
				best, bestLen = t, len(k)
			}
		}
	}
	return best
}

func templateLabel(t stateTemplate) string {
	return strings.TrimSpace(t.Name + " " + t.Version)
}

// resolveServers decides the node each source server's VMs go to.
func resolveServers(snap *VirtualizorSnapshot, opts VirtualizorImportOptions, st *importState) ([]ImportServerRow, map[string]stateNode) {
	byID := map[string]stateNode{}
	for _, n := range st.nodes {
		byID[n.ID] = n
	}
	count := map[string]int{}
	for _, v := range snap.VMs {
		count[v.ServerID]++
	}
	mapped := map[string]stateNode{}
	rows := make([]ImportServerRow, 0, len(snap.Servers))
	for _, s := range snap.Servers {
		row := ImportServerRow{ID: s.ID, Name: s.Name, IP: s.IP, VMs: count[s.ID]}
		if choice, ok := opts.NodeMap[s.ID]; ok && choice != "" {
			if choice == importNodeSkip {
				row.Matched = "skipped"
			} else if n, ok := byID[choice]; ok {
				row.NodeID, row.NodeName, row.Matched = n.ID, n.Name, "chosen"
				mapped[s.ID] = n
			}
		} else {
			ip := net.ParseIP(s.IP)
			for _, n := range st.nodes {
				if (ip != nil && ip.Equal(net.ParseIP(n.IP))) || (s.Name != "" && strings.EqualFold(s.Name, n.Name)) {
					row.NodeID, row.NodeName, row.Matched = n.ID, n.Name, "auto"
					mapped[s.ID] = n
					break
				}
			}
		}
		if row.NodeID != "" {
			if sc := snap.scan(row.NodeID); sc != nil {
				row.ScanError = sc.Error
			}
		}
		rows = append(rows, row)
	}
	return rows, mapped
}

func findDomain(sc *NodeScan, v SourceVM) *vmimport.ImportCandidate {
	if sc == nil {
		return nil
	}
	for i := range sc.Domains {
		if v.UUID != "" && strings.EqualFold(sc.Domains[i].UUID, v.UUID) {
			return &sc.Domains[i]
		}
	}
	for i := range sc.Domains {
		if v.Name != "" && sc.Domains[i].Name == v.Name {
			return &sc.Domains[i]
		}
	}
	return nil
}

func planDetail(p SourcePlan, name string) *ImportPlanDetail {
	return &ImportPlanDetail{Name: name, CPU: int(max(p.CPU, 1)), RAMMB: int(p.RAMMB), DiskGB: int(p.DiskGB),
		BandwidthMbps: importSpeedMbps(p.SpeedKBs), DataQuotaGB: max(p.BandwidthGB, 0)}
}

func describeSize(cpu, ramMB, diskGB int) string {
	return fmt.Sprintf("%d vCPU, %d MB RAM, %d GB disk", cpu, ramMB, diskGB)
}

// planVirtualizorImport is the preview: what an import of snap would do now.
func planVirtualizorImport(snap *VirtualizorSnapshot, opts VirtualizorImportOptions, st *importState) *ImportPlan {
	plan := &ImportPlan{FetchedAt: snap.FetchedAt, Options: opts}
	for _, n := range st.nodes {
		plan.Choices.Nodes = append(plan.Choices.Nodes, ImportChoice{ID: n.ID, Label: n.Name})
	}
	for _, t := range st.templates {
		plan.Choices.Templates = append(plan.Choices.Templates, ImportChoice{ID: t.ID, Label: templateLabel(t)})
	}
	for _, p := range st.plans {
		plan.Choices.Plans = append(plan.Choices.Plans, ImportChoice{ID: p.ID, Label: p.Name})
	}

	servers, nodeOf := resolveServers(snap, opts, st)
	plan.Servers = servers
	serverName := map[string]string{}
	for _, s := range snap.Servers {
		serverName[s.ID] = s.Name
	}

	userItems := map[string]*ImportItem{}
	planItems := map[string]*ImportItem{}
	var items []*ImportItem

	// Users.
	vmsOf := map[string]int{}
	for _, v := range snap.VMs {
		vmsOf[v.UserID]++
	}
	firstWithEmail := map[string]string{}
	for _, u := range snap.Users {
		it := &ImportItem{Kind: importKindUser, Key: u.ID, Label: u.Email, Detail: u.Name}
		if it.Label == "" {
			it.Label = "User #" + u.ID
		}
		if n := vmsOf[u.ID]; n > 0 {
			it.Notes = append(it.Notes, fmt.Sprintf("Owns %d VM(s) on the source.", n))
		}
		if u.Admin {
			it.Notes = append(it.Notes, "It is an administrator on the source; here it becomes a customer account that owns its VMs.")
		}
		if l, ok := st.links[linkKey(importKindUser, u.ID)]; ok && st.userByID[l.TargetID].ID != "" && !st.userByID[l.TargetID].Deleted {
			it.Action, it.TargetID, it.TargetLabel = ImportActionExists, l.TargetID, st.userByID[l.TargetID].Email
		} else if !validImportEmail(u.Email) {
			it.Action, it.Conflicts = ImportActionBlocked, []string{"It has no valid email address."}
		} else if other, dup := firstWithEmail[u.Email]; dup {
			it.Action, it.Conflicts = ImportActionBlocked, []string{fmt.Sprintf("User #%s on the source has the same email.", other)}
		} else if pu, ok := st.users[u.Email]; ok {
			switch {
			case pu.Deleted:
				it.Action, it.Conflicts = ImportActionBlocked, []string{"An account with this email was deleted from this panel; its email stays taken."}
			case pu.Staff:
				it.Action, it.Conflicts = ImportActionBlocked, []string{"The email belongs to a staff account here; a customer's VMs need a customer account."}
			default:
				it.Action, it.TargetID, it.TargetLabel = ImportActionLink, pu.ID, pu.Email
				it.Notes = append(it.Notes, "An account with this email already exists here; the import uses it and changes nothing on it.")
			}
		} else {
			it.Action = ImportActionCreate
			it.Notes = append(it.Notes, "Created as a customer account with no usable password. Nobody is emailed until you send the invitations.")
		}
		if validImportEmail(u.Email) {
			if _, dup := firstWithEmail[u.Email]; !dup {
				firstWithEmail[u.Email] = u.ID
			}
		}
		userItems[u.ID] = it
	}

	// Plans.
	planByID := map[string]statePlan{}
	planByName := map[string]statePlan{}
	for _, p := range st.plans {
		planByID[p.ID] = p
		planByName[strings.ToLower(p.Name)] = p
	}
	for _, p := range snap.Plans {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = "Plan " + p.ID
		}
		d := planDetail(p, name)
		it := &ImportItem{Kind: importKindPlan, Key: p.ID, Label: name, Detail: describeSize(d.CPU, d.RAMMB, d.DiskGB)}
		linkTo := func(pp statePlan) {
			it.Action, it.TargetID, it.TargetLabel = ImportActionLink, pp.ID, pp.Name
			if pp.CPU != d.CPU || pp.RAM != d.RAMMB || pp.Disk != d.DiskGB {
				it.Notes = append(it.Notes, fmt.Sprintf("The panel plan has %s; the source plan has %s. VMs keep their own size either way.",
					describeSize(pp.CPU, pp.RAM, pp.Disk), describeSize(d.CPU, d.RAMMB, d.DiskGB)))
			}
		}
		choice := opts.PlanMap[p.ID]
		if l, ok := st.links[linkKey(importKindPlan, p.ID)]; ok && planByID[l.TargetID].ID != "" {
			it.Action, it.TargetID, it.TargetLabel = ImportActionExists, l.TargetID, planByID[l.TargetID].Name
		} else if !isKVM(p.Virt) {
			it.Action, it.Conflicts = ImportActionBlocked, []string{fmt.Sprintf("Its virtualization type is %s; only KVM plans are imported.", p.Virt)}
		} else if pp, ok := planByID[choice]; ok && choice != importPlanNew {
			linkTo(pp)
		} else if pp, ok := planByName[strings.ToLower(name)]; ok && choice != importPlanNew {
			linkTo(pp)
		} else {
			if _, taken := planByName[strings.ToLower(name)]; taken {
				name += " (imported)"
				d.Name = name
			}
			switch {
			case len(name) > 100:
				it.Action, it.Conflicts = ImportActionBlocked, []string{"Its name is longer than 100 characters."}
			case planByName[strings.ToLower(name)].ID != "":
				it.Action, it.Conflicts = ImportActionBlocked, []string{fmt.Sprintf("A plan named %q already exists here; link to it or rename it.", name)}
			case d.RAMMB < 128:
				it.Action, it.Conflicts = ImportActionBlocked, []string{fmt.Sprintf("It has %d MB of RAM; a plan needs at least 128 MB.", d.RAMMB)}
			case d.DiskGB < 1:
				it.Action, it.Conflicts = ImportActionBlocked, []string{"It has no disk size."}
			default:
				it.Action, it.TargetLabel, it.Plan = ImportActionCreate, name, d
				if p.CPU < 1 {
					it.Notes = append(it.Notes, "The source plan has no CPU count; it is created with 1 vCPU.")
				}
				if d.BandwidthMbps > 0 || d.DataQuotaGB > 0 {
					it.Notes = append(it.Notes, fmt.Sprintf("Port speed %s, monthly transfer %s.", orUnlimited(int64(d.BandwidthMbps), "Mbit/s"), orUnlimited(d.DataQuotaGB, "GB")))
				}
			}
		}
		planItems[p.ID] = it
	}

	// Where each source VM runs here, for the pools' node bindings.
	vmNode := map[string]string{}
	for _, v := range snap.VMs {
		if n, ok := nodeOf[v.ServerID]; ok && isKVM(v.Virt) {
			vmNode[v.ID] = n.ID
		}
	}

	// IP pools.
	poolByID := map[string]statePool{}
	for _, p := range st.pools {
		poolByID[p.ID] = p
	}
	for _, p := range snap.Pools {
		label := p.Name
		if label == "" {
			label = "Pool #" + p.ID
		}
		it := &ImportItem{Kind: importKindPool, Key: p.ID, Label: label}
		cidr, family, err := sourcePoolNetwork(p)
		inUse := 0
		nodeSet := map[string]bool{}
		for _, s := range p.ServerIDs {
			if n, ok := nodeOf[s]; ok {
				nodeSet[n.ID] = true
			}
		}
		for _, a := range p.Addresses {
			if a.VMID != "" {
				inUse++
				if n := vmNode[a.VMID]; n != "" {
					nodeSet[n] = true
				}
			}
		}
		detail := &ImportPoolDetail{CIDR: cidr, Family: family, Gateway: p.Gateway, DNS: p.DNS, Total: len(p.Addresses), InUse: inUse}
		for _, n := range st.nodes {
			if nodeSet[n.ID] {
				detail.NodeIDs = append(detail.NodeIDs, n.ID)
			}
		}
		it.Pool = detail
		it.Detail = strings.TrimSpace(fmt.Sprintf("%s, %d addresses, %d in use", cidr, len(p.Addresses), inUse))
		if l, ok := st.links[linkKey(importKindPool, p.ID)]; ok && poolByID[l.TargetID].ID != "" {
			it.Action, it.TargetID, it.TargetLabel = ImportActionExists, l.TargetID, poolByID[l.TargetID].Name
		} else if err != nil {
			it.Action, it.Conflicts = ImportActionBlocked, []string{"Its network cannot be worked out: " + err.Error()}
		} else {
			var same *statePool
			var overlaps []string
			for i := range st.pools {
				ep := st.pools[i]
				if ep.Family != family || ep.CIDR == "" {
					continue
				}
				if sameCIDR(ep.CIDR, cidr) {
					same = &st.pools[i]
				} else if cidrsOverlap(ep.CIDR, cidr) {
					overlaps = append(overlaps, fmt.Sprintf("%s (%s)", ep.Name, ep.CIDR))
				}
			}
			switch {
			case same != nil:
				it.Action, it.TargetID, it.TargetLabel = ImportActionLink, same.ID, same.Name
				it.Notes = append(it.Notes, "The panel already has a pool for this network; the import uses it, and reserves the addresses source VMs hold there.")
			case len(overlaps) > 0:
				it.Action = ImportActionBlocked
				it.Conflicts = append(it.Conflicts, "It overlaps the panel's pool "+strings.Join(overlaps, ", ")+". VMs' addresses inside that pool are still recorded there.")
			default:
				it.Action = ImportActionCreate
				if len(detail.NodeIDs) == 0 {
					it.Notes = append(it.Notes, "No mapped node uses it, so it would be offered on every node; choose its nodes after the import.")
				}
				if inUse < len(p.Addresses) {
					it.Notes = append(it.Notes, fmt.Sprintf("%d free addresses become available to new VMs here.", len(p.Addresses)-inUse))
				}
			}
			if out := len(p.Addresses) - len(poolAddressesIn(p, cidr)); out > 0 && it.Action != ImportActionBlocked {
				it.Notes = append(it.Notes, fmt.Sprintf("%d of its addresses lie outside %s and are left out of the pool; VMs holding them still keep them.", out, cidr))
			}
			if len(p.DNS) > 0 && it.Action != ImportActionBlocked {
				it.Notes = append(it.Notes, "DNS "+strings.Join(p.DNS, ", ")+" is noted in the pool's description; guests keep their own resolver settings.")
			}
		}
		items = append(items, it)
	}

	for _, p := range snap.Plans {
		items = append(items, planItems[p.ID])
	}
	for _, u := range snap.Users {
		items = append(items, userItems[u.ID])
	}

	// VMs.
	var defaultTemplate *stateTemplate
	for i := range st.templates {
		if st.templates[i].ID == opts.DefaultTemplateID {
			defaultTemplate = &st.templates[i]
		}
	}
	plannedNames := map[string]string{}
	usedDomains := map[string]string{}
	refHolder := map[string]stateVM{}
	for _, ev := range st.vms {
		if ev.BillingRef != "" {
			refHolder[ev.BillingRef] = ev
		}
	}
	plannedRefs := map[string]string{}
	plannedIPs := map[string]string{}
	for _, v := range snap.VMs {
		label := v.Hostname
		if label == "" {
			label = v.Name
		}
		it := &ImportItem{Kind: importKindVM, Key: v.ID, Label: label,
			Detail: fmt.Sprintf("%s on %s", v.Name, orDash(serverName[v.ServerID]))}
		d := &ImportVMDetail{OwnerKey: v.UserID, PlanKey: v.PlanID, IPs: v.IPs, Suspended: v.Suspended}
		it.VM = d
		block := func(why string) {
			it.Action = ImportActionBlocked
			it.Conflicts = append(it.Conflicts, why)
		}
		owner := userItems[v.UserID]
		if owner != nil {
			d.OwnerLabel = owner.Label
		}
		if pi := planItems[v.PlanID]; pi != nil {
			d.PlanLabel = pi.Label
		}
		if ref := opts.BillingRefs[v.ID]; ref != "" {
			d.BillingRef = NormalizeBillingRef(ref)
		}

		if l, ok := st.links[linkKey(importKindVM, v.ID)]; ok && st.vms[l.TargetID].ID != "" {
			ev := st.vms[l.TargetID]
			it.Action, it.TargetID, it.TargetLabel = ImportActionExists, ev.ID, ev.Hostname
			d.DomainUUID, d.Hostname = ev.ID, ev.Hostname
			items = append(items, it)
			continue
		}
		node, mapped := nodeOf[v.ServerID]
		switch {
		case !isKVM(v.Virt):
			block(fmt.Sprintf("Its virtualization type is %s; only KVM VMs are imported.", v.Virt))
		case owner == nil:
			block(fmt.Sprintf("Its owner (user #%s) is not in the source's user list.", v.UserID))
		case owner.Action == ImportActionBlocked:
			block(fmt.Sprintf("Its owner %s cannot be imported: %s", owner.Label, strings.Join(owner.Conflicts, " ")))
		case !mapped:
			block(fmt.Sprintf("Its server %s is not matched to a node here.", orDash(serverName[v.ServerID])))
		case len(d.BillingRef) > 255:
			block("Its billing reference is longer than 255 characters.")
		}
		if mapped {
			d.NodeID, d.NodeName = node.ID, node.Name
		}
		var dom *vmimport.ImportCandidate
		if it.Action == "" {
			sc := snap.scan(node.ID)
			switch {
			case sc == nil:
				block(fmt.Sprintf("Node %s has not been read yet; the preview updates once it has.", node.Name))
			case sc.Error != "":
				block(fmt.Sprintf("Node %s could not be scanned: %s. Read the source again once it answers.", node.Name, sc.Error))
			default:
				if dom = findDomain(sc, v); dom == nil {
					block(fmt.Sprintf("No libvirt domain named %s (or with its UUID) runs on node %s.", orDash(v.Name), node.Name))
				}
			}
		}
		if dom != nil {
			d.DomainUUID = strings.ToLower(dom.UUID)
			if d.Hostname = v.Hostname; d.Hostname == "" {
				if d.Hostname = dom.Name; dom.Metadata != nil && dom.Metadata.Hostname != "" {
					d.Hostname = dom.Metadata.Hostname
				}
			}
			it.Label = d.Hostname
			if other, dup := usedDomains[d.DomainUUID]; dup {
				block(fmt.Sprintf("Source VM #%s is the same libvirt domain.", other))
			}
			usedDomains[d.DomainUUID] = v.ID
			if len(d.Hostname) > 100 {
				block("Its hostname is longer than 100 characters.")
			}
			for _, ip := range v.IPs {
				if h, ok := st.assigned[ip]; ok && h.VMID == "" {
					block(fmt.Sprintf("Address %s is a customer's floating IP here.", ip))
				} else if ok && h.VMID != d.DomainUUID {
					block(fmt.Sprintf("Address %s is assigned here to VM %s.", ip, orDash(h.Hostname)))
				}
				if other, dup := plannedIPs[ip]; dup {
					block(fmt.Sprintf("Source VM #%s holds address %s too.", other, ip))
				} else {
					plannedIPs[ip] = v.ID
				}
			}
			if d.BillingRef != "" {
				if ev, ok := refHolder[d.BillingRef]; ok && ev.ID != d.DomainUUID {
					block(fmt.Sprintf("Its billing reference %s is already on VM %s here.", d.BillingRef, orDash(ev.Hostname)))
				} else if other, dup := plannedRefs[d.BillingRef]; dup {
					block(fmt.Sprintf("Source VM #%s has the same billing reference %s.", other, d.BillingRef))
				}
				plannedRefs[d.BillingRef] = v.ID
			}
			if t := matchTemplate(v.OSName, st.templates); t != nil {
				d.TemplateID, d.TemplateLabel = t.ID, templateLabel(*t)
			} else if defaultTemplate != nil {
				d.TemplateID, d.TemplateLabel = defaultTemplate.ID, templateLabel(*defaultTemplate)
			}
		}
		if it.Action == "" {
			ownerID := owner.TargetID // "" for an account the import creates
			pi := planItems[v.PlanID]
			planOK := pi != nil && pi.Action != ImportActionBlocked
			if !planOK {
				it.Notes = append(it.Notes, "It gets no plan: its source plan is not imported.")
			}
			nameKey := hostKey(ownerID, d.Hostname)
			if ownerID == "" {
				nameKey = hostKey("new:"+v.UserID, d.Hostname)
			}
			if other, dup := plannedNames[nameKey]; dup {
				block(fmt.Sprintf("Source VM #%s of the same owner has the same name; names are unique per owner.", other))
			}
			plannedNames[nameKey] = v.ID

			if ev, exists := st.vms[d.DomainUUID]; exists {
				it.TargetID, it.TargetLabel = ev.ID, ev.Hostname
				if ev.Source != "virtualizor" {
					block("A VM with this ID was created in this panel; it is left alone.")
				} else {
					var changes []string
					if ownerID == "" || ownerID != ev.UserID {
						changes = append(changes, fmt.Sprintf("its owner becomes %s", owner.Label))
						if vmID, taken := st.hostnames[hostKey(ownerID, ev.Hostname)]; ownerID != "" && taken && vmID != ev.ID {
							block(fmt.Sprintf("%s already has a VM named %s.", owner.Label, ev.Hostname))
						}
					}
					if planOK && (pi.Action == ImportActionCreate || pi.TargetID != ev.PlanID) {
						changes = append(changes, "its plan is set to "+d.PlanLabel)
					}
					if d.BillingRef != "" && d.BillingRef != ev.BillingRef {
						changes = append(changes, "its billing reference is set to "+d.BillingRef)
					}
					if it.Action == "" {
						if len(changes) == 0 {
							it.Action = ImportActionLink
							it.Notes = append(it.Notes, "It is already in the panel with this owner; the import only records it.")
						} else {
							it.Action = ImportActionUpdate
							it.Notes = append(it.Notes, "It is already in the panel (imported from its node earlier): "+strings.Join(changes, ", ")+".")
						}
					}
				}
			} else if it.Action == "" {
				if vmID, taken := st.hostnames[hostKey(ownerID, d.Hostname)]; ownerID != "" && taken && vmID != d.DomainUUID {
					block(fmt.Sprintf("%s already has a VM named %s.", owner.Label, d.Hostname))
				} else if d.TemplateID == "" {
					block(fmt.Sprintf("Its system %q matches no template; choose a default template.", v.OSName))
				} else {
					it.Action = ImportActionCreate
					it.Notes = append(it.Notes, fmt.Sprintf("Adopted where it runs, on node %s. Nothing on the node changes.", node.Name))
				}
			}
			if v.Suspended && it.Action != ImportActionBlocked {
				it.Notes = append(it.Notes, "It is suspended on the source and stays suspended here.")
			}
			if (v.SpeedKBs > 0 || v.BandwidthGB > 0) && it.Action == ImportActionCreate {
				it.Notes = append(it.Notes, fmt.Sprintf("Port speed %s and monthly transfer %s are recorded on its addresses, as on the source.",
					orUnlimited(int64(importSpeedMbps(v.SpeedKBs)), "Mbit/s"), orUnlimited(v.BandwidthGB, "GB")))
			}
		}
		items = append(items, it)
	}

	plan.Items = make([]ImportItem, 0, len(items))
	for _, it := range items {
		plan.Items = append(plan.Items, *it)
	}
	if defaultTemplate == nil {
		plan.Notes = append(plan.Notes, "Choose a default template: VMs whose system matches no template need one recorded.")
	}
	for _, s := range servers {
		if s.NodeID == "" && s.Matched != "skipped" && s.VMs > 0 {
			plan.Notes = append(plan.Notes, fmt.Sprintf("Server %s has %d VM(s) and is not matched to a node.", orDash(s.Name), s.VMs))
		}
	}
	return plan
}

func orDash(s string) string {
	if s == "" {
		return "(unnamed)"
	}
	return s
}

func orUnlimited(v int64, unit string) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d %s", v, unit)
}
