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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// DefaultThreshold es la similitud coseno mínima para considerar dos
// observaciones semánticamente duplicadas. Intencionalmente más alto que
// relations.SimilarityThreshold (0.85, pensado para "relacionado" — una
// sugerencia al guardar, no una fusión).
//
// Calibrado 2026-09-11 con --dry-run contra los dos proyectos reales más
// grandes disponibles en el buffer local (atisa-provider-management-all-in-one,
// 630 obs, y kronos-v2, 16 obs), probando 0.85/0.88/0.90/0.93: en NINGÚN
// umbral apareció un candidato por similitud semántica en ninguno de los dos
// proyectos (0 pares en los cuatro casos, ver tabla en el commit) — el
// corpus real no tiene duplicados semánticos que topic_key no resuelva ya.
// Sin evidencia de falsos negativos que bajar el umbral resolviera, se deja
// 0.93: el criterio conservador (evitar fusionar cosas que no son
// duplicados) no tiene costo medido en este corpus, y bajarlo a ciegas solo
// arriesgaría falsos positivos el día que sí aparezca un duplicado real.
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

// minSharedTitleTokens: tokens significativos de título que dos
// observaciones del mismo bucket (proyecto+tipo) tienen que compartir para
// que CUALQUIERA de las dos entre a la pasada de embeddings — ver
// significantTitleTokens. Medido en atisa-provider-management-all-in-one
// (630 obs locales): sin este prefiltro, `gc --consolidate` mandaba a Ollama
// una llamada por cada observación del bucket entero aunque los títulos no
// tuvieran nada en común entre sí (5 evaluadas, 32s, 6.4s por llamada, 0
// candidatos) — con el prefiltro esas llamadas nunca se piden.
//
// Es solo el DEFAULT: Options.MinSharedTitleTokens lo puede bajar con
// `kronos gc --consolidate --min-shared-tokens N` (y con el knob
// consolidation.min_shared_title_tokens). Medido en producción el 2026-09-15
// sobre el histórico completo (1.093 observaciones), y el resultado es que
// bajarlo NO paga en el régimen real: con el mismo tope --max-pairs (60) el
// umbral 2 dejó pasar más candidatos (377 descartados por prefiltro contra
// 679), el tope se repartió entre candidatos flojos y encontró MENOS pares
// reales (2 contra 28). La palanca para encontrar más duplicados es subir
// --max-pairs, no bajar este umbral — y aun así el umbral 2 sumó un par que
// el 3 no vio (SIROC expiration alert), así que con presupuesto grande puede
// tener sentido. Por eso el default queda en 3 y el knob existe para medir.
const minSharedTitleTokens = 3

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

	// MinSharedTitleTokens: mínimo de tokens significativos de título que dos
	// observaciones del mismo bucket (proyecto+tipo) deben compartir para que
	// el prefiltro las considere candidatas de la pasada de embeddings. <=0
	// usa minSharedTitleTokens (el default histórico). Bajarlo sube el recall
	// de duplicados y también el número de llamadas al proveedor de embeddings.
	MinSharedTitleTokens int

	// Since: solo observaciones actualizadas después de este momento entran
	// a la pasada de embeddings (cero = sin filtro, evalúa todo lo que
	// sobrevivió al prefiltro de tokens). No aplica al camino topic_key
	// (gratis, corre completo siempre) ni al prefiltro de tokens (Options no
	// necesita nada para ese paso). Pensado para que el caller (ver `kronos
	// gc --consolidate --since`) persista el timestamp de la corrida anterior
	// y no vuelva a pagar una llamada de embeddings por una observación que
	// ya se evaluó y no cambió desde entonces.
	Since time.Time
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
	// PairsSkippedByPrefilter: observaciones que topic_key no resolvió pero
	// que tampoco entraron a la pasada de embeddings porque ninguna otra
	// observación de su mismo bucket (proyecto+tipo) comparte
	// minSharedTitleTokens tokens de título con ellas — ver
	// significantTitleTokens. Nunca gastan un embedding.
	PairsSkippedByPrefilter int
	// PairsSkippedBySince: observaciones que pasaron el prefiltro de tokens
	// pero quedaron afuera por Options.Since — ya se evaluaron en una
	// corrida anterior y no se actualizaron desde entonces.
	PairsSkippedBySince int
	// PairsSkippedByTemplate: pares detectados por similitud semántica pero
	// descartados porque sus títulos difieren en números — indica que son
	// hechos distintos (ej: tramos distintos de un análisis de lotes).
	PairsSkippedByTemplate int
}

// extractNumbers extrae todas las secuencias de dígitos del título como strings.
func extractNumbers(title string) []string {
	re := regexp.MustCompile(`\d+`)
	return re.FindAllString(title, -1)
}

