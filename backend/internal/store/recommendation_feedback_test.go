package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func feedbackRecommendation(t *testing.T, role string, claimIDs ...string) (*Memory, model.Recommendation) {
	t.Helper()
	memory, scope := recommendationMemory(t)
	for _, claimID := range claimIDs {
		saveRoutingClaim(t, memory, claimID)
	}
	recommendation, err := memory.NextRecommendation(context.Background(), RecommendationQuery{
		Scope: scope, ActorID: "late-tester", Role: role,
	})
	if err != nil {
		t.Fatal(err)
	}
	return memory, recommendation
}

func queryForRecommendation(recommendation model.Recommendation) RecommendationQuery {
	return RecommendationQuery{
		Scope: Scope{
			RunID: recommendation.RunID, TenantID: recommendation.TenantID,
			BuildID: recommendation.BuildID, EnvironmentID: recommendation.EnvironmentID,
			ScenarioID: recommendation.ScenarioID, Device: recommendation.Device,
			Region: recommendation.Region, FeatureFlags: cloneStringMap(recommendation.FeatureFlags),
		},
		ActorID: recommendation.ActorID, Role: recommendation.Role,
	}
}

func TestMemoryRecommendationProgressionAdvancesAfterAcceptAndDismiss(t *testing.T) {
	for _, responseType := range []string{"accepted", "dismissed"} {
		t.Run(responseType, func(t *testing.T) {
			memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b", "claim-c")
			if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, model.RecommendationResponse{
				Response: responseType, Reason: "advance this recommendation progression", ActorID: first.ActorID,
			}); err != nil {
				t.Fatal(err)
			}
			query := queryForRecommendation(first)
			next, err := memory.NextRecommendation(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			if next.ID == first.ID || next.ClaimID == first.ClaimID {
				t.Fatalf("responded recommendation was re-served: first=%+v next=%+v", first, next)
			}
			if next.EvidenceSnapshot != first.EvidenceSnapshot || next.ProgressionSnapshot == first.ProgressionSnapshot {
				t.Fatalf("feedback must advance progression without relabeling evidence: first=%+v next=%+v", first, next)
			}
			retry, err := memory.NextRecommendation(context.Background(), query)
			if err != nil || retry.ID != next.ID || retry.ProgressionSnapshot != next.ProgressionSnapshot {
				t.Fatalf("unchanged evidence and progression must replay the same next step: retry=%+v err=%v", retry, err)
			}
		})
	}
}

func TestMemoryRecommendationOverridePromotesAlternativeAsNewImmutableStep(t *testing.T) {
	memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b", "claim-c")
	if first.ClaimID != "claim-a" {
		t.Fatalf("test requires lexical first claim, got %s", first.ClaimID)
	}
	feedback := model.RecommendationResponse{
		Response: "override", Reason: "claim c is the explicit human routing decision",
		ActorID: first.ActorID, OverrideClaimID: "claim-c",
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, feedback); err != nil {
		t.Fatal(err)
	}
	next, err := memory.NextRecommendation(context.Background(), queryForRecommendation(first))
	if err != nil {
		t.Fatal(err)
	}
	response := memory.responses[first.ID]
	if next.ClaimID != "claim-c" || next.PromotedByResponseID != response.ID {
		t.Fatalf("override target was not promoted with its audit link: response=%+v next=%+v", response, next)
	}
	if next.ID == first.ID || next.EvidenceSnapshot != first.EvidenceSnapshot || next.ProgressionSnapshot == first.ProgressionSnapshot {
		t.Fatalf("override must create a new progression step over unchanged evidence: first=%+v next=%+v", first, next)
	}
}

