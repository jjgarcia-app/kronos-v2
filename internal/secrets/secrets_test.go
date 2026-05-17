package secrets_test

import (
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
)

func TestRedact_AWSAccessKey(t *testing.T) {
	input := "clave: AKIAIOSFODNN7EXAMPLE y mas texto"
	out := secrets.Redact(input)
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS access key not redacted: %s", out)
	}
	if !strings.Contains(out, "[SECRET:aws-access-key:") {
		t.Errorf("expected aws-access-key ref: %s", out)
	}
}

func TestRedact_AnthropicKey(t *testing.T) {
	// Key appears without a generic key= prefix so anthropic-api-key rule fires.
	input := "usa esta clave: sk-ant-api03-abcdefghijklmnopqrstuvwxyz01234567890123456789abc para el request"
	out := secrets.Redact(input)
	if strings.Contains(out, "sk-ant-") {
		t.Errorf("Anthropic key not redacted: %s", out)
	}
	if !strings.Contains(out, "[SECRET:anthropic-api-key:") {
		t.Errorf("expected anthropic-api-key ref: %s", out)
	}
}

func TestRedact_AnthropicKey_WithEnvPrefix(t *testing.T) {
	// When wrapped in ANTHROPIC_API_KEY=..., generic-api-key fires first — still redacted.
	input := "export ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz01234567890123456789abc"
	out := secrets.Redact(input)
	if strings.Contains(out, "sk-ant-") {
		t.Errorf("Anthropic key not redacted in env form: %s", out)
	}
	if !strings.Contains(out, "[SECRET:") {
		t.Errorf("expected a SECRET ref: %s", out)
	}
}

func TestRedact_GitHubToken(t *testing.T) {
	input := "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	out := secrets.Redact(input)
	if strings.Contains(out, "ghp_") {
		t.Errorf("GitHub token not redacted: %s", out)
	}
	if !strings.Contains(out, "[SECRET:github-token:") {
		t.Errorf("expected github-token ref: %s", out)
	}
}

func TestRedact_JWT(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyMTIzIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	input := "Authorization: " + jwt
	out := secrets.Redact(input)
	if strings.Contains(out, jwt) {
		t.Errorf("JWT not redacted: %s", out)
	}
	if !strings.Contains(out, "[SECRET:jwt:") {
		t.Errorf("expected jwt ref: %s", out)
	}
}

func TestRedact_GenericAPIKey(t *testing.T) {
	input := `api_key = "my-super-secret-key-value-here-long"`
	out := secrets.Redact(input)
	if strings.Contains(out, "my-super-secret") {
		t.Errorf("generic api_key not redacted: %s", out)
	}
	if !strings.Contains(out, "[SECRET:generic-api-key:") {
		t.Errorf("expected generic-api-key ref: %s", out)
	}
}

func TestRedact_Password(t *testing.T) {
	input := `password = "s3cr3tP@ssw0rd!"`
	out := secrets.Redact(input)
	if strings.Contains(out, "s3cr3tP@ssw0rd!") {
		t.Errorf("password not redacted: %s", out)
	}
	if !strings.Contains(out, "[SECRET:password:") {
		t.Errorf("expected password ref: %s", out)
	}
}

func TestRedact_NoSecrets(t *testing.T) {
	input := "Este texto no tiene secretos. Solo código normal."
	out := secrets.Redact(input)
	if out != input {
		t.Errorf("text without secrets should be unchanged\ngot: %s", out)
	}
}

func TestRedact_MultipleSecrets(t *testing.T) {
	input := "key1: AKIAIOSFODNN7EXAMPLE y token: ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	out := secrets.Redact(input)
	if strings.Contains(out, "AKIA") {
		t.Error("AWS key not redacted in multi-secret text")
	}
	if strings.Contains(out, "ghp_") {
		t.Error("GitHub token not redacted in multi-secret text")
	}
	if strings.Count(out, "[SECRET:") != 2 {
		t.Errorf("expected 2 refs, got: %s", out)
	}
}

func TestRedact_PreservesRef_Last4(t *testing.T) {
	// The last 4 chars of AKIAIOSFODNN7EXAMPLE are "MPLE"
	input := "key=AKIAIOSFODNN7EXAMPLE"
	out := secrets.Redact(input)
	if !strings.Contains(out, "MPLE") {
		t.Errorf("last4 chars should appear in ref: %s", out)
	}
}

func TestHasSecrets_True(t *testing.T) {
	if !secrets.HasSecrets("token: ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Error("expected HasSecrets=true for GitHub token")
	}
}

func TestHasSecrets_False(t *testing.T) {
	if secrets.HasSecrets("texto sin secretos conocidos aquí") {
		t.Error("expected HasSecrets=false for plain text")
	}
}

func TestDetect_ReturnsMatches(t *testing.T) {
	d := secrets.New()
	input := "key=AKIAIOSFODNN7EXAMPLE"
	matches := d.Detect(input)
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}
	if matches[0].RuleID != "aws-access-key" {
		t.Errorf("expected aws-access-key, got: %s", matches[0].RuleID)
	}
	if matches[0].Ref == "" {
		t.Error("Ref should not be empty")
	}
}
