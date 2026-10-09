package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func TestRunRules_DefaultTarget_PrintsClaudeMD(t *testing.T) {
	chdirTemp(t)
	if err := runRules(nil); err != nil {
		t.Fatalf("runRules: %v", err)
	}
}

func TestRunRules_UnknownTarget_Errors(t *testing.T) {
	chdirTemp(t)
	if err := runRules([]string{"--target", "cursor"}); err == nil {
		t.Fatal("esperaba error con target desconocido")
	}
}

func TestInstallRules_DefaultTarget_WritesClaudeMD(t *testing.T) {
	dir := chdirTemp(t)
	if err := runRules([]string{"--install"}); err != nil {
		t.Fatalf("runRules --install: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}
	if !strings.Contains(string(data), "Kronos — Memoria persistente") {
		t.Error("CLAUDE.md missing kronos fragment")
	}
}

func TestInstallRules_CodexTarget_WritesAgentsMD(t *testing.T) {
	dir := chdirTemp(t)
	if err := runRules([]string{"--install", "--target", "codex"}); err != nil {
		t.Fatalf("runRules --install --target codex: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(data), "Kronos — Memoria persistente") {
		t.Error("AGENTS.md missing kronos fragment")
	}
	// No debe haberse tocado CLAUDE.md.
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md should not be created when target=codex")
	}
}

func TestInstallRules_CodexTarget_Idempotent(t *testing.T) {
	dir := chdirTemp(t)
	if err := runRules([]string{"--install", "--target", "codex"}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := runRules([]string{"--install", "--target", "codex"}); err != nil {
		t.Fatalf("second install: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	count := strings.Count(string(data), "Kronos — Memoria persistente")
	if count != 1 {
		t.Errorf("expected 1 occurrence of kronos section, got %d", count)
	}
}

func TestInstallRules_CodexTarget_AppendsToExistingAgentsMD(t *testing.T) {
	dir := chdirTemp(t)
	existing := "# Mis instrucciones\n\nUsa pnpm siempre.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runRules([]string{"--install", "--target", "codex"}); err != nil {
		t.Fatalf("runRules --install --target codex: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Usa pnpm siempre.") {
		t.Error("existing AGENTS.md content was lost")
	}
	if !strings.Contains(string(data), "Kronos — Memoria persistente") {
		t.Error("kronos fragment not appended")
	}
}
