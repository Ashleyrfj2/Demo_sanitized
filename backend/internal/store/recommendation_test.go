package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func recommendationMemory(t *testing.T) (*Memory, Scope) {
	t.Helper()
	memory := NewMemory()
	ctx := context.Background()
	registration := model.BuildRegistration{TenantID: "tenant-router", EnvironmentID: "local", BuildID: "build-1", SequenceNumber: 1}
	if _, _, err := memory.RegisterBuild(ctx, registration, "controller"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.CreateExperiment(ctx, model.ExperimentRegistration{
		ID: "run-router-1", TenantID: registration.TenantID, BuildID: registration.BuildID,
		EnvironmentID: registration.EnvironmentID, ScenarioID: "scenario-router", Mode: "guided",
	}, "controller"); err != nil {
		t.Fatal(err)
	}
	return memory, Scope{RunID: "run-router-1", TenantID: registration.TenantID, BuildID: registration.BuildID, EnvironmentID: registration.EnvironmentID, ScenarioID: "scenario-router"}
}

func saveRoutingClaim(t *testing.T, memory *Memory, id string) {
	t.Helper()
	if _, _, err := memory.SaveClaimDefinition(context.Background(), model.ClaimDefinition{
		ID: id, TenantID: "tenant-router", Title: id, Target: "shared item " + id,
		StateSignature: "before -> after " + id, ActorContext: "editor", ValidationIntent: "TransitionSucceeds",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryRecommendationIdempotencyEvidenceAndPolicyVersion(t *testing.T) {
	memory, scope := recommendationMemory(t)
	saveRoutingClaim(t, memory, "claim-router-a")
	saveRoutingClaim(t, memory, "claim-router-b")
	query := RecommendationQuery{Scope: scope, ActorID: "late-tester", Role: "editor"}

	first, err := memory.NextRecommendation(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := memory.NextRecommendation(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != retry.ID || !first.CreatedAt.Equal(retry.CreatedAt) || len(memory.recommendations) != 1 {
		t.Fatalf("unchanged retry was not idempotent: first=%s retry=%s rows=%d", first.ID, retry.ID, len(memory.recommendations))
	}
	if first.ClaimID == "" || first.CandidateStateID != "" || first.PolicyVersion == "" || len(first.EvidenceSnapshot) != 64 || len(first.ProgressionSnapshot) != 64 {
		t.Fatalf("recommendation is not claim-level/explainable: %+v", first)
	}

	evidenceContext := model.EvidenceContext{
		ID: "ctx-router-a", TenantID: scope.TenantID, RunID: scope.RunID,
		BuildID: scope.BuildID, EnvironmentID: scope.EnvironmentID, ScenarioID: scope.ScenarioID,
	}
	if _, _, err := memory.SaveEvidenceContext(context.Background(), evidenceContext); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.SaveObservation(context.Background(), model.Observation{
		ID: "obs-router-a", TenantID: scope.TenantID, RunID: scope.RunID,
		ClaimID: first.ClaimID, EvidenceContextID: evidenceContext.ID,
		SourceType: "human", SourceAdapter: "browser", ActorID: "tester-a",
		Assessment: "supports", OccurredAt: time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	afterEvidence, err := memory.NextRecommendation(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if afterEvidence.ID == first.ID || afterEvidence.EvidenceSnapshot == first.EvidenceSnapshot || len(memory.recommendations) != 2 {
		t.Fatalf("new evidence did not produce a new decision: before=%s after=%s rows=%d", first.ID, afterEvidence.ID, len(memory.recommendations))
	}

	memory.routingPolicy.Version = "test-policy-v2"
	afterPolicy, err := memory.NextRecommendation(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if afterPolicy.ID == afterEvidence.ID || afterPolicy.EvidenceSnapshot != afterEvidence.EvidenceSnapshot || len(memory.recommendations) != 3 {
		t.Fatalf("policy version did not produce a new decision: before=%s after=%s rows=%d", afterEvidence.ID, afterPolicy.ID, len(memory.recommendations))
	}
	if afterPolicy.PolicyVersion != "test-policy-v2" || !strings.Contains(afterPolicy.Rationale, "test-policy-v2") {
		t.Fatalf("new policy decision is not explainable: %+v", afterPolicy)
	}
}

func TestMemoryRecommendationExactScopeActorRoleAndRunIsolation(t *testing.T) {
	memory, scope := recommendationMemory(t)
	saveRoutingClaim(t, memory, "claim-router")
	ctx := context.Background()
	base, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: scope, ActorID: "actor-a", Role: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	differentActor, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: scope, ActorID: "actor-b", Role: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	differentRole, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: scope, ActorID: "actor-a", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if base.ID == differentActor.ID || base.ID == differentRole.ID {
		t.Fatal("actor and role must participate in recommendation identity")
	}
	flagScope := scope
	flagScope.FeatureFlags = map[string]string{"camp_grid_v2": "true"}
	flagged, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: flagScope, ActorID: "actor-a", Role: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	if flagged.ID == base.ID || flagged.FeatureFlags["camp_grid_v2"] != "true" {
		t.Fatalf("feature flags must participate in recommendation identity and output: base=%+v flagged=%+v", base, flagged)
	}

	wrongTenant := scope
	wrongTenant.TenantID = "other-tenant"
	if _, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: wrongTenant, ActorID: "actor-a", Role: "editor"}); !errors.Is(err, ErrRunScopeMismatch) {
		t.Fatalf("tenant isolation failed: %v", err)
	}
	wrongEnvironment := scope
	wrongEnvironment.EnvironmentID = "other-env"
	if _, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: wrongEnvironment, ActorID: "actor-a", Role: "editor"}); !errors.Is(err, ErrRunScopeMismatch) {
		t.Fatalf("environment isolation failed: %v", err)
	}
	wrongScenario := scope
	wrongScenario.ScenarioID = "other-scenario"
	if _, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: wrongScenario, ActorID: "actor-a", Role: "editor"}); !errors.Is(err, ErrRunScopeMismatch) {
		t.Fatalf("scenario isolation failed: %v", err)
	}

	if _, _, err := memory.CreateExperiment(ctx, model.ExperimentRegistration{
		ID: "run-router-2", TenantID: scope.TenantID, BuildID: scope.BuildID,
		EnvironmentID: scope.EnvironmentID, ScenarioID: scope.ScenarioID, Mode: "guided",
	}, "controller"); err != nil {
		t.Fatal(err)
	}
	secondScope := scope
	secondScope.RunID = "run-router-2"
	secondRun, err := memory.NextRecommendation(ctx, RecommendationQuery{Scope: secondScope, ActorID: "actor-a", Role: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	if secondRun.ID == base.ID {
		t.Fatal("run identity must isolate persisted recommendation decisions")
	}
}

func TestMemoryEmptyOrIneligibleClaimsDoNotPersist(t *testing.T) {
	memory, scope := recommendationMemory(t)
	query := RecommendationQuery{Scope: scope, ActorID: "actor-a", Role: "editor"}
	if _, err := memory.NextRecommendation(context.Background(), query); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty claim set should return not found, got %v", err)
	}
	if len(memory.recommendations) != 0 {
		t.Fatal("empty claim set persisted a recommendation")
	}

	saveRoutingClaim(t, memory, "claim-blocked")
	evidenceContext := model.EvidenceContext{ID: "ctx-blocked", TenantID: scope.TenantID, RunID: scope.RunID, BuildID: scope.BuildID, EnvironmentID: scope.EnvironmentID, ScenarioID: scope.ScenarioID}
	if _, _, err := memory.SaveEvidenceContext(context.Background(), evidenceContext); err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.SaveObservation(context.Background(), model.Observation{
		ID: "obs-blocked", TenantID: scope.TenantID, RunID: scope.RunID, ClaimID: "claim-blocked",
		EvidenceContextID: evidenceContext.ID, SourceType: "agent", SourceAdapter: "playwright",
		ActorID: "agent-a", Assessment: "blocked", VerifierResult: "required fixture is unavailable",
		OccurredAt: time.Date(2026, 8, 25, 20, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.NextRecommendation(context.Background(), query); !errors.Is(err, ErrNotFound) {
		t.Fatalf("infeasible blocked claim should return not found, got %v", err)
	}
	if len(memory.recommendations) != 0 {
		t.Fatal("ineligible claim set persisted a recommendation")
	}
}
