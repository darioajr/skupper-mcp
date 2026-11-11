package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func allowlistFor(t *testing.T, contexts, namespaces []string) *Allowlist {
	t.Helper()
	cfg := Default()
	cfg.Kubernetes.AllowedContexts = contexts
	cfg.Kubernetes.AllowedNamespaces = namespaces
	a, err := cfg.Allowlist()
	if err != nil {
		t.Fatalf("building allowlist: %v", err)
	}
	return a
}

func TestAllowlistResolveScope(t *testing.T) {
	tests := []struct {
		name        string
		contexts    []string
		namespaces  []string
		reqContext  string
		reqNS       string
		wantContext string
		wantNS      []string
		wantErr     error
	}{
		{
			name:        "empty namespace expands to every allowed namespace, never the cluster",
			contexts:    []string{"west-cluster"},
			namespaces:  []string{"west", "shared"},
			wantContext: "west-cluster",
			wantNS:      []string{"west", "shared"},
		},
		{
			name:        "empty context selects the first allowed context",
			contexts:    []string{"west-cluster", "east-cluster"},
			namespaces:  []string{"west"},
			wantContext: "west-cluster",
			wantNS:      []string{"west"},
		},
		{
			name:        "an allowed namespace is accepted",
			contexts:    []string{"west-cluster"},
			namespaces:  []string{"west", "shared"},
			reqNS:       "shared",
			wantContext: "west-cluster",
			wantNS:      []string{"shared"},
		},
		{
			name:       "an unlisted namespace is refused",
			contexts:   []string{"west-cluster"},
			namespaces: []string{"west"},
			reqNS:      "kube-system",
			wantErr:    ErrNamespaceNotAllowed,
		},
		{
			name:       "an unlisted context is refused",
			contexts:   []string{"west-cluster"},
			namespaces: []string{"west"},
			reqContext: "prod-cluster",
			wantErr:    ErrContextNotAllowed,
		},
		{
			name:        "a context-qualified namespace applies only to that context",
			contexts:    []string{"west-cluster", "east-cluster"},
			namespaces:  []string{"west-cluster/west", "east-cluster/east"},
			reqContext:  "east-cluster",
			wantContext: "east-cluster",
			wantNS:      []string{"east"},
		},
		{
			name:       "a context-qualified namespace is refused in another context",
			contexts:   []string{"west-cluster", "east-cluster"},
			namespaces: []string{"west-cluster/west", "east-cluster/east"},
			reqContext: "east-cluster",
			reqNS:      "west",
			wantErr:    ErrNamespaceNotAllowed,
		},
		{
			name:        "an unqualified namespace applies to every context",
			contexts:    []string{"west-cluster", "east-cluster"},
			namespaces:  []string{"shared", "east-cluster/east"},
			reqContext:  "east-cluster",
			wantContext: "east-cluster",
			wantNS:      []string{"shared", "east"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := allowlistFor(t, tt.contexts, tt.namespaces)
			gotCtx, gotNS, err := a.ResolveScope(tt.reqContext, tt.reqNS)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want it to wrap %v", err, tt.wantErr)
				}
				if gotNS != nil {
					t.Errorf("namespaces = %v on a denied request, want nil; a denial must not "+
						"leak an empty-but-valid scope", gotNS)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveScope: %v", err)
			}
			if gotCtx != tt.wantContext {
				t.Errorf("context = %q, want %q", gotCtx, tt.wantContext)
			}
			if !reflect.DeepEqual(gotNS, tt.wantNS) {
				t.Errorf("namespaces = %v, want %v", gotNS, tt.wantNS)
			}
		})
	}
}

func TestDeniedContextErrorNamesTheAllowedOnes(t *testing.T) {
	a := allowlistFor(t, []string{"west-cluster", "east-cluster"}, []string{"west", "east"})
	_, err := a.ResolveContext("prod")
	if err == nil {
		t.Fatal("ResolveContext accepted an unlisted context")
	}
	msg := err.Error()
	for _, want := range []string{"west-cluster", "east-cluster"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to name the allowed context %q so the caller can correct itself", msg, want)
		}
	}
}

func TestAllowlistDefaultsToCurrentContextAlias(t *testing.T) {
	cfg := Default()
	cfg.Kubernetes.CurrentContextAlias = "in-cluster"
	cfg.Kubernetes.AllowedNamespaces = []string{"skupper"}
	a, err := cfg.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist: %v", err)
	}
	if got, want := a.DefaultContext(), "in-cluster"; got != want {
		t.Errorf("DefaultContext() = %q, want %q", got, want)
	}
	if got := a.Contexts(); len(got) != 1 || got[0] != "in-cluster" {
		t.Errorf("Contexts() = %v, want the alias alone", got)
	}
}

func TestAllowlistRejectsMalformedNamespaceEntries(t *testing.T) {
	cfg := Default()
	cfg.Kubernetes.AllowedNamespaces = []string{"a/b/c"}
	if _, err := cfg.Allowlist(); err == nil {
		t.Fatal("Allowlist accepted the malformed entry \"a/b/c\"")
	}
}

func TestAllContextsCoversEveryAllowedPair(t *testing.T) {
	a := allowlistFor(t,
		[]string{"west-cluster", "east-cluster"},
		[]string{"shared", "west-cluster/west", "east-cluster/east"})

	got := a.AllContexts()
	want := map[string][]string{
		"west-cluster": {"shared", "west"},
		"east-cluster": {"shared", "east"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllContexts() = %v, want %v", got, want)
	}
}
