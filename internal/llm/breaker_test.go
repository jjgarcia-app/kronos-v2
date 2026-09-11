package llm_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

func TestBreaker_AllowsUntilThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")
	b := llm.NewBreaker(path, 3, time.Minute)

	if !b.Allow() {
		t.Fatal("debería permitir la primera llamada")
	}
	b.RecordFailure(errors.New("timeout 1"))
	if !b.Allow() {
		t.Fatal("1 fallo no debería abrir el breaker (threshold=3)")
	}
	b.RecordFailure(errors.New("timeout 2"))
	if !b.Allow() {
		t.Fatal("2 fallos no deberían abrir el breaker (threshold=3)")
	}
	b.RecordFailure(errors.New("timeout 3"))
	if b.Allow() {
		t.Fatal("3 fallos consecutivos deberían abrir el breaker")
	}
}

func TestBreaker_ReopensAfterMinutesElapsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")
	b := llm.NewBreaker(path, 1, 10*time.Millisecond)

	b.RecordFailure(errors.New("timeout"))
	if b.Allow() {
		t.Fatal("debería estar abierto justo después del fallo")
	}
	time.Sleep(30 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("debería permitir de nuevo tras pasar openFor")
	}
}

func TestBreaker_SuccessClosesAndResetsCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")
	b := llm.NewBreaker(path, 2, time.Minute)

	b.RecordFailure(errors.New("timeout 1"))
	b.RecordSuccess()
	// tras el éxito, hacen falta otra vez 2 fallos consecutivos para abrir —
	// si el contador no se hubiera reseteado, un solo fallo más alcanzaría.
	b.RecordFailure(errors.New("timeout 2"))
	if !b.Allow() {
		t.Fatal("un solo fallo tras un éxito no debería abrir el breaker (contador reseteado)")
	}
}

func TestBreaker_CorruptStateFile_StartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")
	if err := os.WriteFile(path, []byte("{ esto no es json válido"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := llm.NewBreaker(path, 3, time.Minute)
	if !b.Allow() {
		t.Fatal("JSON corrupto debería tratarse como estado vacío (permitir), no como fallo")
	}
}

func TestBreaker_MissingStateFile_Allows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-existe", "breaker.json")
	b := llm.NewBreaker(path, 3, time.Minute)
	if !b.Allow() {
		t.Fatal("sin archivo de estado debería permitir (primera vez)")
	}
}

func TestBreaker_RecordFailure_PersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")
	// dos instancias apuntando al mismo archivo simulan dos procesos
	// distintos (daemon + hook) compartiendo el cortacircuitos.
	writer := llm.NewBreaker(path, 2, time.Minute)
	reader := llm.NewBreaker(path, 2, time.Minute)

	writer.RecordFailure(errors.New("timeout 1"))
	writer.RecordFailure(errors.New("timeout 2"))

	if reader.Allow() {
		t.Fatal("una segunda instancia sobre el mismo archivo debería ver el breaker abierto")
	}
}

func TestBreaker_RecordSuccess_NoPriorState_DoesNotWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaker.json")
	b := llm.NewBreaker(path, 3, time.Minute)
	b.RecordSuccess()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("RecordSuccess sin fallos previos no debería crear el archivo de estado")
	}
}
