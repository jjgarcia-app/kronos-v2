package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
)

// TestGetCaptureLLM_RetriesWhenCachedClientIsNil reproduce el bug real
// encontrado en producción: si SetCaptureLLM se llamó con c=nil (Ollama no
// respondió al ping del arranque del daemon — ej. Docker Desktop todavía
// inicializando), getCaptureLLM debe reintentar construir el cliente en la
// siguiente llamada, no quedarse en nil para siempre mientras el daemon
// viva.
func TestGetCaptureLLM_RetriesWhenCachedClientIsNil(t *testing.T) {
	pings := 0
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings++
		w.WriteHeader(http.StatusOK)
	}))
	defer ollama.Close()

	srv, _ := newTestServer(t, "")
	var cfg config.Config
	cfg.Embeddings.OllamaURL = ollama.URL
	srv.SetCaptureLLM(nil, cfg) // simula: el ping falló al arrancar el daemon

	c := srv.getCaptureLLM(context.Background())
	if c == nil {
		t.Fatal("getCaptureLLM debería haber reintentado y conseguido un cliente sano")
	}
	if pings == 0 {
		t.Error("getCaptureLLM no reintentó el ping a Ollama")
	}
}

// TestGetCaptureLLM_CachesOnceHealthy confirma que, una vez que el
// reintento funciona, no se vuelve a pingear en cada llamada — se cachea,
// mismo comportamiento que ya existía para el caso sano.
func TestGetCaptureLLM_CachesOnceHealthy(t *testing.T) {
	pings := 0
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings++
		w.WriteHeader(http.StatusOK)
	}))
	defer ollama.Close()

	srv, _ := newTestServer(t, "")
	var cfg config.Config
	cfg.Embeddings.OllamaURL = ollama.URL
	srv.SetCaptureLLM(nil, cfg)

	first := srv.getCaptureLLM(context.Background())
	second := srv.getCaptureLLM(context.Background())
	if first == nil || second == nil {
		t.Fatal("esperaba un cliente sano en ambas llamadas")
	}
	if pings != 1 {
		t.Errorf("pings = %d, want 1 (la segunda llamada debería reusar el cliente cacheado, no volver a pingear)", pings)
	}
}

// TestGetCaptureLLM_StaysNilWhenOllamaUnreachable confirma que, sin
// Ollama disponible, cada intento sigue devolviendo nil sin pánico ni
// bloqueo — el fail-open de siempre, solo que ahora reintentado.
func TestGetCaptureLLM_StaysNilWhenOllamaUnreachable(t *testing.T) {
	srv, _ := newTestServer(t, "")
	var cfg config.Config
	cfg.Embeddings.OllamaURL = "http://127.0.0.1:1" // puerto reservado, rechaza conexión al instante
	srv.SetCaptureLLM(nil, cfg)

	if c := srv.getCaptureLLM(context.Background()); c != nil {
		t.Error("esperaba nil con Ollama inalcanzable")
	}
}
