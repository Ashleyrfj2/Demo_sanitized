// Package routing ranks claim-level validation opportunities using a transparent,
// deterministic information-value heuristic. It is an allocation aid, not a
// calibrated probability model, autonomous bug oracle, or release gate.
package routing

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

const (
	MinimumExpectedCost = 0.1
	MaximumExpectedCost = 10.0
	MaximumFactorValue  = 1.0
	maximumUsefulDepth  = int(model.DepthCrossSystem)
	defaultSourceTarget = 2
)

var ErrNoEligibleClaims = errors.New("no eligible claims")

// Factors are the five canonical V0 inputs. The first four factors use a
// documented 0..1 scale; business risk, diff impact, and failure prior must be
// non-zero. Expected cost uses a 0.1..10 relative-effort scale.
type Factors struct {
	DeltaUncertainty     float64 `json:"delta_uncertainty"`
	BusinessRisk         float64 `json:"business_risk"`
	DiffImpact           float64 `json:"diff_impact"`
	SmoothedFailurePrior float64 `json:"smoothed_failure_prior"`
	ExpectedCost         float64 `json:"expected_cost"`
}

// ClaimPolicy contains manual V0 inputs that are intentionally separate from
// the generic scoring engine. AllowedRoles is empty when all roles are feasible.
type ClaimPolicy struct {
	Area                     string   `json:"area"`
	BusinessRisk             float64  `json:"business_risk"`
	DiffImpact               float64  `json:"diff_impact"`
	ExpectedCost             float64  `json:"expected_cost"`
	AllowedRoles             []string `json:"allowed_roles"`
	RequiredDepth            int      `json:"required_depth"`
	TargetIndependentSources int      `json:"target_independent_sources"`
	BlockedFeasible          bool     `json:"blocked_feasible"`
}

// Policy is a versioned, manual routing policy. PriorBaseRate and PriorStrength
// define transparent additive smoothing; NoveltyFloor prevents claims with no
// failure history from receiving a zero prior.
type Policy struct {
	Version        string                 `json:"version"`
	NoveltyFloor   float64                `json:"novelty_floor"`
	PriorBaseRate  float64                `json:"prior_base_rate"`
	PriorStrength  float64                `json:"prior_strength"`
	DefaultClaim   ClaimPolicy            `json:"default_claim"`
	ClaimOverrides map[string]ClaimPolicy `json:"claim_overrides"`
}

type RankedClaim struct {
	Summary   model.ClaimEvidenceSummary
	Factors   Factors
	Score     float64
	Area      string
	Rationale string
}

//go:embed policies/information-value-v0.1.json
var activePolicyJSON []byte

var activePolicy = mustLoadPolicy(activePolicyJSON)

func mustLoadPolicy(raw []byte) Policy {
	var policy Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		panic(fmt.Sprintf("decode embedded routing policy: %v", err))
	}
	if err := policy.Validate(); err != nil {
		panic(fmt.Sprintf("invalid embedded routing policy: %v", err))
	}
	return policy
}

