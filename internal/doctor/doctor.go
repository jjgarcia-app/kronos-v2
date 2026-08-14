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
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
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
	if cfg.DB.Backend == "postgres" {
		r.Checks = append(r.Checks, checkSyncQueue(ctx))
	}

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
		model = embeddings.DefaultOllamaModel
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

	// Antes buscaba el literal "kronos hook" — dejó de matchear cuando los
	// hooks pasaron a usar ruta absoluta ("kronos.exe hook ...", con .exe
	// en el medio), reportando un falso "no instalado" con hooks realmente
	// bien instalados. "hook " a secas matchea ambas formas (pelada y con
	// ruta absoluta, cualquier SO).
	if !containsBytes(data, []byte("hook ")) {
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

// checkBinaryInPath detecta no solo si kronos está en PATH, sino si hay MÁS
// DE UNA copia — el bug real que causó una sesión entera de debugging
// confuso: dos kronos.exe en PATH (uno usado por los hooks vía PATH lookup,
// otro por el MCP server vía ruta absoluta) que se desincronizaron sin
// ningún aviso. Ahora los hooks también usan ruta absoluta (kronosBin()),
// así que ya no puede romper silenciosamente — pero dos copias siguen
// siendo una fuente de confusión (¿cuál se actualiza con `go install`?
// ¿cuál corre `kronos setup`?) que vale la pena señalar.
func checkBinaryInPath() Check {
	found := findKronosBinariesInPath()

	if len(found) == 0 {
		// Fallback: nuestro propio ejecutable puede no llamarse "kronos" en
		// el PATH exacto (ej. corrido directo con ./kronos.exe) — si su
		// directorio igual está en PATH, cuenta como encontrado.
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

	if len(found) > 1 {
		return Check{
			Name: "Binario en PATH",
			Detail: fmt.Sprintf("%d copias de kronos en PATH — pueden desincronizarse sin aviso: %s",
				len(found), strings.Join(found, ", ")),
			Status:       StatusWarn,
			FixAvailable: false,
			FixLabel:     "Dejar una sola copia de kronos en PATH y borrar las demás",
		}
	}

	return Check{Name: "Binario en PATH", Detail: found[0], Status: StatusOK}
}

// findKronosBinariesInPath escanea CADA directorio de PATH (no solo el
// primero que resuelva exec.LookPath) buscando un binario kronos,
// deduplicando por ruta real resuelta (symlinks).
func findKronosBinariesInPath() []string {
	names := []string{"kronos", "kronos.exe"}
	seen := map[string]bool{}
	var found []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, name := range names {
			p := filepath.Join(dir, name)
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			resolved := p
			if real, err := filepath.EvalSymlinks(p); err == nil {
				resolved = real
			}
			key := strings.ToLower(resolved)
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, p)
		}
	}
	return found
}

