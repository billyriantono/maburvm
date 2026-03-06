package main

import (
	"fmt"
	"log"
	"os"

	"github.com/maburvm/panel/internal/panel/server"
)

func main() {
	log.Println("Starting MaburVM Panel API Server...")
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}
}
