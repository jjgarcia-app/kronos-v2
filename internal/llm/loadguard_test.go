package llm

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// countingBackend cuenta cuántas veces se llamó a generate — usado para
// verificar que el guardián de carga corta ANTES de llegar al backend, no
// solo que el resultado final sea un error. Implementa consumesLocalCPU
// (true) para representar al backend local (Ollama).
type countingBackend struct {
	calls int
}

func (b *countingBackend) generate(ctx context.Context, prompt string, numPredict int) (string, error) {
	b.calls++
	return `{"content":"ok"}`, nil
}

func (b *countingBackend) consumesLocalCPU() bool { return true }

// remoteBackend representa a un backend que genera FUERA de esta máquina
// (claude-cli): no implementa consumesLocalCPU a propósito.
type remoteBackend struct {
	calls int
}

func (b *remoteBackend) generate(ctx context.Context, prompt string, numPredict int) (string, error) {
	b.calls++
	return `{"found":false}`, nil
}

// TestExtractFinding_LoadGuard_DoesNotApplyToRemoteBackend es el caso real
// medido: con 8 agentes corriendo el load de esta máquina ronda 6-10, así que
// un guardián aplicado también a claude-cli saltearía SIEMPRE y la captura
// automática quedaría muerta. El guardián existe para proteger CPU local
// (llama-server girando al 212% por 13 minutos), no para castigar la nube.
func TestExtractFinding_LoadGuard_DoesNotApplyToRemoteBackend(t *testing.T) {
	if _, err := readLoadAvg1(); err != nil {
		t.Skip("no se pudo leer /proc/loadavg en este sistema — el guardián de carga no aplica")
	}
	backend := &remoteBackend{}
	c := &Client{backend: backend, maxLoadPerCPU: 0.0001} // umbral imposible

	if _, err := c.ExtractFinding(context.Background(), "excerpt de sobra para pasar minExcerptChars, bien largo"); err != nil {
		t.Fatalf("un backend remoto no debería saltearse por carga local: %v", err)
	}
	if backend.calls != 1 {
		t.Fatalf("esperaba que el backend remoto se llamara 1 vez, calls=%d", backend.calls)
	}
}

func TestExtractFinding_LoadGuard_SkipsWhenOverThreshold(t *testing.T) {
	if _, err := readLoadAvg1(); err != nil {
		t.Skip("no se pudo leer /proc/loadavg en este sistema — el guardián de carga no aplica")
	}
	backend := &countingBackend{}
	// 0.0001 es un umbral imposible de no superar en cualquier máquina real
	// con al menos una tarea corriendo — garantiza que el guardián dispare
	// sin depender de la carga real de la máquina que corre el test.
	c := &Client{backend: backend, maxLoadPerCPU: 0.0001}

	_, err := c.ExtractFinding(context.Background(), "excerpt de sobra para pasar minExcerptChars, bien largo")
	if err != errLoadTooHigh {
		t.Fatalf("esperaba errLoadTooHigh, got %v", err)
	}
	if backend.calls != 0 {
		t.Fatalf("el backend no debería haberse invocado con el guardián activo, calls=%d", backend.calls)
	}
}

func TestExtractFinding_LoadGuard_DisabledAllowsCall(t *testing.T) {
	backend := &countingBackend{}
	c := &Client{backend: backend, maxLoadPerCPU: 0} // 0 desactiva el guardián

	if _, err := c.ExtractFinding(context.Background(), "excerpt"); err != nil {
		t.Fatalf("ExtractFinding: %v", err)
	}
	if backend.calls != 1 {
		t.Fatalf("esperaba 1 llamada al backend con el guardián desactivado, calls=%d", backend.calls)
	}
}

func TestExtractFinding_LoadGuard_DoesNotCountAsBreakerFailure(t *testing.T) {
	if _, err := readLoadAvg1(); err != nil {
		t.Skip("no se pudo leer /proc/loadavg en este sistema — el guardián de carga no aplica")
	}
	backend := &countingBackend{}
	breaker := NewBreaker(filepath.Join(t.TempDir(), "breaker.json"), 1, time.Minute)
	c := &Client{backend: backend, maxLoadPerCPU: 0.0001, breaker: breaker}

	if _, err := c.ExtractFinding(context.Background(), "excerpt"); err != errLoadTooHigh {
		t.Fatalf("esperaba errLoadTooHigh, got %v", err)
	}
	if !breaker.Allow() {
		t.Fatal("un salteo por carga alta no debería contar como fallo del cortacircuitos")
	}
}

func TestLoadOverThreshold_ZeroDisablesGuard(t *testing.T) {
	over, _, _ := loadOverThreshold(0)
	if over {
		t.Fatal("maxLoadPerCPU=0 debería desactivar el guardián incondicionalmente")
	}
}

func TestLoadGuardStatus_ZeroReportsUnavailable(t *testing.T) {
	_, _, _, available := LoadGuardStatus(0)
	if available {
		t.Fatal("maxLoadPerCPU=0 debería reportarse como guardián desactivado (available=false)")
	}
}
