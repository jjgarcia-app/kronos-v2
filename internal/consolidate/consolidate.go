// Package consolidate implementa la consolidación conservadora de
// duplicados semánticos: generaliza el patrón de upsert por topic_key que ya
// usan los digests de sesión (topic_key = "session/<id>", ver
// internal/hooks) a pares de observaciones que nunca comparten topic_key
// pero dicen casi lo mismo.
//
// Nunca borra filas. La observación superviviente absorbe el duplicado
// subiendo su revision_count; la reemplazada queda marcada vía una relación
// "supersedes" en memory_relations (mismo mecanismo que ya usa mem_judge) —
// ambas filas siguen existiendo y son consultables. Si hay duda, no se
// fusiona: un falso positivo pierde información, un falso negativo solo deja
// una fila de más.
//
// Costo: comparar por topic_key es gratis (en memoria, ya cargado). Comparar
// por similitud semántica NO lo es — cada observación evaluada implica una
// llamada al proveedor de embeddings (Ollama) para generar el vector de la
// consulta, y con cientos de observaciones eso se nota (cientos de ms a
// varios segundos por llamada). Por eso Run() resuelve primero por topic_key
// (gratis) y recién después gasta el presupuesto de embeddings
// (Options.MaxPairs) solo en lo que topic_key no pudo resolver.
package consolidate

