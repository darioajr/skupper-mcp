package domain

import "strings"

// StatusType is the observed status of a Skupper resource.
//
// Skupper v2 publishes Ready, Pending and Error. Any other value is preserved
// verbatim rather than rejected, so a newer controller cannot make this server
// fail; callers that do not recognise a value should treat it as Unknown.
type StatusType string

const (
	StatusReady   StatusType = "Ready"
	StatusPending StatusType = "Pending"
	StatusError   StatusType = "Error"
	// StatusUnknown is used when the resource carries no status at all.
	StatusUnknown StatusType = "Unknown"
)

// ParseStatusType normalises a raw status string. Empty means Unknown; a
// recognised value is canonicalised; anything else is returned as-is.
func ParseStatusType(raw string) StatusType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return StatusUnknown
	case "ready":
		return StatusReady
	case "pending":
		return StatusPending
	case "error":
		return StatusError
	default:
		return StatusType(raw)
	}
}

// IsKnown reports whether s is one of the statuses this server understands.
func (s StatusType) IsKnown() bool {
	switch s {
	case StatusReady, StatusPending, StatusError, StatusUnknown:
		return true
	default:
		return false
	}
}

// Condition mirrors a metav1.Condition without importing apimachinery into the
// domain.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// Condition types published by the Skupper v2 controller.
const (
	ConditionReady       = "Ready"
	ConditionConfigured  = "Configured"
	ConditionResolved    = "Resolved"
	ConditionMatched     = "Matched"
	ConditionOperational = "Operational"
)

// FindCondition returns the condition with the given type, or nil.
func FindCondition(conditions []Condition, condType string) *Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// ObservedGeneration returns the generation the Ready condition was computed
// from, or the highest observed generation across conditions if there is no
// Ready condition. The second result is false when nothing was observed.
func ObservedGeneration(conditions []Condition) (int64, bool) {
	if ready := FindCondition(conditions, ConditionReady); ready != nil && ready.ObservedGeneration != 0 {
		return ready.ObservedGeneration, true
	}
	var best int64
	var found bool
	for _, c := range conditions {
		if c.ObservedGeneration > best {
			best, found = c.ObservedGeneration, true
		}
	}
	return best, found
}
