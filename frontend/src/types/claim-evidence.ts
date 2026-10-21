export type ClaimClassification = "Solid" | "Weak" | "Stale" | "Conflicted" | "Untouched" | "Blocked";
export type ClaimFreshness = "current" | "stale" | "untouched";

export interface EvidenceContext {
  id: string;
  tenant_id: string;
  run_id: string;
  build_id: string;
  environment_id: string;
  scenario_id?: string;
  feature_flags?: Record<string, string>;
  device?: string;
  region?: string;
  created_at: string;
}

export interface ClaimEvidenceSummary {
  claim_id: string;
  tenant_id: string;
  target: string;
  state_signature: string;
  actor_context: string;
  validation_intent: string;
  classification: ClaimClassification;
  heuristic_score: number | null;
  max_verification_depth: number;
  freshness: ClaimFreshness;
  independent_source_count: number;
  compatible_observation_count: number;
  conflict_count: number;
  latest_observation_at?: string;
  reasons: string[];
  evidence_contexts?: EvidenceContext[];
}

export interface ClaimEvidenceSummariesResponse {
  run_id: string;
  tenant_id: string;
  build_id: string;
  environment_id: string;
  summaries: ClaimEvidenceSummary[];
}

export interface ScopeFilter {
  runId: string;
  tenantId: string;
  buildId: string;
  environmentId: string;
  scenarioId?: string;
  featureFlags?: Record<string, string>;
  device?: string;
  region?: string;
}
