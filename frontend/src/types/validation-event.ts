export type SourceType = "human" | "agent" | "automation";
export type Outcome = "pass" | "fail" | "blocked" | "ambiguous" | "abandoned";

export interface ValidationEvent {
  event_id: string;
  tenant_id: string;
  session_id?: string;
  run_id: string;
  actor_id: string;
  source_type: SourceType;
  source_adapter: string;
  build_id: string;
  environment_id: string;
  context: Record<string, string> & { role: string };
  route: string;
  state_fingerprint: string;
  action_type: string;
  outcome: Outcome;
  verifier_result?: string;
  finding_id?: string;
  verification_status?: string;
  occurred_at: string;
  step_index: number;
  privacy: { masked: boolean; redacted_fields?: string[] };
}

export type EvidenceStatus = "strongly_represented" | "weak_or_uncertain" | "conflicting" | "stale" | "blocked" | "untouched" | "needs_independent_verification";

export interface CandidateState {
  id: string;
  run_id: string;
  tenant_id: string;
  build_id: string;
  environment_id: string;
  role: string;
  module: string;
  record_id: string;
  business_state: string;
  state_fingerprint: string;
  status: EvidenceStatus;
  evidence_count: number;
  independent_actor_count: number;
  source_types: SourceType[];
  event_ids: string[];
  conflict: boolean;
  updated_at: string;
}

export interface VerificationRequest {
  id: string;
  candidate_state_id: string;
  run_id: string;
  requested_role?: string;
  rationale: string;
  status: string;
  created_at: string;
}

export interface EvidenceResponse {
  run_id: string;
  tenant_id: string;
  build_id: string;
  environment_id: string;
  states: CandidateState[];
  verification_requests: VerificationRequest[];
}

export interface RecommendationFactors {
  delta_uncertainty: number;
  business_risk: number;
  diff_impact: number;
  smoothed_failure_prior: number;
  expected_cost: number;
}

export interface RecommendationAlternative {
  claim_id: string;
  title: string;
  area: string;
  classification: "Solid" | "Weak" | "Stale" | "Conflicted" | "Untouched" | "Blocked";
}

export interface Recommendation {
  id: string;
  tenant_id: string;
  candidate_state_id?: string;
  claim_id: string;
  run_id: string;
  actor_id: string;
  role: string;
  build_id: string;
  environment_id: string;
  scenario_id?: string;
  device?: string;
  region?: string;
  area: string;
  classification: "Solid" | "Weak" | "Stale" | "Conflicted" | "Untouched" | "Blocked";
  max_verification_depth: number;
  independent_source_count: number;
  conflict_count: number;
  rationale: string;
  score: number;
  factors: RecommendationFactors;
  policy_version: string;
  evidence_snapshot: string;
  progression_snapshot: string;
  override_alternatives: RecommendationAlternative[];
  promoted_by_response_id?: string;
  feature_flags?: Record<string, string>;
  created_at: string;
}

export interface RecommendationResponse {
  response: "accepted" | "dismissed" | "override";
  reason: string;
  actor_id: string;
  override_claim_id?: string;
}
export interface StateCorrectionRequest {
  target_state_id?: string;
  new_fingerprint?: string;
  new_business_state?: string;
  event_ids?: string[];
  actor_id: string;
  reason: string;
}
export interface MetricValue {
  value: number | null;
  unit: "ratio" | "count" | "seconds" | "per_tester_hour";
  numerator: number | null;
  denominator: number | null;
  undefined_reason: string | null;
}
export interface ExperimentMetrics {
  run_id: string;
  status: "preview" | "frozen";
  definition_version: "festival-v1.1";
  as_of: string;
  frozen_at: string | null;
  metrics: Record<string, MetricValue>;
  breakdowns: Record<string, unknown>;
  input_receipt: {
    schema_version: "festival-metric-input-v1";
    algorithm: "sha256";
    sha256: string;
    immutable: boolean;
    counts: {
      events: number;
      states: number;
      verification_requests: number;
      recommendations: number;
      responses: number;
      corrections: number;
    };
    row_digests: Record<"events" | "states" | "verification_requests" | "recommendations" | "responses" | "corrections", Array<{ id: string; sha256: string }>>;
  };
}
