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

Additionally, extract up to 3 standalone facts worth remembering as their OWN piece of knowledge — not buried in a session summary — for someone querying memory in a DIFFERENT, FUTURE session, possibly weeks from now. A fact only qualifies if it will still be true and useful in 30 days: a bug and its root cause, an architecture/design decision, a config change and why, a non-obvious discovery or gotcha, a reusable pattern, or a learned preference. Do NOT propose facts for routine/ephemeral activity ("ran the tests", "read a file", "started the session") — if nothing qualifies, return an empty list. Each fact must be self-contained (title and content make sense with zero session context).

Respond ONLY with valid JSON (no markdown, no explanation outside JSON):
{"content": "<updated running summary, plain text with line breaks, en español>",
 "facts": [{"type": "<bugfix|decision|config|discovery|pattern|preference>", "title": "<short searchable phrase, verb + what, en español>", "content": "<Qué: ...\nPor qué: ...\nCómo aplicar: ..., en español>"}]}`,
		prior, truncate(excerpt, 6000),
	)
}

// buildDigestFactsOnlyPrompt arma el pedido acotado del reintento de
// "solo hechos" (ver ExtractDigestFacts / DigestPendingKindFacts): la prosa
// del digest ya está guardada de una corrida anterior, así que este prompt
// no la vuelve a pedir — es más corto y barato que buildDigestPrompt, con la
// misma extracción de hechos y la misma válvula explícita de "no hay nada"
// que evita reintentar al pedo.
func buildDigestFactsOnlyPrompt(excerpt string, maxFacts int) string {
	return fmt.Sprintf(`You are extracting standalone facts from a coding-session transcript excerpt for a persistent memory system. The running session summary was already saved separately — this is only about facts worth remembering as their OWN piece of knowledge, for someone querying memory in a DIFFERENT, FUTURE session, possibly weeks from now.

Transcript excerpt (most recent turns, oldest first):
---
%s
---

List up to %d such facts. A fact only qualifies if it will still be true and useful in 30 days: a bug and its root cause, an architecture/design decision, a config change and why, a non-obvious discovery or gotcha, a reusable pattern, or a learned preference. Do NOT propose facts for routine/ephemeral activity ("ran the tests", "read a file", "started the session"). Each fact must be self-contained (title and content make sense with zero session context).

If at least one fact qualifies, respond ONLY with a valid JSON array (no markdown, no explanation outside JSON):
[{"type": "<bugfix|decision|config|discovery|pattern|preference>", "title": "<short searchable phrase, verb + what, en español>", "content": "<Qué: ...\nPor qué: ...\nCómo aplicar: ..., en español>"}]

If nothing qualifies, respond with exactly this line and nothing else:
FACTS: ninguno`,
		truncate(excerpt, 6000), maxFacts,
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
