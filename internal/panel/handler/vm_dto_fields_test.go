package handler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

// The VM endpoints do not serialise models.VM: each maps to a hand-written DTO,
// field by field. That has now silently dropped a newly added field four times —
// vpc_id on create (which made a cross-tenant placement look like it succeeded)
// and region on both the list and the detail response.
//
// Nothing fails to compile and no existing test breaks when a field is added to
// the model and forgotten in a DTO: the DTO is a separate type simply missing a
// line. This test is that missing feedback.
func TestVMDTOsExposeModelFields(t *testing.T) {
	// Internal bookkeeping the API deliberately never exposes.
	internal := map[string]bool{
		"source_migration": true,
	}
	// Detail-only fields: a list row does not need them, and padding every row
	// with them would be noise rather than safety.
	detailOnly := map[string]bool{
		"console_enabled": true,
		"rescue_mode":     true,
	}

	modelKeys := jsonFieldNames(models.VM{})
	listKeys := jsonFieldNames(VMListItem{})
	detailKeys := jsonFieldNames(VMDetailResponse{})

	for key := range modelKeys {
		if internal[key] {
			continue
		}
		if !detailKeys[key] {
			t.Errorf("VMDetailResponse is missing %q — it was added to models.VM and not carried into the detail DTO", key)
		}
		if !detailOnly[key] && !listKeys[key] {
			t.Errorf("VMListItem is missing %q — it was added to models.VM and not carried into the list DTO", key)
		}
	}
}

// jsonFieldNames reads the json tags rather than marshalling a value: an
// omitempty field vanishes from a zero value's output, which would make this
// check pass for exactly the fields most likely to be forgotten.
func jsonFieldNames(v interface{}) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			out[name] = true
		}
	}
	return out
}
