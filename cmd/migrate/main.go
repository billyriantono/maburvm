// Command migrate applies, lists and reverts the panel's SQL migrations. The
// panel image runs the same code as "panel migrate".
//
//	migrate              apply pending migrations
//	migrate status       list them
//	migrate down N --yes revert the N most recently applied
package main

import (
	"os"

	"github.com/maburvm/panel/internal/panel/server"
)

func main() {
	os.Exit(server.MigrateCommand(os.Args[1:], os.Stdout, os.Stderr))
}
