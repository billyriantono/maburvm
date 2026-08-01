package storage

import (
	"bytes"
	"context"
	"os/exec"
)

// CommandResult captures the separated stdout/stderr of an executed command, so
// callers can distinguish a clean run from one that produced diagnostic output
// on stderr (which, for LVM/ZFS, is treated as a failure even with a zero exit
// code). It never munges the streams into a single combined blob.
type CommandResult struct {
	// Stdout is the raw standard output bytes (untrimmed, so whitespace-only
	// output is detectable as non-empty).
	Stdout []byte
	// Stderr is the raw standard error bytes. Any non-empty content — even a
	// single space — is treated as a command-level failure by the strict
	// LVM/ZFS parsers.
	Stderr []byte
	// Err is the process-level error (exec failure, killed, timeout, non-zero
	// exit). A non-nil Err always dominates over Stdout/Stderr parsing.
	Err error
}

// StdoutString returns stdout as a string without trimming (whitespace-only
// output is significant and must remain visible to parsers).
func (r CommandResult) StdoutString() string { return string(r.Stdout) }

// StderrString returns stderr as a string.
func (r CommandResult) StderrString() string { return string(r.Stderr) }

// commandRunner executes external commands (lvs/zfs) and returns the separated
// stdout/stderr plus the process error. It is injectable for tests so no real
// LVM/ZFS binaries are required.
type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) CommandResult
}

// osRunner executes commands via os/exec with separate stdout and stderr
// buffers. It deliberately does NOT use CombinedOutput: a stderr message from
// LVM/ZFS must remain visible and distinct so a silent success with diagnostic
// noise on stderr is correctly treated as a failure.
type osRunner struct{}

func (osRunner) Run(ctx context.Context, name string, args ...string) CommandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Use Run (not Output/CombinedOutput) so stderr diagnostics are captured
	// separately and a non-zero exit surfaces as Err without conflating streams.
	err := cmd.Run()
	return CommandResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
		Err:    err,
	}
}
