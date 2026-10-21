package routing

import (
	"math"
	"strings"
	"testing"

	"github.com/example/qa-platform/backend/internal/model"
)

func testPolicy() Policy {
	return Policy{
		Version:       "test-policy-v1",
		NoveltyFloor:  0.25,
		PriorBaseRate: 0.20,
		PriorStrength: 2,
		DefaultClaim: ClaimPolicy{
			BusinessRisk: 0.5, DiffImpact: 0.5, ExpectedCost: 1,
			RequiredDepth: 3, TargetIndependentSources: 2,
		},
		ClaimOverrides: map[string]ClaimPolicy{},
	}
}

func summary(id, classification string) model.ClaimEvidenceSummary {
	return model.ClaimEvidenceSummary{
		ClaimID: id, TenantID: "tenant-a", Target: "shared item", Classification: classification,
		Freshness: "current", Reasons: []string{"backend-derived reason"},
	}
}

func TestCanonicalMultiplicativeScore(t *testing.T) {
	factors := Factors{
		DeltaUncertainty: 0.8, BusinessRisk: 0.9, DiffImpact: 0.5,
		SmoothedFailurePrior: 0.25, ExpectedCost: 2,
	}
	got, err := Score(factors)
	if err != nil {
		t.Fatal(err)
	}
	want := 0.8 * 0.9 * 0.5 * 0.25 / 2
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("canonical score mismatch: got %.12f want %.12f", got, want)
	}
}

func TestFactorValidationRejectsInvalidCostAndNonFiniteInputs(t *testing.T) {
	valid := Factors{DeltaUncertainty: 0.8, BusinessRisk: 0.9, DiffImpact: 0.5, SmoothedFailurePrior: 0.25, ExpectedCost: 1}
	cases := []struct {
		name   string
		mutate func(*Factors)
	}{
		{"zero cost", func(f *Factors) { f.ExpectedCost = 0 }},
		{"negative cost", func(f *Factors) { f.ExpectedCost = -1 }},
		{"nan cost", func(f *Factors) { f.ExpectedCost = math.NaN() }},
		{"infinite cost", func(f *Factors) { f.ExpectedCost = math.Inf(1) }},
		{"zero risk", func(f *Factors) { f.BusinessRisk = 0 }},
		{"nan uncertainty", func(f *Factors) { f.DeltaUncertainty = math.NaN() }},
		{"factor above scale", func(f *Factors) { f.DiffImpact = 1.01 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			factors := valid
			testCase.mutate(&factors)
			if _, err := Score(factors); err == nil {
				t.Fatal("invalid factors were accepted")
			}
		})
	}
}

func TestNoveltyFloorKeepsUntouchedClaimEligibleWithoutFailureHistory(t *testing.T) {
	policy := testPolicy()
	candidate, eligible, err := Evaluate(summary("claim-new", "Untouched"), "editor", policy)
	if err != nil || !eligible {
		t.Fatalf("untouched claim should remain eligible: eligible=%v err=%v", eligible, err)
	}
	if candidate.Factors.SmoothedFailurePrior != policy.NoveltyFloor {
		t.Fatalf("novelty floor mismatch: got %.2f want %.2f", candidate.Factors.SmoothedFailurePrior, policy.NoveltyFloor)
	}
	if candidate.Score <= 0 {
		t.Fatalf("untouched claim must retain non-zero priority, got %.4f", candidate.Score)
	}
}

func TestRankTieBreaksByStableClaimIdentity(t *testing.T) {
	policy := testPolicy()
	best, err := Rank([]model.ClaimEvidenceSummary{summary("claim-z", "Weak"), summary("claim-a", "Weak")}, "editor", policy)
	if err != nil {
		t.Fatal(err)
	}
	if best.Summary.ClaimID != "claim-a" {
		t.Fatalf("tie must select lexical claim identity, got %s", best.Summary.ClaimID)
	}
}

func TestHighRiskUnresolvedClaimOutranksLowValueOverlap(t *testing.T) {
	policy := testPolicy()
	policy.ClaimOverrides["high-risk"] = ClaimPolicy{BusinessRisk: 1, DiffImpact: 1, ExpectedCost: 1, RequiredDepth: 3, TargetIndependentSources: 2}
	policy.ClaimOverrides["low-overlap"] = ClaimPolicy{BusinessRisk: 0.2, DiffImpact: 0.2, ExpectedCost: 1, RequiredDepth: 3, TargetIndependentSources: 2}
	high := summary("high-risk", "Conflicted")
	high.CompatibleObservationCount, high.ConflictCount = 2, 1
	low := summary("low-overlap", "Solid")
	low.MaxVerificationDepth, low.IndependentSourceCount = 3, 2
	best, err := Rank([]model.ClaimEvidenceSummary{low, high}, "editor", policy)
	if err != nil {
		t.Fatal(err)
	}
	if best.Summary.ClaimID != "high-risk" {
		t.Fatalf("expected high-risk unresolved claim, got %s", best.Summary.ClaimID)
	}
}