// titlesHaveDifferentNumbers retorna true si los títulos difieren en sus números.
// Si ambos no tienen números o tienen exactamente los mismos números, retorna false.
func titlesHaveDifferentNumbers(title1, title2 string) bool {
	nums1 := extractNumbers(title1)
	nums2 := extractNumbers(title2)

	if len(nums1) == 0 && len(nums2) == 0 {
		return false
	}

	if len(nums1) != len(nums2) {
		return true
	}

	for i := range nums1 {
		if nums1[i] != nums2[i] {
			return true
		}
	}

	return false
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

	// Paso 2a: prefiltro por solapamiento de título — de lo que topic_key no
	// resolvió, una observación solo es candidata a la pasada de embeddings
	// si comparte minSharedTitleTokens tokens significativos de título con
	// alguna otra observación de su mismo bucket (o el mismo topic_key no
	// vacío, ya cubierto en la práctica por el paso 1). Gratis — no toca el
	// proveedor de embeddings.
	minTokens := opts.MinSharedTitleTokens
	if minTokens <= 0 {
		minTokens = minSharedTitleTokens
	}

	var afterPrefilter []*store.Observation
	skippedByPrefilter := 0
	for _, bucket := range buckets {
		if len(bucket) < 2 {
			continue
		}
		var remaining []*store.Observation
		for _, o := range bucket {
			if !resolved[o.ID] {
				remaining = append(remaining, o)
			}
		}
		if len(remaining) < 2 {
			skippedByPrefilter += len(remaining)
			continue
		}
		tokensByID := make(map[int64]map[string]bool, len(remaining))
		for _, o := range remaining {
			tokensByID[o.ID] = significantTitleTokens(o.Title)
		}
		for _, o := range remaining {
			if hasTitlePartner(o, remaining, tokensByID, minTokens) {
				afterPrefilter = append(afterPrefilter, o)
			} else {
				skippedByPrefilter++
			}
		}
	}

	// Paso 2b: --since — de lo que sobrevivió al prefiltro, solo evaluar por
	// embeddings lo actualizado después de la corrida anterior. No aplica al
	// camino topic_key ni al prefiltro de tokens, ambos gratis.
	var flatRemaining []*store.Observation
	skippedBySince := 0
	for _, o := range afterPrefilter {
		if !opts.Since.IsZero() && o.UpdatedAt.Before(opts.Since) {
			skippedBySince++
			continue
		}
		flatRemaining = append(flatRemaining, o)
	}
	// Determinístico (ordenado por ID) para que dos corridas con el mismo
	// MaxPairs evalúen siempre el mismo subconjunto.
	sort.Slice(flatRemaining, func(i, j int) bool { return flatRemaining[i].ID < flatRemaining[j].ID })

	useEmbeddings := !opts.NoEmbeddings && rel != nil && rel.Enabled()
	evaluated := 0
	skippedByCap := 0
	skippedByTemplate := 0
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

				if titlesHaveDifferentNumbers(o.Title, cand.Title) {
					skippedByTemplate++
					continue
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
		DryRun:                  opts.DryRun,
		Pairs:                   pairs,
		Merged:                  merged,
		EmbeddingsUsed:          useEmbeddings,
		PairsEvaluated:          evaluated,
		PairsSkippedByCap:       skippedByCap,
		PairsSkippedByPrefilter: skippedByPrefilter,
		PairsSkippedBySince:     skippedBySince,
		PairsSkippedByTemplate:  skippedByTemplate,
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

// titleTokenStopwords son conectores/artículos (español e inglés, el repo
// mezcla ambos en títulos) sin señal de tema — mismo criterio que
// internal/store/relations.go (significantTokens, para el detector de
// relaciones); duplicado acá en vez de exportado porque esta rama no toca
// internal/store salvo lectura.
var titleTokenStopwords = map[string]bool{
	"para": true, "como": true, "pero": true, "esto": true, "esta": true,
	"este": true, "esos": true, "esas": true, "unos": true, "unas": true,
	"cada": true, "todo": true, "toda": true, "todos": true, "todas": true,
	"hace": true, "tiene": true, "tienen": true, "desde": true, "hasta": true,
	"sobre": true, "entre": true, "cuando": true, "donde": true,
	"porque": true, "también": true, "puede": true, "puedo": true,
	"sido": true, "está": true, "están": true, "otro": true, "otra": true,
	"otros": true, "otras": true, "with": true, "that": true, "this": true,
	"from": true, "have": true, "were": true, "when": true, "what": true,
	"will": true, "your": true, "their": true, "there": true, "which": true,
	"about": true, "into": true, "than": true, "then": true, "these": true,
	"those": true, "some": true, "such": true,
}

// significantTitleTokens extrae del título las palabras de ≥4 letras que no
// son stopwords — usado por el prefiltro de la pasada de embeddings
// (minSharedTitleTokens).
func significantTitleTokens(title string) map[string]bool {
	tokens := make(map[string]bool)
	for _, w := range strings.Fields(strings.ToLower(title)) {
		w = strings.Trim(w, ".,;:!?()[]{}\"'—-")
		if len([]rune(w)) < 4 || titleTokenStopwords[w] {
			continue
		}
		tokens[w] = true
	}
	return tokens
}

// sharedTitleTokenCount cuenta cuántos tokens aparecen en ambos sets.
func sharedTitleTokenCount(a, b map[string]bool) int {
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n
}

// hasTitlePartner indica si o tiene, dentro de group, al menos otra
// observación con la que comparta topic_key no vacío o minSharedTitleTokens
// tokens significativos de título.
func hasTitlePartner(o *store.Observation, group []*store.Observation, tokensByID map[int64]map[string]bool, minTokens int) bool {
	for _, other := range group {
		if other.ID == o.ID {
			continue
		}
		if o.TopicKey != "" && o.TopicKey == other.TopicKey {
			return true
		}
		if sharedTitleTokenCount(tokensByID[o.ID], tokensByID[other.ID]) >= minTokens {
			return true
		}
	}
	return false
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
