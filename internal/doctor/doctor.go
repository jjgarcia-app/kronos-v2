package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/setup"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Status represents the result of a health check.
type Status int

const (
	StatusOK   Status = iota
	StatusWarn Status = iota
	StatusFail Status = iota
)

// Check is one health check result.
type Check struct {
	Name         string
	Detail       string
	Status       Status
	FixAvailable bool
	FixLabel     string
}

// Report holds the results of all checks.
type Report struct {
	Checks []Check
}

// Run executes all checks and returns the report.
func Run(ctx context.Context, cfg config.Config) Report {
	var r Report

	r.Checks = append(r.Checks, checkConfigFile())
	r.Checks = append(r.Checks, checkDatabase(ctx, cfg))
	r.Checks = append(r.Checks, checkOllama(ctx, cfg))
	r.Checks = append(r.Checks, checkEmbeddingModel(ctx, cfg))
	r.Checks = append(r.Checks, checkClaudeHooks())
	r.Checks = append(r.Checks, checkBinaryInPath())

	return r
}

// Fix attempts to repair the issue identified by checkName.
// Progress lines are sent to the progress channel.
func Fix(ctx context.Context, cfg config.Config, checkName string, progress chan<- string) error {
	defer close(progress)

	switch checkName {
	case "Config file":
		return fixConfigFile(cfg, progress)
	case "Base de datos":
		if cfg.DB.Backend == "postgres" {
			return fixPostgresDB(ctx, cfg, progress)
		}
		return fixDatabase(ctx, cfg, progress)
	case "Ollama":
		return fixOllama(cfg, progress)
	case "Modelo embeddings":
		return fixEmbeddingModel(cfg, progress)
	case "Hooks Claude Code":
		return fixClaudeHooks(progress)
	case "Binario en PATH":
		progress <- "Instala Kronos en tu PATH: mueve kronos.exe a un directorio incluido en %PATH%"
		return nil
	default:
		return fmt.Errorf("fix no disponible para: %s", checkName)
	}
}

// --- individual checks ---