func TestClassificationTreatmentAndUsefulSolidVerification(t *testing.T) {
	policy := testPolicy()
	for _, classification := range []string{"Conflicted", "Stale", "Untouched", "Weak"} {
		t.Run(classification, func(t *testing.T) {
			_, eligible, err := Evaluate(summary("claim-"+classification, classification), "editor", policy)
			if err != nil || !eligible {
				t.Fatalf("%s should be eligible: eligible=%v err=%v", classification, eligible, err)
			}
		})
	}

	solid := summary("solid-useful", "Solid")
	solid.MaxVerificationDepth, solid.IndependentSourceCount = 3, 1
	if _, eligible, err := Evaluate(solid, "editor", policy); err != nil || !eligible {
		t.Fatalf("first independent or deeper Solid verification must remain eligible: eligible=%v err=%v", eligible, err)
	}
	solid.MaxVerificationDepth, solid.IndependentSourceCount = 4, 2
	if _, eligible, err := Evaluate(solid, "editor", policy); err != nil || eligible {
		t.Fatalf("fully overlapped Solid claim should be ineligible: eligible=%v err=%v", eligible, err)
	}

	blocked := summary("blocked", "Blocked")
	if _, eligible, err := Evaluate(blocked, "editor", policy); err != nil || eligible {
		t.Fatalf("blocked claim without feasible path must be excluded: eligible=%v err=%v", eligible, err)
	}
	blockedPolicy := policy.DefaultClaim
	blockedPolicy.BlockedFeasible = true
	policy.ClaimOverrides[blocked.ClaimID] = blockedPolicy
	if _, eligible, err := Evaluate(blocked, "editor", policy); err != nil || !eligible {
		t.Fatalf("blocked claim with explicit feasible path should be eligible: eligible=%v err=%v", eligible, err)
	}
}

func TestRoleFeasibilityAndExplainablePolicyVersion(t *testing.T) {
	policy := testPolicy()
	override := policy.DefaultClaim
	override.Area = "Authorization boundary"
	override.AllowedRoles = []string{"viewer"}
	policy.ClaimOverrides["claim-role"] = override

	if _, eligible, err := Evaluate(summary("claim-role", "Untouched"), "editor", policy); err != nil || eligible {
		t.Fatalf("infeasible role was accepted: eligible=%v err=%v", eligible, err)
	}
	candidate, eligible, err := Evaluate(summary("claim-role", "Untouched"), "viewer", policy)
	if err != nil || !eligible {
		t.Fatalf("feasible role was rejected: eligible=%v err=%v", eligible, err)
	}
	if !strings.Contains(candidate.Rationale, policy.Version) || !strings.Contains(candidate.Rationale, "priority") || !strings.Contains(candidate.Rationale, "depth") {
		t.Fatalf("rationale is not factor/evidence specific: %s", candidate.Rationale)
	}
}

func TestEvidenceSnapshotIsOrderIndependentAndEvidenceSensitive(t *testing.T) {
	one := summary("claim-a", "Weak")
	two := summary("claim-b", "Untouched")
	first, err := EvidenceSnapshot([]model.ClaimEvidenceSummary{one, two})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EvidenceSnapshot([]model.ClaimEvidenceSummary{two, one})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("snapshot must be independent of input order: %s != %s", first, second)
	}
	one.CompatibleObservationCount++
	changed, err := EvidenceSnapshot([]model.ClaimEvidenceSummary{one, two})
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("relevant evidence change must change the snapshot")
	}

	oneWithFlags := one
	oneWithFlags.EvidenceContexts = []model.EvidenceContext{{
		ID: "ctx-flagged", RunID: "run-1", TenantID: "tenant-a",
		FeatureFlags: map[string]string{"camp_grid_v2": "true"},
	}}
	flaggedSnapshot, err := EvidenceSnapshot([]model.ClaimEvidenceSummary{oneWithFlags, two})
	if err != nil {
		t.Fatal(err)
	}
	if flaggedSnapshot == changed {
		t.Fatal("feature flags on evidence context must change the snapshot")
	}
}
