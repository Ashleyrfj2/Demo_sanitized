package evidence

import (
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func TestReconcileClaimUntouched(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	summary := ReconcileClaim(claim, scope, nil, nil)
	if summary.Classification != "Untouched" {
		t.Fatalf("expected Untouched, got %s", summary.Classification)
	}
	if summary.HeuristicScore == nil || *summary.HeuristicScore != 0.0 {
		t.Fatalf("expected 0.0 score, got %v", summary.HeuristicScore)
	}
	if summary.MaxVerificationDepth != 0 {
		t.Fatalf("expected max depth 0, got %d", summary.MaxVerificationDepth)
	}
	if len(summary.Reasons) == 0 {
		t.Fatalf("expected reason text")
	}
}

func TestReconcileClaimShallowEvidenceWeak(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	obs1 := model.Observation{
		ID:                "obs-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obs1}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Weak" {
		t.Fatalf("expected Weak, got %s", summary.Classification)
	}
	if summary.MaxVerificationDepth != 1 {
		t.Fatalf("expected depth 1, got %d", summary.MaxVerificationDepth)
	}
	if summary.IndependentSourceCount != 1 {
		t.Fatalf("expected 1 source, got %d", summary.IndependentSourceCount)
	}
}

func TestReconcileClaimDuplicateSameSourceProtection(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	// 10 human clicks from different actors using the same shallow browser extension without backend verifier
	var observations []model.Observation
	for i := 0; i < 10; i++ {
		observations = append(observations, model.Observation{
			ID:                "obs-" + string(rune('a'+i)),
			TenantID:          "tenant-a",
			RunID:             "run-001",
			ClaimID:           "claim-001",
			EvidenceContextID: "ctx-001",
			SourceType:        "human",
			SourceAdapter:     "chrome-mv3",
			ActorID:           "editor-" + string(rune('a'+i)),
			Assessment:        "supports",
			OccurredAt:        time.Now().UTC(),
		})
	}

	summary := ReconcileClaim(claim, scope, observations, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Weak" {
		t.Fatalf("10 repeated shallow same-oracle observations must remain Weak, got %s", summary.Classification)
	}
	if summary.IndependentSourceCount != 1 {
		t.Fatalf("same underlying client oracle should count as 1 independent source, got %d", summary.IndependentSourceCount)
	}
}

func TestReconcileClaimCorroboratedSolid(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	obs1 := model.Observation{
		ID:                "obs-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}

	obs2 := model.Observation{
		ID:                "obs-002",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "activity-log",
		ActorID:           "system",
		Assessment:        "supports",
		VerifierResult:    "database audit confirms access revoked and persisted",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obs1, obs2}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Solid" {
		t.Fatalf("expected Solid when corroborated by depth-3 verifier, got %s", summary.Classification)
	}
	if summary.MaxVerificationDepth != 3 {
		t.Fatalf("expected depth 3, got %d", summary.MaxVerificationDepth)
	}
	if summary.IndependentSourceCount < 2 {
		t.Fatalf("expected at least 2 independent sources, got %d", summary.IndependentSourceCount)
	}
}

func TestReconcileClaimConflicted(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	obs1 := model.Observation{
		ID:                "obs-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}

	obs2 := model.Observation{
		ID:                "obs-002",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "activity-log",
		ActorID:           "system",
		Assessment:        "invalidates",
		VerifierResult:    "database check found participant still has active role",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obs1, obs2}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Conflicted" {
		t.Fatalf("expected Conflicted when evidence contradicts, got %s", summary.Classification)
	}
	if summary.ConflictCount != 1 {
		t.Fatalf("expected 1 conflict, got %d", summary.ConflictCount)
	}
	if summary.HeuristicScore != nil {
		t.Fatalf("conflicted claim must not have hardcoded score, got %v", *summary.HeuristicScore)
	}
	if len(summary.Reasons) == 0 {
		t.Fatalf("expected reasons explaining conflict")
	}
}

func TestReconcileClaimFeatureFlags(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	ctxFlagsTrue := model.EvidenceContext{
		ID:            "ctx-flag-true",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		FeatureFlags:  map[string]string{"camp_grid_v2": "true"},
	}
	obs := model.Observation{
		ID:                "obs-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-flag-true",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}

	// Matching feature flag scope -> Weak
	scopeMatching := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		FeatureFlags:  map[string]string{"camp_grid_v2": "true"},
	}
	sumMatching := ReconcileClaim(claim, scopeMatching, []model.Observation{obs}, map[string]model.EvidenceContext{"ctx-flag-true": ctxFlagsTrue})
	if sumMatching.Classification != "Weak" {
		t.Fatalf("expected Weak for matching feature flags, got %s", sumMatching.Classification)
	}

	// Mismatched feature flag scope -> Untouched
	scopeMismatched := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		FeatureFlags:  map[string]string{"camp_grid_v2": "false"},
	}
	sumMismatched := ReconcileClaim(claim, scopeMismatched, []model.Observation{obs}, map[string]model.EvidenceContext{"ctx-flag-true": ctxFlagsTrue})
	if sumMismatched.Classification != "Untouched" {
		t.Fatalf("expected Untouched for mismatched feature flags, got %s", sumMismatched.Classification)
	}
}

