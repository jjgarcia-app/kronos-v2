package main

import (
	"os"
	"testing"
)

// (d) --help no escribe nada: ni en el data dir (DB, dumps) ni en HOME
// (vault por default). setupTempDataDir (ver backup_test.go) ya crea el
// directorio de datos vacío — si --help lo deja vacío, no tocó nada.
func TestRunExport_Help_NoSideEffects(t *testing.T) {
	kronosDir := setupTempDataDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runExport([]string{"--help"}); err != nil {
		t.Fatalf("runExport --help: %v", err)
	}

	if entries, _ := os.ReadDir(kronosDir); len(entries) != 0 {
		t.Errorf("--help no debería crear nada en el data dir, encontré: %v", entries)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("--help no debería crear nada en HOME, encontré: %v", entries)
	}
}

func TestRunGC_Help_NoSideEffects(t *testing.T) {
	kronosDir := setupTempDataDir(t)

	if err := runGC([]string{"--help"}); err != nil {
		t.Fatalf("runGC --help: %v", err)
	}
	if entries, _ := os.ReadDir(kronosDir); len(entries) != 0 {
		t.Errorf("--help no debería crear nada, encontré: %v", entries)
	}
}

func TestRunDoctor_Help_NoSideEffects(t *testing.T) {
	kronosDir := setupTempDataDir(t)

	if err := runDoctor([]string{"--help"}); err != nil {
		t.Fatalf("runDoctor --help: %v", err)
	}
	if entries, _ := os.ReadDir(kronosDir); len(entries) != 0 {
		t.Errorf("--help no debería crear nada, encontré: %v", entries)
	}
}

func TestRunExport_ShortHelpFlag(t *testing.T) {
	setupTempDataDir(t)
	t.Setenv("HOME", t.TempDir())

	if err := runExport([]string{"-h"}); err != nil {
		t.Fatalf("runExport -h: %v", err)
	}
}
