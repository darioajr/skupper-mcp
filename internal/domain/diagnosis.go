package domain

// DiagnosisStatus is the overall verdict of a connectivity diagnosis.
type DiagnosisStatus string

const (
	DiagnosisHealthy  DiagnosisStatus = "HEALTHY"
	DiagnosisDegraded DiagnosisStatus = "DEGRADED"
	DiagnosisBroken   DiagnosisStatus = "BROKEN"
	DiagnosisUnknown  DiagnosisStatus = "UNKNOWN"
)

// CheckStatus is the outcome of one individual check.
type CheckStatus string

const (
	CheckPass    CheckStatus = "PASS"
	CheckWarn    CheckStatus = "WARN"
	CheckFail    CheckStatus = "FAIL"
	CheckUnknown CheckStatus = "UNKNOWN"
)

// Confidence expresses how much of the diagnosis rests on observed state
// versus on inference over incomplete data.
type Confidence string

const (
	ConfidenceHigh   Confidence = "HIGH"
	ConfidenceMedium Confidence = "MEDIUM"
	ConfidenceLow    Confidence = "LOW"
)

// EvidenceKind separates what a resource claims from what a controller
// observed and from what this server concluded. The distinction is load
// bearing: a diagnosis must never present an inference as a measurement.
type EvidenceKind string

const (
	// EvidenceDeclared is a value from a resource spec: intent, not reality.
	EvidenceDeclared EvidenceKind = "DECLARED"
	// EvidenceObserved is a value from a resource status, written by the
	// Skupper controller that reconciles it.
	EvidenceObserved EvidenceKind = "OBSERVED"
	// EvidenceInferred is a conclusion drawn by this server from the above.
	EvidenceInferred EvidenceKind = "INFERRED"
	// EvidenceAbsent records that something expected was not found.
	EvidenceAbsent EvidenceKind = "ABSENT"
)

// Evidence is a single fact a check rests on.
type Evidence struct {
	Kind      EvidenceKind `json:"kind"`
	Source    string       `json:"source,omitempty"`
	Statement string       `json:"statement"`
}

// Check is one deterministic step of the diagnosis.
type Check struct {
	Name           string      `json:"name"`
	Status         CheckStatus `json:"status"`
	Evidence       []Evidence  `json:"evidence"`
	Explanation    string      `json:"explanation"`
	Recommendation string      `json:"recommendation,omitempty"`
}

// Check names, stable across releases so clients can key on them.
const (
	CheckSiteReadiness   = "site-readiness"
	CheckLinkPath        = "link-path"
	CheckLinkHealth      = "link-health"
	CheckSourceListener  = "source-listener"
	CheckTargetConnector = "target-connector"
	CheckEndpointMatch   = "endpoint-match"
	CheckWorkloadBinding = "workload-binding"
)

// PathHop is one site-to-site step of the link path between source and target.
type PathHop struct {
	FromSiteID   string     `json:"fromSiteId"`
	FromSiteName string     `json:"fromSiteName,omitempty"`
	ToSiteID     string     `json:"toSiteId"`
	ToSiteName   string     `json:"toSiteName,omitempty"`
	LinkID       string     `json:"linkId"`
	LinkName     string     `json:"linkName"`
	LinkStatus   StatusType `json:"linkStatus"`
	Cost         int        `json:"cost,omitempty"`
}

// Diagnosis is the result of diagnose_connectivity.
//
// Scope is deliberately narrow: every field describes Skupper resource state
// observed through the Kubernetes API. Nothing here is derived from sending
// traffic, measuring latency, or validating a TLS chain, and the server does
// not do any of those things.
type Diagnosis struct {
	Status     DiagnosisStatus `json:"status"`
	Summary    string          `json:"summary"`
	Confidence Confidence      `json:"confidence"`
	Checks     []Check         `json:"checks"`
	Path       []PathHop       `json:"path"`
	Warnings   []string        `json:"warnings,omitempty"`

	// Method states in the payload itself what was and was not done, so a
	// model relaying this result cannot honestly overclaim.
	Method DiagnosisMethod `json:"method"`
}

// DiagnosisMethod documents the analysis technique in-band.
type DiagnosisMethod struct {
	Technique   string   `json:"technique"`
	NotVerified []string `json:"notVerified"`
}

// DefaultMethod is the in-band disclaimer attached to every diagnosis.
func DefaultMethod() DiagnosisMethod {
	return DiagnosisMethod{
		Technique: "Static analysis of Skupper v2 custom resource spec and status " +
			"read through the Kubernetes API. No traffic was sent and no change was made.",
		NotVerified: []string{
			"end-to-end network reachability",
			"latency or throughput",
			"TLS certificate chain validation",
			"application-level health of the target workload",
			"router-to-router state beyond what the Skupper controller published",
		},
	}
}

// Worst returns the more severe of two check statuses, ordered
// PASS < UNKNOWN < WARN < FAIL.
func Worst(a, b CheckStatus) CheckStatus {
	if checkSeverity(b) > checkSeverity(a) {
		return b
	}
	return a
}

func checkSeverity(s CheckStatus) int {
	switch s {
	case CheckPass:
		return 0
	case CheckUnknown:
		return 1
	case CheckWarn:
		return 2
	case CheckFail:
		return 3
	default:
		return 1
	}
}
