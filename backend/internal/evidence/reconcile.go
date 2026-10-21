package evidence

import (
	"fmt"
	"strings"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

// DetermineDepth returns the verification depth (1 to 4) for an observation.
func DetermineDepth(obs model.Observation) int {
	sourceType := strings.ToLower(strings.TrimSpace(obs.SourceType))
	sourceAdapter := strings.ToLower(strings.TrimSpace(obs.SourceAdapter))
	verifierResult := strings.ToLower(strings.TrimSpace(obs.VerifierResult))
	assessment := strings.ToLower(strings.TrimSpace(obs.Assessment))

	// Depth 4: Cross-system / asynchronous verification
	if sourceType == "telemetry" || strings.Contains(sourceAdapter, "async") || strings.Contains(sourceAdapter, "cross-system") || strings.Contains(verifierResult, "cross-system") {
		return int(model.DepthCrossSystem)
	}

	// Depth 3: Backend / database / persistence verification
	if sourceType == "backend_verifier" || sourceType == "db_verifier" ||
		strings.Contains(sourceAdapter, "activity-log") || strings.Contains(sourceAdapter, "backend") || strings.Contains(sourceAdapter, "database") || strings.Contains(sourceAdapter, "db") ||
		strings.Contains(verifierResult, "persisted") || strings.Contains(verifierResult, "database") || strings.Contains(verifierResult, "backend") ||
		strings.Contains(verifierResult, "audit") || strings.Contains(verifierResult, "revoked") || strings.Contains(verifierResult, "reconnect") ||
		strings.Contains(verifierResult, "reconcile") || strings.Contains(verifierResult, "denied_mutation") || strings.Contains(verifierResult, "permission_denied") {
		return int(model.DepthPersistence)
	}

	// Depth 2: Interaction / client mutation
	if sourceType == "agent" || strings.Contains(sourceAdapter, "playwright") ||
		strings.Contains(assessment, "mutation") || strings.Contains(verifierResult, "click") || strings.Contains(verifierResult, "interact") ||
		strings.Contains(verifierResult, "mutation") {
		return int(model.DepthInteraction)
	}

	// Depth 1: Surface / DOM observation
	return int(model.DepthSurface)
}

// OracleSourceKey returns a grouping key for source independence.
// Multiple actors running the same shallow client-side check without an independent verifier share the same underlying client oracle.
func OracleSourceKey(obs model.Observation) string {
	depth := DetermineDepth(obs)
	sourceType := strings.ToLower(strings.TrimSpace(obs.SourceType))
	sourceAdapter := strings.ToLower(strings.TrimSpace(obs.SourceAdapter))
	actorID := strings.TrimSpace(obs.ActorID)
	verifierResult := strings.ToLower(strings.TrimSpace(obs.VerifierResult))

	// If it's a surface DOM observation without an independent verifier, different actors
	// using the same client adapter share the same underlying client oracle.
	if depth == int(model.DepthSurface) {
		return fmt.Sprintf("surface|%s|%s", sourceType, sourceAdapter)
	}

	// If there's an explicit verifier result / oracle identifier, group by verifier oracle
	if verifierResult != "" {
		return fmt.Sprintf("depth%d|%s|%s|%s|%s", depth, sourceType, sourceAdapter, actorID, verifierResult)
	}

	return fmt.Sprintf("depth%d|%s|%s|%s", depth, sourceType, sourceAdapter, actorID)
}

func scorePtr(v float64) *float64 {
	return &v
}

func areFeatureFlagsCompatible(scopeFlags, ctxFlags map[string]string) bool {
	if len(scopeFlags) == 0 {
		return true
	}
	for k, v := range scopeFlags {
		if cv, ok := ctxFlags[k]; !ok || cv != v {
			return false
		}
	}
	return true
}

// IsContextCompatible evaluates whether an evidence context matches the requested target scope.
// It checks tenant_id, environment_id, scenario_id, feature_flags, device, region, run_id, and build_id.
func IsContextCompatible(scope model.Scope, ctx model.EvidenceContext) (compatible bool, staleBuild bool) {
	if ctx.TenantID != scope.TenantID || ctx.EnvironmentID != scope.EnvironmentID {
		return false, false
	}
	// Scenario / data context check
	if scope.ScenarioID != "" && ctx.ScenarioID != "" && scope.ScenarioID != ctx.ScenarioID {
		return false, false
	}
	// Feature flags check
	if !areFeatureFlagsCompatible(scope.FeatureFlags, ctx.FeatureFlags) {
		return false, false
	}
	// Device check (populated / specified)
	if scope.Device != "" && ctx.Device != "" && !strings.EqualFold(scope.Device, ctx.Device) {
		return false, false
	}
	// Region check (populated / specified)
	if scope.Region != "" && ctx.Region != "" && !strings.EqualFold(scope.Region, ctx.Region) {
		return false, false
	}

	if ctx.BuildID == scope.BuildID {
		if scope.RunID == "" || ctx.RunID == scope.RunID {
			return true, false
		}
		return false, false // different run on same build
	}
	// Different / older build in the same tenant and environment
	return false, true
}

// ReconcileClaim evaluates raw observations for a claim against a requested target scope and returns a transparent summary.
func ReconcileClaim(claim model.ClaimDefinition, scope model.Scope, observations []model.Observation, contexts map[string]model.EvidenceContext) model.ClaimEvidenceSummary {
	summary := model.ClaimEvidenceSummary{
		ClaimID:          claim.ID,
		TenantID:         claim.TenantID,
		Target:           claim.Target,
		StateSignature:   claim.StateSignature,
		ActorContext:     claim.ActorContext,
		ValidationIntent: claim.ValidationIntent,
		Classification:   "Untouched",
		HeuristicScore:   scorePtr(0.0),
		Freshness:        "untouched",
		Reasons:          []string{},
		EvidenceContexts: []model.EvidenceContext{},
	}

	if len(observations) == 0 {
		summary.Reasons = append(summary.Reasons, "No observations exist for this claim.")
		return summary
	}

	var compatibleObs []model.Observation
	var staleObs []model.Observation
	var latestTime *time.Time
	contextsUsed := make(map[string]model.EvidenceContext)

	for _, obs := range observations {
		ctx, hasCtx := contexts[obs.EvidenceContextID]
		if !hasCtx {
			continue
		}

		if latestTime == nil || obs.OccurredAt.After(*latestTime) {
			t := obs.OccurredAt.UTC()
			latestTime = &t
		}

		compatible, staleBuild := IsContextCompatible(scope, ctx)
		if compatible {
			compatibleObs = append(compatibleObs, obs)
			contextsUsed[ctx.ID] = ctx
		} else if staleBuild {
			staleObs = append(staleObs, obs)
			contextsUsed[ctx.ID] = ctx
		}
	}

	summary.LatestObservationAt = latestTime
	for _, ctx := range contextsUsed {
		summary.EvidenceContexts = append(summary.EvidenceContexts, ctx)
	}

	// 1. Untouched / Stale checks
	if len(compatibleObs) == 0 {
		if len(staleObs) > 0 {
			summary.Classification = "Stale"
			summary.Freshness = "stale"
			summary.HeuristicScore = scorePtr(0.3)
			summary.Reasons = append(summary.Reasons, fmt.Sprintf("Prior evidence exists, but belongs to an older or incompatible build context (%s).", staleObs[0].RunID))
			return summary
		}
		summary.Classification = "Untouched"
		summary.Freshness = "untouched"
		summary.HeuristicScore = scorePtr(0.0)
		summary.Reasons = append(summary.Reasons, "No compatible observations exist for the requested build and context.")
		return summary
	}

	summary.CompatibleObservationCount = len(compatibleObs)
	summary.Freshness = "current"

	// 2. Compute depths, source independence, and assessment taxonomy among compatible observations
	maxDepth := 0
	maxSupportingDepth := 0
	oracleKeys := make(map[string]bool)
	var supportCount, conflictCount, invalidateCount, weakenCount, blockedCount int
	var blockedReasons []string
	var conflictDetails []string
	var invalidateDetails []string
	var weakenDetails []string

	for _, obs := range compatibleObs {
		depth := DetermineDepth(obs)
		if depth > maxDepth {
			maxDepth = depth
		}
		oracleKeys[OracleSourceKey(obs)] = true

		assessment := strings.ToLower(strings.TrimSpace(obs.Assessment))

		switch assessment {
		case "blocked":
			blockedCount++
			detail := obs.VerifierResult
			if detail == "" {
				detail = "validation was explicitly unexecutable"
			}
			blockedReasons = append(blockedReasons, detail)
		case "supports":
			supportCount++
			if depth > maxSupportingDepth {
				maxSupportingDepth = depth
			}
		case "weakens":
			weakenCount++
			weakenDetails = append(weakenDetails, fmt.Sprintf("weakened by %s (%s)", obs.SourceType, obs.ActorID))
		case "conflicts":
			conflictCount++
			conflictDetails = append(conflictDetails, fmt.Sprintf("conflicts (%s by %s)", obs.SourceType, obs.ActorID))
		case "invalidates":
			invalidateCount++
			invalidateDetails = append(invalidateDetails, fmt.Sprintf("invalidates (%s by %s)", obs.SourceType, obs.ActorID))
		}
	}

	summary.MaxVerificationDepth = maxDepth
	summary.IndependentSourceCount = len(oracleKeys)
	summary.ConflictCount = conflictCount + invalidateCount

	// 3. Evaluate Blocked (validation could not execute to completion)
	if blockedCount > 0 && supportCount == 0 && conflictCount == 0 && invalidateCount == 0 {
		summary.Classification = "Blocked"
		summary.HeuristicScore = scorePtr(0.0)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Validation could not be completed: %s.", strings.Join(blockedReasons, "; ")))
		return summary
	}

	// 4. Evaluate Conflicted & Invalidation (Disagreement has classification precedence)
	if conflictCount > 0 || (invalidateCount > 0 && supportCount > 0) || (invalidateCount > 0 && conflictCount == 0 && supportCount == 0) {
		summary.Classification = "Conflicted"
		summary.HeuristicScore = nil // Omitted / non-probabilistic for conflict
		var details []string
		details = append(details, conflictDetails...)
		details = append(details, invalidateDetails...)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Credible evidence disagrees on this claim: %d supporting observation(s) vs %d conflicting/invalidating observation(s) [%s].",
			supportCount, conflictCount+invalidateCount, strings.Join(details, ", ")))
		return summary
	}

	// 5. Explicit V0 Sufficiency Rule for Solid
	// Solid requires:
	// - At least one compatible supporting observation
	// - Supporting verification depth >= 3 (backend/persistence or cross-system)
	// - No unresolved conflicts or invalidations
	// - No dominant weakening (unless multiple independent depth-3 sources dominate)
	isSolidSufficiencyMet := supportCount > 0 &&
		maxSupportingDepth >= int(model.DepthPersistence) &&
		conflictCount == 0 &&
		invalidateCount == 0 &&
		(weakenCount == 0 || summary.IndependentSourceCount >= 2)

	if isSolidSufficiencyMet {
		summary.Classification = "Solid"
		summary.HeuristicScore = scorePtr(0.9)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Corroborated by depth-%d backend/persistence verification satisfying validation intent (%d independent source(s), zero conflicts).", maxSupportingDepth, summary.IndependentSourceCount))
		return summary
	}

	// 6. Weak: Surface observations, client interactions, or weakened evidence
	summary.Classification = "Weak"
	if weakenCount > 0 {
		summary.HeuristicScore = scorePtr(0.35)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Evidence is weakened by observation(s) [%s].", strings.Join(weakenDetails, ", ")))
	} else if maxSupportingDepth == int(model.DepthSurface) {
		summary.HeuristicScore = scorePtr(0.4)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Evidence is limited to depth-1 surface DOM observation(s) without backend persistence verification (%d observation(s) across %d source(s)).", len(compatibleObs), summary.IndependentSourceCount))
	} else if maxSupportingDepth == int(model.DepthInteraction) {
		summary.HeuristicScore = scorePtr(0.6)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Evidence includes depth-2 client interaction but lacks independent depth-3 backend/persistence confirmation (%d observation(s)).", len(compatibleObs)))
	} else {
		summary.HeuristicScore = scorePtr(0.5)
		summary.Reasons = append(summary.Reasons, fmt.Sprintf("Evidence does not satisfy depth-3 persistence sufficiency for validation intent (%d observation(s)).", len(compatibleObs)))
	}

	return summary
}
