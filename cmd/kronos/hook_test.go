package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHooksDisabled cubre los valores aceptados de KRONOS_DISABLE
// (case-insensitive) y confirma que cualquier otro valor, incluida la
// variable ausente, deja el comportamiento por defecto (hooks activos).
func TestHooksDisabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"algo-random", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"YES", true},
		{"  1  ", true},
	}
	for _, tc := range cases {
		t.Setenv("KRONOS_DISABLE", tc.value)
		if got := hooksDisabled(); got != tc.want {
			t.Errorf("hooksDisabled() con KRONOS_DISABLE=%q = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestRunHook_KronosDisable confirma que con KRONOS_DISABLE activo runHook
// sale con nil (equivalente a exit 0) sin llegar a resolver rutas de datos
// ni abrir el store — ni siquiera para un hookName válido como
// "prompt-submit" o "session-start", que normalmente sí tocarían disco.
func TestRunHook_KronosDisable(t *testing.T) {
	t.Setenv("KRONOS_DISABLE", "1")

	for _, hookName := range []string{"prompt-submit", "session-start", "pre-compact", "no-existe"} {
		if err := runHook([]string{hookName}); err != nil {
			t.Errorf("runHook([%q]) con KRONOS_DISABLE=1 = %v, want nil", hookName, err)
		}
	}

	if err := runHook(nil); err != nil {
		t.Errorf("runHook(nil) con KRONOS_DISABLE=1 = %v, want nil", err)
	}
}

// TestTryDaemonPromptSubmit_Success confirma el camino feliz: el daemon
// responde 200 y su body se copia tal cual al writer de salida.
func TestTryDaemonPromptSubmit_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[kronos] resultado de prueba\n"))
	}))
	defer ts.Close()

	var out bytes.Buffer
	ok := tryDaemonPromptSubmit(ts.URL, []byte(`{"prompt":"test"}`), &out)
	if !ok {
		t.Fatal("tryDaemonPromptSubmit debería devolver true con daemon respondiendo 200")
	}
	if out.String() != "[kronos] resultado de prueba\n" {
		t.Errorf("output = %q, want el body de la respuesta tal cual", out.String())
	}
}

// TestTryDaemonPromptSubmit_DaemonDown confirma el fallback: si no hay nada
// escuchando en la URL, debe devolver false sin escribir nada — el caller
// (runPromptSubmitHook) cae al camino local en este caso.
func TestTryDaemonPromptSubmit_DaemonDown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := ts.URL
	ts.Close() // puerto liberado, nada escuchando ahí ahora

	var out bytes.Buffer
	ok := tryDaemonPromptSubmit(deadURL, []byte(`{"prompt":"test"}`), &out)
	if ok {
		t.Error("tryDaemonPromptSubmit debería devolver false si el daemon no responde")
	}
	if out.Len() != 0 {
		t.Errorf("no debería escribir nada en el writer si falló: %q", out.String())
	}
}

// TestTryDaemonPromptSubmit_NonOKStatus confirma que un status distinto de
// 200 (ej. 500 si el handler del daemon tuvo un error real) también cae al
// fallback, en vez de propagar una respuesta de error como si fuera válida.
func TestTryDaemonPromptSubmit_NonOKStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error interno"))
	}))
	defer ts.Close()

	var out bytes.Buffer
	ok := tryDaemonPromptSubmit(ts.URL, []byte(`{"prompt":"test"}`), &out)
	if ok {
		t.Error("tryDaemonPromptSubmit debería devolver false ante status != 200")
	}
}
