package sync

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ChunkData es el contenido serializable de un chunk.
// Se almacena como JSON canónico comprimido con gzip.
type ChunkData struct {
	Sessions     []map[string]any `json:"sessions"`
	Observations []map[string]any `json:"observations"`
	Prompts      []map[string]any `json:"prompts"`
}

func chunkDir(syncDir string) string {
	return filepath.Join(syncDir, ".kronos", "chunks")
}

func chunkPath(syncDir, chunkID string) string {
	return filepath.Join(chunkDir(syncDir), chunkID+".jsonl.gz")
}

// marshalChunk serializa ChunkData a JSON canónico comprimido con gzip.
// Retorna el contenido comprimido y el chunkID (primeros 16 chars del SHA-256).
func marshalChunk(cd *ChunkData) ([]byte, string, error) {
	jsonData, err := json.Marshal(cd)
	if err != nil {
		return nil, "", fmt.Errorf("serializar chunk: %w", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(jsonData); err != nil {
		return nil, "", fmt.Errorf("comprimir chunk: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, "", fmt.Errorf("cerrar gzip: %w", err)
	}

	compressed := buf.Bytes()
	sum := sha256.Sum256(compressed)
	chunkID := fmt.Sprintf("%x", sum)[:16]

	return compressed, chunkID, nil
}

// writeChunk escribe el contenido comprimido en el directorio de chunks.
func writeChunk(syncDir, chunkID string, data []byte) error {
	dir := chunkDir(syncDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("crear dir chunks: %w", err)
	}
	return os.WriteFile(chunkPath(syncDir, chunkID), data, 0644)
}

// readChunk lee y descomprime un chunk desde disco.
func readChunk(syncDir, chunkID string) (*ChunkData, error) {
	data, err := os.ReadFile(chunkPath(syncDir, chunkID))
	if err != nil {
		return nil, fmt.Errorf("leer chunk %s: %w", chunkID, err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("abrir gzip chunk %s: %w", chunkID, err)
	}
	defer gz.Close()

	var cd ChunkData
	if err := json.NewDecoder(gz).Decode(&cd); err != nil {
		return nil, fmt.Errorf("decodificar chunk %s: %w", chunkID, err)
	}
	return &cd, nil
}
