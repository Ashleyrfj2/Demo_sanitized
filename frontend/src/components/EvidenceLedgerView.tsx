import { useMemo, useState } from "react";
import type { ClaimClassification, ClaimEvidenceSummary } from "../types/claim-evidence";
import type { Recommendation } from "../types/validation-event";
import { ClaimCard } from "./ClaimCard";
import { RecommendationView } from "./RecommendationView";

interface EvidenceLedgerViewProps {
  summaries: ClaimEvidenceSummary[];
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
  recommendation?: Recommendation | null;
  reason?: string;
  onReasonChange?: (reason: string) => void;
  overrideClaimId?: string;
  onOverrideClaimChange?: (claimId: string) => void;
  onRespond?: (response: "accepted" | "dismissed" | "override", overrideClaimId?: string) => void;
  busy?: boolean;
}

const CLASSIFICATIONS: ClaimClassification[] = [
  "Solid",
  "Weak",
  "Conflicted",
  "Blocked",
  "Stale",
  "Untouched",
];

export function EvidenceLedgerView({
  summaries,
  loading,
  error,
  onRefresh,
  recommendation,
  reason = "",
  onReasonChange,
  overrideClaimId = "",
  onOverrideClaimChange,
  onRespond,
  busy = false,
}: EvidenceLedgerViewProps) {
  const [selectedFilter, setSelectedFilter] = useState<string>("ALL");
  const [searchTerm, setSearchTerm] = useState<string>("");

  const counts = useMemo(() => {
    const map: Record<string, number> = {
      Solid: 0,
      Weak: 0,
      Conflicted: 0,
      Blocked: 0,
      Stale: 0,
      Untouched: 0,
    };
    for (const s of summaries) {
      if (map[s.classification] !== undefined) {
        map[s.classification]++;
      }
    }
    return map;
  }, [summaries]);

  const filteredSummaries = useMemo(() => {
    return summaries.filter((summary) => {
      if (selectedFilter !== "ALL" && summary.classification !== selectedFilter) {
        return false;
      }
      if (searchTerm.trim() !== "") {
        const term = searchTerm.toLowerCase();
        const matchTitle = summary.claim_id.toLowerCase().includes(term);
        const matchTarget = summary.target.toLowerCase().includes(term);
        const matchIntent = summary.validation_intent.toLowerCase().includes(term);
        const matchState = summary.state_signature.toLowerCase().includes(term);
        return matchTitle || matchTarget || matchIntent || matchState;
      }
      return true;
    });
  }, [summaries, selectedFilter, searchTerm]);

  return (
    <section className="evidence-ledger-container" aria-label="Evidence Ledger">
      <div className="ledger-header-panel">
        <div>
          <p className="eyebrow">Layer 2 & 3 · Reconciled System of Record</p>
          <h2>Build Validation & Evidence Ledger</h2>
          <p className="ledger-description">
            Transparent assessment of what the platform has reason to believe about each claim.
            Derived on read across verification depths, source independence, context compatibility, and conflict status.
          </p>
        </div>
      </div>

      <div className="distinction-callout" role="note">
        <strong>Evidence vs. Raw Visitation</strong>
        <span>
          A state visit is not knowledge. While state coverage tracks visited UI paths, the Evidence Ledger reconciles
          independent surface, interaction, and backend verifications into explicit claim beliefs.
        </span>
      </div>

      {onRespond && (
        <RecommendationView
          recommendation={recommendation ?? null}
          reason={reason}
          onReasonChange={onReasonChange ?? (() => {})}
          overrideClaimId={overrideClaimId}
          onOverrideClaimChange={onOverrideClaimChange ?? (() => {})}
          onRespond={onRespond}
          busy={busy}
        />
      )}

      <div className="classification-summary-grid" aria-label="Claim classification metrics">
        <button
          type="button"
          className={`summary-pill-card ${selectedFilter === "ALL" ? "active" : ""}`}
          onClick={() => setSelectedFilter("ALL")}
        >
          <span className="summary-count">{summaries.length}</span>
          <span className="summary-label">Total Claims</span>
        </button>

        {CLASSIFICATIONS.map((classification) => (
          <button
            key={classification}
            type="button"
            className={`summary-pill-card status-${classification} ${selectedFilter === classification ? "active" : ""}`}
            onClick={() => setSelectedFilter(classification)}
          >
            <span className="summary-count">{counts[classification] ?? 0}</span>
            <span className="summary-label">{classification}</span>
          </button>
        ))}
      </div>

      <div className="ledger-controls">
        <div className="search-box">
          <input
            type="text"
            placeholder="Filter claims by target, intent, or signature..."
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            aria-label="Filter claims"
          />
          {searchTerm && (
            <button type="button" className="clear-search" onClick={() => setSearchTerm("")}>
              ✕
            </button>
          )}
        </div>

        <div className="ledger-filter-tags">
          <button
            type="button"
            className={`filter-tag ${selectedFilter === "ALL" ? "active" : ""}`}
            onClick={() => setSelectedFilter("ALL")}
          >
            All ({summaries.length})
          </button>
          {CLASSIFICATIONS.map((classification) => (
            <button
              key={classification}
              type="button"
              className={`filter-tag tag-${classification.toLowerCase()} ${selectedFilter === classification ? "active" : ""}`}
              onClick={() => setSelectedFilter(classification)}
            >
              {classification} ({counts[classification] ?? 0})
            </button>
          ))}
        </div>
      </div>

      {loading && summaries.length === 0 && (
        <div className="ledger-loading" role="status">
          <p>Reconciling claim evidence for active scope...</p>
        </div>
      )}

      {error && (
        <div className="error" role="alert">
          <p>Evidence Reconciliation API Notice: {error}</p>
          <button type="button" className="secondary" onClick={onRefresh}>
            Retry
          </button>
        </div>
      )}

      <div className="claims-grid">
        {filteredSummaries.map((summary) => (
          <ClaimCard
            key={summary.claim_id}
            summary={summary}
            isRecommended={summary.claim_id === recommendation?.claim_id}
          />
        ))}

        {!loading && filteredSummaries.length === 0 && (
          <div className="empty-ledger">
            <p>
              {searchTerm || selectedFilter !== "ALL"
                ? `No claims found matching filter "${selectedFilter}" ${searchTerm ? `and query "${searchTerm}"` : ""}.`
                : "No claim definitions or evidence found for this build and environment context."}
            </p>
          </div>
        )}
      </div>
    </section>
  );
}
