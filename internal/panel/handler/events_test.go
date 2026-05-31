package handler

import "testing"

func TestSameStatusMap(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]string
		want bool
	}{
		{"both empty", map[string]string{}, map[string]string{}, true},
		{"identical", map[string]string{"vm1": "running", "vm2": "stopped"}, map[string]string{"vm1": "running", "vm2": "stopped"}, true},
		{"status changed", map[string]string{"vm1": "running"}, map[string]string{"vm1": "stopped"}, false},
		{"vm added", map[string]string{"vm1": "running"}, map[string]string{"vm1": "running", "vm2": "creating"}, false},
		{"vm removed", map[string]string{"vm1": "running", "vm2": "stopped"}, map[string]string{"vm1": "running"}, false},
		{"order independent", map[string]string{"a": "running", "b": "stopped"}, map[string]string{"b": "stopped", "a": "running"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameStatusMap(tc.a, tc.b); got != tc.want {
				t.Errorf("sameStatusMap(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
