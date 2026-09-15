package consolidate_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/consolidate"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// deterministicFn genera un embedding falso a partir de los bytes del texto —
// mismo criterio que embeddings_test.go y relations_test.go: no es
// semánticamente real, pero textos casi idénticos dan vectores casi
// idénticos, que es justo lo que hace falta para probar el umbral.
func deterministicFn(_ context.Context, text string) ([]float32, error) {
	const dim = 8
	vec := make([]float32, dim)
	for i, ch := range text {
		vec[i%dim] += float32(ch)
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec, nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-consolidate-test-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	// store.New hace migraciones + PRAGMAs (WAL, busy_timeout) y escribe en el
	// filesystem: con la máquina saturada (benchmark de 8 sesiones de Claude
	// Code en paralelo, Ollama al 150% CPU) esto fallaba de forma intermitente
	// y el test se veía como flaky en la suite completa mientras pasaba siempre
	// en solitario. Un reintento con backoff acota el ruido del entorno sin
	// tocar el comportamiento del producto.
	var s *store.Store
	var lastErr error
	for i := 0; i < 3; i++ {
		s, lastErr = store.New(f.Name())
		if lastErr == nil {
			break
		}
		time.Sleep(time.Duration(50*(i+1)) * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("store.New (3 intentos): %v", lastErr)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newDetector(t *testing.T) *relations.Detector {
	t.Helper()
	vs, err := embeddings.NewInMemory(deterministicFn)
	if err != nil {
		t.Fatal(err)
	}
	return relations.New(vs)
}

const (
	obsContentA = "El login falla cuando el usuario tiene MFA activado y el token expiró antes de refrescar."
	obsContentB = "El login falla cuando el usuario tiene MFA activado y el token expiro antes de refrescar."
)

func saveAndIndex(t *testing.T, ctx context.Context, st *store.Store, rel *relations.Detector, project string, typ store.ObservationType, title, content string) *store.Observation {
	t.Helper()
	o, err := st.SaveObservation(ctx, store.SaveParams{
		Project: project,
		Type:    typ,
		Title:   title,
		Content: content,
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	if rel != nil {
		if err := rel.Index(ctx, o.ID, o.Title+" "+o.Content); err != nil {
			t.Fatalf("Index: %v", err)
		}
	}
	return o
}

// (a) Dos observaciones casi idénticas del mismo proyecto/tipo se detectan
// como candidatas y, con escritura habilitada, una queda superseded por la
// otra y la superviviente sube de revisión.
func TestRun_MergesSimilarPairSameProjectAndType(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	o1 := saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa", obsContentA)
	o2 := saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa variante", obsContentB)

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		Threshold:          0.93,
		RequireSameType:    true,
		RequireSameProject: true,
		DryRun:             false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Pairs) != 1 {
		t.Fatalf("esperaba 1 par candidato, obtuve %d: %+v", len(report.Pairs), report.Pairs)
	}
	if report.Merged != 1 {
		t.Fatalf("esperaba 1 fusión, obtuve %d", report.Merged)
	}
	pair := report.Pairs[0]
	if !pair.Applied {
		t.Fatal("el par debería quedar marcado como aplicado")
	}

	survivor, err := st.GetObservation(ctx, pair.SurvivorID)
	if err != nil || survivor == nil {
		t.Fatalf("GetObservation(survivor): %v", err)
	}
	if survivor.RevisionCount != 2 {
		t.Errorf("revision_count del superviviente = %d, esperaba 2", survivor.RevisionCount)
	}

	replaced, err := st.GetObservation(ctx, pair.ReplacedID)
	if err != nil || replaced == nil {
		t.Fatalf("GetObservation(replaced): %v", err)
	}
	if replaced.DeletedAt != nil {
		t.Error("la observación reemplazada NUNCA debe quedar soft-deleted — solo marcada por relación")
	}

	verb, exists, err := st.RelationVerbBetween(ctx, pair.SurvivorSyncID, pair.ReplacedSyncID)
	if err != nil {
		t.Fatalf("RelationVerbBetween: %v", err)
	}
	if !exists || verb != store.RelationSupersedes {
		t.Errorf("esperaba relación supersedes entre %d y %d, verb=%q exists=%v", pair.SurvivorID, pair.ReplacedID, verb, exists)
	}

	// sanity: los dos IDs originales están entre survivor/replaced.
	ids := map[int64]bool{o1.ID: true, o2.ID: true}
	if !ids[pair.SurvivorID] || !ids[pair.ReplacedID] {
		t.Errorf("el par no corresponde a las observaciones creadas: %+v", pair)
	}
}

// (b) Dos observaciones de tipos distintos NO se fusionan aunque el texto
// sea casi idéntico.
func TestRun_DoesNotMergeAcrossDifferentTypes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa", obsContentA)
	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeBugfix, "bug login token expira mfa variante", obsContentB)

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		Threshold:          0.93,
		RequireSameType:    true,
		RequireSameProject: true,
		DryRun:             false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Pairs) != 0 {
		t.Fatalf("esperaba 0 pares (tipos distintos), obtuve %d: %+v", len(report.Pairs), report.Pairs)
	}
	if report.Merged != 0 {
		t.Fatalf("esperaba 0 fusiones, obtuve %d", report.Merged)
	}
}

// (c) dry-run no escribe NADA — se verifica comparando conteos antes/después.
func TestRun_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	o1 := saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa", obsContentA)
	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa variante", obsContentB)

	before, err := st.GetObservation(ctx, o1.ID)
	if err != nil {
		t.Fatalf("GetObservation antes: %v", err)
	}
	relsBefore, err := st.ListRelations(ctx, "proj-a", "", 100, 0)
	if err != nil {
		t.Fatalf("ListRelations antes: %v", err)
	}

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		Threshold:          0.93,
		RequireSameType:    true,
		RequireSameProject: true,
		DryRun:             true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Pairs) != 1 {
		t.Fatalf("esperaba 1 par candidato detectado en dry-run, obtuve %d", len(report.Pairs))
	}
	if report.Merged != 0 {
		t.Fatalf("dry-run no debería fusionar nada, Merged=%d", report.Merged)
	}
	if report.Pairs[0].Applied {
		t.Error("dry-run no debería marcar el par como aplicado")
	}

	after, err := st.GetObservation(ctx, o1.ID)
	if err != nil {
		t.Fatalf("GetObservation después: %v", err)
	}
	if after.RevisionCount != before.RevisionCount {
		t.Errorf("revision_count cambió en dry-run: antes=%d después=%d", before.RevisionCount, after.RevisionCount)
	}

	relsAfter, err := st.ListRelations(ctx, "proj-a", "", 100, 0)
	if err != nil {
		t.Fatalf("ListRelations después: %v", err)
	}
	if len(relsAfter) != len(relsBefore) {
		t.Errorf("dry-run creó relaciones: antes=%d después=%d", len(relsBefore), len(relsAfter))
	}
}