import (
	"context"
	"fmt"
	"sort"

	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// DefaultThreshold es la similitud coseno mínima para considerar dos
// observaciones semánticamente duplicadas. Intencionalmente más alto que
// relations.SimilarityThreshold (0.85, pensado para "relacionado" — una
// sugerencia al guardar, no una fusión).
const DefaultThreshold float32 = 0.93

// DefaultMaxPairs es el tope de observaciones que se consultan contra el
// proveedor de embeddings en una corrida — cada una es una llamada de red a
// Ollama. 50 mantiene una corrida manual en segundos incluso sin caché tibia;
// subirlo es decisión de quien corre el comando (--max-pairs), no un default
// que arriesgue dejar `kronos gc --consolidate` colgado 10-60 minutos contra
// un proyecto con cientos de observaciones.
const DefaultMaxPairs = 50

// similarLimit: cuántos vecinos pedirle al vector store por observación. El
// filtro real de proyecto/tipo se aplica después, así que conviene pedir de
// más.
const similarLimit = 8

// Options controla qué candidatos se consideran y si se escriben cambios.
type Options struct {
	Project            string  // vacío = todos los proyectos
	Threshold          float32 // similitud mínima; <=0 usa DefaultThreshold
	RequireSameType    bool
	RequireSameProject bool
	DryRun             bool

	// NoEmbeddings fuerza el camino topic_key exclusivamente — ni una
	// llamada al proveedor de embeddings. Milisegundos en vez de minutos;
	// pensado para tener un reporte rápido antes de decidir si vale la pena
	// pagar el costo de la pasada semántica.
	NoEmbeddings bool

	// MaxPairs topea cuántas observaciones se consultan contra el vector
	// store en esta corrida (<=0 usa DefaultMaxPairs). No aplica al camino
	// topic_key, que siempre corre completo por ser gratis.
	MaxPairs int
}

// Pair es un par candidato a fusión (o ya fusionado, si Applied=true).
type Pair struct {
	SurvivorID     int64
	SurvivorSyncID string
	SurvivorTitle  string
	ReplacedID     int64
	ReplacedSyncID string
	ReplacedTitle  string
	Project        string
	Type           string
	Reason         string  // "similitud=0.950" o "topic_key=X (revisión desactualizada)"
	Similarity     float32 // 0 cuando el candidato vino del fallback por topic_key
	Applied        bool
}

// Report es el resultado de una corrida de Run.
type Report struct {
	DryRun bool
	Pairs  []Pair
	Merged int

	// EmbeddingsUsed indica si la pasada semántica corrió. false cuando
	// Options.NoEmbeddings=true o no hay proveedor de embeddings disponible
	// — en ese caso Pairs solo contiene candidatos por topic_key.
	EmbeddingsUsed bool
	// PairsEvaluated: observaciones efectivamente consultadas contra el
	// vector store (una llamada al proveedor de embeddings cada una).
	PairsEvaluated int
	// PairsSkippedByCap: observaciones que quedaron sin consultar porque se
	// agotó MaxPairs antes de llegar a ellas. >0 es señal de que una corrida
	// con --max-pairs más alto (o varias corridas por proyecto) puede
	// encontrar más candidatos.
	PairsSkippedByCap int
}

// Run busca pares de observaciones semánticamente duplicadas dentro del
// mismo proyecto y tipo (según Options) y, si DryRun es false, las fusiona.
// rel puede ser nil o estar deshabilitado (sin proveedor de embeddings) — en
// ese caso, igual que con Options.NoEmbeddings=true, corre solo el camino
// topic_key.
func Run(ctx context.Context, st *store.Store, rel *relations.Detector, opts Options) (*Report, error) {
	if opts.Threshold <= 0 {
		opts.Threshold = DefaultThreshold
	}
	maxPairs := opts.MaxPairs
	if maxPairs <= 0 {
		maxPairs = DefaultMaxPairs
	}

	all, err := st.ListAll(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("listar observaciones: %w", err)
	}

	var pool []*store.Observation
	for _, o := range all {
		if o.DeletedAt != nil {
			continue
		}
		if opts.Project != "" && o.Project != opts.Project {
			continue
		}
		pool = append(pool, o)
	}

	buckets := bucketize(pool, opts)

	seen := map[[2]int64]bool{}
	var pairs []Pair

	// Paso 1: topic_key — gratis (en memoria), siempre corre primero y
	// completo, sin importar NoEmbeddings ni MaxPairs.
	resolved := map[int64]bool{}
	for _, bucket := range buckets {
		if len(bucket) < 2 {
			continue
		}
		tkPairs := findTopicKeyPairs(bucket, seen)
		pairs = append(pairs, tkPairs...)
		for _, p := range tkPairs {
			resolved[p.SurvivorID] = true
			resolved[p.ReplacedID] = true
		}
	}

	// Paso 2: similitud semántica — solo sobre lo que topic_key no resolvió,
	// y solo hasta agotar maxPairs. Determinístico (ordenado por ID) para que
	// dos corridas con el mismo MaxPairs evalúen siempre el mismo subconjunto.
	var flatRemaining []*store.Observation
	for _, bucket := range buckets {
		if len(bucket) < 2 {
			continue
		}
		for _, o := range bucket {
			if !resolved[o.ID] {
				flatRemaining = append(flatRemaining, o)
			}
		}
	}
	sort.Slice(flatRemaining, func(i, j int) bool { return flatRemaining[i].ID < flatRemaining[j].ID })

	useEmbeddings := !opts.NoEmbeddings && rel != nil && rel.Enabled()
	evaluated := 0
	skippedByCap := 0
	if useEmbeddings {
		byID := make(map[int64]*store.Observation, len(pool))
		for _, o := range pool {
			byID[o.ID] = o
		}

		limit := maxPairs
		if limit > len(flatRemaining) {
			limit = len(flatRemaining)
		}
		for i := 0; i < limit; i++ {
			o := flatRemaining[i]
			evaluated++
			hits, err := rel.Similar(ctx, o.Title+" "+o.Content, similarLimit, o.ID, opts.Threshold)
			if err != nil {
				continue
			}
			myKey := bucketKeyFor(o, opts)
			for _, h := range hits {
				cand, ok := byID[h.ObsID]
				if !ok || bucketKeyFor(cand, opts) != myKey {
					continue // fuera del bucket — no es candidato bajo esta política
				}
				key := pairKey(o.ID, cand.ID)
				if seen[key] {
					continue
				}
				seen[key] = true

				survivor, replaced := pickSurvivor(o, cand)
				pairs = append(pairs, Pair{
					SurvivorID:     survivor.ID,
					SurvivorSyncID: survivor.SyncID,
					SurvivorTitle:  survivor.Title,
					ReplacedID:     replaced.ID,
					ReplacedSyncID: replaced.SyncID,
					ReplacedTitle:  replaced.Title,
					Project:        survivor.Project,
					Type:           string(survivor.Type),
					Reason:         fmt.Sprintf("similitud=%.3f", h.Similarity),
					Similarity:     h.Similarity,
				})
			}
		}
		skippedByCap = len(flatRemaining) - evaluated
	}

	pairs, err = dropAlreadyMerged(ctx, st, pairs)
	if err != nil {
		return nil, err
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Project != pairs[j].Project {
			return pairs[i].Project < pairs[j].Project
		}
		if pairs[i].Type != pairs[j].Type {
			return pairs[i].Type < pairs[j].Type
		}
		return pairs[i].SurvivorID < pairs[j].SurvivorID
	})

	merged := 0
	if !opts.DryRun {
		for i := range pairs {
			if err := apply(ctx, st, &pairs[i]); err != nil {
				return nil, fmt.Errorf("fusionar #%d <- #%d: %w", pairs[i].SurvivorID, pairs[i].ReplacedID, err)
			}
			merged++
		}
	}

	return &Report{
		DryRun:            opts.DryRun,
		Pairs:             pairs,
		Merged:            merged,
		EmbeddingsUsed:    useEmbeddings,
		PairsEvaluated:    evaluated,
		PairsSkippedByCap: skippedByCap,
	}, nil
}

type bucketKey struct {
	project string
	typ     string
}

// bucketize agrupa el pool según Options — mismo proyecto y/o mismo tipo,
// como determinen RequireSameProject/RequireSameType. Con ambos en true
// (default) nunca se comparan observaciones de proyectos o tipos distintos.
func bucketize(pool []*store.Observation, opts Options) map[bucketKey][]*store.Observation {
	buckets := map[bucketKey][]*store.Observation{}
	for _, o := range pool {
		buckets[bucketKeyFor(o, opts)] = append(buckets[bucketKeyFor(o, opts)], o)
	}
	return buckets
}

