//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS no está en el paquete syscall estándar de Go — es una
// constante fija de la API de Windows (winbase.h).
const detachedProcess = 0x00000008

// spawnDaemonDetached lanza `kronos serve --daemon-mode` como proceso
// independiente que sobrevive a que esta sesión (y su terminal) cierren.
// Validado en el spike de la Fase 1: DETACHED_PROCESS + CREATE_NEW_PROCESS_GROUP,
// sin heredar handles de consola del padre (Stdin/Stdout/Stderr en nil — el
// propio daemon redirige su salida a daemon.log apenas arranca).
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
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}
