package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"gorm.io/gorm"
)

// Virtual hardware beyond a VM's size: what a new VM gets from its plan and
// image, the node's pool of dedicated cores, and staff changing a VM's
// hardware later (applied by its node at the next power cycle).

var (
	// ErrNoDedicatedCPUs refuses a dedicated VM where the node's pool of
	// dedicated cores has too few free.
	ErrNoDedicatedCPUs = errors.New("not enough free dedicated CPU cores")
	// ErrInvalidHardware is a hardware profile that cannot be given.
	ErrInvalidHardware = errors.New("invalid hardware")
	// ErrHardwareUnavailable is hardware the VM's node cannot give.
	ErrHardwareUnavailable = errors.New("the node cannot give this hardware")
)

// NodeFeatureSet asks a node's agent what it can do; empty when it cannot be
// asked.
func (s *VMService) NodeFeatureSet(ctx context.Context, nodeID string) map[string]bool {
	if s.features != nil {
		return s.features(ctx, nodeID)
	}
	out := map[string]bool{}
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return out
	}
	actx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return out
	}
	cctx, cancel := context.WithTimeout(actx, 15*time.Second)
	defer cancel()
	features, _ := nodeFeatures(cctx, client)
	for _, f := range features {
		out[f] = true
	}
	return out
}

// hardwareFeatures are the agent features a profile needs, each with what
// the node lacks when it does not have it. A plain profile, or one with only a
// CPU model, needs none: older agents read the model from the create metadata.
func hardwareFeatures(hw models.Hardware) []struct{ feature, lack string } {
	var need []struct{ feature, lack string }
	add := func(f, lack string) { need = append(need, struct{ feature, lack string }{f, lack}) }
	plain := hw
	plain.CPUModel, plain.Firmware = "", ""
	if !plain.IsZero() {
		add(models.FeatureVMHardware, "its agent needs an update for hardware options")
	}
	if hw.SecureBoot {
		add(models.FeatureSecureBoot, "it has no Secure Boot firmware (the ovmf package)")
	}
	if hw.TPM {
		add(models.FeatureTPM, "it has no TPM emulator (swtpm)")
	}
	if hw.Hugepages {
		add(models.FeatureHugepages, "it has no huge pages reserved")
	}
	return need
}

// checkNodeHardware refuses hardware the node cannot give, saying why.
func (s *VMService) checkNodeHardware(ctx context.Context, nodeID string, hw models.Hardware) error {
	if len(hardwareFeatures(hw)) == 0 {
		return nil
	}
	return s.nodeGives(ctx, nodeID, s.NodeFeatureSet(ctx, nodeID), hw)
}

// nodeGives is checkNodeHardware against features already asked for.
func (s *VMService) nodeGives(ctx context.Context, nodeID string, have map[string]bool, hw models.Hardware) error {
	need := hardwareFeatures(hw)
	var lacks []string
	for _, n := range need {
		if !have[n.feature] {
			lacks = append(lacks, n.lack)
		}
	}
	if len(lacks) > 0 {
		name := nodeID
		if n, err := s.nodeRepo.GetByID(ctx, nodeID); err == nil {
			name = n.Name
		}
		return fmt.Errorf("%w: node %s cannot give it: %s", ErrHardwareUnavailable, name, strings.Join(lacks, "; "))
	}
	return nil
}

// newVMHardware is the profile of a new VM: its plan's (or what staff set
// instead), plus what its image needs. Secure Boot applies to UEFI images
// only; a plan offering it to BIOS images leaves them without it.
func newVMHardware(plan *models.Plan, tmpl *models.OSTemplate, override *models.Hardware, uefi bool, cpuModel string) models.Hardware {
	var hw models.Hardware
	if plan != nil {
		hw = plan.Hardware
	}
	if override != nil {
		hw = *override
	}
	if cpuModel != "" {
		hw.CPUModel = cpuModel
	}
	hw.TPM = hw.TPM || tmpl.TPM
	hw.SecureBoot = (hw.SecureBoot || tmpl.SecureBoot) && uefi
	hw.Firmware, hw.PinnedCPUs = "", ""
	if uefi {
		hw.Firmware = models.FirmwareUEFI
	}
	return hw
}