// Corridas repetidas con escritura habilitada no deben re-fusionar el mismo
// par (no debe subir revision_count dos veces).
func TestRun_IsIdempotentAcrossRuns(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa", obsContentA)
	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa variante", obsContentB)

	opts := consolidate.Options{Threshold: 0.93, RequireSameType: true, RequireSameProject: true, DryRun: false}

	first, err := consolidate.Run(ctx, st, rel, opts)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if first.Merged != 1 {
		t.Fatalf("primera corrida: esperaba 1 fusión, obtuve %d", first.Merged)
	}
	survivorID := first.Pairs[0].SurvivorID

	second, err := consolidate.Run(ctx, st, rel, opts)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(second.Pairs) != 0 {
		t.Fatalf("segunda corrida: esperaba 0 pares (ya fusionado), obtuve %d", len(second.Pairs))
	}

	survivor, err := st.GetObservation(ctx, survivorID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if survivor.RevisionCount != 2 {
		t.Errorf("revision_count no debe subir dos veces por el mismo par: got %d", survivor.RevisionCount)
	}
}

// Fallback sin proveedor de embeddings: mismo topic_key no vacío dentro del
// mismo proyecto/tipo también se detecta y fusiona.
func TestRun_TopicKeyFallbackWhenEmbeddingsDisabled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// dos filas con el mismo topic_key: SaveObservation normalmente haría
	// upsert por topic_key (ver getByTopicKey), así que para simular el
	// escenario real que exige el fallback — un duplicado colado por otra vía
	// de escritura (import / replay entre backends) — la segunda fila se
	// inserta directo por SQL, sin pasar por el upsert.
	o1, err := st.SaveObservation(ctx, store.SaveParams{
		Project: "proj-a", Type: store.TypeConfig, Title: "config X", Content: "valor viejo de config X", TopicKey: "config/x",
	})
	if err != nil {
		t.Fatalf("save o1: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	_, err = st.DB().ExecContext(ctx, `
		INSERT INTO observations
			(sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
			 normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at)
		VALUES (?, NULL, ?, ?, ?, '', ?, 'project', ?, 'otro-hash-distinto', 1, 1, ?, ?, ?)`,
		"distinto-sync-id-manual", string(store.TypeConfig), "config X", "valor nuevo, bastante distinto, de config X",
		"proj-a", "config/x", ts, ts, ts,
	)
	if err != nil {
		t.Fatalf("insert directo o2: %v", err)
	}
	o2, err := st.GetObservationBySyncID(ctx, "distinto-sync-id-manual")
	if err != nil || o2 == nil {
		t.Fatalf("GetObservationBySyncID(o2): %v", err)
	}
	if o1.ID == o2.ID {
		t.Fatal("el escenario requiere dos filas distintas con el mismo topic_key")
	}

	report, err := consolidate.Run(ctx, st, nil, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Pairs) != 1 {
		t.Fatalf("esperaba 1 par por topic_key, obtuve %d: %+v", len(report.Pairs), report.Pairs)
	}
	if report.Pairs[0].Similarity != 0 {
		t.Errorf("el fallback por topic_key no debería reportar similitud, got %f", report.Pairs[0].Similarity)
	}
}

// NoEmbeddings=true nunca debe llamar al proveedor de embeddings, aunque haya
// un Detector habilitado — solo debe encontrar lo que topic_key resuelve.
func TestRun_NoEmbeddingsSkipsEmbeddingPass(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t) // habilitado — si Run lo usara, encontraría el par por similitud

	// par resoluble por topic_key (gratis)
	_, err := st.SaveObservation(ctx, store.SaveParams{
		Project: "proj-a", Type: store.TypeConfig, Title: "config X", Content: "valor viejo de config X", TopicKey: "config/x",
	})
	if err != nil {
		t.Fatalf("save o1: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO observations
			(sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
			 normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at)
		VALUES (?, NULL, ?, ?, ?, '', ?, 'project', ?, 'otro-hash-distinto', 1, 1, ?, ?, ?)`,
		"no-embeddings-o2", string(store.TypeConfig), "config X", "valor nuevo de config X",
		"proj-a", "config/x", ts, ts, ts,
	); err != nil {
		t.Fatalf("insert directo o2: %v", err)
	}

	// par que SOLO se detectaría por similitud semántica (topic_key vacío)
	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa", obsContentA)
	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery, "bug login token expira mfa variante", obsContentB)

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true, NoEmbeddings: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.EmbeddingsUsed {
		t.Error("NoEmbeddings=true no debería usar el proveedor de embeddings")
	}
	if report.PairsEvaluated != 0 {
		t.Errorf("NoEmbeddings=true no debería evaluar ninguna observación por embeddings, got %d", report.PairsEvaluated)
	}
	if len(report.Pairs) != 1 {
		t.Fatalf("esperaba solo el par por topic_key, obtuve %d: %+v", len(report.Pairs), report.Pairs)
	}
	if report.Pairs[0].Similarity != 0 {
		t.Error("el único par reportado debería venir de topic_key, no de similitud")
	}
}

