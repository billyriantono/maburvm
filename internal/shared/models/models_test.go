package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUserValidation(t *testing.T) {
	tests := []struct {
		name     string
		user     *User
		wantErr  bool
		errCount int
	}{
		{
			name: "valid user",
			user: &User{
				Email:        "test@example.com",
				PasswordHash: "hash",
				Role:         RoleClient,
				IPWhitelist:  []string{"192.168.1.1", "10.0.0.1"},
			},
			wantErr:  false,
			errCount: 0,
		},
		{
			name: "invalid email",
			user: &User{
				Email:        "invalid-email",
				PasswordHash: "hash",
				Role:         RoleClient,
			},
			wantErr:  true,
			errCount: 1,
		},
		{
			name: "invalid role",
			user: &User{
				Email:        "test@example.com",
				PasswordHash: "hash",
				Role:         "invalid",
			},
			wantErr:  true,
			errCount: 1,
		},
		{
			name: "invalid ip in whitelist",
			user: &User{
				Email:        "test@example.com",
				PasswordHash: "hash",
				Role:         RoleClient,
				IPWhitelist:  []string{"invalid-ip"},
			},
			wantErr:  true,
			errCount: 1,
		},
		{
			name: "missing required fields",
			user: &User{
				Email: "",
			},
			wantErr:  true,
			errCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.user.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors, got %d: %v", len(errs), errs)
			}
			if len(errs) != tt.errCount {
				t.Errorf("expected %d errors, got %d: %v", tt.errCount, len(errs), errs)
			}
		})
	}
}

func TestNodeValidation(t *testing.T) {
	tests := []struct {
		name    string
		node    *Node
		wantErr bool
	}{
		{
			name: "valid node",
			node: &Node{
				Name:      "test-node",
				IPAddress: "192.168.1.1",
				Status:    NodeStatusActive,
				Token:     "token123",
			},
			wantErr: false,
		},
		{
			name: "invalid status",
			node: &Node{
				Name:      "test-node",
				IPAddress: "192.168.1.1",
				Status:    "invalid",
				Token:     "token123",
			},
			wantErr: true,
		},
		{
			name: "missing name",
			node: &Node{
				IPAddress: "192.168.1.1",
				Status:    NodeStatusActive,
				Token:     "token123",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.node.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors, got %d: %v", len(errs), errs)
			}
		})
	}
}

func TestVMValidation(t *testing.T) {
	tests := []struct {
		name    string
		vm      *VM
		wantErr bool
	}{
		{
			name: "valid VM",
			vm: &VM{
				UserID:       uuid.New().String(),
				NodeID:       uuid.New().String(),
				Hostname:     "test-vm",
				OSTemplateID: uuid.New().String(),
				Resources: Resources{
					CPU:  2,
					RAM:  2048,
					Disk: 50,
				},
				Status: VMStatusRunning,
			},
			wantErr: false,
		},
		{
			name: "invalid VM status",
			vm: &VM{
				UserID:       uuid.New().String(),
				NodeID:       uuid.New().String(),
				Hostname:     "test-vm",
				OSTemplateID: uuid.New().String(),
				Resources: Resources{
					CPU:  2,
					RAM:  2048,
					Disk: 50,
				},
				Status: "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid resources - CPU too low",
			vm: &VM{
				UserID:       uuid.New().String(),
				NodeID:       uuid.New().String(),
				Hostname:     "test-vm",
				OSTemplateID: uuid.New().String(),
				Resources: Resources{
					CPU:  0,
					RAM:  2048,
					Disk: 50,
				},
				Status: VMStatusRunning,
			},
			wantErr: true,
		},
		{
			name: "invalid VNC port",
			vm: &VM{
				UserID:       uuid.New().String(),
				NodeID:       uuid.New().String(),
				Hostname:     "test-vm",
				OSTemplateID: uuid.New().String(),
				Resources: Resources{
					CPU:  2,
					RAM:  2048,
					Disk: 50,
				},
				Status:  VMStatusRunning,
				VNCPort: func() *int { i := 5000; return &i }(),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.vm.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors, got %d: %v", len(errs), errs)
			}
		})
	}
}

