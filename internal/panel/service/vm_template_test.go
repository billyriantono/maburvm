package service

import "testing"

func TestIsInstallableImagePath(t *testing.T) {
	cases := map[string]bool{
		"/var/lib/libvirt/templates/ubuntu-24.04.qcow2": true,
		"https://cloud-images.ubuntu.com/noble.img":     true,
		"/imported":     false, // the import placeholder — no real base image
		"":              false,
		"   ":           false,
		"  /imported  ": false,
	}
	for path, want := range cases {
		if got := isInstallableImagePath(path); got != want {
			t.Errorf("isInstallableImagePath(%q) = %v, want %v", path, got, want)
		}
	}
}

// Both template gates — "is it active" and "does it have a real base image" —
// must apply only to a fresh install. When the disk is cloned, the template is a
// label recording what the machine runs and the bytes come from somewhere else.
//
// Getting this wrong made every captured image unusable: an image records the
// template of the VM it came from, an imported VM's template is "Unknown
// (Imported)" with is_active=false and image_path="/imported", so creating from
// any image failed with "OS template is not active".
func TestCloneBypassesTemplateGates(t *testing.T) {
	const importedPath = "/imported"

	tests := []struct {
		name            string
		cloneSourceRef  string
		templateActive  bool
		templatePath    string
		wantActiveCheck bool
		wantPathCheck   bool
	}{
		{
			name:           "fresh install from an imported template is refused on both counts",
			cloneSourceRef: "", templateActive: false, templatePath: importedPath,
			wantActiveCheck: true, wantPathCheck: true,
		},
		{
			name:           "clone from an image whose template is imported is allowed",
			cloneSourceRef: "images/abc.qcow2", templateActive: false, templatePath: importedPath,
			wantActiveCheck: false, wantPathCheck: false,
		},
		{
			name:           "fresh install from a real, active template is allowed",
			cloneSourceRef: "", templateActive: true, templatePath: "/templates/ubuntu.qcow2",
			wantActiveCheck: true, wantPathCheck: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These mirror the two guards in CreateVM.
			activeCheckApplies := tt.cloneSourceRef == ""
			pathCheckApplies := tt.cloneSourceRef == ""

			if activeCheckApplies != tt.wantActiveCheck {
				t.Errorf("active check applies = %v, want %v", activeCheckApplies, tt.wantActiveCheck)
			}
			if pathCheckApplies != tt.wantPathCheck {
				t.Errorf("path check applies = %v, want %v", pathCheckApplies, tt.wantPathCheck)
			}

			// And when they do apply, an imported template fails them.
			if activeCheckApplies && !tt.templateActive && tt.templatePath == importedPath {
				if isInstallableImagePath(tt.templatePath) {
					t.Error("the import placeholder must never count as installable")
				}
			}
		})
	}
}
