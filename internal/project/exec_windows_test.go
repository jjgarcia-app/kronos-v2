//go:build windows

package project

import (
	"os/exec"
	"testing"
)

// TestHideWindow_SetsHideWindowFlag confirma que hideWindow deja el cmd
// listo para no abrir consola — la mitad que se puede verificar sin
// depender de percepción visual humana (lo otro, que Windows realmente no
// muestre la ventana, se verificó por evidencia externa: grabación de
// pantalla mostrando el flash de "git.exe" antes del fix).
func TestHideWindow_SetsHideWindowFlag(t *testing.T) {
	cmd := exec.Command("git", "--version")
	hideWindow(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("hideWindow no seteó SysProcAttr")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("hideWindow no seteó HideWindow=true")
	}
}
