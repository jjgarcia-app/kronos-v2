package obsidian_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/obsidian"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// (a) edición manual simple: el contenido de la observación se actualiza y
// sube revision_count.
func TestImportVault_ManualEditUpdatesContent(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	path := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw),
		"FTS5 con unicode61 maneja español correctamente.",
		"FTS5 con unicode61 maneja español correctamente. Editado a mano en Obsidian.", 1)
	if edited == string(raw) {
		t.Fatal("el reemplazo no encontró el contenido esperado")
	}
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var target *store.Observation
	for _, o := range all {
		if strings.Contains(o.Title, "SQLite FTS5") {
			target = o
		}
	}
	if target == nil {
		t.Fatal("no encontré la observación de origen")
	}
	revBefore := target.RevisionCount

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if len(stats.Updates) != 1 {
		t.Fatalf("Updates = %d, quería 1 (%+v)", len(stats.Updates), stats.Updates)
	}
	if len(stats.Conflicts) != 0 {
		t.Fatalf("Conflicts = %v, quería ninguno", stats.Conflicts)
	}

	after, err := st.GetObservation(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.Content, "Editado a mano en Obsidian.") {
		t.Errorf("el contenido de la base no se actualizó: %q", after.Content)
	}
	if after.RevisionCount != revBefore+1 {
		t.Errorf("RevisionCount = %d, quería %d", after.RevisionCount, revBefore+1)
	}
}

// (b) conflicto: la base y el archivo cambiaron ambos. No se toca nada y se
// reporta.
func TestImportVault_ConflictWhenBothChanged(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	path := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw),
		"FTS5 con unicode61 maneja español correctamente.",
		"Edición manual en el vault, en conflicto con la base.", 1)
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var target *store.Observation
	for _, o := range all {
		if strings.Contains(o.Title, "SQLite FTS5") {
			target = o
		}
	}
	if target == nil {
		t.Fatal("no encontré la observación de origen")
	}

	// La base también cambia después del export, independientemente del vault.
	newContent := "Cambio en la base, sin pasar por el vault."
	if _, err := st.UpdateObservation(ctx, store.UpdateParams{ID: target.ID, Content: &newContent}); err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}
	revBefore, err := st.GetObservation(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if len(stats.Updates) != 0 {
		t.Fatalf("Updates = %v, quería ninguno (debería ser conflicto)", stats.Updates)
	}
	if len(stats.Conflicts) != 1 {
		t.Fatalf("Conflicts = %d, quería 1", len(stats.Conflicts))
	}
	if stats.Conflicts[0].ID != target.ID {
		t.Errorf("conflicto reporta ID %d, quería %d", stats.Conflicts[0].ID, target.ID)
	}

	after, err := st.GetObservation(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Content != newContent {
		t.Errorf("el conflicto tocó el contenido de la base: %q", after.Content)
	}
	if after.RevisionCount != revBefore.RevisionCount {
		t.Errorf("el conflicto tocó revision_count: %d != %d", after.RevisionCount, revBefore.RevisionCount)
	}
}

// (c) archivo sin marca (nota manual ajena a kronos) → ignorado.
func TestImportVault_IgnoresFilesWithoutMarker(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}
	manualPath := outDir + "/notas-personales.md"
	manualContent := "# Notas mías\n\nEsto no es de kronos.\n"
	if err := os.WriteFile(manualPath, []byte(manualContent), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if stats.IgnoredNoMark < 1 {
		t.Errorf("IgnoredNoMark = %d, quería al menos 1", stats.IgnoredNoMark)
	}

	data, err := os.ReadFile(manualPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != manualContent {
		t.Error("la nota manual fue tocada")
	}
}

// (d) kronos_id inexistente → huérfana, no se crea nada.
func TestImportVault_OrphanedKronosID(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	path := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Un id que no existe en la base.
	lines := strings.Split(string(raw), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "kronos_id: ") {
			lines[i] = "kronos_id: 999999"
		}
	}
	orphanContent := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(orphanContent), 0644); err != nil {
		t.Fatal(err)
	}

	countBefore, err := st.CountObservations(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if stats.Orphaned != 1 {
		t.Errorf("Orphaned = %d, quería 1", stats.Orphaned)
	}

	countAfter, err := st.CountObservations(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if countAfter != countBefore {
		t.Errorf("el import creó observaciones: antes %d, después %d", countBefore, countAfter)
	}
}

// (e) sin --apply no se escribe NADA en la base (revision_count antes/después).
func TestImportVault_DryRunWritesNothing(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	path := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw),
		"FTS5 con unicode61 maneja español correctamente.",
		"Editado a mano, pero sin --apply esto no debería llegar a la base.", 1)
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var target *store.Observation
	for _, o := range all {
		if strings.Contains(o.Title, "SQLite FTS5") {
			target = o
		}
	}
	if target == nil {
		t.Fatal("no encontré la observación de origen")
	}
	revBefore := target.RevisionCount
	contentBefore := target.Content

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: false})
	if err != nil {
		t.Fatalf("ImportVault dry-run: %v", err)
	}
	if len(stats.Updates) != 1 {
		t.Fatalf("Updates = %d, quería 1 (el dry-run también debe reportar)", len(stats.Updates))
	}

	after, err := st.GetObservation(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RevisionCount != revBefore {
		t.Errorf("dry-run modificó revision_count: %d != %d", after.RevisionCount, revBefore)
	}
	if after.Content != contentBefore {
		t.Errorf("dry-run modificó el contenido de la base")
	}
}