// MaxPairs topea cuántas observaciones se consultan contra el vector store —
// el resto queda contabilizado como "fuera del tope", no evaluado.
func TestRun_MaxPairsCapsEmbeddingEvaluations(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	// 4 observaciones sin topic_key, mismo proyecto/tipo — las 4 son
	// elegibles para la pasada de embeddings.
	texts := []string{
		"contenido completamente distinto número uno sobre despliegues",
		"contenido completamente distinto número dos sobre monitoreo",
		"contenido completamente distinto número tres sobre backups",
		"contenido completamente distinto número cuatro sobre alertas",
	}
	// título compartido entre las 4 ("resumen operativo semanal") para que
	// sobrevivan el prefiltro de tokens de título (minSharedTitleTokens=3) —
	// el test quiere ejercitar el tope MaxPairs, no el prefiltro.
	for i, txt := range texts {
		saveAndIndex(t, ctx, st, rel, "proj-cap", store.TypeDiscovery, fmt.Sprintf("resumen operativo semanal %d", i), txt)
	}

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true, MaxPairs: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.EmbeddingsUsed {
		t.Error("esperaba que la pasada de embeddings corriera (aunque topeada)")
	}
	if report.PairsEvaluated != 1 {
		t.Errorf("esperaba exactamente 1 observación evaluada (MaxPairs=1), got %d", report.PairsEvaluated)
	}
	if report.PairsSkippedByCap != 3 {
		t.Errorf("esperaba 3 observaciones fuera del tope, got %d", report.PairsSkippedByCap)
	}
}