func TestReconcileClaimDeviceCompatibility(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	ctxDesktop := model.EvidenceContext{
		ID:            "ctx-desktop",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		Device:        "desktop",
	}
	obs := model.Observation{
		ID:                "obs-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-desktop",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}

	// Specified device mismatch -> Untouched
	scopeMobile := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		Device:        "mobile",
	}
	sumMobile := ReconcileClaim(claim, scopeMobile, []model.Observation{obs}, map[string]model.EvidenceContext{"ctx-desktop": ctxDesktop})
	if sumMobile.Classification != "Untouched" {
		t.Fatalf("expected Untouched for device mismatch, got %s", sumMobile.Classification)
	}

	// Unspecified optional device in scope -> remains compatible (Weak)
	scopeUnspecified := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		Device:        "",
	}
	sumUnspecified := ReconcileClaim(claim, scopeUnspecified, []model.Observation{obs}, map[string]model.EvidenceContext{"ctx-desktop": ctxDesktop})
	if sumUnspecified.Classification != "Weak" {
		t.Fatalf("expected Weak for unspecified optional device, got %s", sumUnspecified.Classification)
	}
}

func TestReconcileClaimRegionCompatibility(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	ctxUSEast := model.EvidenceContext{
		ID:            "ctx-us-east",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		Region:        "us-east-1",
	}
	obs := model.Observation{
		ID:                "obs-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-us-east",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}

	// Specified region mismatch -> Untouched
	scopeEU := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		Region:        "eu-west-1",
	}
	sumEU := ReconcileClaim(claim, scopeEU, []model.Observation{obs}, map[string]model.EvidenceContext{"ctx-us-east": ctxUSEast})
	if sumEU.Classification != "Untouched" {
		t.Fatalf("expected Untouched for region mismatch, got %s", sumEU.Classification)
	}

	// Unspecified optional region in scope -> remains compatible (Weak)
	scopeUnspecified := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
		Region:        "",
	}
	sumUnspecified := ReconcileClaim(claim, scopeUnspecified, []model.Observation{obs}, map[string]model.EvidenceContext{"ctx-us-east": ctxUSEast})
	if sumUnspecified.Classification != "Weak" {
		t.Fatalf("expected Weak for unspecified optional region, got %s", sumUnspecified.Classification)
	}
}

func TestReconcileClaimStaleOlderBuild(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-002",
		BuildID:       "build-002",
		EnvironmentID: "env-001",
	}
	ctxOld := model.EvidenceContext{
		ID:            "ctx-old",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001", // Older build
		EnvironmentID: "env-001",
	}
	obsOld := model.Observation{
		ID:                "obs-old",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-old",
		SourceType:        "automation",
		SourceAdapter:     "activity-log",
		ActorID:           "system",
		Assessment:        "supports",
		VerifierResult:    "database persistence confirmed",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsOld}, map[string]model.EvidenceContext{"ctx-old": ctxOld})
	if summary.Classification != "Stale" {
		t.Fatalf("expected Stale for older build evidence, got %s", summary.Classification)
	}
	if summary.Freshness != "stale" {
		t.Fatalf("expected freshness stale, got %s", summary.Freshness)
	}
}

