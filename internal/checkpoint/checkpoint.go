package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State representa el estado de una tarea en progreso.
// Se persiste como JSON en el data directory para que
// el hook SessionStart pueda inyectarlo al inicio de cada conversación.
type State struct {
	Task      string    `json:"task"`
	Progress  string    `json:"progress,omitempty"`
	NextStep  string    `json:"next_step"`
	Files     string    `json:"files,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	Project   string    `json:"project"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Save persiste el checkpoint del proyecto en dataDir.
func Save(dataDir, project string, s State) error {
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filename(dataDir, project), data, 0644)
}

// Load lee el checkpoint activo del proyecto. Retorna nil si no existe.
func Load(dataDir, project string) (*State, error) {
	data, err := os.ReadFile(filename(dataDir, project))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Clear elimina el checkpoint del proyecto (tarea completada).
func Clear(dataDir, project string) error {
	err := os.Remove(filename(dataDir, project))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func filename(dataDir, project string) string {
	safe := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "_").Replace(project)
	return filepath.Join(dataDir, "checkpoint-"+safe+".json")
}
