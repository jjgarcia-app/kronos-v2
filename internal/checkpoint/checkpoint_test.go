package checkpoint_test

import (
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
)

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := checkpoint.State{
		Task:     "Implementar X",
		Progress: "mitad hecho",
		NextStep: "terminar Y",
		Files:    "a.go, b.go",
		Notes:    "cuidado con Z",
		Project:  "kronos-v2",
	}
	if err := checkpoint.Save(dir, "kronos-v2", s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := checkpoint.Load(dir, "kronos-v2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned nil after Save")
	}
	if got.Task != s.Task || got.NextStep != s.NextStep || got.Files != s.Files || got.Notes != s.Notes {
		t.Errorf("Load = %+v, want fields matching %+v", got, s)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by Save")
	}
}

func TestLoad_NonexistentReturnsNilNoError(t *testing.T) {
	dir := t.TempDir()
	got, err := checkpoint.Load(dir, "no-existe")
	if err != nil {
		t.Fatalf("Load nonexistent: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("Load nonexistent = %+v, want nil", got)
	}
}

func TestSave_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	checkpoint.Save(dir, "p", checkpoint.State{Task: "v1", NextStep: "n1"})
	checkpoint.Save(dir, "p", checkpoint.State{Task: "v2", NextStep: "n2"})

	got, err := checkpoint.Load(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "v2" {
		t.Errorf("Task = %q, want %q (overwrite ganó)", got.Task, "v2")
	}
}

func TestClear_RemovesCheckpoint(t *testing.T) {
	dir := t.TempDir()
	checkpoint.Save(dir, "p", checkpoint.State{Task: "t", NextStep: "n"})

	if err := checkpoint.Clear(dir, "p"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	got, err := checkpoint.Load(dir, "p")
	if err != nil {
		t.Fatalf("Load after Clear: %v", err)
	}
	if got != nil {
		t.Errorf("Load after Clear = %+v, want nil", got)
	}
}

func TestClear_NonexistentIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := checkpoint.Clear(dir, "nunca-existio"); err != nil {
		t.Errorf("Clear on nonexistent checkpoint should not error, got: %v", err)
	}
}

// TestSave_DifferentProjectsDoNotCollide verifica que el sanitizado de
// nombre de archivo no haga que dos proyectos distintos peguen al mismo
// archivo.
func TestSave_DifferentProjectsDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	checkpoint.Save(dir, "proyecto-a", checkpoint.State{Task: "tarea A", NextStep: "n"})
	checkpoint.Save(dir, "proyecto-b", checkpoint.State{Task: "tarea B", NextStep: "n"})

	gotA, err := checkpoint.Load(dir, "proyecto-a")
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := checkpoint.Load(dir, "proyecto-b")
	if err != nil {
		t.Fatal(err)
	}
	if gotA == nil || gotA.Task != "tarea A" {
		t.Errorf("proyecto-a = %+v, want tarea A", gotA)
	}
	if gotB == nil || gotB.Task != "tarea B" {
		t.Errorf("proyecto-b = %+v, want tarea B", gotB)
	}
}

// TestSave_SanitizesProjectNameWithSpecialChars verifica que nombres de
// proyecto con separadores de path (ej. rutas de worktree) no revienten
// el nombre de archivo del checkpoint.
func TestSave_SanitizesProjectNameWithSpecialChars(t *testing.T) {
	dir := t.TempDir()
	project := `org/repo:branch name\sub`
	if err := checkpoint.Save(dir, project, checkpoint.State{Task: "t", NextStep: "n"}); err != nil {
		t.Fatalf("Save con caracteres especiales: %v", err)
	}
	got, err := checkpoint.Load(dir, project)
	if err != nil {
		t.Fatalf("Load con caracteres especiales: %v", err)
	}
	if got == nil || got.Task != "t" {
		t.Errorf("Load = %+v, want Task=t", got)
	}
}
