package network

import "testing"

// The panel accepts "80", "1000-11000" and "80,443"; iptables wants a colon for
// a range and -m multiport for anything that is not a single port. A mismatch
// here is not a cosmetic bug: rules are applied as a set, so one unparsable
// entry aborts the apply and leaves the VM with NO rules — wide open, rather
// than partially fenced.
func TestNormalizePortSpec(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      string
		wantMulti bool
		wantErr   bool
	}{
		{name: "single port", in: "80", want: "80", wantMulti: false},
		{
			name: "hyphenated range becomes a colon range", in: "1000-11000",
			want: "1000:11000", wantMulti: true,
		},
		{
			name: "colon range still accepted", in: "1000:11000",
			want: "1000:11000", wantMulti: true,
		},
		{name: "comma list", in: "80,443,8443", want: "80,443,8443", wantMulti: true},
		{name: "mixed list and range", in: "80,1000-2000", want: "80,1000:2000", wantMulti: true},
		{name: "full range bounds", in: "1-65535", want: "1:65535", wantMulti: true},

		{name: "port zero", in: "0", wantErr: true},
		{name: "port above 65535", in: "65536", wantErr: true},
		{name: "descending range", in: "11000-1000", wantErr: true},
		{name: "not a number", in: "http", wantErr: true},
		{name: "three bounds", in: "80:443:8443", wantErr: true},
		{
			name: "more entries than multiport allows",
			in:   "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16", wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, multi, err := normalizePortSpec(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizePortSpec(%q) = %q, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizePortSpec(%q) returned unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("normalizePortSpec(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if multi != tt.wantMulti {
				t.Errorf("normalizePortSpec(%q) multiport = %v, want %v", tt.in, multi, tt.wantMulti)
			}
		})
	}
}
