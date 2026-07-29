package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestInsertRelationPending_UsesAppGeneratedID confirma que
// memory_relations.id (el judgment_id que ve el usuario en mem_judge)
// también usa NewID() en vez de AUTOINCREMENT/BIGSERIAL — mismo riesgo de
// divergencia entre backends que observations.id, mismo fix (ver idgen.go).
func TestInsertRelationPending_UsesAppGeneratedID(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	id, err := s.insertRelationPending(ctx, "sync-id-source", "sync-id-target")
	if err != nil {
		t.Fatalf("insertRelationPending: %v", err)
	}

	const maxRealisticLegacyID = 100_000
	if id <= maxRealisticLegacyID {
		t.Errorf("id = %d — esperaba un ID estilo Snowflake, muy por encima de cualquier autoincrement legacy (%d)", id, maxRealisticLegacyID)
	}
}