func ActivePolicy() Policy {
	raw, _ := json.Marshal(activePolicy)
	var clone Policy
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func boundedUnit(name string, value float64, allowZero bool) error {
	if !finite(value) || value > MaximumFactorValue || value < 0 || !allowZero && value == 0 {
		bounds := "(0,1]"
		if allowZero {
			bounds = "0..1"
		}
		return fmt.Errorf("%s must be finite and within %s", name, bounds)
	}
	return nil
}

func (f Factors) Validate() error {
	if err := boundedUnit("delta_uncertainty", f.DeltaUncertainty, true); err != nil {
		return err
	}
	if err := boundedUnit("business_risk", f.BusinessRisk, false); err != nil {
		return err
	}
	if err := boundedUnit("diff_impact", f.DiffImpact, false); err != nil {
		return err
	}
	if err := boundedUnit("smoothed_failure_prior", f.SmoothedFailurePrior, false); err != nil {
		return err
	}
	if !finite(f.ExpectedCost) || f.ExpectedCost < MinimumExpectedCost || f.ExpectedCost > MaximumExpectedCost {
		return fmt.Errorf("expected_cost must be finite and within %.1f..%.1f", MinimumExpectedCost, MaximumExpectedCost)
	}
	return nil
}

// Score implements the canonical M5A multiplicative formula.
func Score(f Factors) (float64, error) {
	if err := f.Validate(); err != nil {
		return 0, err
	}
	score := f.DeltaUncertainty * f.BusinessRisk * f.DiffImpact * f.SmoothedFailurePrior / f.ExpectedCost
	if !finite(score) || score < 0 {
		return 0, errors.New("priority score must be finite and non-negative")
	}
	return score, nil
}

func (p Policy) Validate() error {
	if strings.TrimSpace(p.Version) == "" {
		return errors.New("policy version is required")
	}
	if err := boundedUnit("novelty_floor", p.NoveltyFloor, false); err != nil {
		return err
	}
	if err := boundedUnit("prior_base_rate", p.PriorBaseRate, false); err != nil {
		return err
	}
	if !finite(p.PriorStrength) || p.PriorStrength <= 0 || p.PriorStrength > 100 {
		return errors.New("prior_strength must be finite and within (0,100]")
	}
	if err := validateClaimPolicy("default_claim", p.DefaultClaim); err != nil {
		return err
	}
	for claimID, policy := range p.ClaimOverrides {
		if strings.TrimSpace(claimID) == "" {
			return errors.New("claim override identity is required")
		}
		if err := validateClaimPolicy("claim_overrides."+claimID, policy); err != nil {
			return err
		}
	}
	return nil
}

func validateClaimPolicy(name string, policy ClaimPolicy) error {
	if err := boundedUnit(name+".business_risk", policy.BusinessRisk, false); err != nil {
		return err
	}
	if err := boundedUnit(name+".diff_impact", policy.DiffImpact, false); err != nil {
		return err
	}
	if !finite(policy.ExpectedCost) || policy.ExpectedCost < MinimumExpectedCost || policy.ExpectedCost > MaximumExpectedCost {
		return fmt.Errorf("%s.expected_cost must be finite and within %.1f..%.1f", name, MinimumExpectedCost, MaximumExpectedCost)
	}
	if policy.RequiredDepth < int(model.DepthSurface) || policy.RequiredDepth > maximumUsefulDepth {
		return fmt.Errorf("%s.required_depth must be within 1..4", name)
	}
	if policy.TargetIndependentSources < 1 || policy.TargetIndependentSources > 10 {
		return fmt.Errorf("%s.target_independent_sources must be within 1..10", name)
	}
	for _, role := range policy.AllowedRoles {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("%s.allowed_roles cannot contain an empty role", name)
		}
	}
	return nil
}

func policyForClaim(policy Policy, claimID string) ClaimPolicy {
	if override, ok := policy.ClaimOverrides[claimID]; ok {
		return override
	}
	return policy.DefaultClaim
}

func roleAllowed(policy ClaimPolicy, requestedRole string) bool {
	if len(policy.AllowedRoles) == 0 {
		return true
	}
	for _, allowed := range policy.AllowedRoles {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(requestedRole)) {
			return true
		}
	}
	return false
}

func deltaUncertainty(summary model.ClaimEvidenceSummary, claimPolicy ClaimPolicy) (float64, bool, error) {
	var delta float64
	switch summary.Classification {
	case "Conflicted":
		delta = 1.00
	case "Untouched":
		delta = 0.95
	case "Stale":
		delta = 0.85
	case "Weak":
		delta = 0.75
	case "Blocked":
		if !claimPolicy.BlockedFeasible {
			return 0, false, nil
		}
		delta = 0.80
	case "Solid":
		deeperUseful := summary.MaxVerificationDepth < maximumUsefulDepth
		independentUseful := summary.IndependentSourceCount < max(claimPolicy.TargetIndependentSources, defaultSourceTarget)
		if !deeperUseful && !independentUseful {
			return 0, false, nil
		}
		delta = 0.15
		if deeperUseful {
			delta += 0.05
		}
		if independentUseful {
			delta += 0.05
		}
	default:
		return 0, false, fmt.Errorf("unsupported claim classification %q", summary.Classification)
	}

	if summary.Classification != "Solid" && summary.MaxVerificationDepth < claimPolicy.RequiredDepth {
		delta = math.Min(1, delta+0.05)
	}
	if summary.Classification != "Solid" && summary.IndependentSourceCount < claimPolicy.TargetIndependentSources {
		delta = math.Min(1, delta+0.05)
	}
	return delta, true, nil
}

