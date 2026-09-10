package main

import (
	"errors"
	"os"

	"as215520.net/forge/internal/hooks"
)

// runHook is the entry point for git server-side hooks: git-receive-pack
// executes the scripts in the data directory's hooks/ path, which exec
// `forge hook <name>`. Decisions are made by the daemon over its socket.
func runHook(args []string) error {
	if len(args) == 0 {
		return errors.New("hook name required")
	}
	switch args[0] {
	case "pre-receive", "update", "post-receive":
		if err := hooks.Run(args[0], os.Stdin, os.Stderr); err != nil {
			os.Exit(1)
		}
		return nil
	default:
		return errors.New("unknown hook " + args[0])
	}
}
