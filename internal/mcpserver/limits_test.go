package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/config"
)

func TestInvocationLimitsAndRecovery(t *testing.T) {
	for _, tt := range []struct {
		name                      string
		user, session             int
		secondUser, secondSession string
		denied                    bool
	}{
		{"session budget", 10, 1, "alice", "one", true},
		{"separate sessions", 10, 1, "alice", "two", false},
		{"user budget survives reconnect", 1, 10, "alice", "two", true},
		{"separate users", 1, 10, "bob", "one", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default().Limits
			cfg.PerUser, cfg.PerSession, cfg.BreakerThreshold = tt.user, tt.session, 2
			l, err := newInvocationLimiter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0)
			if _, err := l.allow(now, "alice", "one"); err != nil {
				t.Fatal(err)
			}
			_, err = l.allow(now, tt.secondUser, tt.secondSession)
			if errors.Is(err, errLimited) != tt.denied {
				t.Fatalf("second call = %v, want denied=%t", err, tt.denied)
			}
			if tt.denied {
				opened, err := l.allow(now, tt.secondUser, tt.secondSession)
				if !opened || !errors.Is(err, errLimited) {
					t.Fatalf("repeated burst = opened %t, %v, want breaker", opened, err)
				}
				if opened, err := l.allow(now.Add(time.Second), tt.secondUser, tt.secondSession); opened || !errors.Is(err, errLimited) {
					t.Fatal("open breaker did not remain blocked without duplicate alert")
				}
			}
			if _, err := l.allow(now.Add(2*cfg.Window+cfg.Cooldown), "alice", "one"); err != nil {
				t.Fatalf("budget did not recover: %v", err)
			}
		})
	}
}

func TestLimiterCapacityFailsClosedAndExpires(t *testing.T) {
	cfg := config.Default().Limits
	cfg.MaxEntries = 2
	l, err := newInvocationLimiter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if _, err := l.allow(now, "alice", "one"); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if _, err := l.allow(now, "bob", "two"); !errors.Is(err, errLimited) {
			t.Fatal("capacity exhaustion must deny, never evict an active user's budget")
		}
	}
	if len(l.entries) != 2 {
		t.Fatalf("limiter state grew beyond cap: %d", len(l.entries))
	}
	if _, err := l.allow(now.Add(cfg.Window), "bob", "two"); err != nil {
		t.Fatalf("idle state did not expire: %v", err)
	}
}

func TestMalformedToolCallsConsumeBudgetAndOpenAuditedBreaker(t *testing.T) {
	srv, logs := newTestServer(t, demoNetwork())
	cfg := config.Default().Limits
	cfg.PerUser, cfg.PerSession, cfg.BreakerThreshold = 1, 1, 1
	var err error
	srv.limiter, err = newInvocationLimiter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, srv)
	bad := callTool(t, session, "list_sites", map[string]any{"namespace": 42})
	if !bad.IsError {
		t.Fatal("malformed input was accepted")
	}
	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_sites"})
	if err == nil || !strings.Contains(err.Error(), "limit exceeded") {
		t.Fatalf("second invocation = %v, want throttled before I/O", err)
	}
	for _, marker := range []string{`"outcome":"invalid"`, `"outcome":"limited"`, `"event":"mcp_circuit_open"`, `"alert_owner":"local-operator"`} {
		if !strings.Contains(logs.String(), marker) {
			t.Errorf("audit missing %s: %s", marker, logs.String())
		}
	}
	if strings.Contains(logs.String(), `"read_scopes":[{`) {
		t.Fatal("rejected calls must not read any Kubernetes scope")
	}
}

func TestAuditIncludesBroadReadsForToolsResourcesAndDiagnosis(t *testing.T) {
	srv, logs := newTestServer(t, demoNetwork())
	session := connect(t, srv)
	callTool(t, session, "list_sites", nil)
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: URISites}); err != nil {
		t.Fatal(err)
	}
	callTool(t, session, "diagnose_connectivity", map[string]any{"sourceContext": "west-cluster", "sourceNamespace": "west", "targetContext": "east-cluster", "targetNamespace": "east", "routingKey": "backend"})
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `"tool":"list_sites"`) || strings.Contains(line, `"tool":"resource:sites"`) || strings.Contains(line, `"tool":"diagnose_connectivity"`) {
			if !strings.Contains(line, `"context":"west-cluster","namespace":"west"`) || !strings.Contains(line, `"context":"east-cluster","namespace":"east"`) {
				t.Errorf("audit omitted a read scope: %s", line)
			}
		}
	}
}
