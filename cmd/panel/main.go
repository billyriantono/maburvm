package main

import (
	"log/slog"
	"os"

	"github.com/maburvm/panel/internal/panel/server"
)

func main() {
	// "panel migrate status" etc., through the image's entrypoint so the same
	// secrets and data directory apply (docs/DEPLOYMENT.md, Upgrading the panel).
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(server.MigrateCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
	if err := server.Run(); err != nil {
		slog.Default().Error("failed to start server", "error", err)
		os.Exit(1)
	}
}
