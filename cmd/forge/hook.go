package main

import (
	"errors"
)

// runHook is the entry point for git server-side hooks. It is completed
// with the SSH transport (milestone 2): pre-receive enforces ACLs and size
// limits, post-receive records push events.
func runHook(args []string) error {
	if len(args) == 0 {
		return errors.New("hook name required")
	}
	switch args[0] {
	case "pre-receive", "update", "post-receive":
		return nil
	default:
		return errors.New("unknown hook " + args[0])
	}
}
