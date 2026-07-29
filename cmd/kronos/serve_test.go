package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// restoreStdio guarda os.Stdout/os.Stderr y los restaura al terminar el
// test — redirectLogsToFile los pisa como efecto secundario real (es lo que
// hace en producción). También cierra explícitamente el *os.File al que
// quedaron apuntando: en Windows, TempDir() no puede borrar un archivo con
// un handle todavía abierto (a diferencia de Unix, donde unlink funciona
// igual) — sin este Close explícito, el cleanup de t.TempDir() falla.
func restoreStdio(t *testing.T) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	t.Cleanup(func() {
		redirected := os.Stdout
		os.Stdout = origOut
		os.Stderr = origErr
		if redirected != origOut {
			_ = redirected.Close()
		}
	})
}

// TestRedirectLogsToFile_RotatesWhenOverSize — punto 4 de mejoras
// pendientes: daemon.log no debe crecer sin límite. Si ya pesa más de
// 10MB al arrancar, se corre a .1 en vez de seguir creciendo indefinidamente.
func TestRedirectLogsToFile_RotatesWhenOverSize(t *testing.T) {
	dir := t.TempDir()
	restoreStdio(t)
	path := filepath.Join(dir, "daemon.log")

	big := strings.Repeat("x", 11*1024*1024) // 11MB > el umbral de 10MB
	if err := os.WriteFile(path, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}

	if err := redirectLogsToFile(path); err != nil {
		t.Fatalf("redirectLogsToFile: %v", err)
	}

	rotated := path + ".1"
	info, err := os.Stat(rotated)
	if err != nil {
		t.Fatalf("esperaba que %s existiera tras la rotación: %v", rotated, err)
	}
	if info.Size() != int64(len(big)) {
		t.Errorf("el archivo rotado debería tener el contenido viejo completo, size = %d, want %d", info.Size(), len(big))
	}

	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("esperaba que se creara un %s nuevo: %v", path, err)
	}
	if newInfo.Size() != 0 {
		t.Errorf("el archivo nuevo debería empezar vacío, size = %d", newInfo.Size())
	}
}

// TestRedirectLogsToFile_NoRotateWhenUnderSize confirma que un log chico no
// se toca — sigue creciendo por append normal, sin rotación innecesaria.
func TestRedirectLogsToFile_NoRotateWhenUnderSize(t *testing.T) {
	dir := t.TempDir()
	restoreStdio(t)
	path := filepath.Join(dir, "daemon.log")

	small := "contenido chico, muy por debajo del umbral"
	if err := os.WriteFile(path, []byte(small), 0644); err != nil {
		t.Fatal(err)
	}

	if err := redirectLogsToFile(path); err != nil {
		t.Fatalf("redirectLogsToFile: %v", err)
	}

	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("no debería haberse creado un .1 con un log por debajo del umbral")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != small {
		t.Errorf("el contenido original debería preservarse (append, no truncate), got: %q", data)
	}
}

// TestRedirectLogsToFile_CreatesWhenMissing confirma que un primer arranque
// (sin daemon.log todavía) no falla.
func TestRedirectLogsToFile_CreatesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	restoreStdio(t)
	path := filepath.Join(dir, "daemon.log")

	if err := redirectLogsToFile(path); err != nil {
		t.Fatalf("redirectLogsToFile con archivo inexistente no debería fallar: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("esperaba que se creara %s: %v", path, err)
	}
}
