import { useCallback, useEffect, useMemo, useState } from "react";
import {
  correctState,
  getClaimEvidenceSummaries,
  getEvidence,
  getMetrics,
  getRecommendation,
  respondToRecommendation,
  setLocalApiToken,
} from "./api";
import type { CandidateState, EvidenceResponse, ExperimentMetrics, Recommendation } from "./types/validation-event";
import type { ClaimEvidenceSummary, ScopeFilter } from "./types/claim-evidence";
import { EvidenceLedgerView } from "./components/EvidenceLedgerView";
import { RecommendationView } from "./components/RecommendationView";

const defaultScope: ScopeFilter = {
  runId: "festival-guided-001",
  tenantId: "festival-thesis-demo",
  buildId: "festnest-demo-001",
  environmentId: "festnest-local-browser",
  scenarioId: "equipment-handoff-v1",
};

const lateTester = { actorId: "late-tester-d", role: "editor" };

const statusLabel: Record<string, string> = {
  strongly_represented: "Strong",
  weak_or_uncertain: "Uncertain",
  conflicting: "Conflicting",
  stale: "Stale",
  blocked: "Blocked",
  untouched: "Untouched",
  needs_independent_verification: "Needs independent check",
};

type ActiveTab = "ledger" | "states" | "metrics";

