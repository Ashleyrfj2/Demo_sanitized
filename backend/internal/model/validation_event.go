package model

import (
	"encoding/json"
	"time"
)

type BuildRegistration struct {
	TenantID           string `json:"tenant_id"`
	EnvironmentID      string `json:"environment_id"`
	BuildID            string `json:"build_id"`
	SequenceNumber     int64  `json:"sequence_number"`
	PredecessorBuildID string `json:"predecessor_build_id,omitempty"`
}

type RegisteredBuild struct {
	BuildRegistration
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
}

type ExperimentRun struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	BuildID       string     `json:"build_id"`
	EnvironmentID string     `json:"environment_id"`
	ScenarioID    string     `json:"scenario_id"`
	Mode          string     `json:"mode"`
	StartedAt     time.Time  `json:"started_at"`
	FrozenAt      *time.Time `json:"frozen_at"`
	CreatedBy     string     `json:"created_by"`
}

type ExperimentRegistration struct {
	ID            string `json:"id"`
	TenantID      string `json:"tenant_id"`
	BuildID       string `json:"build_id"`
	EnvironmentID string `json:"environment_id"`
	ScenarioID    string `json:"scenario_id"`
	Mode          string `json:"mode"`
}

// ValidationEvent is the vendor-neutral boundary between source adapters and
// the evidence engine. Source-specific payloads should not cross this boundary.
type ValidationEvent struct {
	EventID          string            `json:"event_id"`
	TenantID         string            `json:"tenant_id"`
	SessionID        string            `json:"session_id,omitempty"`
	RunID            string            `json:"run_id"`
	ActorID          string            `json:"actor_id"`
	SourceType       string            `json:"source_type"`
	SourceAdapter    string            `json:"source_adapter"`
	BuildID          string            `json:"build_id"`
	EnvironmentID    string            `json:"environment_id"`
	Context          map[string]string `json:"context"`
	Route            string            `json:"route"`
	StateFingerprint string            `json:"state_fingerprint"`
	ActionType       string            `json:"action_type"`
	Outcome          string            `json:"outcome"`
	VerifierResult   string            `json:"verifier_result,omitempty"`
	FindingID        string            `json:"finding_id,omitempty"`
	Verification     string            `json:"verification_status,omitempty"`
	OccurredAt       time.Time         `json:"occurred_at"`
	StepIndex        int               `json:"step_index"`
	Privacy          PrivacyMetadata   `json:"privacy"`
}

type PrivacyMetadata struct {
	Masked         bool     `json:"masked"`
	RedactedFields []string `json:"redacted_fields,omitempty"`
}

type CandidateState struct {
	ID                    string    `json:"id"`
	RunID                 string    `json:"run_id"`
	TenantID              string    `json:"tenant_id"`
	BuildID               string    `json:"build_id"`
	EnvironmentID         string    `json:"environment_id"`
	Role                  string    `json:"role"`
	Module                string    `json:"module"`
	RecordID              string    `json:"record_id"`
	BusinessState         string    `json:"business_state"`
	StateFingerprint      string    `json:"state_fingerprint"`
	Status                string    `json:"status"`
	EvidenceCount         int       `json:"evidence_count"`
	IndependentActorCount int       `json:"independent_actor_count"`
	SourceTypes           []string  `json:"source_types"`
	EventIDs              []string  `json:"event_ids"`
	Conflict              bool      `json:"conflict"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type VerificationRequest struct {
	ID               string    `json:"id"`
	RunID            string    `json:"run_id"`
	CandidateStateID string    `json:"candidate_state_id"`
	RequestedRole    string    `json:"requested_role,omitempty"`
	Rationale        string    `json:"rationale"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

type EvidenceResponse struct {
	RunID                string                `json:"run_id"`
	TenantID             string                `json:"tenant_id"`
	BuildID              string                `json:"build_id"`
	EnvironmentID        string                `json:"environment_id"`
	States               []CandidateState      `json:"states"`
	VerificationRequests []VerificationRequest `json:"verification_requests"`
}

type RecommendationFactors struct {
	DeltaUncertainty     float64 `json:"delta_uncertainty"`
	BusinessRisk         float64 `json:"business_risk"`
	DiffImpact           float64 `json:"diff_impact"`
	SmoothedFailurePrior float64 `json:"smoothed_failure_prior"`
	ExpectedCost         float64 `json:"expected_cost"`
	legacyJSON           json.RawMessage
}

func (f *RecommendationFactors) UnmarshalJSON(raw []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return err
	}
	if _, canonical := keys["delta_uncertainty"]; !canonical {
		f.legacyJSON = append(f.legacyJSON[:0], raw...)
		return nil
	}
	type plain RecommendationFactors
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*f = RecommendationFactors(decoded)
	return nil
}

