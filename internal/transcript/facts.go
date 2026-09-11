package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// Facts is a structured, deterministic extraction of the tail of a
// transcript — the input to the no-LLM digest (ver internal/hooks/digest.go)
// that always has something concrete to save, unlike TailExcerpt (plain
// text) which only feeds the optional LLM enrichment.
type Facts struct {
	// Prompts: mensajes del usuario, más reciente al final, ≤8, cada uno
	// recortado a ≤300 chars.
	Prompts []string
	// Files: de tool_use Edit/Write/MultiEdit/NotebookEdit, input.file_path,
	// deduplicados, más reciente primero, ≤15.
	Files []string
	// Commands: de tool_use Bash, input.command (primera línea),
	// deduplicados, ≤10, cada uno ≤120 chars.
	Commands []string
	// LastAssistant: último bloque de texto del asistente, ≤600 chars.
	LastAssistant string
	// Turns: cuántos mensajes de usuario con texto real hay en la cola leída.
	Turns int
}

const (
	factPromptMaxChars    = 300
	factCommandMaxChars   = 120
	factAssistantMaxChars = 600
	factMaxPrompts        = 8
	factMaxFiles          = 15
	factMaxCommands       = 10

	// factsMaxFileReadBytes: por debajo de esto, leer el archivo entero es
	// simple y sigue siendo rápido (unos pocos ms) — evita la complejidad de
	// calcular una ventana desde el final para el caso común.
	factsMaxFileReadBytes = 8 * 1024 * 1024
	// factsTailReadBytes: ventana leída desde el final para archivos más
	// grandes que factsMaxFileReadBytes — suficiente para cubrir muchas más
	// líneas que maxEvents en un transcript real sin cargar el archivo
	// completo en memoria.
	factsTailReadBytes = 2 * 1024 * 1024
)

var fileEditTools = map[string]bool{
	"Edit":         true,
	"Write":        true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

type factsBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type factsInput struct {
	FilePath string `json:"file_path"`
	Command  string `json:"command"`
}

// TailFacts lee la cola de un transcript (.jsonl) y devuelve una extracción
// estructurada de los últimos maxEvents eventos — sin nunca fallar el hook
// que la llama: cualquier error de lectura o parseo devuelve Facts{} vacío
// (error siempre nil, mismo contrato fail-open que TailExcerpt).
func TailFacts(path string, maxEvents int) (Facts, error) {
	if path == "" || maxEvents <= 0 {
		return Facts{}, nil
	}

	lines, err := tailFactLines(path, maxEvents)
	if err != nil {
		return Facts{}, nil
	}

	return buildFacts(lines), nil
}

// tailFactLines devuelve hasta maxEvents líneas no vacías del final del
// archivo, en orden original (más vieja primero). Archivos ≤
// factsMaxFileReadBytes se leen completos; más grandes, solo la ventana
// final (factsTailReadBytes), descartando la primera línea si quedó cortada
// por el seek.
func tailFactLines(path string, maxEvents int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	start := int64(0)
	seeked := false
	if info.Size() > factsMaxFileReadBytes {
		window := int64(factsTailReadBytes)
		if info.Size() > window {
			start = info.Size() - window
			seeked = true
		}
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
	}

	r := bufio.NewReader(f)
	if seeked {
		// el seek probablemente cayó a mitad de una línea — descartarla.
		_, _ = r.ReadString('\n')
	}

	var lines []string
	for {
		line, readErr := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
		if readErr != nil {
			break
		}
	}

	if len(lines) > maxEvents {
		lines = lines[len(lines)-maxEvents:]
	}
	return lines, nil
}

func buildFacts(lines []string) Facts {
	var facts Facts
	var files []string    // orden de aparición (más vieja primero)
	var commands []string // orden de aparición (más vieja primero)

	for _, line := range lines {
		var tl transcriptLine
		if err := json.Unmarshal([]byte(line), &tl); err != nil {
			continue // línea rota — se ignora, no aborta la lectura
		}
		if tl.Message == nil {
			continue
		}

		switch tl.Type {
		case "user":
			if text := extractPlainOrBlocks(tl.Message.Content); text != "" {
				facts.Turns++
				facts.Prompts = append(facts.Prompts, truncate(text, factPromptMaxChars))
			}
		case "assistant":
			blocks := decodeBlocks(tl.Message.Content)
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if t := strings.TrimSpace(b.Text); t != "" {
						facts.LastAssistant = truncate(t, factAssistantMaxChars)
					}
				case "tool_use":
					var in factsInput
					_ = json.Unmarshal(b.Input, &in)
					if fileEditTools[b.Name] && in.FilePath != "" {
						files = append(files, in.FilePath)
					} else if b.Name == "Bash" && in.Command != "" {
						firstLine, _, _ := strings.Cut(in.Command, "\n")
						commands = append(commands, truncate(strings.TrimSpace(firstLine), factCommandMaxChars))
					}
				}
			}
		}
	}

	if len(facts.Prompts) > factMaxPrompts {
		facts.Prompts = facts.Prompts[len(facts.Prompts)-factMaxPrompts:]
	}
	facts.Files = dedupeMostRecentFirst(files, factMaxFiles)
	facts.Commands = dedupeMostRecentFirst(commands, factMaxCommands)

	return facts
}

// decodeBlocks intenta decodificar content como array de content blocks;
// devuelve nil si es un string plano (los assistant messages con tool_use
// siempre vienen como array, nunca como string plano).
func decodeBlocks(content json.RawMessage) []factsBlock {
	var blocks []factsBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	return blocks
}

// extractPlainOrBlocks replica extractTurnText pero sin el prefijo "role: "
// — content puede ser un string plano o un array de content blocks (los
// tool_result quedan afuera porque su Type no es "text").
func extractPlainOrBlocks(content json.RawMessage) string {
	var plain string
	if err := json.Unmarshal(content, &plain); err == nil {
		return strings.TrimSpace(plain)
	}
	blocks := decodeBlocks(content)
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// dedupeMostRecentFirst recorre items (más vieja primero, orden de
// aparición) de atrás para adelante, quedándose con la primera ocurrencia de
// cada valor — que, recorriendo al revés, es la más reciente — hasta max
// elementos.
func dedupeMostRecentFirst(items []string, max int) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	var out []string
	for i := len(items) - 1; i >= 0; i-- {
		v := items[i]
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
