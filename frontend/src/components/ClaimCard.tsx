import { useState } from "react";
import type { ClaimEvidenceSummary } from "../types/claim-evidence";

interface ClaimCardProps {
  summary: ClaimEvidenceSummary;
  isRecommended?: boolean;
}

function depthDescription(depth: number): string {
  switch (depth) {
    case 1:
      return "Depth 1 · Surface DOM";
    case 2:
      return "Depth 2 · Client Interaction";
    case 3:
      return "Depth 3 · Backend Persistence";
    case 4:
      return "Depth 4 · Cross-System Telemetry";
    default:
      return "Depth 0 · Unverified";
  }
}

export function ClaimCard({ summary, isRecommended = false }: ClaimCardProps) {
  const [expanded, setExpanded] = useState(false);

  return (
    <article
      className={`claim-card classification-${summary.classification.toLowerCase()}${isRecommended ? " claim-card-recommended" : ""}`}
      data-testid={`claim-card-${summary.claim_id}`}
    >
      <header className="claim-header">
        <div className="claim-identity">
          <span className={`status-pill classification-pill status-${summary.classification}`}>
            {summary.classification}
          </span>
          {isRecommended && (
            <span className="recommended-badge" title="Actively recommended by information-value router">
              Targeted by Router
            </span>
          )}
          <span className="claim-target">{summary.target}</span>
        </div>
        {summary.latest_observation_at && (
          <time className="claim-timestamp" title={summary.latest_observation_at}>
            {new Date(summary.latest_observation_at).toLocaleTimeString()}
          </time>
        )}
      </header>

      <h3 className="claim-title">{summary.claim_id} · {summary.validation_intent.replaceAll("_", " ")}</h3>

      <div className="semantic-signatures">
        <div className="signature-tag">
          <span className="tag-label">State:</span>
          <code>{summary.state_signature}</code>
        </div>
        <div className="signature-tag">
          <span className="tag-label">Actor Context:</span>
          <code>{summary.actor_context}</code>
        </div>
        <div className="signature-tag">
          <span className="tag-label">Intent:</span>
          <code>{summary.validation_intent}</code>
        </div>
      </div>

      <div className="evidence-metrics-grid">
        <div className="metric-chip" title="Maximum verification depth corroborating this claim">
          <span className="metric-chip-label">Depth</span>
          <span className="metric-chip-val">{depthDescription(summary.max_verification_depth)}</span>
        </div>
        <div className="metric-chip">
          <span className="metric-chip-label">Freshness</span>
          <span className={`metric-chip-val freshness-${summary.freshness}`}>{summary.freshness}</span>
        </div>
        <div className="metric-chip">
          <span className="metric-chip-label">Sources</span>
          <span className="metric-chip-val">{summary.independent_source_count} independent</span>
        </div>
        <div className="metric-chip">
          <span className="metric-chip-label">Observations</span>
          <span className="metric-chip-val">{summary.compatible_observation_count} compatible</span>
        </div>
        {summary.conflict_count > 0 && (
          <div className="metric-chip conflict-highlight">
            <span className="metric-chip-label">Conflicts</span>
            <span className="metric-chip-val">{summary.conflict_count} active</span>
          </div>
        )}
      </div>

      {summary.reasons && summary.reasons.length > 0 && (
        <div className="reasons-panel">
          <p className="reasons-heading">Reconciliation Rationale</p>
          <ul className="reasons-list">
            {summary.reasons.map((reason, index) => (
              <li key={index}>{reason}</li>
            ))}
          </ul>
        </div>
      )}

      {summary.evidence_contexts && summary.evidence_contexts.length > 0 && (
        <div className="context-drawer-wrapper">
          <button
            type="button"
            className="text-button context-toggle"
            onClick={() => setExpanded(!expanded)}
            aria-expanded={expanded}
          >
            {expanded ? "Hide Provenance & Context" : `View Provenance Context (${summary.evidence_contexts.length})`}
          </button>

          {expanded && (
            <div className="context-details">
              {summary.evidence_contexts.map((ctx) => (
                <div key={ctx.id} className="context-entry">
                  <div className="context-header">
                    <strong>{ctx.id}</strong>
                    <span>{ctx.build_id} · {ctx.environment_id}</span>
                  </div>
                  <div className="context-meta">
                    {ctx.scenario_id && <span>Scenario: {ctx.scenario_id}</span>}
                    {ctx.device && <span>Device: {ctx.device}</span>}
                    {ctx.region && <span>Region: {ctx.region}</span>}
                    {ctx.feature_flags && Object.keys(ctx.feature_flags).length > 0 && (
                      <span>Flags: {JSON.stringify(ctx.feature_flags)}</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </article>
  );
}
