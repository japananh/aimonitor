// Command aimonitor is the CLI entry point. It dispatches to subcommands
// (add, list, switch, status, config, probe, log, daemon, doctor,
// uninstall) defined in internal/cli.
package main

import (
	"fmt"
	"os"

	"github.com/japananh/aimonitor/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
