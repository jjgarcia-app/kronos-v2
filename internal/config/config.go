package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

type DBConfig struct {
	Backend        string `json:"backend"`
	SQLitePath     string `json:"sqlite_path"`
	PostgresDSN    string `json:"postgres_dsn"`
	PostgresDocker bool   `json:"postgres_docker"`
	// LocalOnlyProjects: nombres de proyecto (ya normalizados, ver
	// internal/project.Normalize) que nunca deben escribirse al primary
	// remoto — se quedan solo en el buffer SQLite local, sin encolarse para
	// sync. Pensado para cuando primary es una DB remota/compartida (el
	// camino a sync entre máquinas) y hay proyectos que el usuario no
	// quiere que salgan de esta máquina.
	LocalOnlyProjects []string `json:"local_only_projects"`
}

type EmbeddingsConfig struct {
	Provider       string `json:"provider"`
	OllamaURL      string `json:"ollama_url"`
	OllamaModel    string `json:"ollama_model"`
	OllamaLLMModel string `json:"ollama_llm_model"`
	OllamaDocker   bool   `json:"ollama_docker"`
	AnthropicKey   string `json:"anthropic_api_key"`
	OpenAIKey      string `json:"openai_api_key"`
}

type MemoryConfig struct {
	MaxObservationLength int `json:"max_observation_length"`
	MaxSearchResults     int `json:"max_search_results"`
	MaxContextResults    int `json:"max_context_results"`
	DedupeWindowMinutes  int `json:"dedupe_window_minutes"`
	RetentionDays        int `json:"retention_days"`
}

type NudgeConfig struct {
	ActionsThreshold int `json:"actions_threshold"`
	FallbackMinutes  int `json:"fallback_minutes"`
}

type SecretsConfig struct {
	Enabled bool `json:"enabled"`
}

type ExportConfig struct {
	DefaultOutput string `json:"default_output"`
	// Enabled activa el mirror en vivo: cada mem_save/update/delete se
	// refleja en DefaultOutput como vault de Obsidian, además del dump
	// manual vía `kronos export`. Default false — opt-in.
	Enabled bool `json:"enabled"`
}

// VaultConfig controla el camino de vuelta vault → base (`kronos vault
// import`, ver internal/obsidian/vault_import.go). Apagado por defecto: leer
// del vault y escribir en la base es más delicado que el camino normal
// (base → vault), así que arranca en modo explícito, no automático.
type VaultConfig struct {
	// AutoImportOnExport: si está en true, `kronos export` corre primero un
	// import en dry-run y avisa conflictos antes de exportar. Nunca escribe
	// en la base por sí solo (el import automático siempre es dry-run).
	AutoImportOnExport bool `json:"auto_import_on_export"`
	// ImportMaxConflictsReport limita cuántos conflictos se listan en
	// detalle por stdout/stderr en `kronos vault import` (el conteo total
	// del resumen siempre es completo).
	ImportMaxConflictsReport int `json:"import_max_conflicts_report"`
}

type LLMConfig struct {
	Provider string `json:"provider"` // ollama | openai | openai-compatible | anthropic | disabled
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
}