func checkSyncQueue(ctx context.Context) Check {
	dbPath, err := platform.DBPath()
	if err != nil {
		return Check{Name: "Sync queue", Detail: "no se pudo leer", Status: StatusWarn}
	}
	st, err := store.New(dbPath)
	if err != nil {
		return Check{Name: "Sync queue", Detail: "no se pudo abrir DB local", Status: StatusWarn}
	}
	defer st.Close()

	n := st.SyncQueueCount(ctx)
	if n == 0 {
		return Check{Name: "Sync queue", Detail: "sin pendientes", Status: StatusOK}
	}
	return Check{
		Name:         "Sync queue",
		Detail:       fmt.Sprintf("%d observación(es) pendiente(s) de sync → ejecuta: kronos sync --pg-flush", n),
		Status:       StatusWarn,
		FixAvailable: false,
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

// dockerCLITimeout acota cada comando docker individual (info/inspect/
// start/run) — nada de esto debería tardar más que unos segundos en
// condiciones normales; sin límite, un Docker Desktop colgado (no caído,
// colgado) puede dejar `kronos doctor --fix` esperando indefinidamente.
const dockerCLITimeout = 10 * time.Second

// checkDockerAvailable distingue las dos causas reales por las que
// cualquier comando docker puede fallar — antes ambas terminaban en el
// mismo error crudo de "docker run", sin decir cuál era: el CLI de docker
// no instalado (raro), vs. instalado pero el daemon/Docker Desktop no está
// corriendo (el caso real que motivó este fix — Postgres Y Ollama caídos
// juntos porque Docker Desktop no había arrancado, no porque cada
// contenedor individualmente fallara).
func checkDockerAvailable(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker no está instalado — instalá Docker Desktop: https://www.docker.com/products/docker-desktop")
	}
	ctx, cancel := context.WithTimeout(ctx, dockerCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Docker Desktop no está corriendo (docker info falló: %s) — arrancá Docker Desktop y reintentá", strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerContainerRunning reporta si un contenedor existe y si está
// corriendo — "existe" y "corre" son dos preguntas distintas a propósito:
// determina si corresponde `docker start` (ya existe, solo hay que
// levantarlo) o `docker run` (crearlo desde cero la primera vez). Antes
// esto siempre usaba `docker run`, que falla con "el nombre ya está en
// uso" si el contenedor ya existía de una corrida anterior — un error que
// tapaba la causa real (Docker Desktop caído) con uno secundario y
// confuso.
func dockerContainerRunning(ctx context.Context, name string) (exists, running bool) {
	ctx, cancel := context.WithTimeout(ctx, dockerCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", name)
	out, err := cmd.Output()
	if err != nil {
		return false, false // no existe (o docker no responde — ya se chequeó antes)
	}
	return true, strings.TrimSpace(string(out)) == "true"
}

// dockerStartOrRun levanta un contenedor existente (docker start) o lo crea
// si nunca existió (docker run con runArgs) — nunca crea uno nuevo cuando
// ya hay uno parado con el mismo nombre.
func dockerStartOrRun(ctx context.Context, name string, runArgs []string, progress chan<- string) error {
	exists, running := dockerContainerRunning(ctx, name)
	if running {
		progress <- fmt.Sprintf("Contenedor %s ya estaba corriendo.", name)
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, dockerCLITimeout)
	defer cancel()

	if exists {
		progress <- fmt.Sprintf("Contenedor %s existe pero está parado, iniciando (docker start)...", name)
		out, err := exec.CommandContext(runCtx, "docker", "start", name).CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker start %s: %s: %w", name, strings.TrimSpace(string(out)), err)
		}
		progress <- fmt.Sprintf("Contenedor %s iniciado.", name)
		return nil
	}

	out, err := exec.CommandContext(runCtx, "docker", runArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %s: %w", strings.TrimSpace(string(out)), err)
	}
	progress <- fmt.Sprintf("Contenedor %s creado e iniciado.", name)
	return nil
}

func fixPostgresDB(ctx context.Context, cfg config.Config, progress chan<- string) error {
	if err := checkDockerAvailable(ctx); err != nil {
		progress <- err.Error()
		return err
	}
	progress <- "Iniciando PostgreSQL en Docker..."
	err := dockerStartOrRun(ctx, "kronos-postgres", []string{
		"run", "-d", "--name", "kronos-postgres",
		"-e", "POSTGRES_PASSWORD=kronos",
		"-e", "POSTGRES_DB=kronos",
		"-p", "5432:5432",
		"postgres:16-alpine",
	}, progress)
	if err != nil {
		progress <- err.Error()
		return err
	}
	progress <- "DSN sugerido: postgres://postgres:kronos@localhost:5432/kronos"
	progress <- "Configura con: kronos config set db.postgres_dsn postgres://postgres:kronos@localhost:5432/kronos"
	return nil
}

func fixOllama(cfg config.Config, progress chan<- string) error {
	model := cfg.Embeddings.OllamaModel
	if model == "" {
		model = embeddings.DefaultOllamaModel
	}

	if cfg.Embeddings.OllamaDocker {
		ctx := context.Background()
		if err := checkDockerAvailable(ctx); err != nil {
			progress <- err.Error()
			return err
		}
		progress <- "Iniciando Ollama en Docker..."
		if err := dockerStartOrRun(ctx, "kronos-ollama", []string{
			"run", "-d", "--name", "kronos-ollama",
			"-p", "11434:11434", "ollama/ollama",
		}, progress); err != nil {
			progress <- err.Error()
			return err
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
		model = embeddings.DefaultOllamaModel
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
