package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// EncryptionKeySize es el tamaño requerido de la clave: AES-256 (32 bytes).
const EncryptionKeySize = 32

// encryptString cifra plaintext con AES-256-GCM. El nonce se genera al azar
// por llamada y va prependeado al ciphertext (patrón estándar de GCM — el
// nonce no es secreto, solo tiene que ser único por clave). Salida en
// base64 estándar, texto plano para guardar en una columna TEXT sin
// preocuparse por bytes no imprimibles.
func encryptString(key []byte, plaintext string) (string, error) {
	if len(key) != EncryptionKeySize {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", EncryptionKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptString revierte encryptString. Devuelve error si la clave es
// incorrecta o el texto fue alterado (GCM es AEAD — falla la autenticación
// en vez de devolver basura en silencio).
func decryptString(key []byte, encoded string) (string, error) {
	if len(key) != EncryptionKeySize {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", EncryptionKeySize, len(key))
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt (clave incorrecta o contenido alterado): %w", err)
	}
	return string(plaintext), nil
}

// NewEncryptionKey genera una clave AES-256 al azar, lista para
// base64-encodear y guardar en config (ver Store.SetEncryptionKey).
func NewEncryptionKey() ([]byte, error) {
	key := make([]byte, EncryptionKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// SetEncryptionKey activa cifrado transparente de content-at-rest para
// ESTE *Store en particular: content se cifra antes de cada INSERT/UPDATE y
// se descifra después de cada SELECT. Pensado para usarse solo en el primary
// remoto (ver docs/architecture.md, "Limitaciones conocidas") — el buffer
// SQLite local se deja siempre sin cifrar, porque FTS necesita texto plano
// para poder indexar.
//
// TODO(no conectado todavía): falta resolver que DualStore.Search no puede
// usar FTS server-side de Postgres contra contenido cifrado — hoy Search
// intenta primary primero. Conectar esto a config/DualStore requiere decidir
// esa política antes (forzar Search siempre a buffer si primary tiene clave,
// o alguna otra estrategia) — ver conversación de diseño, no es un olvido.
func (s *Store) SetEncryptionKey(key []byte) {
	s.encryptionKey = key
}

func (s *Store) maybeEncrypt(plaintext string) (string, error) {
	if s.encryptionKey == nil {
		return plaintext, nil
	}
	return encryptString(s.encryptionKey, plaintext)
}

func (s *Store) maybeDecrypt(stored string) (string, error) {
	if s.encryptionKey == nil {
		return stored, nil
	}
	return decryptString(s.encryptionKey, stored)
}