// writeVMHardware stores a VM's profile. By table name: the field is
// read-only on the model.
func writeVMHardware(tx *gorm.DB, vmID string, hw models.Hardware) error {
	raw, err := json.Marshal(hw)
	if err != nil {
		return err
	}
	if err := tx.Table("vms").Where("id = ?", vmID).Update("hardware", string(raw)).Error; err != nil {
		return fmt.Errorf("failed to store the VM's hardware: %w", err)
	}
	return nil
}

// nodeCPUPool is a node's pool of dedicated cores. With lock, the node's
// cores are held for the rest of the transaction, so two changes never hand
// out the same core.
func nodeCPUPool(ctx context.Context, db *gorm.DB, nodeID string, lock bool) ([]int, error) {
	if lock && db.Dialector.Name() == "postgres" {
		if err := db.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "maburvm-dedicated-cpus:"+nodeID).Error; err != nil {
			return nil, fmt.Errorf("failed to lock the node's dedicated cores: %w", err)
		}
	}
	var pool string
	if err := db.WithContext(ctx).Raw("SELECT COALESCE(dedicated_cpus, '') FROM nodes WHERE id::text = ?", nodeID).Scan(&pool).Error; err != nil {
		return nil, fmt.Errorf("failed to read the node's dedicated cores: %w", err)
	}
	return models.ParseCPUSet(pool)
}

// pinnedVM is a VM holding dedicated cores.
type pinnedVM struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	CPUs     string `json:"cpus"`
}

// pinnedVMs are the VMs on a node holding dedicated cores, but except.
func pinnedVMs(ctx context.Context, db *gorm.DB, nodeID, except string) ([]pinnedVM, error) {
	var out []pinnedVM
	err := db.WithContext(ctx).Raw(`SELECT id::text AS id, hostname, hardware->>'pinned_cpus' AS cpus FROM vms
		WHERE node_id::text = ? AND id::text <> ? AND deleted_at IS NULL AND COALESCE(hardware->>'pinned_cpus', '') <> ''
		ORDER BY hostname`, nodeID, except).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read the node's dedicated VMs: %w", err)
	}
	return out, nil
}

func pinnedSets(vms []pinnedVM) []string {
	out := make([]string, len(vms))
	for i, v := range vms {
		out[i] = v.CPUs
	}
	return out
}

// freeDedicatedCores is how many of a node's dedicated cores no VM holds.
func freeDedicatedCores(ctx context.Context, db *gorm.DB, nodeID string) int {
	pool, err := nodeCPUPool(ctx, db, nodeID, false)
	if err != nil || len(pool) == 0 {
		return 0
	}
	vms, err := pinnedVMs(ctx, db, nodeID, "")
	if err != nil {
		return 0
	}
	free, _ := models.FreeCPUs(pool, pinnedSets(vms), len(pool))
	return len(free)
}

// allocateDedicatedTx gives a VM n cores from its node's pool.
func allocateDedicatedTx(ctx context.Context, tx *gorm.DB, nodeID, vmID string, n int) (string, error) {
	pool, err := nodeCPUPool(ctx, tx, nodeID, true)
	if err != nil {
		return "", err
	}
	if len(pool) == 0 {
		return "", fmt.Errorf("%w: the node has no pool of dedicated cores; set one on its page", ErrNoDedicatedCPUs)
	}
	vms, err := pinnedVMs(ctx, tx, nodeID, vmID)
	if err != nil {
		return "", err
	}
	picked, ok := models.FreeCPUs(pool, pinnedSets(vms), n)
	if !ok {
		return "", fmt.Errorf("%w: the node has %d of its %d dedicated cores free and the VM needs %d", ErrNoDedicatedCPUs, len(picked), len(pool), n)
	}
	return models.FormatCPUSet(picked), nil
}

