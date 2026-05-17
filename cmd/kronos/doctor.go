package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
)

func runDoctor(args []string) error {
	cfg, _ := config.Load()

	// --fix <name>: aplicar fix para un check específico
	if len(args) >= 2 && args[0] == "--fix" {
		checkName := args[1]
		fixCtx, fixCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer fixCancel()
		progress := make(chan string, 32)
		var fixErr error
		go func() {
			fixErr = doctor.Fix(fixCtx, cfg, checkName, progress)
		}()
		fmt.Printf("Aplicando fix para %q...\n", checkName)
		for line := range progress {
			fmt.Println(" ", line)
		}
		return fixErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report := doctor.Run(ctx, cfg)

	for _, check := range report.Checks {
		icon := "OK"
		switch check.Status {
		case doctor.StatusWarn:
			icon = "!!"
		case doctor.StatusFail:
			icon = "XX"
		}
		fmt.Printf("[%s] %-22s %s\n", icon, check.Name+":", check.Detail)
		if check.FixAvailable {
			fmt.Printf("        fix: %s  (kronos doctor --fix %q)\n", check.FixLabel, check.Name)
		}
	}
	return nil
}
