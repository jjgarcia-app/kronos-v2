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
	Provider string `json:"provider"` // ollama | claude-cli | openai | openai-compatible | anthropic | disabled
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
	// BreakerFailures: fallos consecutivos del LLM local (digest, captura
	// pasiva de PreCompact, judge — ver internal/llm.Breaker) que abren el
	// cortacircuitos. Motivado por un bug real medido en producción: sin
	// esto, cada llamada de generación colgada (no la del ping, que tiene su
	// propio timeout corto) se reintentaba en cada prompt sin límite, en
	// silencio, y el proceso de Ollama colgado le robaba CPU al resto de las
	// sesiones. Default 3.
	BreakerFailures int `json:"breaker_failures"`
	// BreakerMinutes: cuánto tiempo queda abierto el cortacircuitos una vez
	// que se abre — durante esa ventana no se intenta ninguna llamada al LLM
	// local. Default 30.
	BreakerMinutes int `json:"breaker_minutes"`
	// CLIPath: binario a invocar cuando Provider es "claude-cli" (ver
	// internal/llm.NewClaudeCLIFromConfig) — se resuelve por PATH si no es
	// una ruta absoluta. Default "claude".
	CLIPath string `json:"cli_path"`
	// TimeoutMs acota cuánto puede tardar una llamada de generación con
	// Provider "claude-cli" — al vencer se mata el subproceso. Medido en
	// vivo: `claude -p --model haiku` con un prompt trivial tardó 6s; 30000
	// (default) deja margen para prompts más largos sin dejar un hook
	// colgado indefinidamente.
	TimeoutMs int `json:"timeout_ms"`
	// MaxLoadPerCPU es el umbral del guardián de carga (ver
	// internal/llm.LoadGuardStatus): si load1/NumCPU supera este valor, no
	// se intenta ninguna llamada de generación (ni Ollama ni claude-cli) —
	// se saltea y se loguea en debug. Motivado por un bug real medido en
	// producción: con la máquina saturada, cada intento fallido dejaba un
	// proceso de generación local girando a >200% CPU durante minutos,
	// empeorando la carga y haciendo fallar el intento siguiente (círculo
	// vicioso). Default 1.0; 0 desactiva el guardián. No aplica a los
	// embeddings del recall (esos ya degradan a FTS por presupuesto de
	// tiempo, no por carga).
	MaxLoadPerCPU float64 `json:"max_load_per_cpu"`
}

