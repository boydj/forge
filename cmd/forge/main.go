// Command forge is the forge daemon and administration tool.
//
//	forge serve  --config /etc/forge/forge.toml
//	forge admin  <subcommand> ...
//	forge hook   <pre-receive|update|post-receive>
//	forge version
package main

import (
	"fmt"
	"os"

	"as215520.net/forge/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "admin":
		err = runAdmin(os.Args[2:])
	case "hook":
		err = runHook(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("forge", version.Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "forge:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: forge <command> [flags]

  serve    run the Gemini/Titan and Git-over-SSH daemon
  admin    administer users, repositories, keys, certificates, nodes
  hook     git server-side hook entry point (invoked by git)
  version  print the version
`)
}