// resizeDedicatedTx gives a dedicated VM n cores: the first n it holds when it
// shrinks, those plus free ones from the pool when it grows.
func resizeDedicatedTx(ctx context.Context, tx *gorm.DB, nodeID, vmID, held string, n int) (string, error) {
	cur, err := models.ParseCPUSet(held)
	if err != nil {
		return "", err
	}
	if n <= len(cur) {
		return models.FormatCPUSet(cur[:n]), nil
	}
	pool, err := nodeCPUPool(ctx, tx, nodeID, true)
	if err != nil {
		return "", err
	}
	vms, err := pinnedVMs(ctx, tx, nodeID, vmID)
	if err != nil {
		return "", err
	}
	extra, ok := models.FreeCPUs(pool, append(pinnedSets(vms), held), n-len(cur))
	if !ok {
		return "", fmt.Errorf("%w: the node has %d dedicated cores free and the VM needs %d more", ErrNoDedicatedCPUs, len(extra), n-len(cur))
	}
	return models.FormatCPUSet(append(cur, extra...)), nil
}

// NodeCPUPool is a node's pool of dedicated cores and who holds them.
type NodeCPUPool struct {
	Pool string     `json:"pool"`
	Size int        `json:"size"`
	Used int        `json:"used"`
	VMs  []pinnedVM `json:"vms"`
}

// GetNodeCPUPool reads a node's pool of dedicated cores.
func GetNodeCPUPool(ctx context.Context, db *gorm.DB, nodeID string) (*NodeCPUPool, error) {
	var n int64
	if err := db.WithContext(ctx).Model(&models.Node{}).Where("id = ?", nodeID).Count(&n).Error; err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNodeNotFound
	}
	pool, err := nodeCPUPool(ctx, db, nodeID, false)
	if err != nil {
		return nil, err
	}
	vms, err := pinnedVMs(ctx, db, nodeID, "")
	if err != nil {
		return nil, err
	}
	used := 0
	for _, v := range vms {
		cpus, _ := models.ParseCPUSet(v.CPUs)
		used += len(cpus)
	}
	if vms == nil {
		vms = []pinnedVM{}
	}
	return &NodeCPUPool{Pool: models.FormatCPUSet(pool), Size: len(pool), Used: used, VMs: vms}, nil
}

// SetNodeCPUPool changes a node's pool of dedicated cores. Cores a VM holds
// stay in it. The pool is only bookkeeping for placing dedicated VMs: the
// node's other VMs are not moved off those cores.
func SetNodeCPUPool(ctx context.Context, db *gorm.DB, nodeID, pool string) (*NodeCPUPool, error) {
	cpus, err := models.ParseCPUSet(pool)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHardware, err)
	}
	if len(cpus) > 1024 {
		return nil, fmt.Errorf("%w: a pool of more than 1024 cores", ErrInvalidHardware)
	}
	in := map[int]bool{}
	for _, c := range cpus {
		in[c] = true
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := nodeCPUPool(ctx, tx, nodeID, true); err != nil {
			return err
		}
		vms, err := pinnedVMs(ctx, tx, nodeID, "")
		if err != nil {
			return err
		}
		for _, v := range vms {
			held, _ := models.ParseCPUSet(v.CPUs)
			for _, c := range held {
				if !in[c] {
					return fmt.Errorf("%w: %s holds core %d; keep it in the pool, or turn off that VM's dedicated CPUs first", ErrInvalidHardware, v.Hostname, c)
				}
			}
		}
		return tx.Table("nodes").Where("id = ?", nodeID).Update("dedicated_cpus", models.FormatCPUSet(cpus)).Error
	})
	if err != nil {
		return nil, err
	}
	return GetNodeCPUPool(ctx, db, nodeID)
}

// HardwareResult is a VM's hardware after a change, and where its node is
// with it.
type HardwareResult struct {
	Hardware models.Hardware `json:"hardware"`
	// Applied: the node has it in the VM's definition. RestartRequired: the VM
	// runs on what it booted with until it is powered off and on.
	Applied         bool     `json:"applied"`
	RestartRequired bool     `json:"restart_required"`
	Note            *string  `json:"note"`
	Warnings        []string `json:"warnings"`
}

