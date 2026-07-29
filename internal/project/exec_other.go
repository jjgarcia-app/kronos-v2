//go:build !windows

package project

import "os/exec"

// hideWindow es un no-op fuera de Windows — el problema que resuelve
// (consola visible al lanzar un binario de consola sin una propia) es
// específico de Windows.
func hideWindow(cmd *exec.Cmd) {}
