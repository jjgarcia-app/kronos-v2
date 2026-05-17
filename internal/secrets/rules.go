package secrets

import "regexp"

// rule describes a single secret pattern.
// pattern must have exactly one capture group that isolates the secret value;
// the whole match is replaced, but last4 is taken from group 1.
// If the pattern has no groups, group 0 (the whole match) is used as the value.
type rule struct {
	id      string
	pattern *regexp.Regexp
	group   int // capture group index of the secret value; 0 = whole match
}

// builtinRules is the default set shipped with Kronos.
// Ordered from most-specific to least-specific to avoid partial overlaps.
var builtinRules = []*rule{
	// Cloud providers
	{
		id:      "aws-access-key",
		pattern: regexp.MustCompile(`((?:A3T[A-Z0-9]|AKIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ASIA)[A-Z0-9]{16})`),
		group:   1,
	},
	{
		id:      "aws-secret-key",
		pattern: regexp.MustCompile(`(?i)(?:aws[_\-. ]?secret[_\-. ]?(?:access[_\-. ]?)?key)\s*[=:]\s*["']?([a-zA-Z0-9+/]{40})["']?`),
		group:   1,
	},

	// GitHub
	{
		id:      "github-token",
		pattern: regexp.MustCompile(`(gh[pousr]_[a-zA-Z0-9]{36}|github_pat_[a-zA-Z0-9_]{82})`),
		group:   1,
	},

	// Anthropic
	{
		id:      "anthropic-api-key",
		pattern: regexp.MustCompile(`(sk-ant-(?:api\d\d-)?[a-zA-Z0-9\-_]{40,})`),
		group:   1,
	},

	// OpenAI / OpenAI-compatible
	{
		id:      "openai-api-key",
		pattern: regexp.MustCompile(`(sk-(?:proj-)?[a-zA-Z0-9]{48,})`),
		group:   1,
	},

	// JWT — three base64url segments separated by dots
	{
		id:      "jwt",
		pattern: regexp.MustCompile(`(eyJ[a-zA-Z0-9\-_]{10,}\.eyJ[a-zA-Z0-9\-_]{10,}\.[a-zA-Z0-9\-_.+/]{10,})`),
		group:   1,
	},

	// PEM private keys
	{
		id:      "private-key",
		pattern: regexp.MustCompile(`(-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY[^\n]*\n[\s\S]*?-----END (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----)`),
		group:   1,
	},

	// Generic bearer tokens in Authorization headers
	{
		id:      "bearer-token",
		pattern: regexp.MustCompile(`(?i)Authorization\s*:\s*Bearer\s+([a-zA-Z0-9\-_.~+/]{20,}=*)`),
		group:   1,
	},

	// Generic API keys / tokens in key=value assignment
	{
		id:      "generic-api-key",
		pattern: regexp.MustCompile(`(?i)(?:api[_\-]?key|api[_\-]?token|access[_\-]?key|secret[_\-]?key|secret[_\-]?token)\s*[=:]\s*["']?([a-zA-Z0-9\-_.~]{20,})["']?`),
		group:   1,
	},

	// Passwords in config / env files (quoted only to reduce false positives)
	{
		id:      "password",
		pattern: regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[=:]\s*["']([^"'\s]{8,})["']`),
		group:   1,
	},
}
