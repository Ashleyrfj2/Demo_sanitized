package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/routing"
)

type progressionStep struct {
	RecommendationID string    `json:"recommendation_id"`
	ClaimID          string    `json:"claim_id"`
	ResponseID       string    `json:"response_id"`
	Response         string    `json:"response"`
	Reason           string    `json:"reason"`
	OverrideClaimID  string    `json:"override_claim_id,omitempty"`
	CreatedAt        time.Time `json:"-"`
}

type progressionSnapshotInput struct {
	EvidenceSnapshot         string            `json:"evidence_snapshot"`
	RespondedRecommendations []progressionStep `json:"responded_recommendations"`
}

type recommendationProgression struct {
	Snapshot       string
	ExcludedClaims map[string]bool
	LatestResponse *model.RecommendationResponseRecord
}

func recommendationMatchesQuery(recommendation model.Recommendation, query RecommendationQuery, policyVersion, evidenceSnapshot string) bool {
	return recommendation.RunID == query.RunID &&
		recommendation.TenantID == query.TenantID &&
		recommendation.BuildID == query.BuildID &&
		recommendation.EnvironmentID == query.EnvironmentID &&
		recommendation.ScenarioID == query.ScenarioID &&
		recommendation.Device == query.Device &&
		recommendation.Region == query.Region &&
		canonicalStringMap(recommendation.FeatureFlags) == canonicalStringMap(query.FeatureFlags) &&
		recommendation.ActorID == query.ActorID &&
		recommendation.Role == query.Role &&
		recommendation.PolicyVersion == policyVersion &&
		recommendation.EvidenceSnapshot == evidenceSnapshot
}

func (memory *Memory) recommendationProgression(query RecommendationQuery, evidenceSnapshot string) (recommendationProgression, error) {
	steps := make([]progressionStep, 0)
	responsesByID := map[string]model.RecommendationResponseRecord{}
	excluded := map[string]bool{}
	for _, recommendation := range memory.recommendations {
		if recommendation.ClaimID == "" || !recommendationMatchesQuery(recommendation, query, memory.routingPolicy.Version, evidenceSnapshot) {
			continue
		}
		response, responded := memory.responses[recommendation.ID]
		if !responded {
			continue
		}
		excluded[recommendation.ClaimID] = true
		responsesByID[response.ID] = response
		steps = append(steps, progressionStep{
			RecommendationID: recommendation.ID,
			ClaimID:          recommendation.ClaimID,
			ResponseID:       response.ID,
			Response:         response.Response,
			Reason:           response.Reason,
			OverrideClaimID:  response.OverrideClaimID,
			CreatedAt:        response.CreatedAt.UTC(),
		})
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].CreatedAt.Equal(steps[j].CreatedAt) {
			return steps[i].RecommendationID < steps[j].RecommendationID
		}
		return steps[i].CreatedAt.Before(steps[j].CreatedAt)
	})
	payload := progressionSnapshotInput{
		EvidenceSnapshot:         evidenceSnapshot,
		RespondedRecommendations: steps,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return recommendationProgression{}, err
	}
	digest := sha256.Sum256(raw)
	progression := recommendationProgression{
		Snapshot:       hex.EncodeToString(digest[:]),
		ExcludedClaims: excluded,
	}
	if len(steps) > 0 {
		latest := responsesByID[steps[len(steps)-1].ResponseID]
		progression.LatestResponse = &latest
	}
	return progression, nil
}

func (memory *Memory) eligibleRecommendationCandidates(summaries []model.ClaimEvidenceSummary, role string, excluded map[string]bool) ([]routing.RankedClaim, error) {
	candidates := make([]routing.RankedClaim, 0, len(summaries))
	for _, summary := range summaries {
		if excluded[summary.ClaimID] {
			continue
		}
		candidate, eligible, err := routing.Evaluate(summary, role, memory.routingPolicy)
		if err != nil {
			return nil, err
		}
		if eligible {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Summary.ClaimID < candidates[j].Summary.ClaimID
		}
		return candidates[i].Score > candidates[j].Score
	})
	return candidates, nil
}

func (memory *Memory) recommendationAlternatives(candidates []routing.RankedClaim, selectedClaimID, tenantID string) []model.RecommendationAlternative {
	alternatives := make([]model.RecommendationAlternative, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Summary.ClaimID == selectedClaimID {
			continue
		}
		claim := memory.claims[tenantID+":"+candidate.Summary.ClaimID]
		alternatives = append(alternatives, model.RecommendationAlternative{
			ClaimID:        candidate.Summary.ClaimID,
			Title:          claim.Title,
			Area:           candidate.Area,
			Classification: candidate.Summary.Classification,
		})
	}
	sort.Slice(alternatives, func(i, j int) bool { return alternatives[i].ClaimID < alternatives[j].ClaimID })
	return alternatives
}

func recommendationHasAlternative(recommendation model.Recommendation, claimID string) bool {
	for _, alternative := range recommendation.OverrideAlternatives {
		if alternative.ClaimID == claimID {
			return true
		}
	}
	return false
}
