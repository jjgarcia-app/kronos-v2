package transcript_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/transcript"
)

func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTailExcerpt_ExtractsPlainStringContent(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"cómo arreglo el bug del gate"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"encontré la causa raíz en pre_tool_use.go"}}`,
	})

	got, err := transcript.TailExcerpt(path, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "cómo arreglo el bug del gate") {
		t.Errorf("falta el texto del usuario: %q", got)
	}
	if !strings.Contains(got, "encontré la causa raíz") {
		t.Errorf("falta el texto del assistant: %q", got)
	}
}

func TestTailExcerpt_ExtractsTextBlocksFromArrayContent_SkipsToolBlocks(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"reviso el archivo primero"},` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"x.go"}}` +
			`]}}`,
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"contenido gigante del archivo leído..."}` +
			`]}}`,
	})

	got, err := transcript.TailExcerpt(path, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "reviso el archivo primero") {
		t.Errorf("falta el bloque de texto real: %q", got)
	}
	if strings.Contains(got, "contenido gigante") {
		t.Errorf("no debería incluir contenido de tool_result: %q", got)
	}
	if strings.Contains(got, "tool_use") {
		t.Errorf("no debería incluir el nombre del tool: %q", got)
	}
}

func TestTailExcerpt_SkipsNonMessageEvents(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"type":"summary","summary":"algo"}`,
		`{"type":"file-history-snapshot"}`,
		`{"type":"user","message":{"role":"user","content":"pregunta real"}}`,
	})

	got, err := transcript.TailExcerpt(path, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user: pregunta real" {
		t.Errorf("esperaba solo el turno real, got: %q", got)
	}
}

func TestTailExcerpt_RespectsMaxChars_KeepsMostRecent(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"AAAAAAAAAA turno viejo"}}`,
		`{"type":"user","message":{"role":"user","content":"BBBBBBBBBB turno reciente"}}`,
	})

	got, err := transcript.TailExcerpt(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "AAAAAAAAAA") {
		t.Errorf("debería haber recortado el turno más viejo, got: %q", got)
	}
	if !strings.Contains(got, "reciente") {
		t.Errorf("debería conservar el turno más reciente, got: %q", got)
	}
}

func TestTailExcerpt_MissingFile_ReturnsEmptyNoError(t *testing.T) {
	got, err := transcript.TailExcerpt(filepath.Join(t.TempDir(), "no-existe.jsonl"), 4000)
	if err != nil {
		t.Errorf("archivo faltante no debería ser error, got: %v", err)
	}
	if got != "" {
		t.Errorf("esperaba excerpt vacío, got: %q", got)
	}
}

func TestTailExcerpt_EmptyPath_ReturnsEmptyNoError(t *testing.T) {
	got, err := transcript.TailExcerpt("", 4000)
	if err != nil || got != "" {
		t.Errorf("path vacío debería devolver (\"\", nil), got (%q, %v)", got, err)
	}
}

func TestTailExcerpt_MalformedLines_SkippedGracefully(t *testing.T) {
	path := writeTranscript(t, []string{
		`esto no es json`,
		`{"type":"assistant","message":{"role":"assistant","content":"turno válido"}}`,
	})

	got, err := transcript.TailExcerpt(path, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "turno válido") {
		t.Errorf("debería extraer el turno válido pese a la línea rota: %q", got)
	}
}
