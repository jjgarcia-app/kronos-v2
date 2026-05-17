package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
)

func runConfig(args []string) error {
	if len(args) == 0 {
		return runConfigShow()
	}

	switch args[0] {
	case "show":
		return runConfigShow()
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("uso: config set <clave> <valor>  (ej: config set db.backend sqlite)")
		}
		return runConfigSet(args[1], args[2])
	case "path":
		return runConfigPath()
	default:
		return fmt.Errorf("subcomando desconocido %q — usa: show | set <key> <value> | path", args[0])
	}
}

func runConfigShow() error {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "advertencia: %v\n", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func runConfigSet(key, value string) error {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "advertencia cargando config: %v\n", err)
		cfg = config.Default()
	}
	if err := cfg.Set(key, value); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("guardar config: %w", err)
	}
	fmt.Printf("ok: %s = %s\n", key, value)
	return nil
}

func runConfigPath() error {
	path, err := config.ConfigPath()
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}