// SmoothedFailurePrior uses compatible observations as attempts and conflicts as
// failures: (failures + prior_strength*base_rate) / (attempts + prior_strength).
// The novelty floor is then applied so untouched or failure-free claims remain
// eligible. This is a heuristic rate, not a calibrated probability.
func SmoothedFailurePrior(summary model.ClaimEvidenceSummary, policy Policy) float64 {
	attempts := math.Max(0, float64(summary.CompatibleObservationCount))
	failures := math.Max(0, float64(summary.ConflictCount))
	if failures > attempts {
		failures = attempts
	}
	value := (failures + policy.PriorStrength*policy.PriorBaseRate) / (attempts + policy.PriorStrength)
	return math.Min(1, math.Max(policy.NoveltyFloor, value))
}

func Evaluate(summary model.ClaimEvidenceSummary, requestedRole string, policy Policy) (RankedClaim, bool, error) {
	if err := policy.Validate(); err != nil {
		return RankedClaim{}, false, err
	}
	claimPolicy := policyForClaim(policy, summary.ClaimID)
	if !roleAllowed(claimPolicy, requestedRole) {
		return RankedClaim{}, false, nil
	}
	delta, eligible, err := deltaUncertainty(summary, claimPolicy)
	if err != nil || !eligible {
		return RankedClaim{}, eligible, err
	}
	factors := Factors{
		DeltaUncertainty:     delta,
		BusinessRisk:         claimPolicy.BusinessRisk,
		DiffImpact:           claimPolicy.DiffImpact,
		SmoothedFailurePrior: SmoothedFailurePrior(summary, policy),
		ExpectedCost:         claimPolicy.ExpectedCost,
	}
	score, err := Score(factors)
	if err != nil {
		return RankedClaim{}, false, err
	}
	area := strings.TrimSpace(claimPolicy.Area)
	if area == "" {
		area = summary.Target
	}
	rationale := fmt.Sprintf(
		"%s (%s) is %s at depth %d with %d independent source(s) and %d conflict(s). Factors: uncertainty reduction %.2f x business risk %.2f x diff impact %.2f x smoothed failure prior %.2f / expected cost %.2f = priority %.4f under policy %s.",
		area, summary.ClaimID, summary.Classification, summary.MaxVerificationDepth,
		summary.IndependentSourceCount, summary.ConflictCount,
		factors.DeltaUncertainty, factors.BusinessRisk, factors.DiffImpact,
		factors.SmoothedFailurePrior, factors.ExpectedCost, score, policy.Version,
	)
	return RankedClaim{Summary: summary, Factors: factors, Score: score, Area: area, Rationale: rationale}, true, nil
}

// Rank selects the highest-priority eligible claim. Equal scores are broken by
// stable lexical claim identity, independent of map or database row order.
func Rank(summaries []model.ClaimEvidenceSummary, requestedRole string, policy Policy) (RankedClaim, error) {
	var best RankedClaim
	found := false
	for _, summary := range summaries {
		candidate, eligible, err := Evaluate(summary, requestedRole, policy)
		if err != nil {
			return RankedClaim{}, err
		}
		if !eligible {
			continue
		}
		if !found || candidate.Score > best.Score || candidate.Score == best.Score && candidate.Summary.ClaimID < best.Summary.ClaimID {
			best, found = candidate, true
		}
	}
	if !found {
		return RankedClaim{}, ErrNoEligibleClaims
	}
	return best, nil
}

// EvidenceSnapshot returns a deterministic digest of the complete reconciled
// claim-summary set used for one routing decision.
func EvidenceSnapshot(summaries []model.ClaimEvidenceSummary) (string, error) {
	canonical := append([]model.ClaimEvidenceSummary(nil), summaries...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ClaimID < canonical[j].ClaimID })
	for index := range canonical {
		canonical[index].EvidenceContexts = append([]model.EvidenceContext(nil), canonical[index].EvidenceContexts...)
		for contextIndex := range canonical[index].EvidenceContexts {
			// Persistence bookkeeping time is not a reconciliation or routing input.
			canonical[index].EvidenceContexts[contextIndex].CreatedAt = time.Time{}
		}
		sort.Slice(canonical[index].EvidenceContexts, func(i, j int) bool {
			left, right := canonical[index].EvidenceContexts[i], canonical[index].EvidenceContexts[j]
			if left.RunID != right.RunID {
				return left.RunID < right.RunID
			}
			return left.ID < right.ID
		})
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode claim evidence snapshot: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
