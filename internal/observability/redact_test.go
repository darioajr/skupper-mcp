package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/darioajr/skupper-scope/internal/config"
)

func newTestLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	log, err := NewLogger(config.Log{Level: "debug", Format: "json"}, &buf)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	return log, &buf
}

func TestRedactingHandlerReplacesSensitiveAttributes(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "token", key: "token", value: "super-secret-token"},
		{name: "camel case", key: "tlsCredentials", value: "link-secret"},
		{name: "snake case", key: "bearer_token", value: "abcdef"},
		{name: "upper case", key: "CA_CERT", value: "cert-body"},
		{name: "nested word", key: "clientSecretRef", value: "secret-ref"},
		{name: "kubeconfig", key: "kubeconfig", value: "/home/user/.kube/config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newTestLogger(t)
			log.Info("test", slog.String(tt.key, tt.value))

			out := buf.String()
			if strings.Contains(out, tt.value) {
				t.Errorf("log output contains the value of %q: %s", tt.key, out)
			}
			if !strings.Contains(out, Placeholder) {
				t.Errorf("log output has no redaction placeholder: %s", out)
			}
		})
	}
}

func TestRedactingHandlerCatchesCredentialShapedValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		leak  string
	}{
		{
			name:  "PEM block under an innocuous key",
			value: "failed to parse -----BEGIN PRIVATE KEY-----MIIEvQIBADANBg",
			leak:  "MIIEvQIBADANBg",
		},
		{
			name:  "authorization header echoed in an error",
			value: `request failed: Bearer eyJhbGciOiJSUzI1NiJ9.payload.sig`,
			leak:  "eyJhbGciOiJSUzI1NiJ9",
		},
		{
			name:  "bare JWT",
			value: "eyJhbGciOiJIUzI1NiJ9.e30.signature",
			leak:  "signature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newTestLogger(t)
			log.Error("upstream failure", slog.String("detail", tt.value))
			if strings.Contains(buf.String(), tt.leak) {
				t.Errorf("log output leaked credential material: %s", buf.String())
			}
		})
	}
}

func TestRedactingHandlerRedactsTheMessageItself(t *testing.T) {
	log, buf := newTestLogger(t)
	log.Info("connect failed with Bearer eyJhbGciOiJIUzI1NiJ9.body.sig")
	if strings.Contains(buf.String(), "eyJhbGciOiJIUzI1NiJ9") {
		t.Errorf("the log message was not redacted: %s", buf.String())
	}
}

func TestRedactingHandlerAppliesToGroupsAndWith(t *testing.T) {
	log, buf := newTestLogger(t)
	log.With(slog.String("token", "outer-secret")).
		Info("test", slog.Group("auth", slog.String("password", "inner-secret")))

	out := buf.String()
	for _, leak := range []string{"outer-secret", "inner-secret"} {
		if strings.Contains(out, leak) {
			t.Errorf("log output leaked %q: %s", leak, out)
		}
	}
}

func TestRedactingHandlerKeepsHarmlessFields(t *testing.T) {
	log, buf := newTestLogger(t)
	log.Info("tool invocation",
		slog.String("tool", "list_sites"),
		slog.String("k8s_namespace", "west"),
		slog.Int("result_count", 3))

	out := buf.String()
	for _, want := range []string{"list_sites", "west", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output is missing the harmless field %q: %s", want, out)
		}
	}
}

func TestNewLoggerRejectsUnknownSettings(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewLogger(config.Log{Level: "verbose", Format: "json"}, &buf); err == nil {
		t.Error("NewLogger accepted an unknown level")
	}
	if _, err := NewLogger(config.Log{Level: "info", Format: "xml"}, &buf); err == nil {
		t.Error("NewLogger accepted an unknown format")
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{"token", "Password", "tlsCredentials", "x509Cert", "authorization", "sessionCookie"}
	for _, key := range sensitive {
		if !IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", key)
		}
	}
	harmless := []string{"tool", "namespace", "routing_key", "duration", "result_count"}
	for _, key := range harmless {
		if IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = true, want false; over-redaction makes the audit log useless", key)
		}
	}
}

func TestAuditorRecordsTheAllowlistedFields(t *testing.T) {
	log, buf := newTestLogger(t)
	NewAuditor(log).Record(context.Background(), Invocation{
		Tool:        "diagnose_connectivity",
		Subject:     "alice",
		Scopes:      []string{"skupper:read"},
		Context:     "west-cluster",
		Namespace:   "west",
		RoutingKey:  "backend",
		ResultCount: 7,
		Outcome:     OutcomeOK,
	})

	out := buf.String()
	for _, want := range []string{"diagnose_connectivity", "alice", "west-cluster", "backend", "\"result_count\":7"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit record is missing %q: %s", want, out)
		}
	}
}

func TestAuditorMarksAnonymousCallers(t *testing.T) {
	log, buf := newTestLogger(t)
	NewAuditor(log).Record(context.Background(), Invocation{Tool: "list_sites", Outcome: OutcomeOK})
	if !strings.Contains(buf.String(), "anonymous") {
		t.Errorf("audit record does not mark the caller as anonymous: %s", buf.String())
	}
}

func TestAuditPreservesVerifiedOpaqueIdentityAndSessionHash(t *testing.T) {
	log, buf := newTestLogger(t)
	identity := "1234567890abcdef1234567890abcdef"
	NewAuditor(log).Record(context.Background(), Invocation{Tool: "list_sites", Subject: identity, Session: identity, Outcome: OutcomeOK})
	if !strings.Contains(buf.String(), `"subject":"`+identity+`"`) || !strings.Contains(buf.String(), `"session_hash":"`+identity+`"`) {
		t.Fatalf("verified identity/session correlation was lost to credential redaction: %s", buf.String())
	}
}

func TestAuditorRaisesTheLevelForFailures(t *testing.T) {
	log, buf := newTestLogger(t)
	NewAuditor(log).Record(context.Background(), Invocation{Tool: "list_sites", Outcome: OutcomeDenied})
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("a denied call was not logged at warn: %s", buf.String())
	}
}

func TestNilAuditorDoesNotPanic(t *testing.T) {
	var a *Auditor
	a.Record(context.Background(), Invocation{Tool: "list_sites"})
}