func checkConfigFile() Check {
	path, err := config.ConfigPath()
	if err != nil {
		return Check{
			Name:         "Config file",
			Detail:       fmt.Sprintf("no se puede determinar la ruta: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Crear config con valores por defecto",
		}
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Check{
			Name:         "Config file",
			Detail:       fmt.Sprintf("%s no existe", path),
			Status:       StatusWarn,
			FixAvailable: true,
			FixLabel:     "Crear config con valores por defecto",
		}
	}
	if err != nil {
		return Check{
			Name:         "Config file",
			Detail:       fmt.Sprintf("error leyendo %s: %v", path, err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Recrear config",
		}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return Check{
			Name:         "Config file",
			Detail:       fmt.Sprintf("JSON inválido: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Recrear config",
		}
	}
	return Check{
		Name:   "Config file",
		Detail: path,
		Status: StatusOK,
	}
}

func checkDatabase(ctx context.Context, cfg config.Config) Check {
	if cfg.DB.Backend == "postgres" {
		return checkPostgresDB(ctx, cfg)
	}
	return checkSQLiteDB(cfg)
}

func checkSQLiteDB(cfg config.Config) Check {
	dbPath, err := platform.DBPath()
	if err != nil {
		return Check{
			Name:         "Base de datos",
			Detail:       fmt.Sprintf("no se puede determinar la ruta: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Inicializar base de datos",
		}
	}
	if cfg.DB.SQLitePath != "" {
		dbPath = cfg.DB.SQLitePath
	}
	st, err := store.New(dbPath)
	if err != nil {
		return Check{
			Name:         "Base de datos",
			Detail:       fmt.Sprintf("error abriendo DB: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Inicializar base de datos",
		}
	}
	st.Close()
	return Check{Name: "Base de datos", Detail: dbPath, Status: StatusOK}
}

func checkPostgresDB(ctx context.Context, cfg config.Config) Check {
	if cfg.DB.PostgresDSN == "" {
		return Check{
			Name:         "Base de datos",
			Detail:       "backend=postgres pero db.postgres_dsn está vacío",
			Status:       StatusFail,
			FixAvailable: false,
		}
	}
	st, err := store.NewPostgres(cfg.DB.PostgresDSN)
	if err != nil {
		return Check{
			Name:         "Base de datos",
			Detail:       fmt.Sprintf("no se puede conectar a postgres: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Iniciar PostgreSQL en Docker",
		}
	}
	st.Close()
	return Check{Name: "Base de datos", Detail: "postgres OK", Status: StatusOK}
}

func checkOllama(ctx context.Context, cfg config.Config) Check {
	url := cfg.Embeddings.OllamaURL
	if url == "" {
		url = "http://localhost:11434"
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		return Check{
			Name:         "Ollama",
			Detail:       fmt.Sprintf("no responde en %s: %v", url, err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Instalar / iniciar Ollama",
		}
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return Check{
			Name:         "Ollama",
			Detail:       fmt.Sprintf("%s respondió HTTP %d", url, resp.StatusCode),
			Status:       StatusWarn,
			FixAvailable: false,
		}
	}
	return Check{
		Name:   "Ollama",
		Detail: url + " OK",
		Status: StatusOK,
	}
}

func checkEmbeddingModel(ctx context.Context, cfg config.Config) Check {
	url := cfg.Embeddings.OllamaURL
	if url == "" {
		url = "http://localhost:11434"
	}
	model := cfg.Embeddings.OllamaModel
	if model == "" {
		model = "bge-m3"
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		return Check{
			Name:   "Modelo embeddings",
			Detail: "Ollama no disponible — omitiendo",
			Status: StatusWarn,
		}
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Check{
			Name:   "Modelo embeddings",
			Detail: "no se pudo leer la lista de modelos",
			Status: StatusWarn,
		}
	}

	for _, m := range result.Models {
		if m.Name == model || m.Name == model+":latest" {
			return Check{
				Name:   "Modelo embeddings",
				Detail: model + " instalado",
				Status: StatusOK,
			}
		}
	}

	return Check{
		Name:         "Modelo embeddings",
		Detail:       fmt.Sprintf("%s no encontrado en Ollama", model),
		Status:       StatusFail,
		FixAvailable: true,
		FixLabel:     fmt.Sprintf("Descargar %s", model),
	}
}

func checkClaudeHooks() Check {
	claudeDir, err := platform.ClaudeDir()
	if err != nil {
		return Check{
			Name:         "Hooks Claude Code",
			Detail:       fmt.Sprintf("no se puede determinar ~/.claude: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Instalar hooks",
		}
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		return Check{
			Name:         "Hooks Claude Code",
			Detail:       settingsPath + " no existe",
			Status:       StatusWarn,
			FixAvailable: true,
			FixLabel:     "Instalar hooks",
		}
	}
	if err != nil {
		return Check{
			Name:         "Hooks Claude Code",
			Detail:       fmt.Sprintf("error leyendo settings: %v", err),
			Status:       StatusFail,
			FixAvailable: true,
			FixLabel:     "Instalar hooks",
		}
	}

	if !containsBytes(data, []byte("kronos hook")) {
		return Check{
			Name:         "Hooks Claude Code",
			Detail:       "hooks de Kronos no encontrados en settings.json",
			Status:       StatusWarn,
			FixAvailable: true,
			FixLabel:     "Instalar hooks",
		}
	}

	return Check{
		Name:   "Hooks Claude Code",
		Detail: "hooks instalados en " + settingsPath,
		Status: StatusOK,
	}
}

func checkBinaryInPath() Check {
	for _, name := range []string{"kronos", "kronos.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return Check{Name: "Binario en PATH", Detail: p, Status: StatusOK}
		}
	}
	// Fallback: check if our own executable's directory is listed in PATH.
	exe, _ := os.Executable()
	exeDir := filepath.Clean(filepath.Dir(exe))
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.EqualFold(filepath.Clean(dir), exeDir) {
			return Check{Name: "Binario en PATH", Detail: exe, Status: StatusOK}
		}
	}
	return Check{
		Name:         "Binario en PATH",
		Detail:       "kronos no encontrado en PATH",
		Status:       StatusWarn,
		FixAvailable: false,
		FixLabel:     "Añadir kronos al PATH manualmente",
	}
}

// --- fix implementations ---

func fixConfigFile(cfg config.Config, progress chan<- string) error {
	path, err := config.ConfigPath()
	if err != nil {
		return err
	}
	progress <- fmt.Sprintf("Creando config en %s...", path)
	def := config.Default()
	if err := def.Save(); err != nil {
		return err
	}
	progress <- "Config creada con valores por defecto."
	return nil
}

func fixDatabase(ctx context.Context, cfg config.Config, progress chan<- string) error {
	dbPath, err := platform.DBPath()
	if err != nil {
		return err
	}
	if cfg.DB.SQLitePath != "" {
		dbPath = cfg.DB.SQLitePath
	}
	progress <- fmt.Sprintf("Creando directorio %s...", filepath.Dir(dbPath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return err
	}
	progress <- "Inicializando base de datos y migraciones..."
	st, err := store.New(dbPath)
	if err != nil {
		return err
	}
	st.Close()
	progress <- "Base de datos inicializada."
	return nil
}

func fixPostgresDB(ctx context.Context, cfg config.Config, progress chan<- string) error {
	progress <- "Iniciando PostgreSQL en Docker..."
	cmd := exec.Command("docker", "run", "-d", "--name", "kronos-postgres",
		"-e", "POSTGRES_PASSWORD=kronos",
		"-e", "POSTGRES_DB=kronos",
		"-p", "5432:5432",
		"postgres:16-alpine")
	if out, err := cmd.CombinedOutput(); err != nil {
		progress <- fmt.Sprintf("docker run: %s — %v", string(out), err)
	} else {
		progress <- "Contenedor kronos-postgres iniciado."
		progress <- "DSN sugerido: postgres://postgres:kronos@localhost:5432/kronos"
		progress <- "Configura con: kronos config set db.postgres_dsn postgres://postgres:kronos@localhost:5432/kronos"
	}
	return nil
}

func fixOllama(cfg config.Config, progress chan<- string) error {
	model := cfg.Embeddings.OllamaModel
	if model == "" {
		model = "bge-m3"
	}

	if cfg.Embeddings.OllamaDocker {
		progress <- "Iniciando Ollama en Docker..."
		cmd := exec.Command("docker", "run", "-d", "--name", "kronos-ollama",
			"-p", "11434:11434", "ollama/ollama")
		if out, err := cmd.CombinedOutput(); err != nil {
			progress <- fmt.Sprintf("docker run: %s — %v", string(out), err)
		} else {
			progress <- "Contenedor kronos-ollama iniciado."
		}
	} else if runtime.GOOS == "windows" {
		progress <- "Descarga Ollama desde: https://ollama.com/download"
		progress <- "Instala el ejecutable y luego ejecuta: ollama serve"
	} else {
		progress <- "Instala Ollama: curl -fsSL https://ollama.com/install.sh | sh"
		progress <- "Luego: ollama serve &"
	}

	progress <- fmt.Sprintf("Descargando modelo %s...", model)
	cmd := exec.Command("ollama", "pull", model)
	cmd.Stdout = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		progress <- fmt.Sprintf("ollama pull: %s — %v", string(out), err)
		return fmt.Errorf("ollama pull %s: %w", model, err)
	}
	progress <- fmt.Sprintf("Modelo %s descargado.", model)
	return nil
}

func fixEmbeddingModel(cfg config.Config, progress chan<- string) error {
	model := cfg.Embeddings.OllamaModel
	if model == "" {
		model = "bge-m3"
	}
	progress <- fmt.Sprintf("Descargando modelo %s...", model)
	cmd := exec.Command("ollama", "pull", model)
	if out, err := cmd.CombinedOutput(); err != nil {
		progress <- fmt.Sprintf("error: %s", string(out))
		return fmt.Errorf("ollama pull %s: %w", model, err)
	}
	progress <- fmt.Sprintf("Modelo %s descargado.", model)
	return nil
}

func fixClaudeHooks(progress chan<- string) error {
	progress <- "Instalando hooks de Kronos en ~/.claude/settings.json..."
	if err := setup.InstallClaudeCode(); err != nil {
		return err
	}
	progress <- "Hooks instalados."
	return nil
}

func containsBytes(haystack, needle []byte) bool {
	return len(haystack) > 0 && len(needle) > 0 &&
		bytesIndex(haystack, needle) >= 0
}

func bytesIndex(s, sub []byte) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if string(s[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}
