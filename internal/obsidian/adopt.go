package obsidian

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// filenamePattern reconoce "<id>-<slug>.md", el nombre de archivo que usa
// el export para observaciones (ver obsName). Es el primer filtro para
// decidir si un archivo del vault es candidato a --adopt.
var filenamePattern = regexp.MustCompile(`^([0-9]+)-[a-z0-9-]*\.md$`)

// maxAdoptWarnings limita los avisos por stderr de --adopt a los primeros
// N archivos no adoptados, para no inundar la consola en un vault grande
// (el caso real que motivó esto tenía 880 archivos en ese estado).
const maxAdoptWarnings = 10

// AdoptStats resume el resultado de `kronos export --adopt`.
type AdoptStats struct {
	Adopted    int
	Unchanged  int            // ya estaban en formato nuevo, nada que adoptar
	NotAdopted map[string]int // motivo -> cantidad
	Warnings   []string       // "<path>: <motivo>" para stderr, con tope
}

// AdoptWithOptions escanea outDir buscando archivos generados por una
// versión ANTERIOR de `kronos export` (formato viejo: frontmatter con
// id/title/type/project/scope/topic_key/created_at/revision/tags, cuerpo
// "# título" + metadata + separador "---" + contenido) y los migra al
// formato no destructivo actual (generated_by/kronos_hash, ver
// nondestructive.go).
//
// Nace de un caso real: el vault ~/kronos-vault tenía 881 archivos de esa
// versión vieja, sin la marca "generated_by: kronos-export". El export no
// destructivo los trataba como notas escritas a mano (writeGenerated no
// pisa nada sin esa marca) y quedaban congelados para siempre — un
// re-export normal los reportaba como "editados a mano (salteados)" sin
// que nadie los hubiera tocado jamás.
//
// Un archivo se adopta solo si se cumplen TODAS estas condiciones (si
// alguna falla, el archivo no se toca y se cuenta aparte con el motivo):
//
//	a) su path matchea <outDir>/<proyecto>/<tipo>/<id>-<slug>.md;
//	b) su frontmatter (parseFrontmatter) tiene un campo "id" que, numéricamente,
//	   coincide con el <id> del nombre de archivo, y "project"/"type" cuyo
//	   safeName() coincide con los directorios del path;
//	c) existe una observación con ese id en el store;
//	d) el contenido del cuerpo viejo (lo que sigue al separador "---" que
//	   cierra el bloque de metadata del cuerpo) coincide, normalizando
//	   espacios de fin de línea y extremos, con el Content de esa observación.
//
// Con dryRun=true no se escribe nada: solo se calculan las stats, como si
// se hubiera adoptado.
func AdoptWithOptions(ctx context.Context, st store.Storer, outDir, project string, dryRun bool) (*AdoptStats, error) {
	root := outDir
	if project != "" {
		root = filepath.Join(outDir, safeName(project))
	}

	stats := &AdoptStats{NotAdopted: map[string]int{}}

	if _, err := os.Stat(root); os.IsNotExist(err) {
		return stats, nil
	}

	reject := func(path, reason string) {
		stats.NotAdopted[reason]++
		if len(stats.Warnings) < maxAdoptWarnings {
			stats.Warnings = append(stats.Warnings, fmt.Sprintf("%s: %s", path, reason))
		}
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		base := filepath.Base(path)
		if base == "_index.md" || base == "_core.md" {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 {
			// No vive en <proyecto>/<tipo>/archivo.md: no es candidato,
			// probablemente una nota manual en otro lado del vault.
			return nil
		}
		dirProject, dirType, filename := parts[0], parts[1], parts[2]

		m := filenamePattern.FindStringSubmatch(filename)
		if m == nil {
			return nil // (a) no matchea el patrón de nombre: no es candidato
		}
		fileID, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(raw)

		fm := parseFrontmatter(content)
		if fm != nil && fm[markerKey] == markerValue {
			stats.Unchanged++ // ya está en formato nuevo
			return nil
		}
		if fm == nil {
			reject(path, "sin frontmatter reconocible")
			return nil
		}

		idStr, hasID := fm["id"]
		fmProject, hasProject := fm["project"]
		fmType, hasType := fm["type"]
		if !hasID || !hasProject || !hasType {
			reject(path, "frontmatter sin id/project/type")
			return nil
		}
		fmID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || fmID != fileID {
			reject(path, "id del frontmatter no coincide con el del nombre de archivo")
			return nil
		}
		if safeName(fmProject) != dirProject || safeName(fmType) != dirType {
			reject(path, "project/type del frontmatter no coinciden con el path")
			return nil
		}

		// (c) debe existir una observación con ese id en el store.
		o, err := st.GetObservation(ctx, fileID)
		if err != nil {
			return err
		}
		if o == nil {
			reject(path, "id inexistente en la base")
			return nil
		}

		// (d) el contenido del cuerpo viejo debe coincidir con el de la base.
		oldContent, ok := extractOldContent(content)
		if !ok {
			reject(path, "no se pudo aislar el contenido del cuerpo viejo")
			return nil
		}
		if normalizeForCompare(oldContent) != normalizeForCompare(o.Content) {
			reject(path, "contenido distinto al de la base — posible edición manual")
			return nil
		}

		stats.Adopted++
		if !dryRun {
			build := observationBuilder(o)
			final := build(contentHash(build(hashPlaceholder)))
			if err := os.WriteFile(path, []byte(final), 0644); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	printAdoptSummary(stats)
	return stats, nil
}

// extractOldContent aísla, dentro de un archivo en formato viejo, el
// contenido real de la observación: todo lo que sigue al separador
// "---\n\n" que cierra el bloque de metadata del cuerpo (el que viene
// después del frontmatter, no el propio cierre del frontmatter). Devuelve
// ok=false si el archivo no tiene la estructura esperada.
func extractOldContent(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "---\n") {
		return "", false
	}
	rest := raw[len("---\n"):]
	fmEnd := strings.Index(rest, "\n---\n")
	if fmEnd < 0 {
		return "", false
	}
	body := rest[fmEnd+len("\n---\n"):]

	sep := strings.Index(body, "---\n\n")
	if sep < 0 {
		return "", false
	}
	return body[sep+len("---\n\n"):], true
}

// normalizeForCompare recorta espacios/tabs de fin de línea y los extremos
// del texto completo, para que diferencias incidentales de whitespace no
// se confundan con una edición manual real (condición (d) de --adopt).
func normalizeForCompare(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// printAdoptSummary imprime el resumen contable de --adopt, con el mismo
// espíritu que printSummary para el export normal: números comparables
// entre corridas, para poder verificar que una adopción dejó el vault
// consistente (ver export.go).
func printAdoptSummary(stats *AdoptStats) {
	total := 0
	for _, c := range stats.NotAdopted {
		total += c
	}
	fmt.Printf("Adopción: %d adoptados | %d sin cambios | %d no adoptados",
		stats.Adopted, stats.Unchanged, total)
	if total > 0 {
		reasons := make([]string, 0, len(stats.NotAdopted))
		for r := range stats.NotAdopted {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		parts := make([]string, 0, len(reasons))
		for _, r := range reasons {
			parts = append(parts, fmt.Sprintf("%s: %d", r, stats.NotAdopted[r]))
		}
		fmt.Printf(" (motivo: %s)", strings.Join(parts, "; "))
	}
	fmt.Println()
	for _, w := range stats.Warnings {
		fmt.Fprintf(os.Stderr, "aviso: no adoptado — %s\n", w)
	}
}
