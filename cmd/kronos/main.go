package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runServe()
	}

	switch args[0] {
	case "init":
		return runInit()
	case "serve", "mcp":
		return runServe(args[1:]...)
	case "hook":
		return runHook(args[1:])
	case "setup":
		return runSetup(args[1:])
	case "export":
		return runExport(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "tui":
		return runTUI()
	case "config":
		return runConfig(args[1:])
	case "sync":
		return runSync(args[1:])
	case "rules":
		return runRules(args[1:])
	case "gc":
		return runGC(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("kronos v2.0.0-dev\n")
		return nil
	default:
		return fmt.Errorf("unknown command %q — use: init | serve | mcp [--tools=PROFILE] | hook | setup | export | doctor | tui | config | sync [--export|--import] | rules | gc | version", args[0])
	}
}