func TestMemoryOverrideFallsBackToFreshRankingWhenEvidenceChanges(t *testing.T) {
	memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b", "claim-c")
	if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, model.RecommendationResponse{
		Response: "override", Reason: "claim c before its evidence changed",
		ActorID: first.ActorID, OverrideClaimID: "claim-c",
	}); err != nil {
		t.Fatal(err)
	}
	query := queryForRecommendation(first)
	contextRecord := model.EvidenceContext{
		ID: "ctx-override-invalidated", TenantID: first.TenantID, RunID: first.RunID,
		BuildID: first.BuildID, EnvironmentID: first.EnvironmentID, ScenarioID: first.ScenarioID,
	}
	if _, _, err := memory.SaveEvidenceContext(context.Background(), contextRecord); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.SaveObservation(context.Background(), model.Observation{
		ID: "obs-override-invalidated", TenantID: first.TenantID, RunID: first.RunID,
		ClaimID: "claim-c", EvidenceContextID: contextRecord.ID,
		SourceType: "agent", SourceAdapter: "playwright", ActorID: "agent-a",
		Assessment: "blocked", VerifierResult: "fixture unavailable",
		OccurredAt: time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	next, err := memory.NextRecommendation(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if next.ClaimID == "claim-c" || next.PromotedByResponseID != "" || next.EvidenceSnapshot == first.EvidenceSnapshot {
		t.Fatalf("changed evidence must start a fresh progression and rerank instead of silently promoting an invalid target: first=%+v next=%+v", first, next)
	}
}

func TestMemoryProgressionSnapshotIsDeterministic(t *testing.T) {
	memory, recommendation := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	query := queryForRecommendation(recommendation)
	beforeOne, err := memory.recommendationProgression(query, recommendation.EvidenceSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	beforeTwo, err := memory.recommendationProgression(query, recommendation.EvidenceSnapshot)
	if err != nil || beforeOne.Snapshot != beforeTwo.Snapshot || beforeOne.Snapshot != recommendation.ProgressionSnapshot {
		t.Fatalf("initial progression snapshot is not deterministic: first=%+v second=%+v recommendation=%+v err=%v", beforeOne, beforeTwo, recommendation, err)
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, model.RecommendationResponse{
		Response: "accepted", Reason: "record deterministic feedback", ActorID: recommendation.ActorID,
	}); err != nil {
		t.Fatal(err)
	}
	afterOne, err := memory.recommendationProgression(query, recommendation.EvidenceSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	afterTwo, err := memory.recommendationProgression(query, recommendation.EvidenceSnapshot)
	if err != nil || afterOne.Snapshot != afterTwo.Snapshot || afterOne.Snapshot == beforeOne.Snapshot {
		t.Fatalf("responded progression snapshot is not deterministic: before=%+v first=%+v second=%+v err=%v", beforeOne, afterOne, afterTwo, err)
	}
}

func TestMemoryRecommendationAlternativesAreBackendEligible(t *testing.T) {
	memory, recommendation := feedbackRecommendation(t, "editor", "FEST-CLAIM-04", "FEST-CLAIM-05", "FEST-CLAIM-08")
	if recommendation.ClaimID != "FEST-CLAIM-04" {
		t.Fatalf("unexpected selected claim: %+v", recommendation)
	}
	if len(recommendation.OverrideAlternatives) != 1 || recommendation.OverrideAlternatives[0].ClaimID != "FEST-CLAIM-08" {
		t.Fatalf("alternatives must contain only backend-eligible claims: %+v", recommendation.OverrideAlternatives)
	}
	if recommendation.OverrideAlternatives[0].Title == "" || recommendation.OverrideAlternatives[0].Area == "" {
		t.Fatalf("alternative payload is incomplete: %+v", recommendation.OverrideAlternatives[0])
	}
	_ = memory
}

func TestMemoryRecommendationFeedbackAcceptAndDismissRemainIdempotent(t *testing.T) {
	for _, responseType := range []string{"accepted", "dismissed"} {
		t.Run(responseType, func(t *testing.T) {
			memory, recommendation := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
			response := model.RecommendationResponse{
				Response: responseType, Reason: "human judgment recorded", ActorID: recommendation.ActorID,
			}
			created, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, response)
			if err != nil || !created {
				t.Fatalf("first feedback failed: created=%v err=%v", created, err)
			}
			created, err = memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, response)
			if err != nil || created {
				t.Fatalf("exact retry must be idempotent: created=%v err=%v", created, err)
			}
			if record := memory.responses[recommendation.ID]; record.Response != responseType || record.OverrideClaimID != "" {
				t.Fatalf("unexpected feedback record: %+v", record)
			}
		})
	}
}

func TestMemoryRecommendationOverridePersistsAlternativeClaimAndIsIdempotent(t *testing.T) {
	memory, recommendation := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	overrideClaimID := "claim-b"
	if recommendation.ClaimID == overrideClaimID {
		overrideClaimID = "claim-a"
	}
	response := model.RecommendationResponse{
		Response: "override", Reason: "the alternate uncertainty is more useful now",
		ActorID: recommendation.ActorID, OverrideClaimID: overrideClaimID,
	}
	created, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, response)
	if err != nil || !created {
		t.Fatalf("override failed: created=%v err=%v", created, err)
	}
	record := memory.responses[recommendation.ID]
	if record.Response != "override" || record.OverrideClaimID != overrideClaimID || record.Reason != response.Reason {
		t.Fatalf("override target was not persisted: %+v", record)
	}
	created, err = memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, response)
	if err != nil || created {
		t.Fatalf("exact override retry must be idempotent: created=%v err=%v", created, err)
	}
}

