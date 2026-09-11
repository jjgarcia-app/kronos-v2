package doctor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// TestRun_Observations_ReportsRealCounts confirma que el check "Observaciones"
// (agregado para que `kronos doctor` deje de mentir sobre el estado real del
// store, mismo bug que mem_doctor) refleja los conteos reales del backend
// configurado — antes `kronos doctor` no reportaba obs/relaciones pendientes
// en absoluto.
func TestRun_Observations_ReportsRealCounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "doctor-obs-test.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "sess-doctor-obs", "kronos-v2", "/tmp"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := st.SaveObservation(ctx, store.SaveParams{
			SessionID: "sess-doctor-obs",
			Type:      store.TypeDiscovery,
			Title:     "obs de prueba",
			Content:   fmt.Sprintf("contenido %d", i),
			Project:   "kronos-v2",
		}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	cfg := config.Default()
	cfg.DB.SQLitePath = dbPath

	report := doctor.Run(ctx, cfg)

	var obsCheck *doctor.Check
	for i := range report.Checks {
		if report.Checks[i].Name == "Observaciones" {
			obsCheck = &report.Checks[i]
		}
	}
	if obsCheck == nil {
		t.Fatal(`report no incluye el check "Observaciones"`)
	}
	if !strings.Contains(obsCheck.Detail, "2 obs") {
		t.Errorf("Detail = %q, esperaba reflejar las 2 observaciones reales guardadas", obsCheck.Detail)
	}
	if !strings.Contains(obsCheck.Detail, "sin relaciones pendientes") {
		t.Errorf("Detail = %q, esperaba 'sin relaciones pendientes' (DB fresca, sin candidatos)", obsCheck.Detail)
	}
}
