package store

import (
	"context"
	"errors"
	"testing"
)

// TestDualStore_MarkDown_IntegrityErrorsDoNotDegrade: un FK o una PK duplicada
// no son fallas de disponibilidad del primary. Antes marcaban el primary caído
// 5-15s y TODAS las lecturas caían al buffer local (memoria congelada). Visto
// en producción el 2026-09-11 (03:07:32/03:52:47 por FK de user_prompts, y al
// reusar un session_id durante las pruebas del bloque core). Este test fija el
// contrato: solo las fallas de conexión degradan el store.
func TestDualStore_MarkDown_IntegrityErrorsDoNotDegrade(t *testing.T) {
	cases := []struct {
		name string
		err  error
		down bool
	}{
		{"FK de sesion", errors.New(`ERROR: insert or update on table "user_prompts" violates foreign key constraint "user_prompts_session_id_fkey" (SQLSTATE 23503)`), false},
		{"PK duplicada", errors.New(`ERROR: duplicate key value violates unique constraint "sessions_pkey" (SQLSTATE 23505)`), false},
		{"cancelacion del llamador", context.Canceled, false},
		{"deadline del llamador", context.DeadlineExceeded, false},
		{"falla real de conexion", errors.New("dial tcp 127.0.0.1:5433: connect: connection refused"), true},
		{"reset de conexion", errors.New("read tcp: connection reset by peer"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := newTestDualStore(t)
			ds.markDown(c.err)
			if got := ds.isPrimaryDown(); got != c.down {
				t.Fatalf("isPrimaryDown() = %v, quiero %v para %q", got, c.down, c.err)
			}
		})
	}
}
