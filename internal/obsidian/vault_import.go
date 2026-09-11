package obsidian

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// ImportOptions configura `kronos vault import` — el camino de vuelta
// (vault → base) que complementa a Export/ExportWithOptions (base → vault).
type ImportOptions struct {
	// Apply aplica los cambios detectados a la base. Sin Apply (default),
	// ImportVault solo calcula y reporta qué haría — no escribe nada.
	Apply bool
	// Project filtra: solo se importan observaciones de este proyecto.
	Project string
	// MaxConflictsReport limita cuántos conflictos se listan en detalle por
	// stderr (el conteo total del resumen final siempre es completo). 0 o
	// negativo = sin límite.
	MaxConflictsReport int
}

// ImportUpdate describe una observación actualizada (o que se actualizaría,
// en dry-run) a partir de una edición manual en el vault.
type ImportUpdate struct {
	ID             int64
	Title          string
	Path           string
	TitleChanged   bool
	ContentChanged bool
}

// ImportConflict describe un archivo que ImportVault decidió no tocar
// porque la observación cambió en la base Y el archivo también — la regla
// es "ante duda, no escribir y reportar" (ver ImportVault).
type ImportConflict struct {
	ID     int64
	Title  string
	Path   string
	Reason string
}

// ImportStats resume el resultado de ImportVault.
type ImportStats struct {
	Updates        []ImportUpdate
	Unchanged      int
	Conflicts      []ImportConflict
	Orphaned       int
	IgnoredNoMark  int
	IgnoredProject int
}

// ImportVault recorre vaultDir buscando archivos generados por `kronos
// export` (frontmatter "generated_by: kronos-export" + "kronos_id") y
// reconstruye, a partir del estado actual de la observación en st, cuál
// sería su contenido si se exportara hoy. Es el camino de vuelta: sin esto,
// una edición manual en Obsidian queda congelada para siempre (el export no
// destructivo nunca la pisa, pero tampoco la trae de vuelta).
//
// Por archivo candidato:
//
//   - Sin "generated_by: kronos-export" → nota manual ajena a kronos: se
//     ignora por completo (IgnoredNoMark).
//   - kronos_id sin observación viva en la base (borrada o inexistente) →
//     huérfano: no se crea nada (Orphaned).
//   - Filtrado por Project: se cuenta aparte (IgnoredProject), sin tocarlo.
//   - Si el archivo no fue tocado desde que se generó (su kronos_hash
//     todavía valida contra su propio contenido — la misma técnica que usa
//     writeGenerated para detectar ediciones a mano) → nada que importar
//     (Unchanged), esté o no la base más adelantada: un `kronos export`
//     normal ya se encarga de refrescarlo.
//   - Si el archivo SÍ fue tocado pero el título/contenido extraídos
//     terminan siendo iguales a los de la base (p. ej. alguien tocó solo
//     scope/tags/topic_key en el frontmatter, que son metadatos de kronos,
//     no del vault) → tampoco hay nada que importar (Unchanged).
//   - Si además de tocado el título o el contenido cambiaron: se compara el
//     hash que se generaría HOY desde la base contra el que el archivo
//     tiene grabado. Si coinciden, la base no se movió desde el export —
//     es una edición manual pura: se actualiza título/contenido (Apply) y
//     revision_count sube solo (vía UpdateObservation). Si no coinciden, la
//     base también cambió desde entonces: conflicto, no se toca nada.
//
// Con Apply=false (default) no se escribe nada en la base — todo lo de
// arriba se calcula igual, para poder reportarlo en dry-run.
func ImportVault(ctx context.Context, st store.Storer, vaultDir string, opts ImportOptions) (*ImportStats, error) {
	stats := &ImportStats{}

	if _, err := os.Stat(vaultDir); os.IsNotExist(err) {
		printImportSummary(vaultDir, stats, opts)
		return stats, nil
	}

	err := filepath.WalkDir(vaultDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		base := filepath.Base(path)
		if base == "_index.md" || base == "_core.md" {
			// Puramente derivados, sin kronos_id: no son candidatos a import.
			return nil
		}
		return importFile(ctx, st, path, opts, stats)
	})
	if err != nil {
		return nil, err
	}

	printImportSummary(vaultDir, stats, opts)
	return stats, nil
}

