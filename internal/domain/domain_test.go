package domain

import "testing"

func TestParseStatusType(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  StatusType
		known bool
	}{
		{name: "ready", input: "Ready", want: StatusReady, known: true},
		{name: "pending", input: "Pending", want: StatusPending, known: true},
		{name: "error", input: "Error", want: StatusError, known: true},
		{name: "empty means unknown", input: "", want: StatusUnknown, known: true},
		{name: "whitespace means unknown", input: "   ", want: StatusUnknown, known: true},
		{name: "lowercase is canonicalised", input: "ready", want: StatusReady, known: true},
		{name: "padded value is canonicalised", input: " Error ", want: StatusError, known: true},
		{
			name:  "an unrecognised value is preserved verbatim",
			input: "Reconfiguring",
			want:  StatusType("Reconfiguring"),
			known: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStatusType(tt.input)
			if got != tt.want {
				t.Errorf("ParseStatusType(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if got.IsKnown() != tt.known {
				t.Errorf("IsKnown() = %t, want %t", got.IsKnown(), tt.known)
			}
		})
	}
}

func TestObservedGeneration(t *testing.T) {
	tests := []struct {
		name       string
		conditions []Condition
		want       int64
		wantOK     bool
	}{
		{
			name:   "no conditions",
			want:   0,
			wantOK: false,
		},
		{
			name: "the Ready condition wins",
			conditions: []Condition{
				{Type: ConditionConfigured, ObservedGeneration: 7},
				{Type: ConditionReady, ObservedGeneration: 3},
			},
			want: 3, wantOK: true,
		},
		{
			name: "without Ready the highest generation is used",
			conditions: []Condition{
				{Type: ConditionConfigured, ObservedGeneration: 2},
				{Type: ConditionResolved, ObservedGeneration: 5},
			},
			want: 5, wantOK: true,
		},
		{
			name:       "a Ready condition with no generation falls through",
			conditions: []Condition{{Type: ConditionReady}, {Type: ConditionMatched, ObservedGeneration: 4}},
			want:       4, wantOK: true,
		},
		{
			name:       "generation zero everywhere is not an observation",
			conditions: []Condition{{Type: ConditionConfigured}},
			want:       0, wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ObservedGeneration(tt.conditions)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ObservedGeneration() = (%d, %t), want (%d, %t)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestResourceRefStale(t *testing.T) {
	tests := []struct {
		name string
		ref  ResourceRef
		want bool
	}{
		{
			name: "status matches the spec",
			ref:  ResourceRef{Generation: 3, ObservedGeneration: 3, GenerationObserved: true},
		},
		{
			name: "status lags the spec",
			ref:  ResourceRef{Generation: 5, ObservedGeneration: 2, GenerationObserved: true},
			want: true,
		},
		{
			name: "nothing was observed, which is not the same as lagging",
			ref:  ResourceRef{Generation: 5, GenerationObserved: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.Stale(); got != tt.want {
				t.Errorf("Stale() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestWorstOrdersCheckSeverity(t *testing.T) {
	tests := []struct {
		a, b, want CheckStatus
	}{
		{CheckPass, CheckPass, CheckPass},
		{CheckPass, CheckUnknown, CheckUnknown},
		{CheckUnknown, CheckWarn, CheckWarn},
		{CheckWarn, CheckFail, CheckFail},
		{CheckFail, CheckPass, CheckFail},
		{CheckWarn, CheckUnknown, CheckWarn},
	}
	for _, tt := range tests {
		if got := Worst(tt.a, tt.b); got != tt.want {
			t.Errorf("Worst(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFindCondition(t *testing.T) {
	conditions := []Condition{
		{Type: ConditionConfigured, Status: "True"},
		{Type: ConditionReady, Status: "False", Reason: "Pending"},
	}
	if got := FindCondition(conditions, ConditionReady); got == nil || got.Reason != "Pending" {
		t.Errorf("FindCondition(Ready) = %+v, want the Ready condition", got)
	}
	if got := FindCondition(conditions, "Nonexistent"); got != nil {
		t.Errorf("FindCondition(Nonexistent) = %+v, want nil", got)
	}
}
