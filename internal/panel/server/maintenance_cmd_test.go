package server

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Wrong arguments are refused before anything is opened: the upgrade script
// reads the exit code.
func TestMaintenanceCommandUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"sideways"},
		{"on", "--wait"},
		{"on", "--wait", "soon"},
		{"on", "--wait", "-1"},
		{"off", "--hold-jobs"},
		{"status", "--message", "x"},
	} {
		var out, errOut bytes.Buffer
		assert.Equal(t, 2, MaintenanceCommand(args, &out, &errOut), "%v", args)
		assert.Contains(t, errOut.String(), "usage: maintenance", "%v", args)
	}
	var out, errOut bytes.Buffer
	assert.Equal(t, 0, MaintenanceCommand([]string{"help"}, &out, &errOut))
	assert.Contains(t, out.String(), "--hold-jobs")
}