func importFile(ctx context.Context, st store.Storer, path string, opts ImportOptions, stats *ImportStats) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // no se pudo leer: no debería pasar en un walk normal, lo salteamos sin abortar el resto
	}
	content := string(raw)

	fm := parseFrontmatter(content)
	if fm == nil || fm[markerKey] != markerValue {
		stats.IgnoredNoMark++
		return nil
	}

	idStr, hasID := fm["kronos_id"]
	if !hasID {
		stats.IgnoredNoMark++
		return nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		stats.IgnoredNoMark++
		return nil
	}

	obs, err := st.GetObservation(ctx, id)
	if err != nil {
		return fmt.Errorf("leer observación %d (%s): %w", id, path, err)
	}
	if obs == nil || obs.DeletedAt != nil {
		stats.Orphaned++
		return nil
	}

	if opts.Project != "" && obs.Project != opts.Project {
		stats.IgnoredProject++
		return nil
	}

	// Misma técnica que writeGenerated (nondestructive.go): si el hash
	// grabado no valida contra el contenido actual del archivo, alguien lo
	// tocó después de que kronos lo generara.
	storedHash := fm[hashKey]
	fileEdited := storedHash == "" || contentHash(canonicalize(content, storedHash)) != storedHash
	if !fileEdited {
		stats.Unchanged++
		return nil
	}

	newTitle, newContent, ok := extractGeneratedParts(content)
	if !ok {
		stats.Conflicts = append(stats.Conflicts, ImportConflict{
			ID:    id,
			Title: obs.Title,
			Path:  path,
			Reason: "no se pudo interpretar el cuerpo del archivo (¿formato viejo? correr antes " +
				"\"kronos export --adopt\")",
		})
		return nil
	}

	titleChanged := newTitle != "" && newTitle != obs.Title
	contentChanged := normalizeForCompare(newContent) != normalizeForCompare(obs.Content)
	if !titleChanged && !contentChanged {
		// Lo único tocado fue metadata de kronos (scope/type/project/tags):
		// eso no viaja de vuelta, así que no hay nada que importar.
		stats.Unchanged++
		return nil
	}

	// ¿La base se movió desde que este archivo se generó? Reconstruimos el
	// contenido que el export produciría HOY a partir de la observación tal
	// como está en la base (sin aplicar todavía lo que dice el archivo) y
	// comparamos su hash contra el que el archivo tiene grabado.
	build := observationBuilder(obs)
	freshHash := contentHash(build(hashPlaceholder))
	baseChanged := freshHash != storedHash

	if baseChanged {
		stats.Conflicts = append(stats.Conflicts, ImportConflict{
			ID:     id,
			Title:  obs.Title,
			Path:   path,
			Reason: "la observación cambió en la base y el archivo también — resolución manual",
		})
		return nil
	}

	stats.Updates = append(stats.Updates, ImportUpdate{
		ID:             id,
		Title:          newTitle,
		Path:           path,
		TitleChanged:   titleChanged,
		ContentChanged: contentChanged,
	})

	if !opts.Apply {
		return nil
	}

	params := store.UpdateParams{ID: id}
	if contentChanged {
		params.Content = &newContent
	}
	if titleChanged {
		params.Title = &newTitle
	}
	updated, err := st.UpdateObservation(ctx, params)
	if err != nil {
		return err
	}

	// El archivo que acabamos de importar ya no representa "el export viejo":
	// la base tiene ahora exactamente su contenido. Lo reescribimos con el
	// render canónico igual que lo haría el export (hash sobre el archivo con
	// el placeholder incluido, no sobre el contenido suelto), pero escribiendo
	// directo: acá NO podemos usar writeGenerated, porque justamente ve el
	// archivo como editado a mano y lo saltearía — que es lo que queremos al
	// exportar y exactamente lo que no queremos acá.
	//
	// Sin esto el frontmatter seguía diciendo "Rev: 1" con el hash viejo, y la
	// nota quedaba marcada como editada a mano para siempre.
	if updated != nil {
		build := observationBuilder(updated)
		final := build(contentHash(build(hashPlaceholder)))
		if err := os.WriteFile(path, []byte(final), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// extractGeneratedParts separa, de un archivo en el formato actual de
// export (ver writeObsBody), el título (H1) y el contenido real de la
// observación — lo único que el usuario puede haber editado a mano y que
// vale la pena traer de vuelta. Devuelve ok=false si el archivo no tiene la
// estructura esperada (por ejemplo, un archivo del formato viejo sin
// adoptar: ver AdoptWithOptions).
func extractGeneratedParts(raw string) (title, content string, ok bool) {
	if !strings.HasPrefix(raw, "---\n") {
		return "", "", false
	}
	rest := raw[len("---\n"):]
	fmEnd := strings.Index(rest, "\n---\n")
	if fmEnd < 0 {
		return "", "", false
	}
	body := strings.TrimPrefix(rest[fmEnd+len("\n---\n"):], "\n")

	if !strings.HasPrefix(body, "# ") {
		return "", "", false
	}
	nl := strings.Index(body, "\n")
	if nl < 0 {
		return "", "", false
	}
	title = strings.TrimSpace(body[2:nl])

	sep := strings.Index(body, "---\n\n")
	if sep < 0 {
		return "", "", false
	}
	content = strings.TrimSuffix(body[sep+len("---\n\n"):], "\n")
	return title, content, true
}

// printImportSummary imprime el detalle (actualizadas/conflictos) y el
// resumen contable final de ImportVault, con el mismo espíritu que
// printSummary/printAdoptSummary.
func printImportSummary(vaultDir string, stats *ImportStats, opts ImportOptions) {
	mode := "dry-run, no se escribió nada"
	if opts.Apply {
		mode = "aplicado"
	}
	fmt.Printf("Import desde %s (%s)\n", vaultDir, mode)

	for _, u := range stats.Updates {
		detail := "contenido"
		switch {
		case u.ContentChanged && u.TitleChanged:
			detail = "contenido y título"
		case u.TitleChanged:
			detail = "título"
		}
		fmt.Printf("  actualizada (%s): %s — %s\n", detail, u.Path, u.Title)
	}

	limit := opts.MaxConflictsReport
	for i, c := range stats.Conflicts {
		if limit > 0 && i >= limit {
			fmt.Fprintf(os.Stderr, "  ... %d conflicto(s) más (ver vault.import_max_conflicts_report)\n",
				len(stats.Conflicts)-limit)
			break
		}
		fmt.Fprintf(os.Stderr, "conflicto: obs %d %q (%s): %s\n", c.ID, c.Title, c.Path, c.Reason)
	}

	fmt.Printf("Import: %d actualizadas | %d sin cambios | %d en conflicto | %d huérfanas | %d ignoradas (sin marca) | %d ignoradas por --project\n",
		len(stats.Updates), stats.Unchanged, len(stats.Conflicts), stats.Orphaned, stats.IgnoredNoMark, stats.IgnoredProject)
}
