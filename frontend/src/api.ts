import type {
  ClaimEvidenceSummariesResponse,
  ScopeFilter,
} from "./types/claim-evidence";
import type {
  EvidenceResponse,
  ExperimentMetrics,
  Recommendation,
  RecommendationResponse,
  StateCorrectionRequest,
} from "./types/validation-event";

const configuredBase = import.meta.env.VITE_QA_API_BASE_URL || "http://127.0.0.1:8080";
// A VITE_ token becomes public JavaScript at build time. Keep this local demo token only in memory.
let apiToken = "";
export function setLocalApiToken(token: string): void { apiToken = token.trim(); }

const apiURL = new URL(configuredBase);
if (!["127.0.0.1", "localhost"].includes(apiURL.hostname) || apiURL.port !== "8080" || apiURL.protocol !== "http:") {
  throw new Error("VITE_QA_API_BASE_URL must point to a loopback host on port 8080");
}


async function request<T>(path: string, init?: RequestInit): Promise<T> {
  if (!apiToken) throw new Error("Enter a local API token before requesting demo data");
  const response = await fetch(new URL(path, apiURL), {
    ...init,
    headers: { "authorization": `Bearer ${apiToken}`, "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(body.error || `Request failed with ${response.status}`);
  }
  return response.json() as Promise<T>;
}

function appendOptionalScope(query: URLSearchParams, scope: ScopeFilter) {
  if (scope.scenarioId) query.set("scenario_id", scope.scenarioId);
  if (scope.device) query.set("device", scope.device);
  if (scope.region) query.set("region", scope.region);
  for (const key of Object.keys(scope.featureFlags ?? {}).sort()) {
    query.append("feature_flag", `${key}=${scope.featureFlags?.[key]}`);
  }
}

export function getClaimEvidenceSummaries(scope: ScopeFilter) {
  const query = new URLSearchParams({
    run_id: scope.runId,
    tenant_id: scope.tenantId,
    build_id: scope.buildId,
    environment_id: scope.environmentId,
  });

  appendOptionalScope(query, scope);

  return request<ClaimEvidenceSummariesResponse>(`/api/v1/claims/evidence?${query}`);
}

export function getEvidence(runId: string, tenantId: string, buildId: string, environmentId: string) {
  const query = new URLSearchParams({ run_id: runId, tenant_id: tenantId, build_id: buildId, environment_id: environmentId });
  return request<EvidenceResponse>(`/api/v1/evidence?${query}`);
}

export function getRecommendation(scope: ScopeFilter, actorId: string, role: string) {
  const query = new URLSearchParams({
    run_id: scope.runId,
    tenant_id: scope.tenantId,
    build_id: scope.buildId,
    environment_id: scope.environmentId,
    actor_id: actorId,
    role,
  });
  appendOptionalScope(query, scope);
  return request<Recommendation>(`/api/v1/recommendations/next?${query}`);
}

export function respondToRecommendation(id: string, response: RecommendationResponse) {
  return request<{ status: string }>(`/api/v1/recommendations/${encodeURIComponent(id)}/responses`, { method: "POST", body: JSON.stringify(response) });
}

export function correctState(id: string, type: "merge" | "split" | "rename", correction: StateCorrectionRequest) {
  return request<{ status: string }>(`/api/v1/states/${encodeURIComponent(id)}/${type}`, { method: "POST", body: JSON.stringify(correction) });
}

export function getMetrics(runId: string) {
  return request<ExperimentMetrics>(`/api/v1/experiments/${encodeURIComponent(runId)}/metrics`);
}
