package store

import (
	"context"
	"testing"
)

// Este archivo cubre el opt-in de sync por proyecto (SetLocalOnlyProjects):
// un proyecto marcado local-only nunca debe escribirse al primary ni quedar
// encolado para sync, incluso con primary sano — se queda solo en buffer.

func TestDualStore_LocalOnlyProject_NeverReachesPrimary(t *testing.T) {
	ds := newTestDualStore(t)
	ds.SetLocalOnlyProjects([]string{"proyecto-privado"})
	ctx := context.Background()

	sess, err := ds.CreateSession(ctx, "s-local", "proyecto-privado", "/tmp")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	obs, err := ds.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDiscovery, Title: "secreto", Content: "no debe salir de acá", Project: "proyecto-privado",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	if err := ds.RecordToolUse(ctx, sess.ID, "proyecto-privado", "Edit"); err != nil {
		t.Fatalf("RecordToolUse: %v", err)
	}
	if err := ds.SavePrompt(ctx, sess.ID, "proyecto-privado", "prompt privado"); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	if err := ds.EndSession(ctx, sess.ID, "listo"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	// nada de esto debería haber tocado primary, ni quedar en sync_queue —
	// se quedó local desde el vamos, no es "pendiente de sincronizar".
	if ds.PendingCount() != 0 {
		t.Errorf("PendingCount = %d, want 0 — local-only no debería encolarse para sync", ds.PendingCount())
	}
	if got, _ := ds.primary.GetSession(ctx, sess.ID); got != nil {
		t.Error("la sesión local-only no debería existir en primary")
	}
	if got, _ := ds.primary.GetObservation(ctx, obs.ID); got != nil {
		t.Error("la observación local-only no debería existir en primary")
	}

	// pero sí debe estar completa y funcional en el buffer local.
	gotObs, err := ds.buffer.GetObservation(ctx, obs.ID)
	if err != nil || gotObs == nil || gotObs.Content != "no debe salir de acá" {
		t.Errorf("la observación debería existir completa en el buffer: %v, err=%v", gotObs, err)
	}
	gotSess, err := ds.buffer.GetSession(ctx, sess.ID)
	if err != nil || gotSess == nil || gotSess.EndedAt == nil {
		t.Errorf("la sesión debería existir y estar cerrada en el buffer: %v, err=%v", gotSess, err)
	}
}

func TestDualStore_NonLocalOnlyProject_StillGoesToPrimary(t *testing.T) {
	ds := newTestDualStore(t)
	ds.SetLocalOnlyProjects([]string{"proyecto-privado"})
	ctx := context.Background()

	sess, err := ds.CreateSession(ctx, "s-normal", "otro-proyecto", "/tmp")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got, _ := ds.primary.GetSession(ctx, sess.ID); got == nil {
		t.Error("un proyecto no marcado local-only debería escribirse normalmente a primary")
	}
}

func TestDualStore_SetLocalOnlyProjects_NormalizesNames(t *testing.T) {
	ds := newTestDualStore(t)
	ds.SetLocalOnlyProjects([]string{"Proyecto Privado"})
	if !ds.isLocalOnly("proyecto-privado") {
		t.Error("isLocalOnly debería matchear tras normalizar (mayúsculas/espacios)")
	}
	if !ds.isLocalOnly("PROYECTO PRIVADO") {
		t.Error("isLocalOnly debería normalizar también el nombre consultado")
	}
}
