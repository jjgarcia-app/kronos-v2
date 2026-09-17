package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestStats_TopOrganicShare_ExcludesBulkImportMinute es la regresión de la
// medición real (2026-09-17): un proyecto con 67% de concentración bruta
// caía a 44% de concentración orgánica al excluir un import masivo de una
// sola vez (628 filas creadas en el mismo minuto). Sin esta distinción,
// `kronos doctor` no puede avisar de un desequilibrio real del corpus sin
// confundirlo con un import legítimo.
func TestStats_TopOrganicShare_ExcludesBulkImportMinute(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats-organic-test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	if _, err := s.CreateSession(ctx, "sess-organic", "atisa", "/tmp"); err != nil {
		t.Fatal(err)
	}

	bulkTS := "2026-09-07T07:02:00Z" // mismo minuto para las 10 filas del import
	for i := 0; i < bulkImportMinuteThreshold+5; i++ {
		if err := saveObservationAt(ctx, s, "atisa", fmt.Sprintf("import masivo %d", i), bulkTS); err != nil {
			t.Fatal(err)
		}
	}

	// 3 observaciones orgánicas de atisa, en minutos distintos entre sí.
	organicTS := []string{"2026-09-14T10:00:00Z", "2026-09-15T11:00:00Z", "2026-09-16T12:00:00Z"}
	for i, ts := range organicTS {
		if err := saveObservationAt(ctx, s, "atisa", fmt.Sprintf("hallazgo real %d", i), ts); err != nil {
			t.Fatal(err)
		}
	}

	// 2 observaciones orgánicas de otro proyecto — sin esto, atisa seguiría
	// siendo el 100% del corpus orgánico y el caso no probaría nada.
	for i, ts := range []string{"2026-09-14T09:00:00Z", "2026-09-15T09:30:00Z"} {
		if err := saveObservationAt(ctx, s, "otro-proyecto", fmt.Sprintf("trabajo otro %d", i), ts); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if stats.TotalObservations != (bulkImportMinuteThreshold+5)+3+2 {
		t.Fatalf("TotalObservations = %d, want %d (bruto, incluye el import)", stats.TotalObservations, (bulkImportMinuteThreshold+5)+3+2)
	}
	if stats.OrganicTotal != 5 {
		t.Fatalf("OrganicTotal = %d, want 5 (excluye las %d filas del import masivo)", stats.OrganicTotal, bulkImportMinuteThreshold+5)
	}
	if stats.TopOrganicProject != "atisa" {
		t.Fatalf("TopOrganicProject = %q, want atisa", stats.TopOrganicProject)
	}
	wantShare := 3.0 / 5.0
	if diff := stats.TopOrganicShare - wantShare; diff > 0.001 || diff < -0.001 {
		t.Fatalf("TopOrganicShare = %.3f, want %.3f (3 de atisa / 5 orgánicas totales, sin las %d del import)",
			stats.TopOrganicShare, wantShare, bulkImportMinuteThreshold+5)
	}
}

// saveObservationAt guarda una observación y luego pisa su created_at por SQL
// directo — SaveObservation siempre usa time.Now(), y este test necesita
// controlar el minuto exacto para simular un import masivo vs. trabajo
// orgánico disperso en el tiempo.
func saveObservationAt(ctx context.Context, s *Store, project, title, createdAt string) error {
	obs, err := s.SaveObservation(ctx, SaveParams{
		Type:    TypeDiscovery,
		Title:   title,
		Content: "contenido de prueba",
		Project: project,
	})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE observations SET created_at = ? WHERE id = ?`), createdAt, obs.ID)
	return err
}