func (f RecommendationFactors) MarshalJSON() ([]byte, error) {
	if len(f.legacyJSON) > 0 {
		return append([]byte(nil), f.legacyJSON...), nil
	}
	type plain RecommendationFactors
	return json.Marshal(plain(f))
}

type Recommendation struct {
	ID                     string                      `json:"id"`
	TenantID               string                      `json:"tenant_id"`
	CandidateStateID       string                      `json:"candidate_state_id,omitempty"`
	ClaimID                string                      `json:"claim_id"`
	RunID                  string                      `json:"run_id"`
	ActorID                string                      `json:"actor_id"`
	Role                   string                      `json:"role"`
	BuildID                string                      `json:"build_id"`
	EnvironmentID          string                      `json:"environment_id"`
	ScenarioID             string                      `json:"scenario_id,omitempty"`
	Device                 string                      `json:"device,omitempty"`
	Region                 string                      `json:"region,omitempty"`
	Area                   string                      `json:"area"`
	Classification         string                      `json:"classification"`
	MaxVerificationDepth   int                         `json:"max_verification_depth"`
	IndependentSourceCount int                         `json:"independent_source_count"`
	ConflictCount          int                         `json:"conflict_count"`
	Rationale              string                      `json:"rationale"`
	Score                  float64                     `json:"score"`
	Factors                RecommendationFactors       `json:"factors"`
	PolicyVersion          string                      `json:"policy_version"`
	EvidenceSnapshot       string                      `json:"evidence_snapshot"`
	ProgressionSnapshot    string                      `json:"progression_snapshot"`
	OverrideAlternatives   []RecommendationAlternative `json:"override_alternatives"`
	PromotedByResponseID   string                      `json:"promoted_by_response_id,omitempty"`
	FeatureFlags           map[string]string           `json:"feature_flags,omitempty"`
	CreatedAt              time.Time                   `json:"created_at"`
}

type RecommendationAlternative struct {
	ClaimID        string `json:"claim_id"`
	Title          string `json:"title"`
	Area           string `json:"area"`
	Classification string `json:"classification"`
}

type RecommendationResponse struct {
	Response        string `json:"response"`
	Reason          string `json:"reason"`
	ActorID         string `json:"actor_id"`
	OverrideClaimID string `json:"override_claim_id,omitempty"`
}

type RecommendationResponseRecord struct {
	ID               string    `json:"id"`
	RunID            string    `json:"run_id"`
	RecommendationID string    `json:"recommendation_id"`
	Response         string    `json:"response"`
	Reason           string    `json:"reason"`
	ActorID          string    `json:"actor_id"`
	OverrideClaimID  string    `json:"override_claim_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type StateCorrectionRequest struct {
	TargetStateID    string   `json:"target_state_id,omitempty"`
	NewFingerprint   string   `json:"new_fingerprint,omitempty"`
	NewBusinessState string   `json:"new_business_state,omitempty"`
	EventIDs         []string `json:"event_ids,omitempty"`
	ActorID          string   `json:"actor_id"`
	Reason           string   `json:"reason"`
}

type StateCorrection struct {
	ID          string                 `json:"id"`
	RunID       string                 `json:"run_id"`
	Type        string                 `json:"correction_type"`
	StateID     string                 `json:"state_id"`
	BeforeState CandidateState         `json:"before_state"`
	Request     StateCorrectionRequest `json:"request"`
	CorrectedBy string                 `json:"corrected_by"`
	Reason      string                 `json:"reason"`
	CreatedAt   time.Time              `json:"created_at"`
}

type MetricValue struct {
	Value           *float64 `json:"value"`
	Unit            string   `json:"unit"`
	Numerator       *float64 `json:"numerator"`
	Denominator     *float64 `json:"denominator"`
	UndefinedReason *string  `json:"undefined_reason"`
}

type InputCounts struct {
	Events               int `json:"events"`
	States               int `json:"states"`
	VerificationRequests int `json:"verification_requests"`
	Recommendations      int `json:"recommendations"`
	Responses            int `json:"responses"`
	Corrections          int `json:"corrections"`
}

type RowDigest struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type MetricInputReceipt struct {
	SchemaVersion string                 `json:"schema_version"`
	Algorithm     string                 `json:"algorithm"`
	SHA256        string                 `json:"sha256"`
	Immutable     bool                   `json:"immutable"`
	Counts        InputCounts            `json:"counts"`
	RowDigests    map[string][]RowDigest `json:"row_digests"`
}

type ExperimentMetrics struct {
	RunID             string                 `json:"run_id"`
	Status            string                 `json:"status"`
	DefinitionVersion string                 `json:"definition_version"`
	AsOf              time.Time              `json:"as_of"`
	FrozenAt          *time.Time             `json:"frozen_at"`
	Metrics           map[string]MetricValue `json:"metrics"`
	Breakdowns        map[string]any         `json:"breakdowns"`
	InputReceipt      MetricInputReceipt     `json:"input_receipt"`
}
