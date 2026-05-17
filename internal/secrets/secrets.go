package secrets

import (
	"fmt"
	"strings"
)

// Detector scans text for secrets and redacts them.
type Detector struct {
	rules []*rule
}

// New returns a Detector loaded with the built-in ruleset.
func New() *Detector {
	return &Detector{rules: builtinRules}
}

// Match describes a detected secret.
type Match struct {
	RuleID string
	Value  string // the raw secret value (from the capture group)
	Start  int    // byte offset of whole match in original text
	End    int
	Ref    string // replacement reference, e.g. [SECRET:aws-access-key:X7K2]
}

// Detect returns all non-overlapping secrets found in text.
// When two rules match at the same position the earlier rule wins (more specific).
func (d *Detector) Detect(text string) []Match {
	var all []Match

	for _, r := range d.rules {
		for _, idx := range r.pattern.FindAllStringSubmatchIndex(text, -1) {
			value := extractGroup(text, idx, r.group)
			all = append(all, Match{
				RuleID: r.id,
				Value:  value,
				Start:  idx[0],
				End:    idx[1],
				Ref:    makeRef(r.id, value),
			})
		}
	}

	// Sort ascending by Start; for ties prefer the earlier rule (already ordered by insertion).
	sortMatchesAsc(all)

	// Remove overlapping matches: keep the first one at each byte range.
	out := all[:0]
	lastEnd := 0
	for _, m := range all {
		if m.Start >= lastEnd {
			out = append(out, m)
			lastEnd = m.End
		}
	}
	return out
}

// Redact replaces every detected secret in text with a [SECRET:type:last4] reference.
// Returns the original text unchanged if no secrets are found.
func (d *Detector) Redact(text string) string {
	matches := d.Detect(text)
	if len(matches) == 0 {
		return text
	}

	var sb strings.Builder
	pos := 0
	for _, m := range matches {
		sb.WriteString(text[pos:m.Start])
		sb.WriteString(m.Ref)
		pos = m.End
	}
	sb.WriteString(text[pos:])
	return sb.String()
}

// HasSecrets reports whether text contains any detectable secret.
func (d *Detector) HasSecrets(text string) bool {
	for _, r := range d.rules {
		if r.pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// --- helpers ---

func extractGroup(text string, idx []int, group int) string {
	if group == 0 || len(idx) < 2*(group+1) {
		return text[idx[0]:idx[1]]
	}
	gs, ge := idx[group*2], idx[group*2+1]
	if gs < 0 {
		return text[idx[0]:idx[1]]
	}
	return text[gs:ge]
}

func makeRef(ruleID, value string) string {
	return fmt.Sprintf("[SECRET:%s:%s]", ruleID, last4Chars(value))
}

func last4Chars(s string) string {
	runes := []rune(s)
	if len(runes) <= 4 {
		return string(runes)
	}
	return string(runes[len(runes)-4:])
}

func sortMatchesAsc(m []Match) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j].Start < m[j-1].Start; j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

// DefaultDetector is a package-level Detector using built-in rules.
var DefaultDetector = New()

// Redact is a package-level shortcut using DefaultDetector.
func Redact(text string) string { return DefaultDetector.Redact(text) }

// HasSecrets is a package-level shortcut using DefaultDetector.
func HasSecrets(text string) bool { return DefaultDetector.HasSecrets(text) }
