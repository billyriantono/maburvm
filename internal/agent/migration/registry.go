package migration

import (
	"sync"
)

// States a migration reports.
const (
	StateRunning   = "running"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateUnknown   = "unknown"
)

// Progress is a migration as the panel sees it.
type Progress struct {
	State     string
	Phase     string
	Total     int64
	Processed int64
	Error     string
}

type job struct {
	migrationID string
	progress    Progress
	cancel      func()
	aborted     bool
}

// Registry tracks the migrations an agent runs, one per VM. Runs outlive the
// call that started them; the panel follows them through Get.
type Registry struct {
	mu   sync.Mutex
	jobs map[string]*job
}

// Start records a new run. It returns false, leaving everything as it was,
// when the VM already has a run in progress; the same migration asked twice
// is that case too, so a retried call does not start a second copy.
func (r *Registry) Start(vmID, migrationID, phase string, cancel func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobs == nil {
		r.jobs = map[string]*job{}
	}
	if j := r.jobs[vmID]; j != nil && j.progress.State == StateRunning {
		return false
	}
	r.jobs[vmID] = &job{migrationID: migrationID, cancel: cancel, progress: Progress{State: StateRunning, Phase: phase}}
	return true
}

// Running reports whether migrationID is the VM's run in progress.
func (r *Registry) Running(vmID, migrationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[vmID]
	return j != nil && j.migrationID == migrationID && j.progress.State == StateRunning
}

// Busy reports whether the VM has any run in progress.
func (r *Registry) Busy(vmID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[vmID]
	return j != nil && j.progress.State == StateRunning
}

// Update sets the phase and byte counts of a run in progress.
func (r *Registry) Update(vmID, migrationID, phase string, total, processed int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j := r.jobs[vmID]; j != nil && j.migrationID == migrationID && j.progress.State == StateRunning {
		if phase != "" {
			j.progress.Phase = phase
		}
		j.progress.Total, j.progress.Processed = total, processed
	}
}

// Finish ends a run: completed with a nil error, cancelled when it was
// aborted, failed otherwise.
func (r *Registry) Finish(vmID, migrationID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[vmID]
	if j == nil || j.migrationID != migrationID {
		return
	}
	switch {
	case err == nil:
		j.progress.State = StateCompleted
	case j.aborted:
		j.progress.State, j.progress.Error = StateCancelled, err.Error()
	default:
		j.progress.State, j.progress.Error = StateFailed, err.Error()
	}
	j.cancel = nil
}

// Get reports a run; State is StateUnknown when this agent has no record of it.
func (r *Registry) Get(vmID, migrationID string) Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j := r.jobs[vmID]; j != nil && j.migrationID == migrationID {
		return j.progress
	}
	return Progress{State: StateUnknown}
}

// Abort asks a run in progress to stop; false when there is none.
func (r *Registry) Abort(vmID, migrationID string) bool {
	r.mu.Lock()
	j := r.jobs[vmID]
	if j == nil || j.migrationID != migrationID || j.progress.State != StateRunning || j.cancel == nil {
		r.mu.Unlock()
		return false
	}
	j.aborted = true
	cancel := j.cancel
	r.mu.Unlock()
	cancel()
	return true
}