// countingEmbedFn envuelve deterministicFn contando invocaciones — usado
// para medir cuántas llamadas al proveedor de embeddings evita cada filtro
// (proyecto/tipo, prefiltro de tokens de título, --since) sin depender de
// Ollama real ni de logs.
func countingEmbedFn(calls *int) embeddings.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		*calls++
		return deterministicFn(ctx, text)
	}
}

func newCountingDetector(t *testing.T) (*relations.Detector, *int) {
	t.Helper()
	calls := 0
	vs, err := embeddings.NewInMemory(countingEmbedFn(&calls))
	if err != nil {
		t.Fatal(err)
	}
	return relations.New(vs), &calls
}

// (item de verificación c, primera mitad) proyectos o tipos distintos nunca
// llegan a la pasada de embeddings — bucketize() los separa antes de que
// Run() arme flatRemaining, así que el proveedor de embeddings no se toca
// aunque los títulos sean idénticos.
func TestRun_CrossTypeAndCrossProject_NoEmbeddingCalls(t *testing.T) {
	t.Run("tipos distintos, mismo proyecto", func(t *testing.T) {
		ctx := context.Background()
		st := newTestStore(t)
		rel, calls := newCountingDetector(t)

		saveAndIndex(t, ctx, st, rel, "proj-x", store.TypeDiscovery, "informe de despliegue semanal", "contenido A")
		saveAndIndex(t, ctx, st, rel, "proj-x", store.TypeBugfix, "informe de despliegue semanal", "contenido B")
		*calls = 0 // descartar las llamadas de Index(), solo interesa la pasada de Run()

		report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
			RequireSameType: true, RequireSameProject: true, DryRun: true,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if *calls != 0 {
			t.Errorf("tipos distintos no deberían gastar embeddings, got %d llamadas", *calls)
		}
		if report.PairsEvaluated != 0 {
			t.Errorf("esperaba 0 observaciones evaluadas, got %d", report.PairsEvaluated)
		}
	})

	t.Run("proyectos distintos, mismo tipo", func(t *testing.T) {
		ctx := context.Background()
		st := newTestStore(t)
		rel, calls := newCountingDetector(t)

		saveAndIndex(t, ctx, st, rel, "proj-y1", store.TypeDiscovery, "informe de despliegue semanal", "contenido A")
		saveAndIndex(t, ctx, st, rel, "proj-y2", store.TypeDiscovery, "informe de despliegue semanal", "contenido B")
		*calls = 0

		report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
			RequireSameType: true, RequireSameProject: true, DryRun: true,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if *calls != 0 {
			t.Errorf("proyectos distintos no deberían gastar embeddings, got %d llamadas", *calls)
		}
		if report.PairsEvaluated != 0 {
			t.Errorf("esperaba 0 observaciones evaluadas, got %d", report.PairsEvaluated)
		}
	})
}

