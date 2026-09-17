package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// testConsolidateDSN es la misma base que usa el kronos real de esta máquina
// (ver config.json en platform.ConfigDir(), postgres_docker=true en 5433) —
// no producción. store_postgres_test.go apunta a :5432, que en esta máquina
// es un Postgres del sistema sin las credenciales de kronos (siempre se
// salta acá); :5433 es el contenedor real que usa el daemon.
const testConsolidateDSN = "postgresql://postgres:kronos@localhost:5433/kronos?sslmode=disable"

// TestRunGCConsolidate_PostgresBackend_ReadsPrimaryNotStaleBuffer es la
// regresión del bug real: con backend=postgres configurado, runGCConsolidate
// abría igual el buffer SQLite local (store.New(dbPath)) para detectar
// candidatos, así que un buffer desactualizado o inexistente (como el de este
// test, que arranca en un HOME temporal sin kronos.db) hacía que la
// consolidación no encontrara nada aunque el primario sí tuviera el
// duplicado. El fix hace que consolidate.Run corra directo contra Postgres
// cuando el backend configurado es postgres.
//
// El buffer local en este test NUNCA se crea (no hay kronos.db en el HOME
// temporal) — si el código todavía abriera el buffer, store.New crearía un
// SQLite vacío con 0 observaciones y el reporte no fusionaría nada. Con el
// fix, encuentra el par por topic_key en Postgres y lo fusiona.
func TestRunGCConsolidate_PostgresBackend_ReadsPrimaryNotStaleBuffer(t *testing.T) {
	prim, err := store.NewPostgres(testConsolidateDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testConsolidateDSN, err)
	}
	// Close() va en t.Cleanup, no en defer: los t.Cleanup registrados
	// DESPUÉS (el borrado de las filas de prueba, más abajo) corren antes
	// que los registrados ANTES (LIFO) — pero un defer de la propia función
	// corre antes que CUALQUIER t.Cleanup, así que un `defer prim.Close()`
	// cerraba la conexión antes de que el t.Cleanup de borrado pudiera usar
	// prim.DB(), y el error del DELETE quedaba silenciado por `_, _ =`.
	// Medido en vivo: dos corridas de este test dejaron 4 filas y 2
	// relaciones "supersedes" de prueba sueltas en la base compartida.
	t.Cleanup(func() { _ = prim.Close() })

	ctx := context.Background()
	testProject := fmt.Sprintf("kronos-gc-consolidate-test-%d", time.Now().UnixNano())
	topicKey := "dup-topic"

	obsA, err := prim.SaveObservation(ctx, store.SaveParams{
		Type:     store.TypeDiscovery,
		Title:    "Hallazgo duplicado A",
		Content:  "contenido original",
		Project:  testProject,
		TopicKey: topicKey,
	})
	if err != nil {
		t.Fatalf("SaveObservation A: %v", err)
	}

	// Fila B con el MISMO topic_key insertada directo por SQL — simula una
	// vía de escritura alternativa (import, replay entre backends) que deja
	// dos filas activas con el mismo topic_key, el caso que findTopicKeyPairs
	// resuelve. SaveObservation no sirve acá: haría upsert sobre la fila A en
	// vez de crear una segunda fila.
	syncIDB := fmt.Sprintf("test-dup-b-%d", time.Now().UnixNano())
	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := prim.DB().ExecContext(ctx, `
		INSERT INTO observations
			(type, title, content, project, scope, topic_key, normalized_hash,
			 revision_count, duplicate_count, last_seen_at, created_at, updated_at,
			 sync_id, tool_name)
		VALUES ($1,$2,$3,$4,'project',$5,'',1,1,$6,$6,$6,$7,'')`,
		string(store.TypeDiscovery), "Hallazgo duplicado B", "contenido viejo",
		testProject, topicKey, ts, syncIDB,
	); err != nil {
		t.Fatalf("insertar fila B directo: %v", err)
	}

	t.Cleanup(func() {
		if _, err := prim.DB().ExecContext(ctx, `DELETE FROM memory_relations WHERE source_id = $1 OR target_id = $1 OR source_id = $2 OR target_id = $2`, obsA.SyncID, syncIDB); err != nil {
			t.Logf("cleanup: borrar memory_relations de prueba: %v", err)
		}
		if _, err := prim.DB().ExecContext(ctx, `DELETE FROM observations WHERE project = $1`, testProject); err != nil {
			t.Logf("cleanup: borrar observations de prueba: %v", err)
		}
	})

	setupTempDataDir(t)
	cfgPath, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfgJSON := fmt.Sprintf(`{"db":{"backend":"postgres","postgres_dsn":%q}}`, testConsolidateDSN)
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatalf("escribir config.json: %v", err)
	}

	if err := runGCConsolidate([]string{
		"--project", testProject,
		"--no-dry-run",
		"--no-embeddings",
	}); err != nil {
		t.Fatalf("runGCConsolidate: %v", err)
	}

	verb, exists, err := prim.RelationVerbBetween(ctx, obsA.SyncID, syncIDB)
	if err != nil {
		t.Fatalf("RelationVerbBetween: %v", err)
	}
	if !exists || verb != string(store.RelationSupersedes) {
		t.Fatalf("esperaba relación supersedes entre %s y %s en el PRIMARIO, got exists=%v verb=%q — la consolidación no vio el par (¿sigue leyendo del buffer local?)", obsA.SyncID, syncIDB, exists, verb)
	}

	updatedA, err := prim.GetObservation(ctx, obsA.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if updatedA.RevisionCount != 2 {
		t.Errorf("revision_count del superviviente = %d, want 2", updatedA.RevisionCount)
	}
}