// CoreConfig controla el bloque siempre-presente que SessionStart inyecta
// en cada arranque (ver internal/hooks/core_block.go). Existe porque medido
// en producción, mem_search se llamó 60 veces sobre 9.470 prompts (0,63%):
// pedirle al agente que consulte memoria a mano no funciona, así que el
// contexto relevante tiene que aparecer solo, acotado por presupuesto.
type CoreConfig struct {
	Enabled           bool `json:"enabled"`
	CharsLimit        int  `json:"chars_limit"`
	MaxItems          int  `json:"max_items"`
	IncludeCheckpoint bool `json:"include_checkpoint"`
	// MaxGlobalChars: presupuesto máximo, en chars, para observaciones
	// scope=global dentro del bloque core (se renderizan comprimidas: tipo +
	// título, sin "Qué: ..."). Medido en benchmark 2026-09-11: sin este tope,
	// 9 observaciones globales sin comprimir ocupaban ~1890/2000 chars —
	// el bloque entero, y eran todas de OTRO proyecto.
	MaxGlobalChars int `json:"max_global_chars"`
	// ProjectMinChars: reserva mínima, en chars, para contenido del
	// proyecto actual — limita cuánto de lo que sobra puede gastar la
	// sección global antes de dejarle lugar al proyecto (ver
	// internal/hooks/core_block.go).
	ProjectMinChars int `json:"project_min_chars"`
	// MaxPerType: tope de items por tipo dentro del bloque. Medido en
	// producción (proyecto kronos-v2, 2026-09-11): 6 de 7 items de proyecto
	// eran [architecture], varios del mismo hilo de trabajo del día — un
	// solo tipo se comía casi todo el bloque. 0 usa el default (3).
	MaxPerType int `json:"max_per_type"`
	// MaxItemChars: tope de caracteres por línea de item (tipo + título +
	// resumen). 0 usa el default (110).
	MaxItemChars int `json:"max_item_chars"`
	// StaleDays: a partir de cuántos días sin actualización una decisión o
	// arquitectura se marca "(antiguo)" en el bloque, para que el agente
	// sepa que puede estar desactualizada. 0 usa el default (90).
	StaleDays int `json:"stale_days"`
}

// RecallConfig controla la inyección por relevancia en UserPromptSubmit (ver
// internal/hooks/prompt_submit.go): mismo hallazgo que motivó CoreConfig
// (60/9.470 mem_search, 0,63%), pero acá el disparador es cada prompt del
// usuario en vez de solo el arranque de sesión — así que el presupuesto por
// defecto es más chico y el timeout más corto, porque esto corre con mucha
// más frecuencia y no puede arriesgar la latencia del turno.
//
// Estrategia FTS-first (ver runRecall): medido en esta máquina, un embedding
// síncrono contra Ollama tarda entre 800ms y 6s según carga (Ollama
// compartido con el daemon y otras sesiones), mientras que FTS5 sobre las
// observaciones existentes responde en milisegundos, sin red ni LLM de por
// medio. Por eso FTS corre siempre primero (determinístico y rápido) y el
// camino vectorial es una mejora oportunista que solo se paga cuando FTS no
// encontró nada — nunca el camino principal.
type RecallConfig struct {
	Enabled       bool    `json:"enabled"`
	K             int     `json:"k"`
	MinSimilarity float64 `json:"min_similarity"`
	CharsLimit    int     `json:"chars_limit"`
	TimeoutMs     int     `json:"timeout_ms"`
	// FallbackFTS habilita el camino FTS (siempre el primero en intentarse).
	// Default true — desactivarlo solo tiene sentido para aislar el camino
	// vectorial en pruebas.
	FallbackFTS bool `json:"fallback_fts"`
	// MinFTSResults es la cantidad mínima de resultados FTS para darlos por
	// buenos e inyectarlos sin gastar un embedding. Default 1: cualquier
	// match FTS real es preferible a esperar un round-trip a Ollama.
	MinFTSResults int `json:"min_fts_results"`
	// VectorOnFTSMiss controla si se intenta el camino vectorial cuando FTS
	// no llega a MinFTSResults. Default true — es la mejora oportunista, pero
	// puede desactivarse en máquinas donde ni vale la pena el intento.
	VectorOnFTSMiss bool `json:"vector_on_fts_miss"`
	// MinMatchedTerms: con la query FTS armada por OR (ver runRecall), un
	// resultado puede matchear con un solo término de tres — demasiado débil
	// para inyectarlo como si fuera relevante. Medido en la ronda 2 del
	// benchmark: "alfresco aspect remove" (3 términos) con el AND implícito
	// anterior daba 0 filas porque exigía los tres en la misma observación;
	// con OR puro cualquier observación que mencione UNA vez "aspect" ya
	// entra, ruido igual de malo que el AND. La guarda de precisión: para
	// prompts con ≥3 términos significativos, exigir que al menos
	// MinMatchedTerms (verificados contra título+contenido, no solo lo que
	// reporta el motor FTS) estén presentes. Prompts de 1-2 términos —
	// "postgres driver"— no tienen margen para exigir 2, así que ahí alcanza
	// con 1. Default 2.
	MinMatchedTerms int `json:"min_matched_terms"`
	// TotalBudgetMs es el techo real de tiempo para FTS+vector combinados en
	// runRecall — reemplaza en la práctica a TimeoutMs como el límite que de
	// verdad importa (se usa min(TimeoutMs, TotalBudgetMs) como deadline de
	// ctx2, así que TimeoutMs sigue siendo compatible para quien ya lo tenía
	// customizado más chico). Medido: un hook de UserPromptSubmit que tarda
	// 1500ms en el peor caso (default viejo de TimeoutMs) se siente en cada
	// prompt del usuario; 400ms es el punto donde FTS (milisegundos) más un
	// intento vectorial corto todavía entran sin que el turno se note lento.
	TotalBudgetMs int `json:"total_budget_ms"`
	// VectorProbeMs es el umbral de la sonda barata que decide si el
	// proveedor de embeddings "viene caliente": si la ÚLTIMA llamada real
	// (ver embeddings.VectorStore.LastLatency) tardó más que esto, se asume
	// que Ollama está lento AHORA (carga compartida con el daemon u otras
	// sesiones, medido entre 800ms y 6s en esta máquina) y se saltea el
	// intento vectorial en vez de gastar el presupuesto entero esperándolo.
	// Es una sonda barata a propósito: no dispara una llamada nueva, solo lee
	// la duración de la llamada anterior — un round-trip real (aunque sea
	// solo para medir) costaría lo mismo que el intento que se quiere evitar.
	// Sin datos previos (primera llamada del proceso) se asume caliente.
	// Default 300ms.
	VectorProbeMs int `json:"vector_probe_ms"`
}