// (item de verificación c, segunda mitad) el prefiltro de tokens de título
// (minSharedTitleTokens=3) descarta observaciones del MISMO bucket
// (proyecto+tipo) cuyos títulos no tienen nada en común, sin gastar
// embeddings — y sí los evalúa cuando el título comparte suficiente señal.
func TestRun_TitlePrefilter_SkipsUnrelatedTitles_NoEmbeddingCalls(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel, calls := newCountingDetector(t)

	// 4 observaciones mismo proyecto/tipo, títulos SIN tokens en común entre
	// sí (cada una habla de un tema distinto) — ninguna debería sobrevivir
	// al prefiltro.
	saveAndIndex(t, ctx, st, rel, "proj-noise", store.TypeDiscovery, "mysql pool exhausted", "contenido 1")
	saveAndIndex(t, ctx, st, rel, "proj-noise", store.TypeDiscovery, "alfresco upload retry", "contenido 2")
	saveAndIndex(t, ctx, st, rel, "proj-noise", store.TypeDiscovery, "webhook signature secret", "contenido 3")
	saveAndIndex(t, ctx, st, rel, "proj-noise", store.TypeDiscovery, "cron backup schedule", "contenido 4")
	*calls = 0

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *calls != 0 {
		t.Errorf("títulos sin tokens compartidos no deberían gastar embeddings, got %d llamadas", *calls)
	}
	if report.PairsEvaluated != 0 {
		t.Errorf("esperaba 0 observaciones evaluadas, got %d", report.PairsEvaluated)
	}
	if report.PairsSkippedByPrefilter != 4 {
		t.Errorf("esperaba 4 observaciones descartadas por el prefiltro de título, got %d", report.PairsSkippedByPrefilter)
	}

	// Contraste: dos observaciones más, mismo bucket, que SÍ comparten >=3
	// tokens de título entre sí — estas dos deben pasar el prefiltro y
	// gastar embeddings (una llamada cada una).
	saveAndIndex(t, ctx, st, rel, "proj-noise", store.TypeDiscovery, "postgres driver timeout error", "contenido 5")
	saveAndIndex(t, ctx, st, rel, "proj-noise", store.TypeDiscovery, "postgres driver timeout otra vez", "contenido 6")
	*calls = 0

	report2, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run (2): %v", err)
	}
	if *calls == 0 {
		t.Error("las dos observaciones con título compartido deberían gastar al menos un embedding")
	}
	if report2.PairsEvaluated != 2 {
		t.Errorf("esperaba exactamente 2 observaciones evaluadas (las que comparten título), got %d", report2.PairsEvaluated)
	}
	if report2.PairsSkippedByPrefilter != 4 {
		t.Errorf("las 4 originales siguen sin pareja de título, esperaba 4 descartadas por prefiltro, got %d", report2.PairsSkippedByPrefilter)
	}
}

// (item de verificación d) --since evita reevaluar por embeddings
// observaciones que no cambiaron desde la corrida anterior.
func TestRun_Since_SkipsAlreadyEvaluatedObservations(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel, calls := newCountingDetector(t)

	o1 := saveAndIndex(t, ctx, st, rel, "proj-since", store.TypeDiscovery, "postgres driver timeout", "contenido 1")
	o2 := saveAndIndex(t, ctx, st, rel, "proj-since", store.TypeDiscovery, "postgres driver timeout otra vez", "contenido 2")
	*calls = 0

	// since = después de que ambas se actualizaron por última vez -> ninguna
	// entra a la pasada de embeddings.
	since := o1.UpdatedAt
	if o2.UpdatedAt.After(since) {
		since = o2.UpdatedAt
	}
	since = since.Add(time.Second)

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true, Since: since,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *calls != 0 {
		t.Errorf("--since posterior a ambas observaciones no debería gastar embeddings, got %d llamadas", *calls)
	}
	if report.PairsEvaluated != 0 {
		t.Errorf("esperaba 0 observaciones evaluadas, got %d", report.PairsEvaluated)
	}
	if report.PairsSkippedBySince != 2 {
		t.Errorf("esperaba 2 observaciones descartadas por --since, got %d", report.PairsSkippedBySince)
	}

	// Sin --since (cero) o con una fecha anterior, sí se evalúan.
	report2, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run (sin since): %v", err)
	}
	if *calls == 0 {
		t.Error("sin --since, las observaciones deberían gastar embeddings")
	}
	if report2.PairsEvaluated != 2 {
		t.Errorf("esperaba 2 observaciones evaluadas sin --since, got %d", report2.PairsEvaluated)
	}
	if report2.PairsSkippedBySince != 0 {
		t.Errorf("sin --since no debería haber descartes por since, got %d", report2.PairsSkippedBySince)
	}
}

