//go:build windows

package project

import (
	"os/exec"
	"syscall"
)

// hideWindow evita que Windows abra una consola nueva y visible cuando
// gitCmd lanza git.exe: git.exe es un binario de subsistema consola, y sin
// este flag Windows le crea una consola propia aunque el proceso padre
// (kronos.exe, subsistema GUI) no tenga una — eso es lo que causaba el
// parpadeo de ventana en cada envío de prompt (project.Detect corre en
// prompt_submit.go en cada uno, y siempre intenta "git remote get-url
// origin" primero).
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