func TestNetworkValidation(t *testing.T) {
	tests := []struct {
		name    string
		network *Network
		wantErr bool
	}{
		{
			name: "valid network",
			network: &Network{
				VMID:           uuid.New().String(),
				IPAddress:      "192.168.1.100",
				BandwidthLimit: 1000,
			},
			wantErr: false,
		},
		{
			name: "invalid VLAN ID",
			network: &Network{
				VMID:           uuid.New().String(),
				IPAddress:      "192.168.1.100",
				BandwidthLimit: 1000,
				VLANID:         func() *int { i := 5000; return &i }(),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.network.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors, got %d: %v", len(errs), errs)
			}
		})
	}
}

func TestFirewallRuleValidation(t *testing.T) {
	tests := []struct {
		name    string
		rule    *FirewallRule
		wantErr bool
	}{
		{
			name: "valid firewall rule",
			rule: &FirewallRule{
				VMID:      uuid.New().String(),
				Protocol:  "tcp",
				PortRange: "80,443,8080-8090",
				Action:    "allow",
				Direction: "inbound",
				Priority:  100,
			},
			wantErr: false,
		},
		{
			name: "invalid port range",
			rule: &FirewallRule{
				VMID:      uuid.New().String(),
				Protocol:  "tcp",
				PortRange: "invalid",
				Action:    "allow",
				Direction: "inbound",
				Priority:  100,
			},
			wantErr: true,
		},
		{
			name: "port range out of bounds",
			rule: &FirewallRule{
				VMID:      uuid.New().String(),
				Protocol:  "tcp",
				PortRange: "70000",
				Action:    "allow",
				Direction: "inbound",
				Priority:  100,
			},
			wantErr: true,
		},
		{
			name: "invalid priority",
			rule: &FirewallRule{
				VMID:      uuid.New().String(),
				Protocol:  "tcp",
				Action:    "allow",
				Direction: "inbound",
				Priority:  2000,
			},
			wantErr: true,
		},
		{
			name: "valid CIDR source",
			rule: &FirewallRule{
				VMID:      uuid.New().String(),
				Protocol:  "tcp",
				Action:    "allow",
				Direction: "inbound",
				SourceIP:  "10.0.0.0/8",
				Priority:  100,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.rule.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors, got %d: %v", len(errs), errs)
			}
		})
	}
}

func TestSessionValidation(t *testing.T) {
	tests := []struct {
		name    string
		session *Session
		wantErr bool
	}{
		{
			name: "valid session",
			session: &Session{
				UserID:    uuid.New().String(),
				Token:     "token123",
				ExpiresAt: time.Now().Add(time.Hour),
			},
			wantErr: false,
		},
		{
			name: "expired session should still validate",
			session: &Session{
				UserID:    uuid.New().String(),
				Token:     "token123",
				ExpiresAt: time.Now().Add(-time.Hour),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.session.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors, got %d: %v", len(errs), errs)
			}
		})
	}
}

func TestPortRangeValidator(t *testing.T) {
	tests := []struct {
		name      string
		portRange string
		want      bool
	}{
		{"single port", "80", true},
		{"port range", "80-443", true},
		{"port list", "80,443,8080", true},
		{"complex", "22,80,443,8000-9000", true},
		{"invalid chars", "abc", false},
		{"port too high", "70000", false},
		{"port zero", "0", false},
		{"empty allowed", "", true},
		{"mixed valid invalid", "80,abc", false},
		{"range with high port", "80-70000", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateStruct(&FirewallRule{PortRange: tt.portRange, VMID: uuid.New().String(), Protocol: "tcp", Action: "allow", Direction: "inbound", Priority: 100})
			got := len(errs) == 0
			if tt.want != got {
				t.Errorf("portRange(%q) = %v, want %v, errors: %v", tt.portRange, got, tt.want, errs)
			}
		})
	}
}

func TestUUIDValidator(t *testing.T) {
	tests := []struct {
		name  string
		uuid  string
		valid bool
	}{
		{"valid UUID", "550e8400-e29b-41d4-a716-446655440000", true},
		{"invalid UUID", "not-a-uuid", false},
		{"empty allowed", "", true},
		{"partial UUID", "550e8400-e29b", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type testStruct struct {
				ID string `validate:"uuid"`
			}
			s := testStruct{ID: tt.uuid}
			errs := ValidateStruct(s)
			got := len(errs) == 0
			if tt.valid != got {
				t.Errorf("uuid(%q) = %v, want %v, errors: %v", tt.uuid, got, tt.valid, errs)
			}
		})
	}
}
