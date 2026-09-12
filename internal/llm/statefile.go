package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// atomicWriteJSON serializa v y lo escribe en path con tmp+rename — mismo
// patrón que Breaker.save (ver breaker.go): el sufijo del temporal lleva el
// PID para que dos procesos escribiendo a la vez no compartan archivo
// temporal, y el rename final es atómico así que un lector nunca ve JSON a
// medio escribir. Usado por Usage (contador de llamadas) y por el registro
// de última falla de claude-cli — mismo mecanismo de archivo compartido
// entre procesos que ya resuelve el cortacircuitos.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
