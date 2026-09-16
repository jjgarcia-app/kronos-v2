package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// TestEvalRecallInyeccion mide el camino REAL de inyección (runRecall) contra
// el set de evaluación de kronos-eval, sin escribir nada en la base: llama a
// gatherRecallCandidates + rankAndDedupeRecallItemsOpts + formatRecallBlock,
// que son las mismas funciones que corre UserPromptSubmit, y vuelca en JSON
// qué observaciones habrían llegado al agente para cada prompt.
//
// No corre en CI ni con `go test ./...` a secas: exige KEVAL_SET.
//
//	KEVAL_SET=/home/orca/projects/Personal/kronos-eval/set.json \
//	KEVAL_OUT=/home/orca/projects/Personal/kronos-eval/inyeccion.json \
//	KEVAL_MODO=real go test -run TestEvalRecallInyeccion -v ./internal/hooks/
//
// KEVAL_MODO: "real" (config tal cual) | "fts" (solo FTS, sin vector) |
// "vector" (solo vector, sin FTS).
func TestEvalRecallInyeccion(t *testing.T) {
	setPath := os.Getenv("KEVAL_SET")
	if setPath == "" {
		t.Skip("KEVAL_SET no seteado — test de medición, no de CI")
	}
	outPath := os.Getenv("KEVAL_OUT")
	if outPath == "" {
		outPath = "/tmp/kronos-eval-inyeccion.json"
	}
	modo := os.Getenv("KEVAL_MODO")
	if modo == "" {
		modo = "real"
	}

	type promptSet struct {
		ID        string   `json:"id"`
		Project   string   `json:"project"`
		Prompt    string   `json:"prompt"`
		SessionID string   `json:"session_id"`
		Esperado  []string `json:"esperado"`
	}
	raw, err := os.ReadFile(setPath)
	if err != nil {
		t.Fatalf("leer set: %v", err)
	}
	var set []promptSet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("parsear set: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.DB.PostgresDSN == "" {
		t.Skip("sin db.postgres_dsn en la config")
	}
	st, err := store.NewPostgres(cfg.DB.PostgresDSN)
	if err != nil {
		t.Fatalf("abrir Postgres: %v", err)
	}
	defer st.Close()

	rc := cfg.Recall
	switch modo {
	case "fts":
		rc.VectorOnFTSMiss = false
	case "vector":
		rc.FallbackFTS = false
	}

	var vs *embeddings.VectorStore
	if rc.FallbackFTS && !rc.VectorOnFTSMiss {
		// nada de vector en este modo
	} else {
		dataDir, derr := platform.DataDir()
		if derr != nil {
			dataDir = ""
		}
		if v, err := embeddings.New(context.Background(), dataDir); err == nil {
			vs = v
		} else {
			t.Logf("sin VectorStore (%v) — se mide solo FTS", err)
		}
	}

	k := rc.K
	if k <= 0 {
		k = 3
	}
	minFTSResults := rc.MinFTSResults
	if minFTSResults <= 0 {
		minFTSResults = 1
	}
	ftsTimeout := ftsTimeoutFor(rc)
	vectorBudget := vectorBudgetFor(rc)

	type itemOut struct {
		ID           string  `json:"id"`
		Title        string  `json:"title"`
		Typ          string  `json:"typ"`
		MatchedTerms int     `json:"matched_terms"`
		Similarity   float64 `json:"similarity"`
	}
	type promptOut struct {
		ID          string    `json:"id"`
		Project     string    `json:"project"`
		Prompt      string    `json:"prompt"`
		Esperado    []string  `json:"esperado"`
		Candidatos  int       `json:"candidatos"`
		Items       []itemOut `json:"items"`
		BlockChars  int       `json:"block_chars"`
		Milisegundos int64    `json:"ms"`
	}

	salida := make([]promptOut, 0, len(set))
	for _, c := range set {
		pq := buildPromptQuery(c.Prompt)
		t0 := time.Now()
		cands := gatherRecallCandidates(context.Background(), c.Prompt, st, vs, c.Project, pq, rc, k, minFTSResults, ftsTimeout, vectorBudget)
		cands = rankAndDedupeRecallItemsOpts(cands, k, rc.MaxSessionItems, rellenoDensidadFactorFor(rc))
		block, usedIDs := formatRecallBlock(cands, rc.CharsLimit)
		ms := time.Since(t0).Milliseconds()

		porID := make(map[string]recallItem, len(cands))
		for _, it := range cands {
			porID[it.id] = it
		}
		items := make([]itemOut, 0, len(usedIDs))
		for _, id := range usedIDs {
			it := porID[id]
			items = append(items, itemOut{
				ID: it.id, Title: it.title, Typ: it.typ,
				MatchedTerms: it.matchedTerms, Similarity: it.similarity,
			})
		}
		salida = append(salida, promptOut{
			ID: c.ID, Project: c.Project, Prompt: c.Prompt, Esperado: c.Esperado,
			Candidatos: len(cands), Items: items, BlockChars: len(block), Milisegundos: ms,
		})
		fmt.Printf("  [%s] %-34s cand=%2d inyecta=%d (%d ms)\n", c.ID, c.Project, len(cands), len(items), ms)
	}

	cab := map[string]any{
		"modo": modo, "k": k, "chars_limit": rc.CharsLimit, "min_matched_terms": rc.MinMatchedTerms,
		"min_fts_results": minFTSResults, "fallback_fts": rc.FallbackFTS,
		"vector_on_fts_miss": rc.VectorOnFTSMiss, "min_similarity": rc.MinSimilarity,
		"max_session_items": rc.MaxSessionItems, "relleno_densidad": rellenoDensidadFactorFor(rc), "config": cfgPathNote(),
	}
	full := map[string]any{"cabecera": cab, "prompts": salida}
	buf, _ := json.MarshalIndent(full, "", " ")
	if err := os.WriteFile(outPath, buf, 0o644); err != nil {
		t.Fatalf("escribir salida: %v", err)
	}
	t.Logf("escrito %s (%d prompts, modo %s)", outPath, len(salida), modo)
}

func cfgPathNote() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h + "/.config/kronos/config.json"
	}
	return ""
}