func TestMemoryRecommendationOverrideFailsClosedForInvalidReplacement(t *testing.T) {
	memory, recommendation := feedbackRecommendation(t, "editor", "claim-a", "claim-b")

	cases := []model.RecommendationResponse{
		{Response: "override", Reason: "missing replacement", ActorID: recommendation.ActorID},
		{Response: "override", Reason: "same claim", ActorID: recommendation.ActorID, OverrideClaimID: recommendation.ClaimID},
		{Response: "override", Reason: "unknown claim", ActorID: recommendation.ActorID, OverrideClaimID: "missing-claim"},
		{Response: "accepted", Reason: "accepted cannot carry override", ActorID: recommendation.ActorID, OverrideClaimID: "claim-b"},
	}
	for _, response := range cases {
		if _, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, response); !errors.Is(err, ErrRecommendationOverrideInvalid) {
			t.Fatalf("expected invalid override for %+v, got %v", response, err)
		}
	}

	if _, _, err := memory.SaveClaimDefinition(context.Background(), model.ClaimDefinition{
		ID: "other-tenant-claim", TenantID: "other-tenant", Title: "other tenant",
		Target: "other", StateSignature: "before -> after", ActorContext: "editor", ValidationIntent: "TransitionSucceeds",
	}); err != nil {
		t.Fatal(err)
	}
	crossTenant := model.RecommendationResponse{
		Response: "override", Reason: "cross tenant", ActorID: recommendation.ActorID, OverrideClaimID: "other-tenant-claim",
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, crossTenant); !errors.Is(err, ErrRecommendationOverrideInvalid) {
		t.Fatalf("cross-tenant override must fail closed: %v", err)
	}
}

func TestMemoryRecommendationOverrideRespectsRoleFeasibility(t *testing.T) {
	memory, recommendation := feedbackRecommendation(t, "editor", "FEST-CLAIM-04", "FEST-CLAIM-05")
	if recommendation.ClaimID != "FEST-CLAIM-04" {
		t.Fatalf("expected editor recommendation FEST-CLAIM-04; got %s", recommendation.ClaimID)
	}
	response := model.RecommendationResponse{
		Response: "override", Reason: "try viewer-only delete authorization",
		ActorID: recommendation.ActorID, OverrideClaimID: "FEST-CLAIM-05",
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, response); !errors.Is(err, ErrRecommendationOverrideInvalid) {
		t.Fatalf("role-infeasible override must fail closed: %v", err)
	}
}

func TestMemoryRecommendationFeedbackRejectsActorMismatchAndConflictingRetry(t *testing.T) {
	memory, recommendation := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	if _, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, model.RecommendationResponse{
		Response: "accepted", Reason: "forged actor", ActorID: "different-actor",
	}); !errors.Is(err, ErrRecommendationActorMismatch) {
		t.Fatalf("expected actor mismatch, got %v", err)
	}

	first := model.RecommendationResponse{Response: "accepted", Reason: "first decision", ActorID: recommendation.ActorID}
	if created, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, first); err != nil || !created {
		t.Fatalf("first feedback failed: created=%v err=%v", created, err)
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), recommendation.ID, model.RecommendationResponse{
		Response: "dismissed", Reason: "changed decision", ActorID: recommendation.ActorID,
	}); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("different retry must conflict, got %v", err)
	}
}

