package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest es el índice append-only de todos los chunks exportados.
// Es mergeable en git porque solo se agregan entradas al slice.
type Manifest struct {
	Version int          `json:"version"`
	Chunks  []ChunkEntry `json:"chunks"`
}

// ChunkEntry describe un chunk exportado.
type ChunkEntry struct {
	ID        string `json:"id"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	Sessions  int    `json:"sessions"`
	Memories  int    `json:"memories"`
	Prompts   int    `json:"prompts"`
}

func manifestPath(syncDir string) string {
	return filepath.Join(syncDir, ".kronos", "manifest.json")
}

// loadManifest lee el manifest desde disco. Si no existe, retorna un manifest vacío.
func loadManifest(syncDir string) (*Manifest, error) {
	path := manifestPath(syncDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Manifest{Version: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leer manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsear manifest: %w", err)
	}
	if m.Version == 0 {
		m.Version = 1
	}
	return &m, nil
}

// saveManifest escribe el manifest a disco (crea directorios si no existen).
func saveManifest(syncDir string, m *Manifest) error {
	path := manifestPath(syncDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("crear dir manifest: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar manifest: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// hasChunk retorna true si el chunkID ya está registrado en el manifest.
func (m *Manifest) hasChunk(chunkID string) bool {
	for _, e := range m.Chunks {
		if e.ID == chunkID {
			return true
		}
	}
	return false
}
