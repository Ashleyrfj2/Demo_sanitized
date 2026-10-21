import React from "react";
import type { Recommendation } from "../types/validation-event";

export interface RecommendationViewProps {
  recommendation: Recommendation | null;
  reason: string;
  onReasonChange: (reason: string) => void;
  overrideClaimId: string;
  onOverrideClaimChange: (claimId: string) => void;
  onRespond: (response: "accepted" | "dismissed" | "override", overrideClaimId?: string) => void;
  busy: boolean;
}

export function RecommendationView({
  recommendation,
  reason,
  onReasonChange,
  overrideClaimId,
  onOverrideClaimChange,
  onRespond,
  busy,
}: RecommendationViewProps) {
  const hasReason = Boolean(reason.trim());
  const availableAlternatives = recommendation?.override_alternatives ?? [];
  const selectedOverrideIsAvailable = availableAlternatives.some(
    (claim) => claim.claim_id === overrideClaimId
  );

  return (
    <section className="recommendation" aria-labelledby="recommendation-title">
      <div>
        <p className="eyebrow">Next high-value area · Late Tester D</p>
        <h2 id="recommendation-title">{recommendation?.area ?? "Waiting for qualifying claim evidence"}</h2>
        <p>
          {recommendation?.rationale ?? "A recommendation will appear after a claim has an eligible validation path."}
        </p>
        {recommendation && (
          <div className="factors">
            <span>claim {recommendation.claim_id}</span>
            <span>priority {recommendation.score.toFixed(4)}</span>
            <span>policy {recommendation.policy_version}</span>
            {Object.entries(recommendation.factors)
              .filter(([, value]) => value !== 0)
              .map(([name, value]) => (
                <span key={name}>
                  {name.replaceAll("_", " ")} {value.toFixed(2)}
                </span>
              ))}
          </div>
        )}
      </div>
      {recommendation && (
        <div className="response-panel">
          <label htmlFor="response-reason">Required response reason</label>
          <textarea
            id="response-reason"
            value={reason}
            onChange={(event) => onReasonChange(event.target.value)}
            placeholder="Why is this recommendation useful, not useful, or worth overriding?"
          />

          <label htmlFor="override-claim">Alternative claim for override</label>
          <select
            id="override-claim"
            value={overrideClaimId}
            onChange={(event) => onOverrideClaimChange(event.target.value)}
            disabled={busy || availableAlternatives.length === 0}
          >
            <option value="">Choose a different claim</option>
            {availableAlternatives.map((claim) => (
              <option key={claim.claim_id} value={claim.claim_id}>
                {claim.claim_id} · {claim.title} · {claim.classification}
              </option>
            ))}
          </select>

          <div className="actions">
            <button disabled={busy || !hasReason} onClick={() => onRespond("accepted")}>
              Accept area
            </button>
            <button
              className="secondary"
              disabled={busy || !hasReason || !selectedOverrideIsAvailable}
              onClick={() => onRespond("override", overrideClaimId)}
            >
              Override
            </button>
            <button
              className="secondary dark"
              disabled={busy || !hasReason}
              onClick={() => onRespond("dismissed")}
            >
              Dismiss
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
