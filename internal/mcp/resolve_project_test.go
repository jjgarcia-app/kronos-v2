package mcp_test

import (
	"testing"
)

// TestMemSave_ResolvesProjectFromSessionID reproduce el bug real encontrado
// en vivo el 2026-09-04: un mem_save de un proyecto ATISA quedó archivado
// bajo "kronos-v2" porque el caller no pasó "project" ni "directory" —
// resolveProject caía a project.DetectFull("") → os.Getwd() del proceso
// daemon compartido, no de la sesión real que llamaba. Ahora, sin "project"
// ni "directory", debe resolver el proyecto real desde la sesión (GetSession
// vía "session_id").
func TestMemSave_ResolvesProjectFromSessionID(t *testing.T) {
	srv, st := newTestServerWithStore(t)

	if _, err := st.CreateSession(t.Context(), "sess-atisa", "atisa-provider-management-all-in-one", "/repo/atisa"); err != nil {
		t.Fatal(err)
	}

	call(t, srv, "mem_save", map[string]any{
		"session_id": "sess-atisa",
		"title":      "fix real de atisa",
		"content":    "Qué: x\nPor qué: y\nCómo aplicar: z",
		"type":       "bugfix",
	})

	obs, err := st.ListObservations(t.Context(), "atisa-provider-management-all-in-one", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("esperaba 1 observación bajo el proyecto real de la sesión, hay %d", len(obs))
	}

	kronosObs, err := st.ListObservations(t.Context(), "kronos-v2", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range kronosObs {
		if o.Title == "fix real de atisa" {
			t.Fatal("el save quedó mal archivado bajo kronos-v2 en vez del proyecto real de la sesión")
		}
	}
}

// TestMemSave_NoProjectNoDirectoryNoSessionID_FailsLoud confirma que, sin
// ningún dato para resolver el proyecto, mem_save falla con un error claro
// en vez de adivinar contra el cwd del proceso daemon — ese fallback
// silencioso era justo la causa del bug real (mezclaba observaciones entre
// proyectos sin que nadie se enterara).
func TestMemSave_NoProjectNoDirectoryNoSessionID_FailsLoud(t *testing.T) {
	srv := newTestServer(t)

	out := callExpectError(t, srv, "mem_save", map[string]any{
		"title":   "algo",
		"content": "Qué: x\nPor qué: y\nCómo aplicar: z",
		"type":    "bugfix",
	})

	if out == "" {
		t.Error("esperaba un mensaje de error explicando cómo resolver el proyecto")
	}
}

// TestMemSave_ExplicitProjectWins_OverSessionID confirma que "project"
// explícito sigue ganando incluso cuando session_id también está presente —
// session_id es solo un fallback, no debe pisar un valor explícito del caller.
func TestMemSave_ExplicitProjectWins_OverSessionID(t *testing.T) {
	srv, st := newTestServerWithStore(t)

	if _, err := st.CreateSession(t.Context(), "sess-other", "other-project", "/repo/other"); err != nil {
		t.Fatal(err)
	}

	call(t, srv, "mem_save", map[string]any{
		"session_id": "sess-other",
		"project":    "kronos-v2",
		"title":      "explicito gana",
		"content":    "Qué: x\nPor qué: y\nCómo aplicar: z",
		"type":       "bugfix",
	})

	obs, err := st.ListObservations(t.Context(), "kronos-v2", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range obs {
		if o.Title == "explicito gana" {
			found = true
		}
	}
	if !found {
		t.Error("project explícito debería ganarle al fallback por session_id")
	}
}
