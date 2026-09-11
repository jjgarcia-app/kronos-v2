package store

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// TestDualStore_MarkDown_LogsUnderlyingError es la regresión del bug de
// observabilidad: antes, markDown() solo imprimía "primary caído" sin el
// error real (timeout, breaker, auth, TLS, connection reset, etc.), lo que
// hacía imposible diagnosticar por qué el daemon parpadeaba entre Postgres y
// el buffer local. Cerramos el primary para forzar un error real de
// escritura y verificamos que ese error termina en el log, no solo el
// mensaje genérico.
func TestDualStore_MarkDown_LogsUnderlyingError(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	ds.primary.Close() // cualquier operación siguiente sobre primary falla con un error real

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	_, saveErr := ds.SaveObservation(ctx, SaveParams{
		Type:    TypeDiscovery,
		Title:   "cae a buffer",
		Content: "primary cerrado a propósito",
		Project: "p",
	})

	w.Close()
	os.Stderr = origStderr
	logged, _ := io.ReadAll(r)
	out := string(logged)

	if saveErr != nil {
		t.Fatalf("SaveObservation debería caer al buffer sin propagar error: %v", saveErr)
	}
	if !strings.Contains(out, "primary caído") {
		t.Fatalf("log no contiene el mensaje de caída: %q", out)
	}
	if !strings.Contains(out, "database is closed") {
		t.Fatalf("log no contiene el error real que provocó la caída (solo el mensaje genérico): %q", out)
	}
}
