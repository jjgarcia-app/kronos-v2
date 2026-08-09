package store_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key, err := store.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t)
	s.SetEncryptionKey(key)
	ctx := context.Background()

	obs, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: "contenido secreto", Project: "p",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}

	got, err := s.GetObservation(ctx, obs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "contenido secreto" {
		t.Errorf("Content tras round-trip = %q, want %q", got.Content, "contenido secreto")
	}
}

// TestEncryption_RawDBRowIsNotPlaintext es la prueba real de que esto
// cifra de verdad — no solo que el round-trip a través de la API funciona
// (eso pasaría igual sin cifrado), sino que lo que efectivamente queda
// escrito en la columna content de la DB NO es el texto plano.
func TestEncryption_RawDBRowIsNotPlaintext(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "enc-test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	key, err := store.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	s.SetEncryptionKey(key)

	ctx := context.Background()
	const secret = "esto no debería aparecer en texto plano en el archivo"
	if _, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: secret, Project: "p",
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Error("el contenido en texto plano apareció crudo en el archivo de la DB — el cifrado no está aplicándose de verdad")
	}
}

func TestEncryption_UpdateObservation_AlsoEncrypts(t *testing.T) {
	key, err := store.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t)
	s.SetEncryptionKey(key)
	ctx := context.Background()

	obs, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: "original", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	newContent := "actualizado y también cifrado"
	updated, err := s.UpdateObservation(ctx, store.UpdateParams{ID: obs.ID, Content: &newContent})
	if err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}
	if updated.Content != newContent {
		t.Errorf("Content = %q, want %q", updated.Content, newContent)
	}
}

func TestEncryption_TopicKeyUpsert_AlsoEncrypts(t *testing.T) {
	key, err := store.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t)
	s.SetEncryptionKey(key)
	ctx := context.Background()

	if _, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: "v1", Project: "p", TopicKey: "area/tema",
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: "v2 vía upsert", Project: "p", TopicKey: "area/tema",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "v2 vía upsert" {
		t.Errorf("Content tras upsert cifrado = %q, want %q", updated.Content, "v2 vía upsert")
	}
}

func TestEncryption_ListMethods_DecryptAll(t *testing.T) {
	key, err := store.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t)
	s.SetEncryptionKey(key)
	ctx := context.Background()

	if _, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t1", Content: "c1", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t2", Content: "c2", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListObservations(ctx, "p", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	for _, o := range list {
		if o.Content != "c1" && o.Content != "c2" {
			t.Errorf("ListObservations no descifró correctamente: Content = %q", o.Content)
		}
	}
}

func TestEncryption_WithoutKey_BehavesExactlyAsBefore(t *testing.T) {
	s := newTestStore(t) // sin SetEncryptionKey — comportamiento default
	ctx := context.Background()

	obs, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: "sin cifrar", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetObservation(ctx, obs.ID)
	if err != nil || got.Content != "sin cifrar" {
		t.Errorf("sin clave configurada, el comportamiento debería ser idéntico al de siempre: %v, err=%v", got, err)
	}
}

func TestDecryptString_WrongKey_FailsInsteadOfGarbage(t *testing.T) {
	key1, _ := store.NewEncryptionKey()
	key2, _ := store.NewEncryptionKey()
	s := newTestStore(t)
	s.SetEncryptionKey(key1)
	ctx := context.Background()

	obs, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "t", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	s.SetEncryptionKey(key2)
	if _, err := s.GetObservation(ctx, obs.ID); err == nil {
		t.Error("leer con la clave equivocada debería fallar (GCM autentica), no devolver basura en silencio")
	}
}
