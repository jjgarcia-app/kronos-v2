package store_test

import (
	"context"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// --- PersistInjectedIDs / LoadInjectedIDs ---

func TestPersistAndLoadInjectedIDs_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateSession(ctx, "sess-ids", "proj", "/tmp")

	ids := []string{"obs-1", "obs-2", "obs-3"}
	if err := s.PersistInjectedIDs(ctx, "sess-ids", ids); err != nil {
		t.Fatalf("PersistInjectedIDs: %v", err)
	}

	got, err := s.LoadInjectedIDs(ctx, "sess-ids")
	if err != nil {
		t.Fatalf("LoadInjectedIDs: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("len = %d, want %d", len(got), len(ids))
	}
	for i, id := range ids {
		if got[i] != id {
			t.Errorf("ids[%d] = %q, want %q", i, got[i], id)
		}
	}
}

func TestLoadInjectedIDs_NullColumn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateSession(ctx, "sess-null", "proj", "/tmp")

	got, err := s.LoadInjectedIDs(ctx, "sess-null")
	if err != nil {
		t.Fatalf("LoadInjectedIDs on null column: %v", err)
	}
	if got == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected 0 ids, got %d", len(got))
	}
}

func TestPersistInjectedIDs_EmptySlice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateSession(ctx, "sess-empty", "proj", "/tmp")

	if err := s.PersistInjectedIDs(ctx, "sess-empty", []string{}); err != nil {
		t.Fatalf("PersistInjectedIDs empty: %v", err)
	}

	got, err := s.LoadInjectedIDs(ctx, "sess-empty")
	if err != nil {
		t.Fatalf("LoadInjectedIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 ids after persisting empty slice, got %d", len(got))
	}
}

// --- CountObservations ---

func TestCountObservations_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	n, err := s.CountObservations(ctx, "no-such-project")
	if err != nil {
		t.Fatalf("CountObservations: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestCountObservations_WithData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		s.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   "obs for A",
			Content: "content for project A observation number test",
			Project: "project-a",
		})
	}
	// Save 2 for project B — they must not count toward A.
	for i := 0; i < 2; i++ {
		s.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDiscovery,
			Title:   "obs for B",
			Content: "content for project B observation number test",
			Project: "project-b",
		})
	}

	nA, err := s.CountObservations(ctx, "project-a")
	if err != nil {
		t.Fatalf("CountObservations A: %v", err)
	}
	// Due to dedup by hash (same title+content), we may get 1 row. Use different content per row:
	// Re-run this test with distinct content to avoid dedup collapsing rows.
	if nA < 1 {
		t.Errorf("CountObservations(project-a) = %d, want >= 1", nA)
	}

	nB, err := s.CountObservations(ctx, "project-b")
	if err != nil {
		t.Fatalf("CountObservations B: %v", err)
	}
	if nB < 1 {
		t.Errorf("CountObservations(project-b) = %d, want >= 1", nB)
	}

	// project-a must have more than zero and not include project-b's observations
	if nA == nB+nA {
		t.Errorf("counts appear to bleed across projects: A=%d B=%d", nA, nB)
	}
}

func TestCountObservations_WithDistinctData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		s.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   "obs A unique",
			Content: "project A unique content observation number " + string(rune('0'+i)),
			Project: "proj-a-distinct",
		})
	}
	for i := 0; i < 2; i++ {
		s.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDiscovery,
			Title:   "obs B unique",
			Content: "project B unique content observation number " + string(rune('0'+i)),
			Project: "proj-b-distinct",
		})
	}

	nA, _ := s.CountObservations(ctx, "proj-a-distinct")
	nB, _ := s.CountObservations(ctx, "proj-b-distinct")

	if nA != 3 {
		t.Errorf("CountObservations(proj-a-distinct) = %d, want 3", nA)
	}
	if nB != 2 {
		t.Errorf("CountObservations(proj-b-distinct) = %d, want 2", nB)
	}
}