func TestReconcileClaimExplicitBlocked(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	obsBlocked := model.Observation{
		ID:                "obs-blocked",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           "claim-001",
		EvidenceContextID: "ctx-001",
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "agent-c",
		Assessment:        "blocked",
		VerifierResult:    "unhandled modal overlay prevented navigation to target route",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsBlocked}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Blocked" {
		t.Fatalf("expected Blocked, got %s", summary.Classification)
	}
}

func TestReconcileClaimExpectedDatabaseDenialSupportsPermissionClaim(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-perm-001",
		TenantID:         "tenant-a",
		Title:            "Non-owner member cannot pack claimed canopy",
		Target:           "canopy_supply_item",
		StateSignature:   "denied_mutation_preserved",
		ActorContext:     "viewer",
		ValidationIntent: "verify_permission_denial",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	// Expected database denial is SUPPORTING evidence for a permission denial claim
	obsPermissionSupport := model.Observation{
		ID:                "obs-perm-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "database",
		ActorID:           "system",
		Assessment:        "supports",
		VerifierResult:    "database mutation denied_mutation as expected for non-owner viewer",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsPermissionSupport}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification == "Blocked" {
		t.Fatalf("expected Solid or Weak for supported permission denial claim, got Blocked")
	}
	if summary.Classification != "Solid" {
		t.Fatalf("expected Solid for depth-3 supported permission denial, got %s", summary.Classification)
	}
	if summary.MaxVerificationDepth != 3 {
		t.Fatalf("expected depth 3, got %d", summary.MaxVerificationDepth)
	}
}

func TestReconcileClaimGenuinelyUnexecutableReturnsBlocked(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	obsUnexecutable := model.Observation{
		ID:                "obs-unexec-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "agent-tester",
		Assessment:        "blocked",
		VerifierResult:    "browser crashed before step 3: execution halted",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsUnexecutable}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Blocked" {
		t.Fatalf("expected Blocked for unexecutable validation, got %s", summary.Classification)
	}
	if summary.HeuristicScore == nil || *summary.HeuristicScore != 0.0 {
		t.Fatalf("expected 0.0 score for Blocked, got %v", summary.HeuristicScore)
	}
}

