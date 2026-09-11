package obsidian

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// markerKey/markerValue identifican un archivo generado por `kronos export`
// (o por el mirror en vivo, que comparte el mismo formato). hashKey guarda
// el hash de ese contenido generado — es lo que permite distinguir "el dato
// de origen cambió" (hay que reescribir) de "alguien tocó el archivo a mano"
// (no se toca, se avisa).
const (
	markerKey       = "generated_by"
	markerValue     = "kronos-export"
	hashKey         = "kronos_hash"
	hashPlaceholder = "PENDIENTE"
)

// writeResult indica qué pasó al intentar escribir un archivo generado.
type writeResult int

const (
	resWritten writeResult = iota
	resUnchanged
	resSkippedManual
)

// contentHash es un sha256 corto (12 hex) — alcanza para detectar cambios,
// no hace falta el hash completo en el frontmatter.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:12]
}

// parseFrontmatter extrae los pares clave: valor del bloque YAML entre los
// primeros dos delimitadores "---". No es un parser YAML real — el
// frontmatter que este paquete genera es siempre "clave: valor" de una
// línea, así que alcanza con partir por ":" y recortar comillas.
func parseFrontmatter(content string) map[string]string {
	if !strings.HasPrefix(content, "---\n") {
		return nil
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.Trim(strings.TrimSpace(line[idx+1:]), `"`)
		out[key] = val
	}
	return out
}

// canonicalize reemplaza el valor real de kronos_hash por un placeholder fijo,
// para poder recalcular el hash del contenido "tal como se generó" sin que el
// propio hash (que depende de sí mismo) rompa la comparación.
func canonicalize(content, storedHash string) string {
	if storedHash == "" {
		return content
	}
	return strings.Replace(content, hashKey+": "+storedHash, hashKey+": "+hashPlaceholder, 1)
}

// writeGenerated escribe un archivo marcado como generado por kronos, sin
// pisar nunca uno editado a mano. build(hash) arma el contenido completo del
// archivo insertando hash en el campo kronos_hash — se llama dos veces:
// una con el placeholder (para obtener el contenido canónico a hashear) y
// otra con el hash real (para el contenido final a escribir).
func writeGenerated(path string, build func(hash string) string) (writeResult, error) {
	canonicalNew := build(hashPlaceholder)
	newHash := contentHash(canonicalNew)
	final := build(newHash)

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(path, []byte(final), 0644); err != nil {
			return 0, err
		}
		return resWritten, nil
	}

	fm := parseFrontmatter(string(existing))
	if fm == nil || fm[markerKey] != markerValue {
		// Nota escrita a mano ocupando el path esperado: no se toca.
		return resSkippedManual, nil
	}
	storedHash := fm[hashKey]
	actualHash := contentHash(canonicalize(string(existing), storedHash))
	if actualHash != storedHash {
		// El archivo generado fue editado a mano después de exportarse.
		return resSkippedManual, nil
	}
	if newHash == storedHash {
		return resUnchanged, nil
	}
	if err := os.WriteFile(path, []byte(final), 0644); err != nil {
		return 0, err
	}
	return resWritten, nil
}

// pruneOrphans borra, dentro de root, los archivos .md marcados como
// generados por kronos cuyo kronos_id ya no está en validIDs — es decir,
// observaciones borradas cuyo archivo del vault sobrevivió al export
// normal (que nunca borra, solo deja de tocar). _index.md y _core.md no
// llevan kronos_id, así que quedan afuera de este barrido.
func pruneOrphans(root string, validIDs map[int64]bool) (int, error) {
	pruned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fm := parseFrontmatter(string(content))
		if fm == nil || fm[markerKey] != markerValue {
			return nil
		}
		idStr, ok := fm["kronos_id"]
		if !ok {
			return nil
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return nil
		}
		if validIDs[id] {
			return nil
		}
		if err := os.Remove(path); err == nil {
			pruned++
		}
		return nil
	})
	return pruned, err
}
