package libvirt

import (
	"errors"
	"fmt"
	"testing"

	"libvirt.org/go/libvirt"
)

func TestIsNoDomain(t *testing.T) {
	noDomain := fmt.Errorf("domain not found: %w", libvirt.Error{Code: libvirt.ERR_NO_DOMAIN})
	socketDown := fmt.Errorf("failed to connect to libvirt: %w", libvirt.Error{Code: libvirt.ERR_SYSTEM_ERROR})
	if !IsNoDomain(noDomain) {
		t.Error("wrapped ERR_NO_DOMAIN not recognised")
	}
	if IsNoDomain(socketDown) || IsNoDomain(errors.New("domain not found")) || IsNoDomain(nil) {
		t.Error("non-NO_DOMAIN error treated as a missing domain")
	}
}