func TestMemoryOverrideExactRetryAfterEvidenceDrift(t *testing.T) {
	memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	override := model.RecommendationResponse{
		Response: "override", Reason: "override to claim-b",
		ActorID: first.ActorID, OverrideClaimID: "claim-b",
	}
	created, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, override)
	if err != nil || !created {
		t.Fatalf("initial override failed: created=%v err=%v", created, err)
	}

	contextRecord := model.EvidenceContext{
		ID: "ctx-drift-evidence", TenantID: first.TenantID, RunID: first.RunID,
		BuildID: first.BuildID, EnvironmentID: first.EnvironmentID, ScenarioID: first.ScenarioID,
	}
	if _, _, err := memory.SaveEvidenceContext(context.Background(), contextRecord); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.SaveObservation(context.Background(), model.Observation{
		ID: "obs-drift-blocked", TenantID: first.TenantID, RunID: first.RunID,
		ClaimID: "claim-b", EvidenceContextID: contextRecord.ID,
		SourceType: "agent", SourceAdapter: "playwright", ActorID: "agent-a",
		Assessment: "blocked", VerifierResult: "fixture unavailable",
		OccurredAt: time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	created, err = memory.SaveRecommendationFeedback(context.Background(), first.ID, override)
	if err != nil || created {
		t.Fatalf("exact override retry after evidence drift must be idempotent success: created=%v err=%v", created, err)
	}
}

func TestMemoryOverrideConflictingRetryAfterEvidenceDrift(t *testing.T) {
	memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b", "claim-c")
	override := model.RecommendationResponse{
		Response: "override", Reason: "override to claim-b",
		ActorID: first.ActorID, OverrideClaimID: "claim-b",
	}
	created, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, override)
	if err != nil || !created {
		t.Fatalf("initial override failed: created=%v err=%v", created, err)
	}

	contextRecord := model.EvidenceContext{
		ID: "ctx-drift-conflict", TenantID: first.TenantID, RunID: first.RunID,
		BuildID: first.BuildID, EnvironmentID: first.EnvironmentID, ScenarioID: first.ScenarioID,
	}
	if _, _, err := memory.SaveEvidenceContext(context.Background(), contextRecord); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.SaveObservation(context.Background(), model.Observation{
		ID: "obs-drift-conflict-blocked", TenantID: first.TenantID, RunID: first.RunID,
		ClaimID: "claim-b", EvidenceContextID: contextRecord.ID,
		SourceType: "agent", SourceAdapter: "playwright", ActorID: "agent-a",
		Assessment: "blocked", VerifierResult: "fixture unavailable",
		OccurredAt: time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	conflicting := model.RecommendationResponse{
		Response: "override", Reason: "changed mind to claim-c",
		ActorID: first.ActorID, OverrideClaimID: "claim-c",
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, conflicting); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("conflicting retry after evidence drift must return ErrResponseConflict, got %v", err)
	}

	conflictingAccepted := model.RecommendationResponse{
		Response: "accepted", Reason: "changed mind to accept",
		ActorID: first.ActorID,
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, conflictingAccepted); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("conflicting accepted retry after evidence drift must return ErrResponseConflict, got %v", err)
	}
}

func TestMemoryOverrideExactRetryAfterRoutingPolicyDrift(t *testing.T) {
	memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	override := model.RecommendationResponse{
		Response: "override", Reason: "override under original policy",
		ActorID: first.ActorID, OverrideClaimID: "claim-b",
	}
	created, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, override)
	if err != nil || !created {
		t.Fatalf("initial override failed: created=%v err=%v", created, err)
	}

	memory.routingPolicy.Version = "information-value-v0.2-drift"

	created, err = memory.SaveRecommendationFeedback(context.Background(), first.ID, override)
	if err != nil || created {
		t.Fatalf("exact override retry after policy drift must be idempotent success: created=%v err=%v", created, err)
	}

	conflicting := model.RecommendationResponse{
		Response: "dismissed", Reason: "different under drifted policy",
		ActorID: first.ActorID,
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, conflicting); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("conflicting retry under drifted policy must return ErrResponseConflict, got %v", err)
	}
}

func TestMemoryNewOverrideUnderChangedPolicyOrEvidenceFailsClosed(t *testing.T) {
	memory, first := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	memory.routingPolicy.Version = "information-value-v0.2-drift"
	override := model.RecommendationResponse{
		Response: "override", Reason: "new override under drifted policy",
		ActorID: first.ActorID, OverrideClaimID: "claim-b",
	}
	if _, err := memory.SaveRecommendationFeedback(context.Background(), first.ID, override); !errors.Is(err, ErrRecommendationOverrideInvalid) {
		t.Fatalf("new override under drifted policy must fail closed, got %v", err)
	}

	memory2, first2 := feedbackRecommendation(t, "editor", "claim-a", "claim-b")
	contextRecord := model.EvidenceContext{
		ID: "ctx-drift-new", TenantID: first2.TenantID, RunID: first2.RunID,
		BuildID: first2.BuildID, EnvironmentID: first2.EnvironmentID, ScenarioID: first2.ScenarioID,
	}
	if _, _, err := memory2.SaveEvidenceContext(context.Background(), contextRecord); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory2.SaveObservation(context.Background(), model.Observation{
		ID: "obs-drift-new-blocked", TenantID: first2.TenantID, RunID: first2.RunID,
		ClaimID: "claim-b", EvidenceContextID: contextRecord.ID,
		SourceType: "agent", SourceAdapter: "playwright", ActorID: "agent-a",
		Assessment: "blocked", VerifierResult: "fixture unavailable",
		OccurredAt: time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory2.SaveRecommendationFeedback(context.Background(), first2.ID, override); !errors.Is(err, ErrRecommendationOverrideInvalid) {
		t.Fatalf("new override for ineligible target must fail closed, got %v", err)
	}
}
