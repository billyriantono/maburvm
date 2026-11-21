package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// An imported VM's hardware change sends only what changed, so what the panel
// never recorded in its definition stays; a refused change is not recorded,
// and one that may not have arrived is sent again without the defaults.
func TestSetVMHardwareSendsOnlyWhatItMust(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT, status TEXT, token TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT, node_id TEXT, hostname TEXT, os_template_id TEXT, resources TEXT, status TEXT,
			hardware TEXT NOT NULL DEFAULT '{}', hardware_note TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`INSERT INTO nodes (id, name, status, token) VALUES ('n1', 'node-a', 'active', 'tok')`,
		`INSERT INTO vms (id, node_id, hostname, resources, status) VALUES ('vm-1', 'n1', 'imported', '{"cpu":2,"ram":2048,"disk":20}', 'running')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	var sent [][]string
	var resp *pb.SetVMHardwareResponse
	var sendErr error
	s := &VMService{db: db, vmRepo: repository.NewVMRepository(db), nodeRepo: repository.NewNodeRepository(db), logger: slog.Default(),
		features: func(context.Context, string) map[string]bool { return map[string]bool{models.FeatureVMHardware: true} },
		sendHW: func(_ context.Context, _ string, req *pb.SetVMHardwareRequest) (*pb.SetVMHardwareResponse, error) {
			sent = append(sent, req.Fields)
			return resp, sendErr
		}}
	stored := func() (models.Hardware, *string) {
		t.Helper()
		vm, err := s.vmRepo.GetByID(ctx, "vm-1")
		if err != nil {
			t.Fatal(err)
		}
		return vm.Hardware, vm.HardwareNote
	}

	resp = &pb.SetVMHardwareResponse{Success: false, Message: "no"}
	res, err := s.SetVMHardware(ctx, "vm-1", models.Hardware{Watchdog: true})
	if err != nil || res.Applied || res.Note == nil {
		t.Fatalf("refused change: %+v %v", res, err)
	}
	if hw, note := stored(); hw.Watchdog || note == nil {
		t.Fatalf("a refused change was recorded: %+v %v", hw, note)
	}
	if !slices.Equal(sent[0], []string{"watchdog"}) {
		t.Fatalf("sent %v, want only the watchdog", sent[0])
	}

	resp, sendErr = nil, errors.New("unreachable")
	if _, err := s.SetVMHardware(ctx, "vm-1", models.Hardware{Watchdog: true}); err != nil {
		t.Fatal(err)
	}
	if hw, note := stored(); !hw.Watchdog || note == nil {
		t.Fatalf("a change of unknown fate must stay recorded and marked: %+v %v", hw, note)
	}
	// Saving the same again sends what the record sets, never all of it.
	resp, sendErr = &pb.SetVMHardwareResponse{Success: true}, nil
	if res, err := s.SetVMHardware(ctx, "vm-1", models.Hardware{Watchdog: true}); err != nil || !res.Applied {
		t.Fatalf("resend: %+v %v", res, err)
	}
	if got := sent[len(sent)-1]; !slices.Equal(got, []string{"watchdog"}) {
		t.Fatalf("resend sent %v: parts at their defaults would strip an imported VM's own", got)
	}
	if _, note := stored(); note != nil {
		t.Fatalf("note kept after the node took it: %v", *note)
	}
}

func TestNewVMHardwareTakesPlanImageAndOverride(t *testing.T) {
	plan := &models.Plan{Hardware: models.Hardware{Watchdog: true, SecureBoot: true, CPUModel: "host-model"}}
	bios := &models.OSTemplate{Firmware: models.FirmwareBIOS}
	win11 := &models.OSTemplate{Firmware: models.FirmwareUEFI, SecureBoot: true, TPM: true}

	hw := newVMHardware(plan, bios, nil, false, "")
	if !hw.Watchdog || hw.SecureBoot || hw.Firmware != "" || hw.CPUModel != "host-model" {
		t.Fatalf("BIOS image on a Secure Boot plan: %+v", hw)
	}
	hw = newVMHardware(nil, win11, nil, true, "")
	if !hw.SecureBoot || !hw.TPM || hw.Firmware != models.FirmwareUEFI {
		t.Fatalf("Windows 11 image: %+v", hw)
	}
	hw = newVMHardware(plan, win11, &models.Hardware{RestartOnCrash: true, PinnedCPUs: "1-2"}, true, "Haswell")
	if hw.Watchdog || !hw.RestartOnCrash || !hw.TPM || hw.PinnedCPUs != "" || hw.CPUModel != "Haswell" {
		t.Fatalf("staff override: %+v", hw)
	}
}

func TestHardwareFeaturesNeededByAProfile(t *testing.T) {
	names := func(hw models.Hardware) []string {
		var out []string
		for _, n := range hardwareFeatures(hw) {
			out = append(out, n.feature)
		}
		return out
	}
	if n := names(models.Hardware{CPUModel: "host-model", Firmware: models.FirmwareUEFI}); len(n) != 0 {
		t.Fatalf("a CPU model and UEFI need no new feature: %v", n)
	}
	got := names(models.Hardware{SecureBoot: true, TPM: true, Hugepages: true})
	want := []string{models.FeatureVMHardware, models.FeatureSecureBoot, models.FeatureTPM, models.FeatureHugepages}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestChangedHardwareNamesWhatTheAgentChanges(t *testing.T) {
	a := models.Hardware{CPUModel: "host-model", Sockets: 1}
	b := models.Hardware{CPUModel: "Haswell", Sockets: 2, DedicatedCPU: true, PinnedCPUs: "4-5", Watchdog: true}
	want := []string{"cpu_model", "topology", "pinning", "watchdog"}
	if got := changedHardware(a, b); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := changedHardware(b, b); len(got) != 0 {
		t.Fatalf("no change gave %v", got)
	}
}

func TestHardwareWarningsSayWhatAMoveNeeds(t *testing.T) {
	w := strings.Join(hardwareWarnings(models.Hardware{CPUModel: "Haswell", DedicatedCPU: true, Hugepages: true}), "\n")
	for _, want := range []string{"Haswell", "Dedicated cores", "Huge pages"} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %q in %s", want, w)
		}
	}
	if w := hardwareWarnings(models.Hardware{}); !strings.Contains(w[0], "same CPU") {
		t.Fatal(w)
	}
}

func TestRescueImageSettings(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	for _, ok := range []RescueSettings{
		{},
		{ImageURL: "https://images.example.com/rescue.qcow2", ImageSHA256: strings.ToUpper(sum)},
		{ImageURL: "/var/lib/maburvm/rescue.qcow2"},
	} {
		if err := ok.Normalize(); err != nil {
			t.Fatalf("%+v: %v", ok, err)
		}
	}
	for _, bad := range []RescueSettings{
		{ImageURL: "https://images.example.com/rescue.qcow2"},
		{ImageURL: "ftp://x/y", ImageSHA256: sum},
		{ImageURL: "/var/../etc/shadow"},
		{ImageSHA256: sum},
		{ImageURL: "/r.qcow2", ImageSHA256: "nothex"},
	} {
		if err := bad.Normalize(); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestTemplateBootExtras(t *testing.T) {
	if err := checkBootExtras(&models.OSTemplate{Firmware: models.FirmwareBIOS, SecureBoot: true}); err == nil {
		t.Fatal("Secure Boot on a BIOS image accepted")
	}
	if err := checkBootExtras(&models.OSTemplate{Firmware: models.FirmwareUEFI, Kind: models.TemplateKindISO, SecureBoot: true, TPM: true}); err != nil {
		t.Fatalf("a Windows 11 installer: %v", err)
	}
	if err := checkBootExtras(&models.OSTemplate{Firmware: models.FirmwareUEFI, SecureBoot: true, TPM: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanHardwareIsValidated(t *testing.T) {
	base := PlanRequest{Name: "p", CPU: 2, RAM: 1024, Disk: 20}
	ok := base
	ok.Hardware = &models.Hardware{DedicatedCPU: true, NUMA: true, Threads: 2}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	for _, hw := range []models.Hardware{{PinnedCPUs: "1"}, {Firmware: models.FirmwareUEFI}, {NUMA: true}, {Sockets: 99}} {
		r := base
		r.Hardware = &hw
		if err := r.validate(); err == nil {
			t.Fatalf("%+v accepted", hw)
		}
	}
}
