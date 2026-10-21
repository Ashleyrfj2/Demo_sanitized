package model

import "time"

type VerificationDepth int

const (
	DepthNone         VerificationDepth = 0
	DepthSurface      VerificationDepth = 1 // surface / DOM observation
	DepthInteraction  VerificationDepth = 2 // interaction / client mutation
	DepthPersistence  VerificationDepth = 3 // backend/persistence verification
	DepthCrossSystem  VerificationDepth = 4 // cross-system / async verification
)

type Scope struct {
	RunID         string            `json:"run_id"`
	TenantID      string            `json:"tenant_id"`
	BuildID       string            `json:"build_id"`
	EnvironmentID string            `json:"environment_id"`
	ScenarioID    string            `json:"scenario_id,omitempty"`
	FeatureFlags  map[string]string `json:"feature_flags,omitempty"`
	Device        string            `json:"device,omitempty"`
	Region        string            `json:"region,omitempty"`
}

type ClaimDefinition struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id"`
	Title            string    `json:"title"`
	Description      string    `json:"description,omitempty"`
	Target           string    `json:"target"`
	StateSignature   string    `json:"state_signature"`
	ActorContext     string    `json:"actor_context"`
	ValidationIntent string    `json:"validation_intent"`
	CreatedAt        time.Time `json:"created_at"`
}

type EvidenceContext struct {
	ID            string            `json:"id"`
	TenantID      string            `json:"tenant_id"`
	RunID         string            `json:"run_id"`
	BuildID       string            `json:"build_id"`
	EnvironmentID string            `json:"environment_id"`
	ScenarioID    string            `json:"scenario_id,omitempty"`
	FeatureFlags  map[string]string `json:"feature_flags,omitempty"`
	Device        string            `json:"device,omitempty"`
	Region        string            `json:"region,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

type Observation struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenant_id"`
	RunID             string    `json:"run_id"`
	ClaimID           string    `json:"claim_id"`
	EvidenceContextID string    `json:"evidence_context_id"`
	EventID           string    `json:"event_id,omitempty"`
	SourceType        string    `json:"source_type"`
	SourceAdapter     string    `json:"source_adapter"`
	ActorID           string    `json:"actor_id"`
	Assessment        string    `json:"assessment"` // supports, weakens, conflicts, invalidates, blocked
	VerifierResult    string    `json:"verifier_result,omitempty"`
	OccurredAt        time.Time `json:"occurred_at"`
	CreatedAt         time.Time `json:"created_at"`
}

type ClaimEvidence struct {
	Claim        ClaimDefinition            `json:"claim"`
	Observations []Observation              `json:"observations"`
	Contexts     map[string]EvidenceContext `json:"contexts"`
}

type ClaimEvidenceSummary struct {
	ClaimID                    string            `json:"claim_id"`
	TenantID                   string            `json:"tenant_id"`
	Target                     string            `json:"target"`
	StateSignature             string            `json:"state_signature"`
	ActorContext               string            `json:"actor_context"`
	ValidationIntent           string            `json:"validation_intent"`
	Classification             string            `json:"classification"` // Solid, Weak, Stale, Conflicted, Untouched, Blocked
	HeuristicScore             *float64          `json:"heuristic_score,omitempty"`
	MaxVerificationDepth       int               `json:"max_verification_depth"`
	Freshness                  string            `json:"freshness"` // current, stale, untouched
	IndependentSourceCount     int               `json:"independent_source_count"`
	CompatibleObservationCount int               `json:"compatible_observation_count"`
	ConflictCount              int               `json:"conflict_count"`
	LatestObservationAt        *time.Time        `json:"latest_observation_at,omitempty"`
	Reasons                    []string          `json:"reasons"`
	EvidenceContexts           []EvidenceContext `json:"evidence_contexts,omitempty"`
}

type ClaimEvidenceSummariesResponse struct {
	RunID         string                 `json:"run_id"`
	TenantID      string                 `json:"tenant_id"`
	BuildID       string                 `json:"build_id"`
	EnvironmentID string                 `json:"environment_id"`
	Summaries     []ClaimEvidenceSummary `json:"summaries"`
}
