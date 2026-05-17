package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// runHook dispatches a named hook.
//
// Usage:
//
//	kronos hook <name> [--reason <reason>]
//	kronos hook <name> <reason>
//
// For session-start, reason="compact" triggers post-compaction recovery.
// Reason "startup", "clear", or empty all trigger the normal session start.
func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: kronos hook <session-start|prompt-submit|subagent-stop|session-stop> [--reason compact]")
	}

	hookName := args[0]
	reason := parseReason(args[1:])

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	return hooks.RunWithReason(context.Background(), hookName, reason, st)
}

// parseReason extracts the reason value from remaining args.
// Accepts --reason <value> or a bare positional argument.
func parseReason(args []string) string {
	for i, arg := range args {
		if arg == "--reason" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(arg, "--reason=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, "--reason="))
		}
		// bare positional argument that is not a flag
		if !strings.HasPrefix(arg, "-") {
			return strings.TrimSpace(arg)
		}
	}
	return ""
}
