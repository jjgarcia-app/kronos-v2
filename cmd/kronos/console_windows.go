//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// AttachConsole no está expuesta en golang.org/x/sys/windows (a diferencia
// de GetStdHandle) — se declara a mano vía syscall, mismo patrón que
// detach_windows.go ya usa para CreationFlags que tampoco están en el
// paquete estándar.
var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
)

// attachParentProcess es el valor mágico ATTACH_PARENT_PROCESS de la API de
// Windows (winbase.h) — no es una constante expuesta por x/sys/windows.
const attachParentProcess = 0xFFFFFFFF

func attachConsole(processID uint32) error {
	r, _, err := procAttachConsole.Call(uintptr(processID))
	if r == 0 {
		return err
	}
	return nil
}

// attachConsoleIfInteractive se corre al arrancar en Windows. kronos.exe se
// compila como subsistema GUI (-H windowsgui, ver .goreleaser.yml) para que
// Windows no le asigne una consola nueva automáticamente — eso era lo que
// causaba el parpadeo de ventana que Jerry vio: Git Bash lanza binarios
// nativos de consola y Windows les crea una consola real y visible aunque
// stdout esté redirigido a un pipe (los hooks SIEMPRE van piped — Claude
// Code necesita leer su salida; kronos mcp también, para el transporte MCP
// por stdio).
//
// Sin consola propia, un uso interactivo real (alguien tipeando
// "kronos doctor" en una terminal) se quedaría sin salida visible — un
// proceso subsistema GUI no hereda handles de consola automáticamente al
// lanzarse sin redirección. Por eso: si stdout/stdin YA son handles válidos
// (pipe redirigido — el caso de los hooks y de kronos mcp, CRÍTICO no
// tocar), no hacemos nada. Si NO hay ningún handle válido (el caso
// interactivo, sin redirección), pedimos prestada la consola del proceso
// padre y recién ahí redirigimos stdout/stderr/stdin — así el uso manual
// sigue funcionando igual que con un binario de consola normal.
func attachConsoleIfInteractive() {
	if hasValidStdHandle(windows.STD_OUTPUT_HANDLE) || hasValidStdHandle(windows.STD_INPUT_HANDLE) {
		return // ya hay E/S real (pipe heredado) — no tocar nada
	}
	if err := attachConsole(attachParentProcess); err != nil {
		return // no hay consola del padre a la que pegarse (lanzado detached) — seguimos sin consola, ok
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stderr = f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
}

func hasValidStdHandle(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	return err == nil && h != windows.InvalidHandle && h != 0
}
