package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func TestPostgresRecommendationOverrideDurabilityImmutabilityAndReceipt(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	registerIntegrationBuild(t, repository, "tenant-feedback", "local", "build-1", 1, "")
	createIntegrationRun(t, repository, "run-feedback", "tenant-feedback", "build-1", "local")

	for _, claimID := range []string{"claim-a", "claim-b"} {
		if _, _, err := repository.SaveClaimDefinition(ctx, model.ClaimDefinition{
			ID: claimID, TenantID: "tenant-feedback", Title: claimID, Target: "target " + claimID,
			StateSignature: "before -> after " + claimID, ActorContext: "editor", ValidationIntent: "TransitionSucceeds",
		}); err != nil {
			t.Fatal(err)
		}
	}
	recommendation, err := repository.NextRecommendation(ctx, RecommendationQuery{
		Scope:   Scope{RunID: "run-feedback", TenantID: "tenant-feedback", BuildID: "build-1", EnvironmentID: "local"},
		ActorID: "late-tester", Role: "editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recommendation.ProgressionSnapshot) != 64 || len(recommendation.OverrideAlternatives) != 1 {
		t.Fatalf("initial progression/alternatives were not persisted in the model: %+v", recommendation)
	}
	overrideClaimID := "claim-b"
	if recommendation.ClaimID == overrideClaimID {
		overrideClaimID = "claim-a"
	}
	feedback := model.RecommendationResponse{
		Response: "override", Reason: "human chose the alternate uncertainty",
		ActorID: "late-tester", OverrideClaimID: overrideClaimID,
	}
	created, err := repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || !created {
		t.Fatalf("override insert failed: created=%v err=%v", created, err)
	}
	created, err = repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || created {
		t.Fatalf("exact override retry must be idempotent: created=%v err=%v", created, err)
	}

	var responseType, reason, actorID, persistedOverride string
	if err := repository.db.QueryRowContext(ctx, `
		select response,reason,actor_id,coalesce(override_claim_id,'')
		from recommendation_responses where run_id=$1 and recommendation_id=$2`,
		recommendation.RunID, recommendation.ID).Scan(&responseType, &reason, &actorID, &persistedOverride); err != nil {
		t.Fatal(err)
	}
	if responseType != "override" || reason != feedback.Reason || actorID != feedback.ActorID || persistedOverride != overrideClaimID {
		t.Fatalf("persisted override mismatch: response=%s reason=%s actor=%s override=%s", responseType, reason, actorID, persistedOverride)
	}
	next, err := repository.NextRecommendation(ctx, RecommendationQuery{
		Scope:   Scope{RunID: "run-feedback", TenantID: "tenant-feedback", BuildID: "build-1", EnvironmentID: "local"},
		ActorID: "late-tester", Role: "editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == recommendation.ID || next.ClaimID != overrideClaimID || next.EvidenceSnapshot != recommendation.EvidenceSnapshot || next.ProgressionSnapshot == recommendation.ProgressionSnapshot || next.PromotedByResponseID == "" {
		t.Fatalf("postgres override progression did not promote the target: first=%+v next=%+v", recommendation, next)
	}
	var persistedProgression, promotedBy string
	var persistedAlternatives []byte
	if err := repository.db.QueryRowContext(ctx, `select progression_snapshot,override_alternatives,coalesce(promoted_by_response_id,'') from recommendation_events where id=$1`, next.ID).Scan(&persistedProgression, &persistedAlternatives, &promotedBy); err != nil {
		t.Fatal(err)
	}
	if persistedProgression != next.ProgressionSnapshot || promotedBy != next.PromotedByResponseID || !json.Valid(persistedAlternatives) {
		t.Fatalf("progression persistence mismatch: snapshot=%s promoted_by=%s alternatives=%s", persistedProgression, promotedBy, persistedAlternatives)
	}
	if _, err := repository.db.ExecContext(ctx, `update recommendation_events set progression_snapshot=repeat('0',64) where id=$1`, next.ID); err == nil {
		t.Fatal("claim-level recommendation progression update must be rejected")
	}
	if _, err := repository.db.ExecContext(ctx, `delete from recommendation_events where id=$1`, next.ID); err == nil {
		t.Fatal("claim-level recommendation delete must be rejected")
	}
	if _, err := repository.db.ExecContext(ctx, `update recommendation_responses set reason='mutated' where recommendation_id=$1`, recommendation.ID); err == nil {
		t.Fatal("recommendation feedback update must be rejected")
	}
	if _, err := repository.db.ExecContext(ctx, `delete from recommendation_responses where recommendation_id=$1`, recommendation.ID); err == nil {
		t.Fatal("recommendation feedback delete must be rejected")
	}

	repository.Close()
	repository, err = NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	created, err = repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || created {
		t.Fatalf("override retry after restart must be idempotent: created=%v err=%v", created, err)
	}
	if record := repository.memory.responses[recommendation.ID]; record.OverrideClaimID != overrideClaimID {
		t.Fatalf("override target did not survive restart: %+v", record)
	}
	loadedNext := repository.memory.recommendations[next.ID]
	progression, progressionErr := repository.memory.recommendationProgression(RecommendationQuery{
		Scope:   Scope{RunID: "run-feedback", TenantID: "tenant-feedback", BuildID: "build-1", EnvironmentID: "local"},
		ActorID: "late-tester", Role: "editor",
	}, recommendation.EvidenceSnapshot)
	restartedNext, err := repository.NextRecommendation(ctx, RecommendationQuery{
		Scope:   Scope{RunID: "run-feedback", TenantID: "tenant-feedback", BuildID: "build-1", EnvironmentID: "local"},
		ActorID: "late-tester", Role: "editor",
	})
	if err != nil || restartedNext.ID != next.ID || restartedNext.ProgressionSnapshot != next.ProgressionSnapshot || restartedNext.PromotedByResponseID != next.PromotedByResponseID {
		t.Fatalf("restart did not hydrate the same progressed recommendation: got=%+v want=%+v loaded=%+v computed_progression=%+v progression_err=%v err=%v", restartedNext, next, loadedNext, progression, progressionErr, err)
	}

	if _, _, err := repository.FreezeExperiment(ctx, recommendation.RunID, recommendation.TenantID); err != nil {
		t.Fatalf("freeze failed: %v", err)
	}
	var canonicalInput []byte
	if err := repository.db.QueryRowContext(ctx, `select canonical_input from experiment_metrics where run_id=$1`, recommendation.RunID).Scan(&canonicalInput); err != nil {
		t.Fatal(err)
	}
	var canonical struct {
		Recommendations []model.Recommendation               `json:"recommendations"`
		Responses       []model.RecommendationResponseRecord `json:"responses"`
	}
	if err := json.Unmarshal(canonicalInput, &canonical); err != nil {
		t.Fatalf("decode frozen canonical input: %v", err)
	}
	found := false
	for _, record := range canonical.Responses {
		if record.RecommendationID != recommendation.ID {
			continue
		}
		found = true
		if record.OverrideClaimID != overrideClaimID {
			t.Fatalf("frozen metric input receipt has wrong override target: got=%s want=%s record=%+v", record.OverrideClaimID, overrideClaimID, record)
		}
	}
	if !found {
		t.Fatalf("frozen metric input receipt omitted recommendation response: %s", canonicalInput)
	}
	foundPromoted := false
	for _, record := range canonical.Recommendations {
		if record.ID == next.ID {
			foundPromoted = record.ClaimID == overrideClaimID && record.EvidenceSnapshot == recommendation.EvidenceSnapshot && record.ProgressionSnapshot == next.ProgressionSnapshot && record.PromotedByResponseID == next.PromotedByResponseID
		}
	}
	if !foundPromoted {
		t.Fatalf("frozen metric input cannot reconstruct A -> override B -> recommendation B: %s", canonicalInput)
	}
	if _, err := repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback); !errors.Is(err, ErrRunFrozen) {
		t.Fatalf("frozen run must reject response replay under existing lifecycle semantics, got %v", err)
	}
}

func TestMigration0010BackfillsExistingM5ARowsAndPreservesImmutableHistory(t *testing.T) {
	databaseURL := prepareIntegrationDatabaseThrough(t, "0009_recommendation_feedback_v0.sql")
	ctx := context.Background()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
		insert into tenants (id,name) values ('tenant-legacy','Legacy tenant');
		insert into build_registry (tenant_id,environment_id,build_id,sequence_number,registered_by)
		values ('tenant-legacy','local','build-1',1,'controller');
		insert into experiment_runs (id,tenant_id,build_id,environment_id,scenario_id,mode,created_by)
		values ('run-legacy','tenant-legacy','build-1','local','scenario-legacy','guided','controller');
		insert into claim_definitions (id,tenant_id,title,target,state_signature,actor_context,validation_intent)
		values ('claim-legacy','tenant-legacy','Legacy claim','legacy target','before -> after','editor','TransitionSucceeds');
		insert into recommendation_events (
			id,tenant_id,claim_id,actor_id,role,build_id,environment_id,run_id,scenario_id,
			area,classification,max_verification_depth,independent_source_count,conflict_count,
			rationale,score,factors,policy_version,evidence_snapshot,feature_flags
		) values (
			'rec-legacy','tenant-legacy','claim-legacy','late-tester','editor','build-1','local','run-legacy','scenario-legacy',
			'legacy target','Untouched',0,0,0,'legacy recommendation',0.0625,
			'{"delta_uncertainty":1,"business_risk":0.5,"diff_impact":0.5,"smoothed_failure_prior":0.25,"expected_cost":1}',
			'information-value-v0.1',repeat('a',64),'{}'
		);
		insert into recommendation_responses (id,run_id,recommendation_id,response,reason,actor_id)
		values ('response-legacy','run-legacy','rec-legacy','accepted','legacy response','late-tester');
	`); err != nil {
		t.Fatalf("seed pre-0010 M5A/M5B history: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	migrationPath := filepath.Join(filepath.Dir(file), "../../../supabase/migrations/0010_recommendation_progression_v0.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply 0010 over existing M5A row: %v", err)
	}
	var progression string
	var alternatives []byte
	if err := db.QueryRowContext(ctx, `select progression_snapshot,override_alternatives from recommendation_events where id='rec-legacy'`).Scan(&progression, &alternatives); err != nil {
		t.Fatal(err)
	}
	if len(progression) != 64 || string(alternatives) != "[]" {
		t.Fatalf("legacy recommendation backfill is incomplete: progression=%q alternatives=%s", progression, alternatives)
	}
	var responseCount int
	if err := db.QueryRowContext(ctx, `select count(*) from recommendation_responses where id='response-legacy' and response='accepted'`).Scan(&responseCount); err != nil || responseCount != 1 {
		t.Fatalf("legacy immutable response was not preserved: count=%d err=%v", responseCount, err)
	}
	if _, err := db.ExecContext(ctx, `
		insert into recommendation_events (
			id,tenant_id,claim_id,actor_id,role,build_id,environment_id,run_id,scenario_id,
			area,classification,max_verification_depth,independent_source_count,conflict_count,
			rationale,score,factors,policy_version,evidence_snapshot,progression_snapshot,override_alternatives,feature_flags
		) select
			'rec-progressed',tenant_id,claim_id,actor_id,role,build_id,environment_id,run_id,scenario_id,
			area,classification,max_verification_depth,independent_source_count,conflict_count,
			'new progression step',score,factors,policy_version,evidence_snapshot,repeat('b',64),'[]',feature_flags
		from recommendation_events where id='rec-legacy'
	`); err != nil {
		t.Fatalf("0010 uniqueness must allow a new immutable step over unchanged evidence: %v", err)
	}
	if _, err := db.ExecContext(ctx, `update recommendation_events set rationale='mutated' where id='rec-legacy'`); err == nil {
		t.Fatal("0010 must restore M5A recommendation immutability after backfill")
	}
}

func TestPostgresOverrideExactRetryAndConflictAfterEvidenceAndPolicyDrift(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "tenant-drift", "local", "build-1", 1, "")
	createIntegrationRun(t, repository, "run-drift", "tenant-drift", "build-1", "local")

	for _, claimID := range []string{"claim-a", "claim-b", "claim-c"} {
		if _, _, err := repository.SaveClaimDefinition(ctx, model.ClaimDefinition{
			ID: claimID, TenantID: "tenant-drift", Title: claimID, Target: "target " + claimID,
			StateSignature: "before -> after " + claimID, ActorContext: "editor", ValidationIntent: "TransitionSucceeds",
		}); err != nil {
			t.Fatal(err)
		}
	}

	recommendation, err := repository.NextRecommendation(ctx, RecommendationQuery{
		Scope:   Scope{RunID: "run-drift", TenantID: "tenant-drift", BuildID: "build-1", EnvironmentID: "local"},
		ActorID: "late-tester", Role: "editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if recommendation.ClaimID != "claim-a" {
		t.Fatalf("expected claim-a, got %s", recommendation.ClaimID)
	}

	feedback := model.RecommendationResponse{
		Response: "override", Reason: "human chose alternate claim-b",
		ActorID: "late-tester", OverrideClaimID: "claim-b",
	}
	created, err := repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || !created {
		t.Fatalf("initial override failed: created=%v err=%v", created, err)
	}

	contextRecord := model.EvidenceContext{
		ID: "ctx-postgres-drift", TenantID: "tenant-drift", RunID: "run-drift",
		BuildID: "build-1", EnvironmentID: "local",
	}
	if _, _, err := repository.SaveEvidenceContext(ctx, contextRecord); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.SaveObservation(ctx, model.Observation{
		ID: "obs-postgres-drift-blocked", TenantID: "tenant-drift", RunID: "run-drift",
		ClaimID: "claim-b", EvidenceContextID: contextRecord.ID,
		SourceType: "agent", SourceAdapter: "playwright", ActorID: "agent-a",
		Assessment: "blocked", VerifierResult: "fixture unavailable",
		OccurredAt: time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	created, err = repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || created {
		t.Fatalf("exact override retry after evidence drift must be idempotent success: created=%v err=%v", created, err)
	}

	conflictingReason := model.RecommendationResponse{
		Response: "override", Reason: "different reason for claim-b",
		ActorID: "late-tester", OverrideClaimID: "claim-b",
	}
	if _, err := repository.SaveRecommendationFeedback(ctx, recommendation.ID, conflictingReason); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("conflicting reason for drifted target must return ErrResponseConflict, got %v", err)
	}

	conflictingAccepted := model.RecommendationResponse{
		Response: "accepted", Reason: "changed mind to accept",
		ActorID: "late-tester",
	}
	if _, err := repository.SaveRecommendationFeedback(ctx, recommendation.ID, conflictingAccepted); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("conflicting accepted retry after evidence drift must return ErrResponseConflict, got %v", err)
	}

	repository.memory.routingPolicy.Version = "information-value-v0.2-postgres-drift"

	created, err = repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || created {
		t.Fatalf("exact override retry after policy drift must be idempotent success: created=%v err=%v", created, err)
	}

	repository.Close()
	repository, err = NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	repository.memory.routingPolicy.Version = "information-value-v0.2-postgres-drift"

	created, err = repository.SaveRecommendationFeedback(ctx, recommendation.ID, feedback)
	if err != nil || created {
		t.Fatalf("exact override retry after restart and policy/evidence drift must be idempotent success: created=%v err=%v", created, err)
	}
	if record := repository.memory.responses[recommendation.ID]; record.OverrideClaimID != "claim-b" {
		t.Fatalf("override target mismatch after restart: %+v", record)
	}

	if _, err := repository.SaveRecommendationFeedback(ctx, recommendation.ID, conflictingReason); !errors.Is(err, ErrResponseConflict) {
		t.Fatalf("conflicting retry after restart must return ErrResponseConflict, got %v", err)
	}
}
