package main

import (
	"log/slog"
	"os"

	"github.com/maburvm/panel/internal/panel/server"
)

func main() {
	if err := server.Run(); err != nil {
		slog.Default().Error("failed to start server", "error", err)
		os.Exit(1)
	}
}