// CoreConfig controla el bloque siempre-presente que SessionStart inyecta
// en cada arranque (ver internal/hooks/core_block.go). Existe porque medido
// en producción, mem_search se llamó 60 veces sobre 9.470 prompts (0,63%):
// pedirle al agente que consulte memoria a mano no funciona, así que el
// contexto relevante tiene que aparecer solo, acotado por presupuesto.
type CoreConfig struct {
	Enabled bool `json:"enabled"`
	// CharsLimit: presupuesto total del bloque en caracteres (alias de config:
	// "budget_chars"). 0 usa el default.
	CharsLimit        int  `json:"chars_limit"`
	MaxItems          int  `json:"max_items"`
	IncludeCheckpoint bool `json:"include_checkpoint"`
	// GlobalsMaxChars: presupuesto máximo, en chars, para observaciones
	// scope=global dentro del bloque core (se renderizan comprimidas: tipo +
	// título, sin "Qué: ..."). Medido en benchmark 2026-09-11: sin este tope,
	// 9 observaciones globales sin comprimir ocupaban ~1890/2000 chars —
	// el bloque entero, y eran todas de OTRO proyecto. Default 600 (~30% del
	// presupuesto total), bajado de 800 tras medir que incluso comprimidas
	// las globales seguían comiéndose la mitad del bloque en kronos-v2.
	GlobalsMaxChars int `json:"globals_max_chars"`
	// GlobalsMaxItems: tope máximo de CANTIDAD de observaciones globales,
	// independiente de GlobalsMaxChars — sin esto, muchas globales cortas
	// podían seguir monopolizando la lista de items (core.max_items) aunque
	// entraran cómodas en chars. Default 4.
	GlobalsMaxItems int `json:"globals_max_items"`
	// RelevanceFilter: si true (default), una observación global solo entra
	// si su proyecto de origen es el actual o comparte pertinencia real con
	// él (ver internal/hooks/core_block.go, classifyGlobalRelevance).
	// Medido 2026-09-11 (proyecto kronos-v2): sin este filtro, 6-7 de 12
	// items inyectados eran globales de OTRO proyecto (ATISA) sin relación
	// con lo que se estaba trabajando.
	RelevanceFilter bool `json:"relevance_filter"`
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
	// MaxSessionItems: tope de items de tipo session (resúmenes del agente y
	// digests automáticos) dentro del bloque. 0 usa el default (1): el bloque
	// ya trae el checkpoint ("dónde quedamos"), así que más de un resumen de
	// sesión desplaza conocimiento real. Medido 2026-09-11 contra la base real:
	// type=session era el tipo MÁS inyectado de todos (76 items históricos,
	// ~19% del total) y entra por la sección de relleno sin tope propio.
	MaxSessionItems int `json:"max_session_items"`
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
	// TotalBudgetMs es el techo real de tiempo para la fase vectorial en
	// runRecall. Medido: un hook de UserPromptSubmit que tarda 1500ms en el
	// peor caso (default viejo de TimeoutMs) se siente en cada prompt del
	// usuario; 400ms es el punto donde un intento vectorial corto todavía
	// entra sin que el turno se note lento.
	//
	// Hasta la separación de fases (ver FTSTimeoutMs) este campo acotaba
	// FTS+vector combinados compartiendo un mismo deadline — bug real medido:
	// con la máquina cargada (load 9-10) el deadline se agotaba DURANTE la
	// FTS (barata, ~2ms en Postgres) y el recall devolvía vacío aunque la FTS
	// ya tuviera el resultado en la mano. Ahora FTS tiene su propio
	// presupuesto (FTSTimeoutMs) y este campo acota solo lo que sigue después
	// — el camino vectorial oportunista, que es el caro (round-trip a Ollama,
	// 800ms-6s medidos). Regla: lo que ya se tiene, se entrega — el
	// presupuesto solo puede recortar trabajo adicional, nunca descartar lo
	// ya obtenido (ver runRecall/gatherRecallCandidates en
	// internal/hooks/prompt_submit.go).
	TotalBudgetMs int `json:"total_budget_ms"`
	// FTSTimeoutMs es el presupuesto de tiempo EXCLUSIVO de la fase FTS,
	// separado de TotalBudgetMs (que ahora acota solo la fase vectorial).
	// Medido: la FTS sobre Postgres local responde en ~2ms; 1000ms (default)
	// deja margen de sobra incluso con la máquina saturada (load 9-10), sin
	// arriesgar nunca el resultado ya obtenido por compartir deadline con el
	// round-trip a Ollama, que es la fase realmente cara. TimeoutMs (el
	// límite histórico compartido) sigue actuando como techo de
	// compatibilidad si alguien lo tenía configurado más chico que este
	// default — nunca se relaja, solo se puede volver más estricto (ver
	// capByLegacyTimeout en internal/hooks/prompt_submit.go).
	FTSTimeoutMs int `json:"fts_timeout_ms"`
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
	// MaxSessionItems: tope de items de tipo session que el recall puede
	// inyectar por prompt. 0 usa el default (1). Mismo motivo que
	// CoreConfig.MaxSessionItems: un resumen de sesión que entra al bloque
	// relevante desplaza conocimiento real, y medido contra la base real
	// type=session era el tipo más inyectado de todos.
	MaxSessionItems int `json:"max_session_items"`
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

// GateConfig controla el gate determinístico "buscar antes de editar"
// (RunPreToolUse, ver internal/hooks/pre_tool_use.go). Las env vars
// KRONOS_PRETOOL_GATE / KRONOS_GATE_BLOCK / KRONOS_GATE_TOOLS ganan sobre
// esta config si están seteadas — no rompen lo que ya haya en
// ~/.claude/settings.json de instalaciones existentes.
type GateConfig struct {
	Enabled bool     `json:"enabled"`
	Block   bool     `json:"block"`
	Tools   []string `json:"tools"`
	// MinObservations: si el proyecto (observaciones no borradas, ver
	// Storer.CountObservations) tiene menos que esto, el gate no tiene nada
	// contra qué medir "ya buscaste en este proyecto" — se deja pasar sin
	// bloquear y se loguea en debug. Medido en benchmark 2026-09-11: el gate
	// en modo bloqueo agregó 57s (117s -> 174s) a una sesión de bugfix; en un
	// proyecto con 0-4 observaciones esa búsqueda forzada es puro trámite,
	// no hay nada que mem_search pueda encontrar. Default 5.
	MinObservations int `json:"min_observations"`
	// SatisfiedByInjection: si true (default), una sesion donde kronos YA
	// inyecto memoria sin que el agente pidiera nada -- el bloque core trajo
	// al menos un item del proyecto en SessionStart, o algun recall de
	// UserPromptSubmit inyecto al menos un item (ver
	// internal/hooks/session_start.go, sess.InjectedObservationIDs) -- cuenta
	// como "sesion informada" y el gate no bloquea, aunque nunca haya
	// llamado mem_search. Medido 2026-09-11: con el bloque core y el recall
	// por relevancia ya activos, exigir ademas una busqueda explicita antes
	// del primer Edit/Write es redundante casi siempre -- la sesion gasta un
	// turno buscando algo que kronos ya le mostro.
	SatisfiedByInjection bool `json:"satisfied_by_injection"`
}

// DigestConfig controla el "digest corriente por sesión" (ver
// internal/hooks/digest.go, MaybeUpdateDigest) — el resumen automático de
// "en qué se viene trabajando" que arma el hilo de continuidad de una
// sesión sin depender de que el agente llame mem_save. Medido en producción
// el 2026-09-11: el camino con LLM nunca se completaba en esta máquina
// (Ollama colgaba en la llamada de generación, no en el ping) y fallaba
// siempre en silencio — por eso el determinístico (Enabled) es el camino
// principal y el LLM (LLMEnrichment) es una mejora oportunista, no una
// dependencia dura.
type DigestConfig struct {
	// Enabled activa el digest automático por sesión (determinístico +
	// LLM oportunista). Default true.
	Enabled bool `json:"enabled"`
	// IntervalMinutes: cuánto tiempo mínimo tiene que pasar desde la última
	// actualización del digest de una sesión antes de intentar otra.
	// Default 20.
	IntervalMinutes int `json:"interval_minutes"`
	// LLMEnrichment: si true (default), además del determinístico se
	// intenta una versión en prosa vía el LLM local, dentro del presupuesto
	// de LLMTimeoutMs. Si false, el digest queda siempre en su forma
	// determinística (útil para máquinas donde el LLM local no sirve, o
	// para desactivar el costo de Ollama sin perder el digest).
	LLMEnrichment bool `json:"llm"`
	// LLMTimeoutMs acota cuánto puede tardar el intento de enriquecimiento
	// por LLM en el daemon (los procesos de hook de vida corta usan su
	// propio presupuesto, más chico — ver cmd/kronos/hook.go). Default
	// 20000 (20s).
	LLMTimeoutMs int `json:"llm_timeout_ms"`
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
	Gate          GateConfig          `json:"gate"`
	Digest        DigestConfig        `json:"digest"`
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
		LLM: LLMConfig{
			BreakerFailures: 3,
			BreakerMinutes:  30,
			CLIPath:         "claude",
			TimeoutMs:       30000,
			MaxLoadPerCPU:   1.0,
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
			GlobalsMaxChars:   600,
			GlobalsMaxItems:   4,
			RelevanceFilter:   true,
			ProjectMinChars:   600,
			MaxPerType:        3,
			MaxItemChars:      110,
			MaxSessionItems:   1,
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
			FTSTimeoutMs:    1000,
			VectorProbeMs:   300,
			MaxSessionItems: 1,
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
		Gate: GateConfig{
			Enabled:              true,
			Block:                false,
			Tools:                []string{"Edit", "Write", "Bash"},
			MinObservations:      5,
			SatisfiedByInjection: true,
		},
		Digest: DigestConfig{
			Enabled:         true,
			IntervalMinutes: 20,
			LLMEnrichment:   true,
			LLMTimeoutMs:    20000,
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

// applyCoreAliases traduce los alias de la sección core (budget_chars →
// chars_limit, item_chars → max_item_chars) a sus campos canónicos. Si el
// archivo trae el nombre canónico, ese manda; si trae los dos, el alias se
// ignora (nunca pisa un valor explícito del nombre bueno).
func applyCoreAliases(data []byte, cfg *Config) error {
	var raw struct {
		Core map[string]json.RawMessage `json:"core"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.Core == nil {
		// Sin sección core (o JSON ya inválido, que el Load principal reporta
		// con mejor mensaje) no hay nada que traducir.
		return nil
	}

	setInt := func(alias, canonical string, dst *int) error {
		if _, ok := raw.Core[canonical]; ok {
			return nil
		}
		v, ok := raw.Core[alias]
		if !ok {
			return nil
		}
		var n int
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("config core.%s: %w", alias, err)
		}
		*dst = n
		return nil
	}

	if err := setInt("budget_chars", "chars_limit", &cfg.Core.CharsLimit); err != nil {
		return err
	}
	return setInt("item_chars", "max_item_chars", &cfg.Core.MaxItemChars)
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

	// Alias de claves en la sección core (ver CoreConfig): el brief de la ronda
	// 4 llamó "budget_chars"/"item_chars" a lo que acá se llama
	// "chars_limit"/"max_item_chars". Se aceptan los dos nombres y el canónico
	// gana si el archivo trae ambos, así ninguna config.json queda ignorada en
	// silencio. El switch de Set() acepta los alias por su lado.
	if err := applyCoreAliases(data, &cfg); err != nil {
		return cfg, err
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
	if len(cfg.Gate.Tools) == 0 {
		cfg.Gate.Tools = def.Gate.Tools
	}
	if cfg.Gate.MinObservations == 0 {
		cfg.Gate.MinObservations = def.Gate.MinObservations
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
		case "breaker_failures":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.LLM.BreakerFailures = n
		case "breaker_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.LLM.BreakerMinutes = n
		case "cli_path":
			c.LLM.CLIPath = value
		case "timeout_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.LLM.TimeoutMs = n
		case "max_load_per_cpu":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid float: %s", value)
			}
			c.LLM.MaxLoadPerCPU = f
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
		case "chars_limit", "budget_chars":
			// Mismo campo: "chars_limit" es el nombre histórico y "budget_chars"
			// el alias que quedó del brief de la ronda 4. Se aceptan los dos
			// para no romper config.json existentes.
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
		case "globals_max_chars":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.GlobalsMaxChars = n
		case "globals_max_items":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.GlobalsMaxItems = n
		case "relevance_filter":
			c.Core.RelevanceFilter = parseBool(value)
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
		case "max_item_chars", "item_chars":
			// "max_item_chars" histórico, "item_chars" alias (mismo campo).
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
		case "max_session_items":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Core.MaxSessionItems = n
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
		case "fts_timeout_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.FTSTimeoutMs = n
		case "vector_probe_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.VectorProbeMs = n
		case "max_session_items":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Recall.MaxSessionItems = n
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
	case "gate":
		switch field {
		case "enabled":
			c.Gate.Enabled = parseBool(value)
		case "block":
			c.Gate.Block = parseBool(value)
		case "tools":
			c.Gate.Tools = parseList(value)
		case "min_observations":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Gate.MinObservations = n
		case "satisfied_by_injection":
			c.Gate.SatisfiedByInjection = parseBool(value)
		default:
			return fmt.Errorf("unknown gate field: %s", field)
		}
	case "digest":
		switch field {
		case "enabled":
			c.Digest.Enabled = parseBool(value)
		case "interval_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Digest.IntervalMinutes = n
		case "llm":
			c.Digest.LLMEnrichment = parseBool(value)
		case "llm_timeout_ms":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Digest.LLMTimeoutMs = n
		default:
			return fmt.Errorf("unknown digest field: %s", field)
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
