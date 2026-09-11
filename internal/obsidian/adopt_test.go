package obsidian_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/obsidian"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// findObservation busca, entre las observaciones sembradas, la que tiene el
// título dado — para saber su ID/Project/Type/Content reales al construir
// un archivo en formato viejo.
func findObservation(t *testing.T, st *store.Store, title string) *store.Observation {
	t.Helper()
	all, err := st.ListAll(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	for _, o := range all {
		if o.Title == title {
			return o
		}
	}
	t.Fatalf("no encontré la observación %q", title)
	return nil
}

// oldFormatFile arma el contenido de un archivo en el formato ANTERIOR del
// export (el que --adopt tiene que migrar): frontmatter con id/title/type/
// project/..., cuerpo "# título" + metadata + separador "---" + contenido.
func oldFormatFile(o *store.Observation, content string) string {
	return fmt.Sprintf(`---
id: %d
title: %q
type: %s
project: %s
scope: session
created_at: 2024-01-01
revision: 1
tags: [%s, %s]
---

# %s

**ID**: %d | **Tipo**: %s | **Proyecto**: %s
**Scope**: session
**Creado**: 2024-01-01 | **Rev**: 1

---

%s
`, o.ID, o.Title, o.Type, o.Project, o.Type, o.Project, o.Title, o.ID, o.Type, o.Project, content)
}

// oldFilePath construye el path <outDir>/<project>/<type>/<id>-x.md que
// espera --adopt. El texto del slug no importa para la verificación (solo
// se valida vía frontmatter), así que se usa uno fijo.
func oldFilePath(outDir string, o *store.Observation) string {
	return filepath.Join(outDir, string(o.Project), string(o.Type), fmt.Sprintf("%04d-x.md", o.ID))
}

func writeOldFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAdopt_HappyPath(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	o := findObservation(t, st, "SQLite FTS5 para búsqueda")

	outDir := t.TempDir()
	path := oldFilePath(outDir, o)
	writeOldFile(t, path, oldFormatFile(o, o.Content))

	stats, err := obsidian.AdoptWithOptions(context.Background(), st, outDir, "", false)
	if err != nil {
		t.Fatalf("AdoptWithOptions: %v", err)
	}
	if stats.Adopted != 1 {
		t.Errorf("Adopted = %d, quería 1 (NotAdopted=%v)", stats.Adopted, stats.NotAdopted)
	}
	if len(stats.NotAdopted) != 0 {
		t.Errorf("NotAdopted = %v, quería vacío", stats.NotAdopted)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"generated_by: kronos-export", "kronos_hash:", o.Content} {
		if !strings.Contains(got, want) {
			t.Errorf("archivo adoptado no contiene %q:\n%s", want, got)
		}
	}
}

func TestAdopt_RejectsContentMismatch(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	o := findObservation(t, st, "SQLite FTS5 para búsqueda")

	outDir := t.TempDir()
	path := oldFilePath(outDir, o)
	original := oldFormatFile(o, "Esto no es lo que hay en la base, alguien lo editó a mano.")
	writeOldFile(t, path, original)

	stats, err := obsidian.AdoptWithOptions(context.Background(), st, outDir, "", false)
	if err != nil {
		t.Fatalf("AdoptWithOptions: %v", err)
	}
	if stats.Adopted != 0 {
		t.Errorf("Adopted = %d, quería 0", stats.Adopted)
	}
	found := false
	for reason, n := range stats.NotAdopted {
		if strings.Contains(reason, "contenido distinto") && n == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("NotAdopted = %v, quería un motivo de contenido distinto", stats.NotAdopted)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("el archivo con contenido distinto fue modificado por --adopt")
	}
}

func TestAdopt_RejectsUnknownID(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	o := findObservation(t, st, "SQLite FTS5 para búsqueda")

	outDir := t.TempDir()
	fake := *o
	fake.ID = 999999
	path := oldFilePath(outDir, &fake)
	writeOldFile(t, path, oldFormatFile(&fake, o.Content))

	stats, err := obsidian.AdoptWithOptions(context.Background(), st, outDir, "", false)
	if err != nil {
		t.Fatalf("AdoptWithOptions: %v", err)
	}
	if stats.Adopted != 0 {
		t.Errorf("Adopted = %d, quería 0", stats.Adopted)
	}
	if stats.NotAdopted["id inexistente en la base"] != 1 {
		t.Errorf("NotAdopted = %v, quería id inexistente en la base: 1", stats.NotAdopted)
	}
}

func TestAdopt_DryRunDoesNotWrite(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	o := findObservation(t, st, "SQLite FTS5 para búsqueda")

	outDir := t.TempDir()
	path := oldFilePath(outDir, o)
	original := oldFormatFile(o, o.Content)
	writeOldFile(t, path, original)

	stats, err := obsidian.AdoptWithOptions(context.Background(), st, outDir, "", true)
	if err != nil {
		t.Fatalf("AdoptWithOptions: %v", err)
	}
	if stats.Adopted != 1 {
		t.Errorf("Adopted = %d, quería 1 (dry-run igual cuenta lo que adoptaría)", stats.Adopted)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("--dry-run escribió el archivo, no debería")
	}
}
