package transcript_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/transcript"
)

func writeFactsTranscript(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTailFacts_PlainStringPrompt(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"arreglá el bug del gate"}}`,
	})
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Prompts) != 1 || facts.Prompts[0] != "arreglá el bug del gate" {
		t.Errorf("Prompts = %v", facts.Prompts)
	}
	if facts.Turns != 1 {
		t.Errorf("Turns = %d, want 1", facts.Turns)
	}
}

func TestTailFacts_ArrayContentTextBlocks(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"otra pregunta real"}]}}`,
	})
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Prompts) != 1 || facts.Prompts[0] != "otra pregunta real" {
		t.Errorf("Prompts = %v", facts.Prompts)
	}
}

func TestTailFacts_SkipsToolResultOnlyUserMessages(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"salida del comando"}]}}`,
		`{"type":"user","message":{"role":"user","content":"pregunta real del usuario"}}`,
	})
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Prompts) != 1 {
		t.Fatalf("Prompts = %v, esperaba solo el prompt real (tool_result no cuenta)", facts.Prompts)
	}
	if facts.Turns != 1 {
		t.Errorf("Turns = %d, want 1 (tool_result no es un turno de usuario)", facts.Turns)
	}
}

func TestTailFacts_ExtractsEditWriteBashToolUse(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"reviso y arreglo el archivo"},` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/repo/internal/foo.go","old_string":"a","new_string":"b"}}` +
			`]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"/repo/internal/bar.go","content":"package bar"}}` +
			`]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"go test ./...\nsome second line","description":"run tests"}}` +
			`]}}`,
	})
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Files) != 2 {
		t.Fatalf("Files = %v, want 2 entries", facts.Files)
	}
	// más reciente primero: bar.go (Write, línea 2) antes que foo.go (Edit, línea 1)
	if facts.Files[0] != "/repo/internal/bar.go" || facts.Files[1] != "/repo/internal/foo.go" {
		t.Errorf("Files = %v, orden esperado [bar.go, foo.go]", facts.Files)
	}
	if len(facts.Commands) != 1 || facts.Commands[0] != "go test ./..." {
		t.Errorf("Commands = %v", facts.Commands)
	}
	if facts.LastAssistant != "reviso y arreglo el archivo" {
		t.Errorf("LastAssistant = %q", facts.LastAssistant)
	}
}

func TestTailFacts_DeduplicatesFilesKeepingMostRecent(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/x.go"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/y.go"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/x.go"}}]}}`,
	})
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Files) != 2 {
		t.Fatalf("Files = %v, want 2 (deduplicado)", facts.Files)
	}
	if facts.Files[0] != "/repo/x.go" {
		t.Errorf("Files[0] = %q, want /repo/x.go (última aparición)", facts.Files[0])
	}
}

func TestTailFacts_MalformedLines_SkippedGracefully(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`esto no es json`,
		`{"type":"user","message":{"role":"user","content":"prompt válido pese a la línea rota"}}`,
	})
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Prompts) != 1 || facts.Prompts[0] != "prompt válido pese a la línea rota" {
		t.Errorf("Prompts = %v", facts.Prompts)
	}
}

func TestTailFacts_MissingFile_ReturnsEmptyNoError(t *testing.T) {
	facts, err := transcript.TailFacts(filepath.Join(t.TempDir(), "no-existe.jsonl"), 100)
	if err != nil {
		t.Errorf("archivo faltante no debería ser error, got: %v", err)
	}
	if len(facts.Prompts) != 0 || len(facts.Files) != 0 || facts.Turns != 0 {
		t.Errorf("esperaba Facts vacío, got: %+v", facts)
	}
}

func TestTailFacts_EmptyPath_ReturnsEmptyNoError(t *testing.T) {
	facts, err := transcript.TailFacts("", 100)
	if err != nil {
		t.Errorf("path vacío no debería ser error, got: %v", err)
	}
	if len(facts.Prompts) != 0 {
		t.Errorf("esperaba Facts vacío, got: %+v", facts)
	}
}

func TestTailFacts_CapsPromptsAtEight_MostRecentLast(t *testing.T) {
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"prompt numero %d"}}`, i))
	}
	path := writeFactsTranscript(t, lines)
	facts, err := transcript.TailFacts(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Prompts) != 8 {
		t.Fatalf("Prompts = %d, want 8 (tope)", len(facts.Prompts))
	}
	if facts.Prompts[len(facts.Prompts)-1] != "prompt numero 11" {
		t.Errorf("último prompt = %q, want 'prompt numero 11' (más reciente al final)", facts.Prompts[len(facts.Prompts)-1])
	}
	if facts.Turns != 12 {
		t.Errorf("Turns = %d, want 12 (cuenta todos los leídos, no solo los que entran en el tope)", facts.Turns)
	}
}

func TestTailFacts_LargeTranscript_OnlyReadsTail(t *testing.T) {
	var sb strings.Builder
	// > 8MB para forzar el camino de lectura por ventana desde el final.
	filler := strings.Repeat("x", 2000)
	for i := 0; i < 5000; i++ {
		sb.WriteString(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"relleno %d %s"}]}}`, i, filler))
		sb.WriteString("\n")
	}
	sb.WriteString(`{"type":"user","message":{"role":"user","content":"prompt final real, el que importa"}}` + "\n")

	path := filepath.Join(t.TempDir(), "big.jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 8*1024*1024 {
		t.Fatalf("el archivo de prueba no llegó a superar 8MB (%d bytes) — ajustar el relleno", info.Size())
	}

	facts, err := transcript.TailFacts(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Prompts) != 1 || facts.Prompts[0] != "prompt final real, el que importa" {
		t.Errorf("Prompts = %v, esperaba encontrar el prompt final pese al archivo grande", facts.Prompts)
	}
}

func TestTailFacts_ZeroMaxEvents_ReturnsEmptyNoError(t *testing.T) {
	path := writeFactsTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"algo"}}`,
	})
	facts, err := transcript.TailFacts(path, 0)
	if err != nil {
		t.Errorf("maxEvents=0 no debería ser error, got: %v", err)
	}
	if len(facts.Prompts) != 0 {
		t.Errorf("esperaba Facts vacío con maxEvents=0, got: %+v", facts)
	}
}
