package main

import (
	"testing"

	"github.com/kardianos/service"
)

func TestServiceStatusString(t *testing.T) {
	cases := []struct {
		status service.Status
		want   string
	}{
		{service.StatusRunning, "corriendo"},
		{service.StatusStopped, "detenido"},
		{service.StatusUnknown, "desconocido"},
	}
	for _, c := range cases {
		if got := serviceStatusString(c.status); got != c.want {
			t.Errorf("serviceStatusString(%v) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestRunServiceCmd_NoArgs_ReturnsUsageError(t *testing.T) {
	err := runServiceCmd(nil)
	if err == nil {
		t.Fatal("esperaba error de uso sin argumentos")
	}
}

func TestRunServiceCmd_UnknownSubcommand_ReturnsError(t *testing.T) {
	err := runServiceCmd([]string{"vuela"})
	if err == nil {
		t.Fatal("esperaba error con un subcomando desconocido")
	}
}

// TestKronosProgram_StopWithoutStart_DoesNotPanic confirma que Stop() es
// seguro de llamar incluso si Start() nunca corrió (p.stop == nil) — el SCM
// puede en teoría invocar Stop antes de que Start haya inicializado el
// channel, dependiendo del timing del SO.
func TestKronosProgram_StopWithoutStart_DoesNotPanic(t *testing.T) {
	p := &kronosProgram{}
	if err := p.Stop(nil); err != nil {
		t.Errorf("Stop() sin Start() previo no debería fallar: %v", err)
	}
}