// changedHardware names the parts of a profile that differ, as the agent's
// SetVMHardware takes them.
func changedHardware(a, b models.Hardware) []string {
	var out []string
	if a.CPUModel != b.CPUModel {
		out = append(out, "cpu_model")
	}
	if a.Sockets != b.Sockets || a.Threads != b.Threads {
		out = append(out, "topology")
	}
	if a.DedicatedCPU != b.DedicatedCPU || a.NUMA != b.NUMA || a.PinnedCPUs != b.PinnedCPUs {
		out = append(out, "pinning")
	}
	if a.Hugepages != b.Hugepages {
		out = append(out, "hugepages")
	}
	if a.Watchdog != b.Watchdog {
		out = append(out, "watchdog")
	}
	if a.RestartOnCrash != b.RestartOnCrash {
		out = append(out, "restart_on_crash")
	}
	return out
}

// hardwareWarnings says what a profile means for moving the VM.
func hardwareWarnings(hw models.Hardware) []string {
	var out []string
	switch hw.CPUModel {
	case "", "host-passthrough":
		out = append(out, "The VM sees its node's own CPU: it moves live only to a node with the same CPU.")
	case "host-model":
		out = append(out, "The VM sees a CPU modelled on its node's: it moves live to nodes with that CPU or a newer one of the same family.")
	default:
		out = append(out, fmt.Sprintf("The VM sees a %s CPU: it moves live only to nodes whose CPUs offer that model.", hw.CPUModel))
	}
	if hw.DedicatedCPU {
		out = append(out, "Dedicated cores belong to the node: turn them off before moving the VM.")
	}
	if hw.Hugepages {
		out = append(out, "Huge pages must be reserved on any node the VM moves to.")
	}
	return out
}

// SetVMHardware changes a VM's hardware (staff only) and has its node put it
// in the VM's definition, for the VM's next power off and on. Firmware, Secure
// Boot and the TPM are set when a VM is created and do not change.
func (s *VMService) SetVMHardware(ctx context.Context, vmID string, want models.Hardware) (*HardwareResult, error) {
	vm, err := s.limitsVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	// A move carries the definition it read at its start; a change made on the
	// old node meanwhile would be recorded and lost.
	if VMMigrating(ctx, s.db, vm.ID) {
		return nil, ErrVMMigrating
	}
	cur := vm.Hardware
	if want.SecureBoot != cur.SecureBoot || want.TPM != cur.TPM || (want.Firmware != "" && want.Firmware != cur.Firmware) {
		return nil, fmt.Errorf("%w: firmware, Secure Boot and the TPM are set when a VM is created; reinstalling or recreating it is the way to change them", ErrInvalidHardware)
	}
	want.Firmware, want.SecureBoot, want.TPM = cur.Firmware, cur.SecureBoot, cur.TPM
	want.PinnedCPUs = ""
	if want.DedicatedCPU && cur.DedicatedCPU {
		want.PinnedCPUs = cur.PinnedCPUs
	}
	if err := want.Validate(0); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHardware, err)
	}
	// Only what differs goes to the node. A VM older than its record (an
	// imported one) may carry pinning, a topology or a watchdog the panel never
	// saw, which sending every part would strip.
	fields := changedHardware(cur, want)
	if len(fields) == 0 && vm.HardwareNote != nil {
		// An earlier change may not have reached the node: send again the parts
		// the record sets, never those it leaves at their defaults.
		fields = changedHardware(models.Hardware{}, cur)
	}
	if len(fields) == 0 {
		if vm.HardwareNote != nil {
			_ = s.db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("hardware_note", nil).Error
		}
		return &HardwareResult{Hardware: cur, Applied: true, Warnings: hardwareWarnings(cur)}, nil
	}
	have := s.NodeFeatureSet(ctx, vm.NodeID)
	if !have[models.FeatureVMHardware] {
		return nil, fmt.Errorf("%w: the VM's node needs an agent update before its hardware can change", ErrHardwareUnavailable)
	}
	if err := s.nodeGives(ctx, vm.NodeID, have, want); err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if want.DedicatedCPU && want.PinnedCPUs == "" {
			cpus, err := allocateDedicatedTx(ctx, tx, vm.NodeID, vm.ID, vm.Resources.CPU)
			if err != nil {
				return err
			}
			want.PinnedCPUs = cpus
		}
		return writeVMHardware(tx, vm.ID, want)
	})
	if err != nil {
		return nil, err
	}
	res := &HardwareResult{Hardware: want, Warnings: hardwareWarnings(want)}
	send := s.sendHW
	if send == nil {
		send = s.sendHardware
	}
	resp, err := send(ctx, vm.NodeID, &pb.SetVMHardwareRequest{VmId: vm.ID, Hardware: queue.HardwareProto(&want), Fields: fields})
	refused := false
	switch {
	case err != nil:
		// Whether it got there is unknown: the record keeps the change, and the
		// cores it took, until saving again sends it again.
		note := "The node did not take the change: " + err.Error() + ". Save again once it answers."
		res.Note = &note
	case !resp.GetSuccess():
		note := "The node refused the change: " + agentMessage(resp.GetError(), resp.GetMessage())
		res.Note, refused = &note, true
	default:
		res.Applied, res.RestartRequired = true, resp.GetRestartRequired()
	}
	if refused {
		// The record goes back to what the node has, and cores taken for the
		// change go back to the pool.
		if werr := writeVMHardware(s.db.WithContext(ctx), vm.ID, cur); werr != nil {
			s.logger.ErrorContext(ctx, "failed to restore the VM's hardware record", "vm_id", vm.ID, "error", werr)
		} else {
			res.Hardware, res.Warnings = cur, hardwareWarnings(cur)
		}
	}
	if werr := s.db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("hardware_note", res.Note).Error; werr != nil {
		s.logger.ErrorContext(ctx, "failed to record whether the node has the VM's hardware", "vm_id", vm.ID, "error", werr)
	}
	return res, nil
}