func bucketKeyFor(o *store.Observation, opts Options) bucketKey {
	k := bucketKey{}
	if opts.RequireSameProject {
		k.project = o.Project
	}
	if opts.RequireSameType {
		k.typ = string(o.Type)
	}
	return k
}

// findTopicKeyPairs agrupa por topic_key no vacío dentro del bucket.
// SaveObservation ya hace upsert por topic_key en el camino normal (ver
// getByTopicKey), así que no debería haber más de una fila activa por
// topic_key+proyecto — pero vías alternativas de escritura (import, replay
// entre backends) pueden dejar duplicados con el mismo topic_key. Cuando eso
// pasa, se conserva la más completa/reciente y el resto queda "supersedes"
// hacia ella. Sin costo: no llama al proveedor de embeddings.
func findTopicKeyPairs(bucket []*store.Observation, seen map[[2]int64]bool) []Pair {
	byTopic := map[string][]*store.Observation{}
	for _, o := range bucket {
		if o.TopicKey == "" {
			continue
		}
		byTopic[o.TopicKey] = append(byTopic[o.TopicKey], o)
	}

	var pairs []Pair
	for topicKey, group := range byTopic {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			return completeness(group[i]) > completeness(group[j])
		})
		survivor := group[0]
		for _, replaced := range group[1:] {
			key := pairKey(survivor.ID, replaced.ID)
			if seen[key] {
				continue
			}
			seen[key] = true
			pairs = append(pairs, Pair{
				SurvivorID:     survivor.ID,
				SurvivorSyncID: survivor.SyncID,
				SurvivorTitle:  survivor.Title,
				ReplacedID:     replaced.ID,
				ReplacedSyncID: replaced.SyncID,
				ReplacedTitle:  replaced.Title,
				Project:        survivor.Project,
				Type:           string(survivor.Type),
				Reason:         fmt.Sprintf("topic_key=%s (revisión desactualizada)", topicKey),
			})
		}
	}
	return pairs
}

func pairKey(a, b int64) [2]int64 {
	if a < b {
		return [2]int64{a, b}
	}
	return [2]int64{b, a}
}

// pickSurvivor decide cuál de las dos observaciones sobrevive: la más
// "completa" (más revisiones, más contenido) y, en empate, la más reciente.
// Conservador a propósito: nunca se descarta contenido, solo se elige cuál
// fila queda como principal — la otra sigue existiendo, solo marcada.
func pickSurvivor(a, b *store.Observation) (survivor, replaced *store.Observation) {
	ca, cb := completeness(a), completeness(b)
	if ca > cb {
		return a, b
	}
	if cb > ca {
		return b, a
	}
	if b.UpdatedAt.After(a.UpdatedAt) {
		return b, a
	}
	return a, b
}

func completeness(o *store.Observation) int {
	return o.RevisionCount*1_000_000 + len(o.Content)
}

// dropAlreadyMerged descarta pares donde ya existe una relación "supersedes"
// entre las dos observaciones (en cualquier dirección) — de una corrida
// anterior con --no-dry-run. Sin este chequeo, correr la consolidación dos
// veces subiría revision_count del superviviente en cada corrida por el
// mismo par.
func dropAlreadyMerged(ctx context.Context, st *store.Store, pairs []Pair) ([]Pair, error) {
	var out []Pair
	for _, p := range pairs {
		verb, exists, err := st.RelationVerbBetween(ctx, p.SurvivorSyncID, p.ReplacedSyncID)
		if err != nil {
			return nil, fmt.Errorf("verificar relación existente: %w", err)
		}
		if exists && verb == store.RelationSupersedes {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// apply persiste la fusión: relación "supersedes" (superviviente -> reemplazada)
// vía el mismo mecanismo que usa mem_judge (JudgeBySemantic hace upsert por
// par, no duplica filas en memory_relations), y sube el revision_count del
// superviviente. Nunca toca deleted_at — ambas observaciones quedan activas y
// consultables; la relación es lo que documenta cuál reemplazó a cuál.
func apply(ctx context.Context, st *store.Store, p *Pair) error {
	confidence := 0.9
	if p.Similarity > 0 {
		confidence = float64(p.Similarity)
	}
	if _, err := st.JudgeBySemantic(ctx, p.SurvivorSyncID, p.ReplacedSyncID, store.RelationSupersedes, confidence, p.Reason, "kronos-gc-consolidate"); err != nil {
		return fmt.Errorf("registrar relación supersedes: %w", err)
	}
	if _, err := st.IncrementRevisionCount(ctx, p.SurvivorID); err != nil {
		return fmt.Errorf("subir revision_count: %w", err)
	}
	p.Applied = true
	return nil
}
