package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/preflight"
	"github.com/maburvm/panel/internal/agent/server"
	"github.com/maburvm/panel/internal/shared/buildinfo"
)

func main() {
	// Used by self-update to prove a new binary runs on this host before it is
	// swapped in, and handy over SSH.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		bi := buildinfo.Get()
		fmt.Printf("maburvm-agent %s %s %s\n", bi.Version, bi.Commit, bi.BuildTime)
		return
	}
	// The installer runs this at the end; it changes nothing on the host.
	if len(os.Args) > 1 && os.Args[1] == "--preflight" {
		os.Exit(runPreflight(os.Args[2:], os.Stdout))
	}
	// sshd runs this for another node's migration key (its forced command).
	if len(os.Args) > 1 && os.Args[1] == migration.ShellCommand {
		os.Exit(server.RunMigrationShell(os.Getenv("SSH_ORIGINAL_COMMAND"), os.Stdout, os.Stderr))
	}
	log.Println("Starting MaburVM Node Agent...")
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start agent: %v\n", err)
		os.Exit(1)
	}
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// runPreflight prints the host checks and exits 1 when one fails.
func runPreflight(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	var pools, bridges listFlag
	fs.Var(&pools, "pool", "storage pool directory to check (repeatable; default: the default image directory)")
	fs.Var(&bridges, "bridge", "bridge VMs attach to (repeatable)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	checks := server.Preflight(context.Background(), pools, bridges)
	if *asJSON {
		_ = json.NewEncoder(w).Encode(checks)
	} else {
		for _, c := range checks {
			fmt.Fprintf(w, "%-5s %-28s %s\n", c.Status, c.Name, c.Detail)
			if c.Fix != "" {
				fmt.Fprintf(w, "      %-28s Fix: %s\n", "", c.Fix)
			}
		}
	}
	if preflight.Worst(checks) == preflight.Fail {
		return 1
	}
	return 0
}