func TestReconcileClaimSingleDepth3SufficiencyRule(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	// 1. One depth-3 supporting observation satisfies sufficiency rule -> Solid
	obsSupporting := model.Observation{
		ID:                "obs-d3-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "database",
		ActorID:           "system",
		Assessment:        "supports",
		VerifierResult:    "database persistence check confirms state signature revoked",
		OccurredAt:        time.Now().UTC(),
	}
	sumSolid := ReconcileClaim(claim, scope, []model.Observation{obsSupporting}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if sumSolid.Classification != "Solid" {
		t.Fatalf("expected Solid for depth-3 supporting observation, got %s", sumSolid.Classification)
	}

	// 2. One depth-3 observation with assessment='weakens' does NOT satisfy sufficiency rule -> Weak
	obsWeak := model.Observation{
		ID:                "obs-d3-weak",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "database",
		ActorID:           "system",
		Assessment:        "weakens",
		VerifierResult:    "database check observed transient replication delay",
		OccurredAt:        time.Now().UTC(),
	}
	sumWeak := ReconcileClaim(claim, scope, []model.Observation{obsWeak}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if sumWeak.Classification == "Solid" {
		t.Fatalf("expected depth-3 non-supporting observation to NOT be Solid")
	}
	if sumWeak.Classification != "Weak" {
		t.Fatalf("expected Weak, got %s", sumWeak.Classification)
	}
}

func TestReconcileClaimWeakensAloneDoesNotMeanConflicted(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	obsSupport := model.Observation{
		ID:                "obs-sup-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}
	obsWeaken := model.Observation{
		ID:                "obs-weak-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-b",
		Assessment:        "weakens",
		VerifierResult:    "transient UI flicker observed during revocation",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsSupport, obsWeaken}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification == "Conflicted" {
		t.Fatalf("weakens observation must not trigger Conflicted status")
	}
	if summary.Classification != "Weak" {
		t.Fatalf("expected Weak when evidence is weakened, got %s", summary.Classification)
	}
	if summary.HeuristicScore == nil || *summary.HeuristicScore != 0.35 {
		t.Fatalf("expected 0.35 heuristic score for weakened evidence, got %v", summary.HeuristicScore)
	}
}

func TestReconcileClaimSupportPlusCredibleConflictProducesConflicted(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	obsSupport := model.Observation{
		ID:                "obs-sup-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}
	obsConflict := model.Observation{
		ID:                "obs-conf-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-b",
		Assessment:        "conflicts",
		VerifierResult:    "observed access badge still visible and interactive",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsSupport, obsConflict}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification != "Conflicted" {
		t.Fatalf("expected Conflicted for support + conflict, got %s", summary.Classification)
	}
	if summary.HeuristicScore != nil {
		t.Fatalf("expected nil heuristic score for Conflicted, got %v", *summary.HeuristicScore)
	}
	if summary.ConflictCount != 1 {
		t.Fatalf("expected conflict count 1, got %d", summary.ConflictCount)
	}
}

func TestReconcileClaimInvalidationPreventsSolid(t *testing.T) {
	claim := model.ClaimDefinition{
		ID:               "claim-001",
		TenantID:         "tenant-a",
		Title:            "Access revoked after removal",
		Target:           "canopy_supply_item",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	scope := model.Scope{
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}
	ctx1 := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      "tenant-a",
		RunID:         "run-001",
		BuildID:       "build-001",
		EnvironmentID: "env-001",
	}

	// Prior depth-3 support
	obsSupport := model.Observation{
		ID:                "obs-sup-d3",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "database",
		ActorID:           "system",
		Assessment:        "supports",
		VerifierResult:    "database initially confirmed revoked",
		OccurredAt:        time.Now().UTC().Add(-time.Minute),
	}
	// Invalidation observation
	obsInvalidate := model.Observation{
		ID:                "obs-inval-001",
		TenantID:          "tenant-a",
		RunID:             "run-001",
		ClaimID:           claim.ID,
		EvidenceContextID: "ctx-001",
		SourceType:        "automation",
		SourceAdapter:     "database",
		ActorID:           "system",
		Assessment:        "invalidates",
		VerifierResult:    "subsequent audit found state reverted back to active",
		OccurredAt:        time.Now().UTC(),
	}

	summary := ReconcileClaim(claim, scope, []model.Observation{obsSupport, obsInvalidate}, map[string]model.EvidenceContext{"ctx-001": ctx1})
	if summary.Classification == "Solid" {
		t.Fatalf("invalidation must prevent Solid status")
	}
	if summary.Classification != "Conflicted" {
		t.Fatalf("expected Conflicted when evidence is invalidated, got %s", summary.Classification)
	}
	if summary.ConflictCount != 1 {
		t.Fatalf("expected conflict count 1, got %d", summary.ConflictCount)
	}
}

func TestReconcileM6HeterogeneousVerifierWeakToSolidAndConflict(t *testing.T) {
	now := time.Now().UTC()
	claim := model.ClaimDefinition{
		ID:               "FEST-CLAIM-04",
		TenantID:         "festival-thesis-demo",
		Title:            "Packed canopy persists after refresh",
		Description:      "A packed canopy remains packed after refresh/requery when authoritative state is current.",
		Target:           "Supply List / canopy persistence",
		StateSignature:   "packed before reload -> packed after reload",
		ActorContext:     "permitted trip member",
		ValidationIntent: "DataPersistenceOnRefresh",
	}

	scope := model.Scope{
		RunID:         "run-m6-001",
		TenantID:      "festival-thesis-demo",
		BuildID:       "festnest-demo-001",
		EnvironmentID: "festnest-local-browser",
		ScenarioID:    "equipment-handoff-v1",
	}

	ctx1 := model.EvidenceContext{
		ID:            "ctx-m6-001",
		TenantID:      "festival-thesis-demo",
		RunID:         "run-m6-001",
		BuildID:       "festnest-demo-001",
		EnvironmentID: "festnest-local-browser",
		ScenarioID:    "equipment-handoff-v1",
		CreatedAt:     now.Add(-2 * time.Minute),
	}

	// 1. Shallow Human Observation (Depth 1) -> Weak
	obsHuman := model.Observation{
		ID:                "obs-m6-human",
		TenantID:          "festival-thesis-demo",
		RunID:             "run-m6-001",
		ClaimID:           "FEST-CLAIM-04",
		EvidenceContextID: "ctx-m6-001",
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "late-tester-d",
		Assessment:        "supports",
		OccurredAt:        now.Add(-2 * time.Minute),
	}

	summaryWeak := ReconcileClaim(claim, scope, []model.Observation{obsHuman}, map[string]model.EvidenceContext{"ctx-m6-001": ctx1})
	if summaryWeak.Classification != "Weak" {
		t.Fatalf("expected Weak for depth-1 human observation alone, got %s", summaryWeak.Classification)
	}
	if summaryWeak.MaxVerificationDepth != 1 {
		t.Fatalf("expected max depth 1, got %d", summaryWeak.MaxVerificationDepth)
	}
	if summaryWeak.IndependentSourceCount != 1 {
		t.Fatalf("expected 1 independent source, got %d", summaryWeak.IndependentSourceCount)
	}
	if summaryWeak.ConflictCount != 0 {
		t.Fatalf("expected 0 conflicts, got %d", summaryWeak.ConflictCount)
	}

	// 2. Corroborating Deeper Independent Verifier Observation (Depth 3) -> Solid
	obsVerifier := model.Observation{
		ID:                "obs-m6-verifier",
		TenantID:          "festival-thesis-demo",
		RunID:             "run-m6-001",
		ClaimID:           "FEST-CLAIM-04",
		EvidenceContextID: "ctx-m6-001",
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "playwright-agent-c",
		Assessment:        "supports",
		VerifierResult:    "persisted_authoritative_state_verified",
		OccurredAt:        now.Add(-1 * time.Minute),
	}

	summarySolid := ReconcileClaim(claim, scope, []model.Observation{obsHuman, obsVerifier}, map[string]model.EvidenceContext{"ctx-m6-001": ctx1})
	if summarySolid.Classification != "Solid" {
		t.Fatalf("expected Solid when corroborated by depth-3 verifier, got %s", summarySolid.Classification)
	}
	if summarySolid.MaxVerificationDepth != 3 {
		t.Fatalf("expected max depth 3, got %d", summarySolid.MaxVerificationDepth)
	}
	if summarySolid.IndependentSourceCount != 2 {
		t.Fatalf("expected 2 independent sources, got %d", summarySolid.IndependentSourceCount)
	}
	if summarySolid.HeuristicScore == nil || *summarySolid.HeuristicScore < 0.8 {
		t.Fatalf("expected high heuristic score for Solid claim, got %v", summarySolid.HeuristicScore)
	}

	// 3. Contradictory Deeper Observation -> Conflicted
	obsConflict := model.Observation{
		ID:                "obs-m6-conflict",
		TenantID:          "festival-thesis-demo",
		RunID:             "run-m6-001",
		ClaimID:           "FEST-CLAIM-04",
		EvidenceContextID: "ctx-m6-001",
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "playwright-agent-c",
		Assessment:        "conflicts",
		VerifierResult:    "persisted_state_mismatch_item_reverted_to_unassigned",
		OccurredAt:        now.Add(-30 * time.Second),
	}

	summaryConflicted := ReconcileClaim(claim, scope, []model.Observation{obsHuman, obsConflict}, map[string]model.EvidenceContext{"ctx-m6-001": ctx1})
	if summaryConflicted.Classification != "Conflicted" {
		t.Fatalf("expected Conflicted for disagreement between human and verifier, got %s", summaryConflicted.Classification)
	}
	if summaryConflicted.HeuristicScore != nil {
		t.Fatalf("expected nil heuristic score for Conflicted, got %v", *summaryConflicted.HeuristicScore)
	}
	if summaryConflicted.ConflictCount != 1 {
		t.Fatalf("expected 1 conflict, got %d", summaryConflicted.ConflictCount)
	}
}
