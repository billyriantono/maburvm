package server

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
)

const maintenanceUsage = `usage: maintenance on [--message TEXT] [--hold-jobs] [--wait SECONDS] | off | status
  on            customers and the billing system cannot change anything, and a
                banner says why; staff carry on. The running panel sees it within seconds.
    --hold-jobs   start no new jobs; running ones finish, queued ones wait
    --wait N      with --hold-jobs, wait up to N seconds for running jobs to
                  finish; exits 3 if some are still running then
  off           end maintenance and release held jobs
  status        print the state and the jobs still running
`

// maintenancePoll is how often --wait looks at the running jobs.
var maintenancePoll = 5 * time.Second

// MaintenanceCommand is "panel maintenance ...", used by scripts/upgrade-panel.sh.
// It returns the exit code.
func MaintenanceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, maintenanceUsage)
		return 2
	}
	cmd, args := args[0], args[1:]
	message, hold, wait := "", false, 0
	for i := 0; i < len(args); i++ {
		switch {
		case cmd == "on" && args[i] == "--hold-jobs":
			hold = true
		case cmd == "on" && args[i] == "--message" && i+1 < len(args):
			i++
			message = args[i]
		case cmd == "on" && args[i] == "--wait" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				fmt.Fprint(stderr, maintenanceUsage)
				return 2
			}
			wait = n
		default:
			fmt.Fprint(stderr, maintenanceUsage)
			return 2
		}
	}
	switch cmd {
	case "on", "off", "status":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, maintenanceUsage)
		return 0
	default:
		fmt.Fprint(stderr, maintenanceUsage)
		return 2
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintln(stderr, "maintenance: failed to load config:", err)
		return 1
	}
	db, err := repository.InitDB(repository.NewDBConfig(&cfg.Database))
	if err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	ctx := context.Background()
	if cmd == "status" {
		m := middleware.ReadMaintenance(db)
		if !m.Enabled {
			fmt.Fprintln(stdout, "maintenance: off")
		} else {
			fmt.Fprintf(stdout, "maintenance: on since %s by %s, jobs held: %t\n", m.Since.Format(time.RFC3339), m.By, m.HoldJobs)
		}
		return printRunning(ctx, stdout, stderr, db)
	}

	// An insert-only queue client: it only pauses and resumes the queues the
	// running panel works.
	pool, err := pgxpool.New(ctx, databaseURL(cfg.Database))
	if err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	defer pool.Close()
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	if _, err := service.SetMaintenance(ctx, db, rc, cmd == "on", message, hold, "command line"); err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	if cmd == "off" {
		fmt.Fprintln(stdout, "maintenance: off; held jobs released")
		return 0
	}
	fmt.Fprintf(stdout, "maintenance: on, jobs held: %t\n", hold)
	if !hold || wait == 0 {
		return 0
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		jobs, err := service.RunningJobs(ctx, db)
		if err != nil {
			fmt.Fprintln(stderr, "maintenance: could not read the job queue:", err)
			return 1
		}
		if len(jobs) == 0 {
			fmt.Fprintln(stdout, "maintenance: no job is running")
			return 0
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "maintenance: %d job(s) still running after %d seconds:\n", len(jobs), wait)
			for _, j := range jobs {
				fmt.Fprintf(stderr, "  job %d %s %s (running since %s)\n", j.ID, j.Kind, j.Subject, j.AttemptAt.Format(time.RFC3339))
			}
			return 3
		}
		time.Sleep(maintenancePoll)
	}
}

func printRunning(ctx context.Context, stdout, stderr io.Writer, db *gorm.DB) int {
	jobs, err := service.RunningJobs(ctx, db)
	if err != nil {
		fmt.Fprintln(stderr, "maintenance: could not read the job queue:", err)
		return 1
	}
	fmt.Fprintf(stdout, "running jobs: %d\n", len(jobs))
	for _, j := range jobs {
		fmt.Fprintf(stdout, "  job %d %s %s (running since %s)\n", j.ID, j.Kind, j.Subject, j.AttemptAt.Format(time.RFC3339))
	}
	return 0
}