// (f) título cambiado en el H1 → se reporta y se actualiza el título.
func TestImportVault_TitleChangeReportedAndApplied(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	path := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw),
		"# SQLite FTS5 para búsqueda",
		"# SQLite FTS5 para búsqueda (título editado a mano)", 1)
	if edited == string(raw) {
		t.Fatal("no encontré el H1 esperado en el archivo generado")
	}
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var target *store.Observation
	for _, o := range all {
		if strings.Contains(o.Title, "SQLite FTS5") {
			target = o
		}
	}
	if target == nil {
		t.Fatal("no encontré la observación de origen")
	}

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if len(stats.Updates) != 1 {
		t.Fatalf("Updates = %d, quería 1", len(stats.Updates))
	}
	if !stats.Updates[0].TitleChanged {
		t.Error("el update no reporta TitleChanged=true")
	}

	after, err := st.GetObservation(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "SQLite FTS5 para búsqueda (título editado a mano)" {
		t.Errorf("título no actualizado: %q", after.Title)
	}
}

// (g) --project filtra: solo procesa observaciones del proyecto pedido.
func TestImportVault_ProjectFilter(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Editamos a mano un archivo de kronos-v2 y uno de atisa.
	kronosPath := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	atisaPath := findGeneratedFile(t, outDir, "Separamos dominio de infraestructura.")

	for _, p := range []string{kronosPath, atisaPath} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		edited := string(raw) + "\nLínea agregada a mano.\n"
		if err := os.WriteFile(p, []byte(edited), 0644); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true, Project: "kronos-v2"})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if len(stats.Updates) != 1 {
		t.Fatalf("Updates = %d, quería 1 (solo kronos-v2)", len(stats.Updates))
	}
	if stats.IgnoredProject != 1 {
		t.Errorf("IgnoredProject = %d, quería 1 (atisa filtrado)", stats.IgnoredProject)
	}

	all, err := st.ListAll(ctx, "atisa")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range all {
		if strings.Contains(o.Content, "Línea agregada a mano.") {
			t.Error("--project kronos-v2 no debería haber tocado una observación de atisa")
		}
	}
}

// (h) tras aplicar una edición manual, el frontmatter del archivo queda al día
// (revision y kronos_hash), así la nota no miente sobre su propia versión y la
// segunda pasada es un no-op.
func TestImportVault_RefreshesFrontmatterOnApply(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()
	ctx := context.Background()

	if err := obsidian.Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	path := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := string(raw)
	if !strings.Contains(before, "kronos_hash: ") {
		t.Fatalf("el archivo exportado no tiene kronos_hash en el frontmatter:\n%s", before)
	}
	oldHash := frontmatterValue(before, "kronos_hash")
	if oldHash == "" {
		t.Fatal("no pude leer el kronos_hash previo")
	}

	edited := strings.Replace(before, "FTS5 con unicode61 maneja español correctamente.",
		"FTS5 con unicode61 maneja español correctamente. Nota escrita a mano.", 1)
	if edited == before {
		t.Fatal("el reemplazo no encontró el contenido esperado")
	}
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if len(stats.Updates) != 1 {
		t.Fatalf("Updates = %d, quería 1", len(stats.Updates))
	}

	var target *store.Observation
	all, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range all {
		if strings.Contains(o.Title, "SQLite FTS5") {
			target = o
		}
	}
	if target == nil {
		t.Fatal("no encontré la observación de origen")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)
	if !strings.Contains(got, "Nota escrita a mano.") {
		t.Error("la edición manual se perdió al refrescar el frontmatter")
	}
	wantRev := fmt.Sprintf("revision: %d", target.RevisionCount)
	if !strings.Contains(got, wantRev) {
		t.Errorf("el frontmatter no refleja la revisión de la base (%q):\n%s", wantRev, got)
	}
	if h := frontmatterValue(got, "kronos_hash"); h == "" || h == oldHash {
		t.Errorf("kronos_hash no se actualizó (antes %q, ahora %q)", oldHash, h)
	}

	// Segunda pasada: ya no hay nada que importar.
	stats2, err := obsidian.ImportVault(ctx, st, outDir, obsidian.ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("segunda ImportVault: %v", err)
	}
	if len(stats2.Updates) != 0 || len(stats2.Conflicts) != 0 {
		t.Errorf("segunda pasada: %d actualizadas, %d conflictos — esperaba 0 y 0",
			len(stats2.Updates), len(stats2.Conflicts))
	}
}

// frontmatterValue devuelve el valor de una clave del frontmatter YAML (vacío
// si no está).
func frontmatterValue(file, key string) string {
	for _, line := range strings.Split(file, "\n") {
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return ""
}