// ConsolidationConfig controla la consolidación de duplicados semánticos
// (kronos gc --consolidate y su equivalente periódico en el daemon). Apagada
// por defecto: fusionar observaciones es una operación de juicio, no algo
// para dejar corriendo solo sin que nadie mire el reporte primero.
type ConsolidationConfig struct {
	Enabled            bool    `json:"enabled"`
	IntervalHours      int     `json:"interval_hours"`
	Threshold          float64 `json:"threshold"`
	RequireSameType    bool    `json:"require_same_type"`
	RequireSameProject bool    `json:"require_same_project"`
}

// RelationsConfig controla el filtro anti-ruido de FindCandidates — el
// detector de candidatos a relación que corre tras cada mem_save sobre el
// store local (ver internal/mcp/handlers.go). Medido en producción el
// 2026-09-11: con el BM25Floor permisivo (-2.0) y sin filtro de tokens
// compartidos ni de tipo, cada mem_save con un título que compartía UNA sola
// palabra común con otra observación ("kronos", "fix", "session") generaba
// 2-3 "Conflictos potenciales detectados" — ese día se juzgaron 11 pendientes
// a mano, los 11 falsos positivos. RequireSameType por sí solo ya elimina los
// cruces preferencia↔bugfix que causaron la mayoría.
type RelationsConfig struct {
	BM25Floor       float64 `json:"bm25_floor"`
	MinSharedTokens int     `json:"min_shared_tokens"`
	RequireSameType bool    `json:"require_same_type"`
	CandidatesLimit int     `json:"candidates_limit"`
}