func (s *VMService) sendHardware(ctx context.Context, nodeID string, req *pb.SetVMHardwareRequest) (*pb.SetVMHardwareResponse, error) {
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	authCtx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	authCtx, cancel := context.WithTimeout(authCtx, 30*time.Second)
	defer cancel()
	return client.SetVMHardware(authCtx, req)
}

// SetAlerts has the service raise alerts about VMs that keep crashing.
func (s *VMService) SetAlerts(a *AlertService) { s.alerts = a }

// crashAlertAfter is how many crashes within the hour make a VM whose node
// restarts it worth an alert.
const crashAlertAfter = 2

// noteCrashes raises an alert for a VM its node keeps restarting after
// crashes, critical once the node has stopped restarting it, and clears it
// when the crashes have aged out.
func (s *VMService) noteCrashes(ctx context.Context, vm *models.VM, resp *pb.VMStatusResponse) {
	if s.alerts == nil {
		return
	}
	key := "vm_crash:" + vm.ID
	n, paused := int(resp.GetCrashRestarts()), resp.GetCrashRestartPaused()
	in := AlertInput{Key: key, Kind: models.AlertKindVMCrash, ResourceType: "vm", ResourceID: vm.ID, ResourceName: vm.Hostname}
	switch {
	case paused:
		in.Severity, in.Title = models.AlertSeverityCritical, vm.Hostname+" keeps crashing and was left off"
		in.Detail = fmt.Sprintf("It crashed %d times within an hour, so its node stopped starting it again. Look at its console or logs, then start it.", n)
	case n >= crashAlertAfter:
		in.Severity, in.Title = models.AlertSeverityWarning, fmt.Sprintf("%s crashed %d times in the last hour", vm.Hostname, n)
		in.Detail = "Its node started it again each time. After a few more crashes it stops doing so."
	default:
		if _, raised := s.crashAlerted.Load(vm.ID); raised || vm.Hardware.RestartOnCrash {
			s.alerts.Clear(ctx, key)
			s.crashAlerted.Delete(vm.ID)
		}
		return
	}
	s.alerts.Raise(ctx, in)
	s.crashAlerted.Store(vm.ID, true)
}
