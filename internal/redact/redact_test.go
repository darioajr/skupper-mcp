package redact

import (
	"strings"
	"testing"
)

func TestStringRemovesCredentialMaterial(t *testing.T) {
	tests := []struct {
		name string
		in   string
		// leak must not appear in the output.
		leak string
		// keep must still appear, so the message stays diagnostically useful.
		keep string
	}{
		{
			name: "PEM private key",
			in:   "handshake failed with -----BEGIN PRIVATE KEY-----MIIEvQIBADANBgkqh-----END PRIVATE KEY-----",
			leak: "MIIEvQIBADANBgkqh",
			keep: "handshake failed",
		},
		{
			name: "unterminated PEM block",
			in:   "cert: -----BEGIN CERTIFICATE-----MIIDdzCCAl+gAwIBAgIJAKlx",
			leak: "MIIDdzCCAl",
			keep: "cert:",
		},
		{
			name: "bearer token in an error",
			in:   "GET /api failed: Bearer abc123def456ghi789jkl",
			leak: "abc123def456ghi789jkl",
			keep: "GET /api failed",
		},
		{
			name: "JSON web token",
			in:   "rejected eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.sig by the issuer",
			leak: "eyJzdWIiOiJhbGljZSJ9",
			keep: "by the issuer",
		},
		{
			name: "a word naming itself a secret",
			in:   "failed to mount skupper-site-ca-tls-secret-material",
			leak: "skupper-site-ca-tls-secret-material",
			keep: "failed to mount",
		},
		{
			name: "an opaque access key",
			in:   "using AKIAIOSFODNN7EXAMPLE for the registry",
			leak: "AKIAIOSFODNN7EXAMPLE",
			keep: "for the registry",
		},
		{
			name: "a long base64 blob",
			in:   "payload dGhpc2lzYXZlcnlsb25nc2VjcmV0dmFsdWU= rejected",
			leak: "dGhpc2lzYXZlcnlsb25nc2VjcmV0dmFsdWU",
			keep: "rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.in)
			if strings.Contains(got, tt.leak) {
				t.Errorf("String(%q) = %q, still contains %q", tt.in, got, tt.leak)
			}
			if tt.keep != "" && !strings.Contains(got, tt.keep) {
				t.Errorf("String(%q) = %q, want %q preserved so the message stays useful", tt.in, got, tt.keep)
			}
			if !strings.Contains(got, Placeholder) {
				t.Errorf("String(%q) = %q, want a redaction placeholder", tt.in, got)
			}
		})
	}
}

// TestStringLeavesOperationalTextAlone guards the other failure mode:
// over-redaction that turns a diagnosis into noise.
func TestStringLeavesOperationalTextAlone(t *testing.T) {
	unchanged := []string{
		"",
		"waiting for router",
		"no pods matched selector app=backend",
		"Site west is Ready",
		"link west-to-east reports status Pending",
		"east-router.example.internal:55671",
		"11111111-1111-1111-1111-111111111111",
		"aaaaaaaa-0000-0000-0000-00000000000a",
		"backend-7d9f4c8b6-abcde",
		"routingKey backend on port 8080",
		"context west-cluster/namespace west",
		"the skupper.io/v2alpha1 CRD is not installed",
	}
	for _, in := range unchanged {
		if got := String(in); got != in {
			t.Errorf("String(%q) = %q, want it unchanged; over-redaction destroys the diagnosis", in, got)
		}
	}
}

func TestStringIsIdempotent(t *testing.T) {
	in := "mount failed: -----BEGIN PRIVATE KEY-----MIIEvQ and secret-token-value"
	once := String(in)
	twice := String(once)
	if once != twice {
		t.Errorf("String is not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{
		"token", "Token", "TOKEN", "bearer_token", "tlsCredentials",
		"password", "clientSecret", "CA_CERT", "kubeconfig", "sessionCookie",
		"authorization", "x509Certificate",
	}
	for _, key := range sensitive {
		if !IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", key)
		}
	}

	harmless := []string{"tool", "namespace", "routing_key", "duration", "result_count", "outcome", "subject"}
	for _, key := range harmless {
		if IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", key)
		}
	}
}

func TestIsCredentialLikeExcludesKubernetesNames(t *testing.T) {
	notCredentials := []string{
		"aaaaaaaa-0000-0000-0000-00000000000a", // a UID
		"skupper-router-deployment-abc",        // a hyphenated resource name
		"backend-7d9f4c8b6-abcde",              // a pod name
		"my_long_variable_name_here",           // snake case
	}
	for _, token := range notCredentials {
		if isCredentialLike(token) {
			t.Errorf("isCredentialLike(%q) = true, want false", token)
		}
	}

	credentials := []string{
		"AKIAIOSFODNN7EXAMPLE",
		"dGhpc2lzYXZlcnlsb25nc2VjcmV0",
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4",
	}
	for _, token := range credentials {
		if !isCredentialLike(token) {
			t.Errorf("isCredentialLike(%q) = false, want true", token)
		}
	}
}