// Dos observaciones con títulos de plantilla y números distintos no se fusionan.
// El guardia detecta que "batch 1" vs "batch 2" son hechos distintos.
func TestRun_TemplateGuardian_SkipsDifferentNumbers(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	const commonContent = "Se analizó la solicitud de acceso y se verificó la identidad del usuario. La operación fue exitosa."

	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery,
		"Análisis verificado Gmail tickets batch 1",
		"batch 1 "+commonContent)
	saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery,
		"Análisis verificado Gmail tickets batch 2",
		"batch 2 "+commonContent)

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		Threshold:          0.70,
		RequireSameType:    true,
		RequireSameProject: true,
		DryRun:             false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Pairs) != 0 {
		t.Fatalf("esperaba 0 pares (números distintos), obtuve %d: %+v", len(report.Pairs), report.Pairs)
	}
	if report.Merged != 0 {
		t.Fatalf("esperaba 0 fusiones, obtuve %d", report.Merged)
	}
	if report.PairsSkippedByTemplate == 0 {
		t.Errorf("esperaba PairsSkippedByTemplate > 0, obtuve %d", report.PairsSkippedByTemplate)
	}
}

// Dos observaciones con el mismo hecho y el MISMO número (o sin números) y
// contenido casi idéntico SÍ se fusionan (el guardia no rompe casos legítimos).
func TestRun_TemplateGuardian_AllowsSameNumbers(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel := newDetector(t)

	o1 := saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery,
		"El login falla batch 1",
		"El login falla cuando el usuario tiene MFA activado y el token expiró antes de refrescar. batch 1")
	o2 := saveAndIndex(t, ctx, st, rel, "proj-a", store.TypeDiscovery,
		"El login falla batch 1 variante",
		"El login falla cuando el usuario tiene MFA activado y el token expiro antes de refrescar. batch 1")

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		Threshold:          0.60,
		RequireSameType:    true,
		RequireSameProject: true,
		DryRun:             false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Pairs) != 1 {
		t.Fatalf("esperaba 1 par (mismo número), obtuve %d: %+v", len(report.Pairs), report.Pairs)
	}
	if report.Merged != 1 {
		t.Fatalf("esperaba 1 fusión, obtuve %d", report.Merged)
	}

	pair := report.Pairs[0]
	if !pair.Applied {
		t.Fatal("el par debería quedar marcado como aplicado")
	}

	ids := map[int64]bool{o1.ID: true, o2.ID: true}
	if !ids[pair.SurvivorID] || !ids[pair.ReplacedID] {
		t.Errorf("el par no corresponde a las observaciones creadas: %+v", pair)
	}
}

// TestRun_TitlePrefilter_UmbralConfigurable: el prefiltro de título era un
// constante de 3 tokens. Medido en producción el 2026-09-15, dos
// observaciones del mismo proyecto y tipo que eran el MISMO hallazgo con
// títulos redactados distinto compartían 2 tokens ("aprobacion extraccion
// bloqueada" vs "aprobacion extraccion pendiente"), así que nunca llegaban a
// compararse. Con el umbral en 2 sí pasan; con el default 3, no.
func TestRun_TitlePrefilter_UmbralConfigurable(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rel, calls := newCountingDetector(t)

	saveAndIndex(t, ctx, st, rel, "proj-umbral", store.TypeBugfix,
		"aprobacion extraccion bloqueada", "la aprobacion manual no se bloqueaba")
	saveAndIndex(t, ctx, st, rel, "proj-umbral", store.TypeBugfix,
		"aprobacion extraccion pendiente", "la aprobacion manual quedo pendiente")

	// Con el default (3 tokens): ningún par sobrevive, cero embeddings.
	*calls = 0
	r3, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run (default): %v", err)
	}
	if *calls != 0 {
		t.Errorf("con umbral 3 no debería gastar embeddings, got %d", *calls)
	}
	if r3.PairsSkippedByPrefilter != 2 {
		t.Errorf("umbral 3: esperaba 2 descartadas por prefiltro, got %d", r3.PairsSkippedByPrefilter)
	}

	// Con umbral 2: las dos pasan el prefiltro y se evalúan (una llamada cada una).
	*calls = 0
	r2, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		RequireSameType: true, RequireSameProject: true, DryRun: true,
		MinSharedTitleTokens: 2,
	})
	if err != nil {
		t.Fatalf("Run (umbral 2): %v", err)
	}
	if r2.PairsSkippedByPrefilter != 0 {
		t.Errorf("umbral 2: esperaba 0 descartadas por prefiltro, got %d", r2.PairsSkippedByPrefilter)
	}
	if *calls != 2 {
		t.Errorf("umbral 2: esperaba 2 llamadas a embeddings, got %d", *calls)
	}
}
