package main

import (
	"fmt"
	"log"
	"os"

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
	log.Println("Starting MaburVM Node Agent...")
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start agent: %v\n", err)
		os.Exit(1)
	}
}