type Config struct {
	DB            DBConfig            `json:"db"`
	Embeddings    EmbeddingsConfig    `json:"embeddings"`
	LLM           LLMConfig           `json:"llm"`
	Memory        MemoryConfig        `json:"memory"`
	Nudge         NudgeConfig         `json:"nudge"`
	Secrets       SecretsConfig       `json:"secrets"`
	Export        ExportConfig        `json:"export"`
	Vault         VaultConfig         `json:"vault"`
	Core          CoreConfig          `json:"core"`
	Recall        RecallConfig        `json:"recall"`
	Consolidation ConsolidationConfig `json:"consolidation"`
	Relations     RelationsConfig     `json:"relations"`
	APIToken      string              `json:"api_token"`
}

// Default returns a Config populated with sensible defaults.
func Default() Config {
	return Config{
		DB: DBConfig{
			Backend: "sqlite",
		},
		Embeddings: EmbeddingsConfig{
			Provider:       "ollama",
			OllamaURL:      "http://localhost:11434",
			OllamaModel:    "nomic-embed-text",
			OllamaLLMModel: "llama3.2",
		},
		Memory: MemoryConfig{
			MaxObservationLength: 50000,
			MaxSearchResults:     20,
			MaxContextResults:    10,
			DedupeWindowMinutes:  15,
		},
		Nudge: NudgeConfig{
			ActionsThreshold: 10,
			FallbackMinutes:  20,
		},
		Secrets: SecretsConfig{
			Enabled: true,
		},
		Export: ExportConfig{
			DefaultOutput: "~/kronos-vault",
		},
		Vault: VaultConfig{
			AutoImportOnExport:       false,
			ImportMaxConflictsReport: 10,
		},
		Core: CoreConfig{
			Enabled:           true,
			CharsLimit:        2000,
			MaxItems:          12,
			IncludeCheckpoint: true,
			MaxGlobalChars:    800,
			ProjectMinChars:   600,
			MaxPerType:        3,
			MaxItemChars:      110,
			StaleDays:         90,
		},
		Recall: RecallConfig{
			Enabled: true,
			K:       3,
			// 0.62: punto medio medido entre los dos mejores matches
			// legítimos reales contra Ollama/nomic-embed-text en esta
			// máquina — "alfresco aspect remove" dio 0.62 y "postgres
			// driver" dio 0.66 de similitud coseno real. El default
			// anterior (0.72) nunca disparaba con ninguno de los dos: quedaba
			// por encima de ambos, así que el camino vectorial jamás se
			// activaba en la práctica.
			MinSimilarity: 0.62,
			CharsLimit:    600,
			// 1500ms: el round-trip real de un embedding contra Ollama en
			// esta máquina midió entre 800ms y 6s (4-6 cores, Ollama
			// compartido con el daemon y otras sesiones de trabajo). Con
			// FTS-first, este timeout ya no acota el camino principal (FTS
			// responde en milisegundos) — es presupuesto exclusivo para el
			// intento vectorial oportunista que solo se paga si FTS no
			// encontró nada. 800ms (default anterior, pensado para acotar
			// TODO el recall) cortaba ese intento casi siempre antes de que
			// Ollama llegara a responder.
			TimeoutMs:       1500,
			FallbackFTS:     true,
			MinFTSResults:   1,
			VectorOnFTSMiss: true,
			MinMatchedTerms: 2,
			TotalBudgetMs:   400,
			VectorProbeMs:   300,
		},
		Consolidation: ConsolidationConfig{
			Enabled:            false,
			IntervalHours:      24,
			Threshold:          0.93,
			RequireSameType:    true,
			RequireSameProject: true,
		},
		Relations: RelationsConfig{
			BM25Floor:       -6.0,
			MinSharedTokens: 2,
			RequireSameType: true,
			CandidatesLimit: 3,
		},
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	dir, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config from disk, applying defaults for missing fields.
func Load() (Config, error) {
	cfg := Default()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	// Apply defaults for zero-value fields
	def := Default()
	if cfg.DB.Backend == "" {
		cfg.DB.Backend = def.DB.Backend
	}
	if cfg.Embeddings.Provider == "" {
		cfg.Embeddings.Provider = def.Embeddings.Provider
	}
	if cfg.Embeddings.OllamaURL == "" {
		cfg.Embeddings.OllamaURL = def.Embeddings.OllamaURL
	}
	if cfg.Embeddings.OllamaModel == "" {
		cfg.Embeddings.OllamaModel = def.Embeddings.OllamaModel
	}
	if cfg.Embeddings.OllamaLLMModel == "" {
		cfg.Embeddings.OllamaLLMModel = def.Embeddings.OllamaLLMModel
	}
	if cfg.Memory.MaxObservationLength == 0 {
		cfg.Memory.MaxObservationLength = def.Memory.MaxObservationLength
	}
	if cfg.Memory.MaxSearchResults == 0 {
		cfg.Memory.MaxSearchResults = def.Memory.MaxSearchResults
	}
	if cfg.Memory.MaxContextResults == 0 {
		cfg.Memory.MaxContextResults = def.Memory.MaxContextResults
	}
	if cfg.Memory.DedupeWindowMinutes == 0 {
		cfg.Memory.DedupeWindowMinutes = def.Memory.DedupeWindowMinutes
	}
	if cfg.Nudge.ActionsThreshold == 0 {
		cfg.Nudge.ActionsThreshold = def.Nudge.ActionsThreshold
	}
	if cfg.Nudge.FallbackMinutes == 0 {
		cfg.Nudge.FallbackMinutes = def.Nudge.FallbackMinutes
	}
	if cfg.Export.DefaultOutput == "" {
		cfg.Export.DefaultOutput = def.Export.DefaultOutput
	}
	if cfg.Vault.ImportMaxConflictsReport == 0 {
		cfg.Vault.ImportMaxConflictsReport = def.Vault.ImportMaxConflictsReport
	}
	if cfg.Consolidation.IntervalHours == 0 {
		cfg.Consolidation.IntervalHours = def.Consolidation.IntervalHours
	}
	if cfg.Consolidation.Threshold == 0 {
		cfg.Consolidation.Threshold = def.Consolidation.Threshold
	}

	return cfg, nil
}

// Save writes the config to disk.
func (c Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// Set updates a config field by dot-notation key (e.g. "db.backend").
func (c *Config) Set(key, value string) error {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid key %q — use format: section.field", key)
	}
	section, field := parts[0], parts[1]

	switch section {
	case "db":
		switch field {
		case "backend":
			c.DB.Backend = value
		case "sqlite_path":
			c.DB.SQLitePath = value
		case "postgres_dsn":
			c.DB.PostgresDSN = value
		case "postgres_docker":
			c.DB.PostgresDocker = parseBool(value)
		case "local_only_projects":
			c.DB.LocalOnlyProjects = parseList(value)
		default:
			return fmt.Errorf("unknown db field: %s", field)
		}
	case "embeddings":
		switch field {
		case "provider":
			c.Embeddings.Provider = value
		case "ollama_url":
			c.Embeddings.OllamaURL = value
		case "ollama_model":
			c.Embeddings.OllamaModel = value
		case "ollama_llm_model":
			c.Embeddings.OllamaLLMModel = value
		case "ollama_docker":
			c.Embeddings.OllamaDocker = parseBool(value)
		case "anthropic_api_key":
			c.Embeddings.AnthropicKey = value
		case "openai_api_key":
			c.Embeddings.OpenAIKey = value
		default:
			return fmt.Errorf("unknown embeddings field: %s", field)
		}
	case "memory":
		switch field {
		case "max_observation_length":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.MaxObservationLength = n
		case "max_search_results":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.MaxSearchResults = n
		case "max_context_results":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.MaxContextResults = n
		case "dedupe_window_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.DedupeWindowMinutes = n
		case "retention_days":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.RetentionDays = n
		default:
			return fmt.Errorf("unknown memory field: %s", field)
		}
	case "nudge":
		switch field {
		case "actions_threshold":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Nudge.ActionsThreshold = n
		case "fallback_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Nudge.FallbackMinutes = n
		default:
			return fmt.Errorf("unknown nudge field: %s", field)
		}
	case "secrets":
		switch field {
		case "enabled":
			c.Secrets.Enabled = parseBool(value)
		default:
			return fmt.Errorf("unknown secrets field: %s", field)
		}
	case "llm":
		switch field {
		case "provider":
			c.LLM.Provider = value
		case "model":
			c.LLM.Model = value
		case "api_key":
			c.LLM.APIKey = value
		case "base_url":
			c.LLM.BaseURL = value
		default:
			return fmt.Errorf("unknown llm field: %s", field)
		}
	case "export":
		switch field {
		case "default_output":
			c.Export.DefaultOutput = value
		case "enabled":
			c.Export.Enabled = parseBool(value)
		default:
			return fmt.Errorf("unknown export field: %s", field)
		}
	case "vault":
		switch field {
		case "auto_import_on_export":
			c.Vault.AutoImportOnExport = parseBool(value)
		case "import_max_conflicts_report":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Vault.ImportMaxConflictsReport = n
		default:
			return fmt.Errorf("unknown vault field: %s", field)
		}
	case "core":
		switch field {
		case "enabled":
			c.Core.Enabled = parseBool(value)
		case "chars_limit":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.CharsLimit = n
		case "max_items":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.MaxItems = n
		case "include_checkpoint":
			c.Core.IncludeCheckpoint = parseBool(value)
		case "max_global_chars":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.MaxGlobalChars = n
		case "project_min_chars":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.ProjectMinChars = n
		case "max_per_type":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.MaxPerType = n
		case "max_item_chars":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.MaxItemChars = n
		case "stale_days":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.StaleDays = n
		default:
			return fmt.Errorf("unknown core field: %s", field)
		}
	case "recall":
		switch field {
		case "enabled":
			c.Recall.Enabled = parseBool(value)
		case "k":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.K = n
		case "min_similarity":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid float: %s", value)
			}
			c.Recall.MinSimilarity = n
		case "chars_limit":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.CharsLimit = n
		case "timeout_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.TimeoutMs = n
		case "fallback_fts":
			c.Recall.FallbackFTS = parseBool(value)
		case "min_fts_results":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.MinFTSResults = n
		case "vector_on_fts_miss":
			c.Recall.VectorOnFTSMiss = parseBool(value)
		case "min_matched_terms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.MinMatchedTerms = n
		case "total_budget_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.TotalBudgetMs = n
		case "vector_probe_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.VectorProbeMs = n
		default:
			return fmt.Errorf("unknown recall field: %s", field)
		}
	case "consolidation":
		switch field {
		case "enabled":
			c.Consolidation.Enabled = parseBool(value)
		case "interval_hours":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Consolidation.IntervalHours = n
		case "threshold":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid float: %s", value)
			}
			c.Consolidation.Threshold = f
		case "require_same_type":
			c.Consolidation.RequireSameType = parseBool(value)
		case "require_same_project":
			c.Consolidation.RequireSameProject = parseBool(value)
		default:
			return fmt.Errorf("unknown consolidation field: %s", field)
		}
	case "relations":
		switch field {
		case "bm25_floor":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid float: %s", value)
			}
			c.Relations.BM25Floor = f
		case "min_shared_tokens":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Relations.MinSharedTokens = n
		case "require_same_type":
			c.Relations.RequireSameType = parseBool(value)
		case "candidates_limit":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Relations.CandidatesLimit = n
		default:
			return fmt.Errorf("unknown relations field: %s", field)
		}
	case "root":
		switch field {
		case "api_token":
			c.APIToken = value
		default:
			return fmt.Errorf("unknown root field: %s", field)
		}
	default:
		return fmt.Errorf("unknown config section: %s", section)
	}
	return nil
}

func parseList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseBool(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}
