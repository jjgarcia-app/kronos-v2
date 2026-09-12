package llm

import (
	"fmt"
	"strings"
)

func buildDigestPrompt(previousDigest, excerpt string) string {
	prior := "No hay resumen previo — esta es la primera actualización."
	if strings.TrimSpace(previousDigest) != "" {
		prior = fmt.Sprintf("Resumen previo:\n---\n%s\n---", truncate(previousDigest, 3000))
	}

	return fmt.Sprintf(`You maintain a running summary of an ongoing coding session for a persistent memory system — so that later, anyone (or any agent) querying memory gets a real answer to "what has been worked on here", without re-reading the full transcript.

%s

New transcript excerpt since the last update (oldest first):
---
%s
---

Extend the summary with anything new and concrete from this excerpt: what was investigated, decided, fixed, or built. Keep it dense — short bullet points, no filler, no restating obvious code. Preserve earlier content that's still relevant; drop anything superseded by newer information. If truly nothing new and substantive happened (small talk, routine back-and-forth with no real progress), return the previous summary completely unchanged.

Respond ONLY with valid JSON (no markdown, no explanation outside JSON):
{"content": "<updated running summary, plain text with line breaks, en español>"}`,
		prior, truncate(excerpt, 6000),
	)
}

func buildExtractPrompt(excerpt string) string {
	return fmt.Sprintf(`You are screening a coding-session transcript excerpt for a persistent memory system, right before the conversation context gets compacted (destroyed).

Decide if this excerpt documents something worth remembering permanently:
- a bug that got fixed, together with its root cause
- an architecture, design, or implementation decision that was made
- a non-obvious discovery: a gotcha, an unexpected behavior, a hard-won workaround
- a configuration change and the reason for it

Do NOT flag: small talk, routine unremarkable edits, restating what the code already makes obvious, work that's still in progress with no conclusion yet.

Transcript excerpt (most recent turns, oldest first):
---
%s
---

Respond ONLY with valid JSON (no markdown, no explanation outside JSON).
If nothing qualifies: {"found": false}
If something qualifies: {"found": true, "title": "<short searchable phrase, verb + what>", "content": "<Qué: ...\nPor qué: ...\nCómo aplicar: ...>"}`,
		truncate(excerpt, 6000),
	)
}

func buildJudgePrompt(aTitle, aContent, bTitle, bContent string, similarity float32) string {
	return fmt.Sprintf(
		`You are a knowledge conflict analyzer for a persistent memory system.
Two memory observations have %.0f%% semantic similarity and require classification.

Observation A:
Title: %s
Content: %s

Observation B:
Title: %s
Content: %s

Classify their relationship by choosing exactly ONE verb:
- "conflicts_with"  → contradictory or mutually exclusive information
- "supersedes"      → A replaces/updates B with newer or more accurate info
- "related"         → same topic, complementary, should coexist
- "compatible"      → different aspects of a shared domain, no conflict
- "scoped"          → A is a specific instance/subset of B (or vice versa)
- "not_conflict"    → topically unrelated despite surface similarity

Respond ONLY with valid JSON (no markdown, no explanation outside JSON):
{"relation": "<verb>", "reason": "<one concise sentence>", "confidence": <0.0-1.0>}`,
		float64(similarity)*100,
		aTitle, truncate(aContent, 400),
		bTitle, truncate(bContent, 400),
	)
}