export default function App() {
  const [activeTab, setActiveTab] = useState<ActiveTab>("ledger");
  const [activeRunId, setActiveRunId] = useState<string>(() => {
    if (typeof window !== "undefined") {
      const params = new URLSearchParams(window.location.search);
      const urlRunId = params.get("runId");
      if (urlRunId?.trim()) return urlRunId.trim();
    }
    return defaultScope.runId;
  });

  const scope: ScopeFilter = useMemo(() => ({
    ...defaultScope,
    runId: activeRunId,
  }), [activeRunId]);

  const [claimSummaries, setClaimSummaries] = useState<ClaimEvidenceSummary[]>([]);
  const [evidence, setEvidence] = useState<EvidenceResponse | null>(null);
  const [recommendation, setRecommendation] = useState<Recommendation | null>(null);
  const [metrics, setMetrics] = useState<ExperimentMetrics[]>([]);
  const [reason, setReason] = useState("");
  const [overrideClaimId, setOverrideClaimId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [localToken, setLocalToken] = useState("");
  const [isConnected, setIsConnected] = useState(false);
  const [loadingClaims, setLoadingClaims] = useState(true);

  const refresh = useCallback(async () => {
    try {
      // 1. Fetch claims evidence summaries (Source of Truth for Build Validation)
      const claimsPromise = getClaimEvidenceSummaries(scope)
        .then((res) => {
          setClaimSummaries(res.summaries ?? []);
          setLoadingClaims(false);
          return res.summaries ?? [];
        })
        .catch((err) => {
          setLoadingClaims(false);
          throw err;
        });

      // 2. Fetch candidate state coverage and metrics
      const metricsPromise = Promise.allSettled([getMetrics("festival-baseline-001"), getMetrics(scope.runId)]);
      const nextEvidence = await getEvidence(scope.runId, scope.tenantId, scope.buildId, scope.environmentId);
      setEvidence(nextEvidence);

      const nextClaimSummaries = await claimsPromise;

      const runMetrics = await metricsPromise;
      setMetrics(runMetrics.flatMap((result) => (result.status === "fulfilled" ? [result.value] : [])));
      const guidedMetrics = runMetrics.find(
        (result) => result.status === "fulfilled" && result.value.run_id === scope.runId
      );

      if (
        nextClaimSummaries.length > 0 &&
        (guidedMetrics?.status !== "fulfilled" || guidedMetrics.value.status !== "frozen")
      ) {
        try {
          setRecommendation(
            await getRecommendation(scope, lateTester.actorId, lateTester.role)
          );
        } catch {
          setRecommendation(null);
        }
      } else {
        setRecommendation(null);
      }
      setError(null);
    } catch (refreshError) {
      setError(refreshError instanceof Error ? refreshError.message : "Unable to load evidence");
    }
  }, []);

  useEffect(() => {
    if (!isConnected) return;
    void refresh();
    const timer = window.setInterval(() => void refresh(), 4_000);
    return () => window.clearInterval(timer);
  }, [refresh, isConnected]);

  useEffect(() => {
    setOverrideClaimId("");
  }, [recommendation?.id]);

  const statusCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const state of evidence?.states ?? []) counts[state.status] = (counts[state.status] ?? 0) + 1;
    return counts;
  }, [evidence]);

  async function respond(response: "accepted" | "dismissed" | "override", selectedOverrideClaimId?: string) {
    if (!recommendation || !reason.trim()) return;
    if (response === "override" && !selectedOverrideClaimId?.trim()) return;
    setBusy(true);
    try {
      await respondToRecommendation(recommendation.id, {
        response,
        reason: reason.trim(),
        actor_id: lateTester.actorId,
        ...(response === "override" ? { override_claim_id: selectedOverrideClaimId?.trim() } : {}),
      });
      setReason("");
      setOverrideClaimId("");
      await refresh();
    } catch (responseError) {
      setError(responseError instanceof Error ? responseError.message : "Response failed");
    } finally {
      setBusy(false);
    }
  }

  async function correction(state: CandidateState, type: "rename" | "merge" | "split") {
    const correctionReason = window.prompt("Reason for this evidence-state correction?");
    if (!correctionReason?.trim()) return;
    const base = { actor_id: lateTester.actorId, reason: correctionReason.trim() };
    let request;
    if (type === "rename") {
      const nextState = window.prompt("New business-state name", state.business_state);
      if (!nextState) return;
      request = {
        ...base,
        new_business_state: nextState,
        new_fingerprint: state.state_fingerprint.replace(/status:[^:]+$/, `status:${nextState}`),
      };
    } else if (type === "merge") {
      const target = window.prompt(
        "Target state ID",
        evidence?.states.find((candidate) => candidate.id !== state.id)?.id ?? ""
      );
      if (!target) return;
      request = { ...base, target_state_id: target };
    } else {
      const event = state.event_ids.at(-1);
      if (!event) return;
      const nextState = window.prompt("Business-state name for the split evidence", `${state.business_state}-variant`);
      if (!nextState) return;
      request = {
        ...base,
        event_ids: [event],
        new_business_state: nextState,
        new_fingerprint: `${state.state_fingerprint}:variant:${nextState}`,
      };
    }
    setBusy(true);
    try {
      await correctState(state.id, type, request);
      await refresh();
    } catch (correctionError) {
      setError(correctionError instanceof Error ? correctionError.message : "Correction failed");
    } finally {
      setBusy(false);
    }
  }

  if (!isConnected) {
    return (
      <main>
        <header className="hero">
          <div>
            <p className="eyebrow">Local demo connection</p>
            <h1>Connect to Build Validation</h1>
            <p className="lede">Enter a locally generated API token. It stays in memory for this page and is never embedded in the frontend build.</p>
          </div>
        </header>
        <form className="scope" onSubmit={(event) => {
          event.preventDefault();
          if (!localToken.trim()) return;
          setLocalApiToken(localToken);
          setLocalToken("");
          setIsConnected(true);
        }}>
          <label htmlFor="local-demo-api-token">Local demo API token</label>
          <input
            id="local-demo-api-token"
            type="password"
            autoComplete="off"
            value={localToken}
            onChange={(event) => setLocalToken(event.target.value)}
            required
          />
          <button type="submit">Connect</button>
        </form>
        <p>Generate credentials locally with <code>node scripts/demo/create-local-auth.mjs</code> and use the human:late-tester-d token from the ignored <code>demo/.generated/source-tokens.json</code>. Never use production credentials in this browser.</p>
      </main>
    );
  }

  return (
    <main>
      <header className="hero">
        <div>
          <p className="eyebrow">Festival virtual QA · Validation-Evidence System of Record</p>
          <h1>Build Validation</h1>
          <p className="lede">
            Reconcile multi-source observations into durable claim verification, preserve high-depth proof, and eliminate false confidence from raw state visits.
          </p>
        </div>
        <button className="secondary" type="button" onClick={() => void refresh()} disabled={busy}>
          Refresh Evidence
        </button>
      </header>

      <section className="scope" aria-label="Active evidence scope">
        <span>{scope.runId}</span>
        <span>{scope.tenantId}</span>
        <span>{scope.buildId}</span>
        <span>{scope.environmentId}</span>
        {scope.scenarioId && <span>{scope.scenarioId}</span>}
      </section>

      {error && <p className="error" role="alert">Transport or API notice: {error}</p>}

      {/* Top-level Navigation Switcher */}
      <nav className="main-view-nav" aria-label="View Navigation">
        <button
          type="button"
          className={`nav-tab ${activeTab === "ledger" ? "active" : ""}`}
          onClick={() => setActiveTab("ledger")}
        >
          <strong>Evidence Ledger</strong>
          <span className="tab-badge">{claimSummaries.length} Claims</span>
        </button>
        <button
          type="button"
          className={`nav-tab ${activeTab === "states" ? "active" : ""}`}
          onClick={() => setActiveTab("states")}
        >
          <strong>Candidate States</strong>
          <span className="tab-badge">{evidence?.states.length ?? 0} Paths</span>
        </button>
        <button
          type="button"
          className={`nav-tab ${activeTab === "metrics" ? "active" : ""}`}
          onClick={() => setActiveTab("metrics")}
        >
          <strong>Experiment Metrics</strong>
          <span className="tab-badge">{metrics.length} Runs</span>
        </button>
      </nav>

      {/* Primary View: Evidence Ledger */}
      {activeTab === "ledger" && (
        <EvidenceLedgerView
          summaries={claimSummaries}
          loading={loadingClaims}
          error={error}
          onRefresh={() => void refresh()}
          recommendation={recommendation}
          reason={reason}
          onReasonChange={setReason}
          overrideClaimId={overrideClaimId}
          onOverrideClaimChange={setOverrideClaimId}
          onRespond={(response, selectedOverrideClaimId) => void respond(response, selectedOverrideClaimId)}
          busy={busy}
        />
      )}

      {/* Secondary View: Candidate States & Recommendations */}
      {activeTab === "states" && (
        <section className="states-view-container" aria-label="Candidate States Coverage">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Supporting Layer · State Visitation</p>
              <h2>Reconciled Candidate States</h2>
            </div>
            <p>{evidence?.states.length ?? 0} state groups recorded</p>
          </div>

          <section className="summary-grid" aria-label="Evidence status summary">
            {Object.entries(statusLabel).map(([status, label]) => (
              <article className={`summary status-${status}`} key={status}>
                <strong>{statusCounts[status] ?? 0}</strong>
                <span>{label}</span>
              </article>
            ))}
          </section>

          <RecommendationView
            recommendation={recommendation}
            reason={reason}
            onReasonChange={setReason}
            overrideClaimId={overrideClaimId}
            onOverrideClaimChange={setOverrideClaimId}
            onRespond={(response, selectedOverrideClaimId) => void respond(response, selectedOverrideClaimId)}
            busy={busy}
          />

          <section className="state-grid">
            {(evidence?.states ?? []).map((state) => (
              <article className="state-card" key={state.id}>
                <div className="state-top">
                  <span className={`status-pill status-${state.status}`}>{statusLabel[state.status]}</span>
                  <time>{new Date(state.updated_at).toLocaleTimeString()}</time>
                </div>
                <h3>
                  {state.module} · {state.business_state}
                </h3>
                <p className="fingerprint">{state.state_fingerprint}</p>
                <dl>
                  <div>
                    <dt>Role</dt>
                    <dd>{state.role}</dd>
                  </div>
                  <div>
                    <dt>Evidence</dt>
                    <dd>{state.evidence_count}</dd>
                  </div>
                  <div>
                    <dt>Actors</dt>
                    <dd>{state.independent_actor_count}</dd>
                  </div>
                  <div>
                    <dt>Sources</dt>
                    <dd>{state.source_types.join(", ") || "none"}</dd>
                  </div>
                </dl>
                <div className="card-actions">
                  <button className="text-button" onClick={() => void correction(state, "rename")}>
                    Rename
                  </button>
                  <button
                    className="text-button"
                    onClick={() => void correction(state, "split")}
                    disabled={state.event_ids.length === 0}
                  >
                    Split
                  </button>
                  <button
                    className="text-button"
                    onClick={() => void correction(state, "merge")}
                    disabled={(evidence?.states.length ?? 0) < 2}
                  >
                    Merge
                  </button>
                </div>
              </article>
            ))}
            {evidence?.states.length === 0 && (
              <p className="empty">No normalized evidence has arrived for this exact build and environment.</p>
            )}
          </section>

          <section className="lower-grid">
            <article className="panel">
              <p className="eyebrow">Open verification requests</p>
              <h2>Human judgment queue</h2>
              {(evidence?.verification_requests ?? []).map((request) => (
                <div className="request" key={request.id}>
                  <strong>{request.requested_role || "Independent tester"}</strong>
                  <p>{request.rationale}</p>
                </div>
              ))}
              {evidence?.verification_requests.length === 0 && <p>No open verification requests.</p>}
            </article>
          </section>
        </section>
      )}

      {/* Tertiary View: Experiment Metrics */}
      {activeTab === "metrics" && (
        <section className="metrics-view-container" aria-label="Versioned Experiment Metrics">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Layer 4 · Research Benchmark</p>
              <h2>Versioned Experiment Metrics</h2>
            </div>
            <p>{metrics.length} runs loaded</p>
          </div>
          <article className="panel">
            <p className="eyebrow">Baseline & Guided Runs</p>
            <h2>Comparative Metrics Receipts</h2>
            {metrics.map((run) => (
              <details key={run.run_id} open>
                <summary>
                  {run.run_id} · {run.definition_version} · {run.status}
                </summary>
                <dl className="metrics">
                  {Object.entries(run.metrics).map(([name, metric]) => (
                    <div key={name}>
                      <dt>{name.replaceAll("_", " ")}</dt>
                      <dd>
                        {metric.value === null
                          ? `Undefined · ${metric.undefined_reason}`
                          : `${metric.value.toFixed(2)} ${metric.unit}`}
                      </dd>
                    </div>
                  ))}
                </dl>
              </details>
            ))}
            {metrics.length === 0 && <p>Metrics previews appear as soon as an explicit run is created.</p>}
          </article>
        </section>
      )}
    </main>
  );
}
