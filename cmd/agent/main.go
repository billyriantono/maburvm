package main

import (
	"fmt"
	"log"
	"os"

	"github.com/maburvm/panel/internal/agent/server"
)

func main() {
	log.Println("Starting MaburVM Node Agent...")
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start agent: %v\n", err)
		os.Exit(1)
	}
}
