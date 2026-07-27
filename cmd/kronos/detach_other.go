//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// spawnDaemonDetached lanza `kronos serve --daemon-mode` como proceso
// independiente que sobrevive a que esta sesión (y su terminal) cierren.
// Setsid crea una nueva sesión sin terminal de control — el equivalente
// Unix del DETACHED_PROCESS de Windows (ver detach_windows.go).
func spawnDaemonDetached(port int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve", "--daemon-mode", fmt.Sprintf("--port=%d", port))
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	return cmd.Start()
}
