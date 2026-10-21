package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/routing"
)

func validateRecommendationFeedback(memory *Memory, recommendation model.Recommendation, response model.RecommendationResponse) error {
	response.Response = strings.TrimSpace(response.Response)
	response.Reason = strings.TrimSpace(response.Reason)
	response.ActorID = strings.TrimSpace(response.ActorID)
	response.OverrideClaimID = strings.TrimSpace(response.OverrideClaimID)

	if response.Reason == "" || response.ActorID == "" {
		return ErrContractInvalid
	}
	if response.ActorID != recommendation.ActorID {
		return ErrRecommendationActorMismatch
	}

	switch response.Response {
	case "accepted", "dismissed":
		if response.OverrideClaimID != "" {
			return ErrRecommendationOverrideInvalid
		}
		return nil
	case "override":
		if response.OverrideClaimID == "" || response.OverrideClaimID == recommendation.ClaimID {
			return ErrRecommendationOverrideInvalid
		}
		if !recommendationHasAlternative(recommendation, response.OverrideClaimID) {
			return ErrRecommendationOverrideInvalid
		}
		if memory.routingPolicy.Version != recommendation.PolicyVersion {
			return ErrRecommendationOverrideInvalid
		}
		scope := Scope{
			RunID: recommendation.RunID, TenantID: recommendation.TenantID,
			BuildID: recommendation.BuildID, EnvironmentID: recommendation.EnvironmentID,
			ScenarioID: recommendation.ScenarioID, Device: recommendation.Device,
			Region: recommendation.Region, FeatureFlags: cloneStringMap(recommendation.FeatureFlags),
		}
		for _, summary := range memory.claimEvidenceSummaries(scope) {
			if summary.ClaimID != response.OverrideClaimID {
				continue
			}
			_, eligible, err := routing.Evaluate(summary, recommendation.Role, memory.routingPolicy)
			if err == nil && eligible {
				return nil
			}
			return ErrRecommendationOverrideInvalid
		}
		return ErrRecommendationOverrideInvalid
	default:
		return ErrContractInvalid
	}
}

func sameRecommendationFeedback(existing model.RecommendationResponseRecord, response model.RecommendationResponse) bool {
	return existing.Response == strings.TrimSpace(response.Response) &&
		existing.Reason == strings.TrimSpace(response.Reason) &&
		existing.ActorID == strings.TrimSpace(response.ActorID) &&
		existing.OverrideClaimID == strings.TrimSpace(response.OverrideClaimID)
}

func newRecommendationFeedbackRecord(recommendation model.Recommendation, response model.RecommendationResponse, createdAt time.Time) model.RecommendationResponseRecord {
	return model.RecommendationResponseRecord{
		ID:    stableID("response", recommendation.RunID+"|"+recommendation.ID+"|"+response.ActorID+"|"+createdAt.UTC().Format(time.RFC3339Nano)),
		RunID: recommendation.RunID, RecommendationID: recommendation.ID,
		Response: strings.TrimSpace(response.Response), Reason: strings.TrimSpace(response.Reason),
		ActorID: strings.TrimSpace(response.ActorID), OverrideClaimID: strings.TrimSpace(response.OverrideClaimID),
		CreatedAt: createdAt.UTC(),
	}
}

func (memory *Memory) SaveRecommendationFeedback(_ context.Context, id string, response model.RecommendationResponse) (bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	recommendation, exists := memory.recommendations[id]
	if !exists {
		return false, ErrNotFound
	}
	if _, err := memory.writableRun(recommendation.RunID); err != nil {
		return false, err
	}
	if existing, ok := memory.responses[id]; ok {
		if sameRecommendationFeedback(existing, response) {
			return false, nil
		}
		return false, ErrResponseConflict
	}
	if err := validateRecommendationFeedback(memory, recommendation, response); err != nil {
		return false, err
	}
	memory.responses[id] = newRecommendationFeedbackRecord(recommendation, response, time.Now().UTC())
	return true, nil
}

func (store *Postgres) SaveRecommendationFeedback(ctx context.Context, id string, response model.RecommendationResponse) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	recommendation, exists := store.memory.recommendations[id]
	if !exists {
		return false, ErrNotFound
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := lockWritableRunTx(ctx, tx, recommendation.RunID); err != nil {
		return false, err
	}

	var existing model.RecommendationResponseRecord
	err = tx.QueryRowContext(ctx, `
		select id,run_id,recommendation_id,response,reason,actor_id,coalesce(override_claim_id,''),created_at
		from recommendation_responses
		where run_id=$1 and recommendation_id=$2`, recommendation.RunID, id).
		Scan(&existing.ID, &existing.RunID, &existing.RecommendationID, &existing.Response, &existing.Reason, &existing.ActorID, &existing.OverrideClaimID, &existing.CreatedAt)
	if err == nil {
		if sameRecommendationFeedback(existing, response) {
			store.memory.responses[id] = existing
			return false, nil
		}
		return false, ErrResponseConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	var createdAt time.Time
	if err := tx.QueryRowContext(ctx, `select transaction_timestamp()`).Scan(&createdAt); err != nil {
		return false, err
	}
	record := newRecommendationFeedbackRecord(recommendation, response, createdAt)
	_, err = tx.ExecContext(ctx, `
		insert into recommendation_responses
		(id,run_id,recommendation_id,response,reason,actor_id,override_claim_id,created_at)
		values ($1,$2,$3,$4,$5,$6,nullif($7,''),$8)`,
		record.ID, record.RunID, record.RecommendationID, record.Response, record.Reason,
		record.ActorID, record.OverrideClaimID, record.CreatedAt)
	if err != nil {
		return false, mapRunError(err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	store.memory.responses[id] = record
	return true, nil
}
