package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/state"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresAtomicRollbackDuplicateConflictAndConcurrency(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")

	repository.failAfterRawInsertForTest = func() error { return errors.New("injected projection failure") }
	event := integrationEvent("atomic-event", "actor-a", "packed")
	if created, err := repository.SaveEvent(ctx, event); err == nil || created {
		t.Fatalf("expected injected rollback, got created=%v err=%v", created, err)
	}
	assertTableCount(t, repository.db, "validation_events", 0)
	assertTableCount(t, repository.db, "candidate_states", 0)
	assertTableCount(t, repository.db, "evidence_records", 0)

	repository.failAfterRawInsertForTest = nil
	if created, err := repository.SaveEvent(ctx, event); err != nil || !created {
		t.Fatalf("retry should succeed: created=%v err=%v", created, err)
	}
	if created, err := repository.SaveEvent(ctx, event); err != nil || created {
		t.Fatalf("exact duplicate should be idempotent: created=%v err=%v", created, err)
	}
	conflict := event
	conflict.Outcome = "fail"
	if _, err := repository.SaveEvent(ctx, conflict); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("expected payload conflict, got %v", err)
	}

	concurrent := integrationEvent("concurrent-event", "actor-b", "claimed")
	var createdCount atomic.Int32
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			created, err := repository.SaveEvent(ctx, concurrent)
			if err != nil {
				errorsSeen <- err
				return
			}
			if created {
				createdCount.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent ingest failed: %v", err)
	}
	if createdCount.Load() != 1 {
		t.Fatalf("expected one concurrent insert, got %d", createdCount.Load())
	}
	adapter := integrationEvent("adapter-same-actor", "actor-a", "packed")
	adapter.SourceType, adapter.SourceAdapter = "automation", "festival-activity-log-v1"
	if created, err := repository.SaveEvent(ctx, adapter); err != nil || !created {
		t.Fatalf("same stable actor must support another authenticated source: created=%v err=%v", created, err)
	}
	assertTableCount(t, repository.db, "validation_events", 3)
	assertTableCount(t, repository.db, "evidence_records", 3)
}

func TestPostgresCorrectionScopeChainAndRestart(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")

	first := integrationEvent("correction-a", "actor-a", "claimed")
	second := integrationEvent("correction-b", "actor-b", "claimed")
	if _, err := repository.SaveEvent(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveEvent(ctx, second); err != nil {
		t.Fatal(err)
	}
	evidence, _ := repository.Evidence(ctx, Scope{RunID: first.RunID, TenantID: first.TenantID, BuildID: first.BuildID, EnvironmentID: first.EnvironmentID})
	if len(evidence.States) != 1 {
		t.Fatalf("expected one state, got %+v", evidence.States)
	}
	originalID := evidence.States[0].ID
	if err := repository.CorrectState(ctx, "merge", originalID, model.StateCorrectionRequest{TargetStateID: originalID, ActorID: "researcher", Reason: "invalid self merge"}); err == nil {
		t.Fatal("self merge must fail")
	}

	other := integrationEvent("other-scope", "actor-c", "claimed")
	other.TenantID, other.RunID = "other-tenant", "other-run"
	registerIntegrationBuild(t, repository, other.TenantID, other.EnvironmentID, other.BuildID, 1, "")
	createIntegrationRun(t, repository, other.RunID, other.TenantID, other.BuildID, other.EnvironmentID)
	if _, err := repository.SaveEvent(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherEvidence, _ := repository.Evidence(ctx, Scope{RunID: other.RunID, TenantID: other.TenantID, BuildID: other.BuildID, EnvironmentID: other.EnvironmentID})
	if err := repository.CorrectState(ctx, "merge", originalID, model.StateCorrectionRequest{TargetStateID: otherEvidence.States[0].ID, ActorID: "researcher", Reason: "invalid cross scope"}); err == nil {
		t.Fatal("cross-scope merge must fail")
	}

	rename := model.StateCorrectionRequest{NewFingerprint: "supply-list:item:canopy:status:reviewed", NewBusinessState: "reviewed", ActorID: "researcher", Reason: "reviewed identity"}
	if err := repository.CorrectState(ctx, "rename", originalID, rename); err != nil {
		t.Fatal(err)
	}
	renamed, _ := repository.Evidence(ctx, Scope{RunID: first.RunID, TenantID: first.TenantID, BuildID: first.BuildID, EnvironmentID: first.EnvironmentID})
	if len(renamed.States) != 1 || renamed.States[0].StateFingerprint != rename.NewFingerprint {
		t.Fatalf("rename not applied: %+v", renamed.States)
	}
	renamedID := renamed.States[0].ID

	repository.Close()
	repository, err = NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, _ := repository.Evidence(ctx, Scope{RunID: first.RunID, TenantID: first.TenantID, BuildID: first.BuildID, EnvironmentID: first.EnvironmentID})
	if len(afterRestart.States) != 1 || afterRestart.States[0].ID != renamedID || afterRestart.States[0].EvidenceCount != 2 {
		t.Fatalf("rename did not survive restart: %+v", afterRestart.States)
	}

	future := integrationEvent("correction-future", "actor-d", "claimed")
	if _, err := repository.SaveEvent(ctx, future); err != nil {
		t.Fatal(err)
	}
	withFuture, _ := repository.Evidence(ctx, Scope{RunID: first.RunID, TenantID: first.TenantID, BuildID: first.BuildID, EnvironmentID: first.EnvironmentID})
	if len(withFuture.States) != 1 || withFuture.States[0].ID != renamedID || withFuture.States[0].EvidenceCount != 3 {
		t.Fatalf("future evidence ignored rename rule: %+v", withFuture.States)
	}

	split := model.StateCorrectionRequest{NewFingerprint: "supply-list:item:canopy:status:independently-reviewed", NewBusinessState: "independently-reviewed", EventIDs: []string{second.EventID}, ActorID: "researcher", Reason: "separate independent review"}
	if err := repository.CorrectState(ctx, "split", renamedID, split); err != nil {
		t.Fatal(err)
	}
	repository.Close()
	repository, err = NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	afterChain, _ := repository.Evidence(ctx, Scope{RunID: first.RunID, TenantID: first.TenantID, BuildID: first.BuildID, EnvironmentID: first.EnvironmentID})
	if len(afterChain.States) != 2 {
		t.Fatalf("correction chain did not survive restart: %+v", afterChain.States)
	}
	assertTableCount(t, repository.db, "state_corrections", 2)
}

func TestPostgresBuildOrderSurvivesRestartAndDelayedEvents(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")
	old := integrationEvent("old-build-event", "actor-a", "claimed")
	if _, err := repository.SaveEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	second := model.BuildRegistration{TenantID: old.TenantID, EnvironmentID: old.EnvironmentID, BuildID: "festnest-demo-002", SequenceNumber: 2, PredecessorBuildID: old.BuildID}
	registered, created, err := repository.RegisterBuild(ctx, second, "researcher-a")
	if err != nil || !created || registered.RegisteredBy != "researcher-a" {
		t.Fatalf("register second build: registered=%+v created=%v err=%v", registered, created, err)
	}
	evidence, err := repository.Evidence(ctx, Scope{RunID: old.RunID, TenantID: old.TenantID, BuildID: old.BuildID, EnvironmentID: old.EnvironmentID})
	if err != nil || len(evidence.States) != 1 || evidence.States[0].Status != "stale" {
		t.Fatalf("registration alone must stale older persisted evidence: %+v err=%v", evidence, err)
	}
	if retry, created, err := repository.RegisterBuild(ctx, second, "researcher-b"); err != nil || created || retry.RegisteredAt != registered.RegisteredAt || retry.RegisteredBy != "researcher-a" {
		t.Fatalf("exact retry must preserve original audit: registered=%+v created=%v err=%v", retry, created, err)
	}
	current := integrationEvent("current-build-event", "actor-a", "claimed")
	current.BuildID = second.BuildID
	current.RunID = "festival-guided-002"
	createIntegrationRun(t, repository, current.RunID, current.TenantID, current.BuildID, current.EnvironmentID)
	current.OccurredAt = old.OccurredAt.Add(time.Minute)
	if _, err := repository.SaveEvent(ctx, current); err != nil {
		t.Fatal(err)
	}
	delayed := integrationEvent("delayed-old-build-event", "actor-b", "claimed")
	delayed.OccurredAt = old.OccurredAt.Add(time.Hour)
	if _, err := repository.SaveEvent(ctx, delayed); err != nil {
		t.Fatal(err)
	}
	evidence, err = repository.Evidence(ctx, Scope{RunID: old.RunID, TenantID: old.TenantID, BuildID: old.BuildID, EnvironmentID: old.EnvironmentID})
	if err != nil || len(evidence.States) != 1 || evidence.States[0].Status != "stale" {
		t.Fatalf("older build should remain stale before restart: %+v err=%v", evidence, err)
	}
	repository.Close()
	repository, err = NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	evidence, err = repository.Evidence(ctx, Scope{RunID: old.RunID, TenantID: old.TenantID, BuildID: old.BuildID, EnvironmentID: old.EnvironmentID})
	if err != nil || len(evidence.States) != 1 || evidence.States[0].Status != "stale" || evidence.States[0].EvidenceCount != 2 {
		t.Fatalf("registered order must survive restart: %+v err=%v", evidence, err)
	}
	currentEvidence, err := repository.Evidence(ctx, Scope{RunID: current.RunID, TenantID: current.TenantID, BuildID: current.BuildID, EnvironmentID: current.EnvironmentID})
	if err != nil || len(currentEvidence.States) != 1 || currentEvidence.States[0].Status == "stale" {
		t.Fatalf("newer build must remain current after reverse arrival and restart: %+v err=%v", currentEvidence, err)
	}
}

func TestPostgresConcurrentNextBuildRegistrationAdmitsOne(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	candidates := []model.BuildRegistration{
		{TenantID: "festival-thesis-demo", EnvironmentID: "festnest-local-browser", BuildID: "festnest-demo-002-a", SequenceNumber: 2, PredecessorBuildID: "festnest-demo-001"},
		{TenantID: "festival-thesis-demo", EnvironmentID: "festnest-local-browser", BuildID: "festnest-demo-002-b", SequenceNumber: 2, PredecessorBuildID: "festnest-demo-001"},
	}
	var createdCount atomic.Int32
	var conflictCount atomic.Int32
	var wait sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, created, err := repository.RegisterBuild(ctx, candidate, "integration-controller")
			if err == nil && created {
				createdCount.Add(1)
				return
			}
			if errors.Is(err, ErrBuildRegistrationConflict) {
				conflictCount.Add(1)
				return
			}
			t.Errorf("unexpected registration result for %s: created=%v err=%v", candidate.BuildID, created, err)
		}()
	}
	wait.Wait()
	if createdCount.Load() != 1 || conflictCount.Load() != 1 {
		t.Fatalf("expected one registration and one conflict, got created=%d conflict=%d", createdCount.Load(), conflictCount.Load())
	}
}

func prepareIntegrationDatabase(t *testing.T) string {
	return prepareIntegrationDatabaseThrough(t, "")
}

func prepareIntegrationDatabaseThrough(t *testing.T, finalMigration string) string {
	t.Helper()
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatalf("TEST_DATABASE_URL must use loopback: %v", err)
	}
	dbname := strings.TrimPrefix(parsed.Path, "/")
	if dbname == "qa_platform" {
		t.Fatalf("GUARDRAIL VIOLATION: TEST_DATABASE_URL targets persistent Demo database 'qa_platform'. Resetting it is strictly forbidden!")
	}
	if parsed.Port() != "54332" {
		t.Fatalf("GUARDRAIL VIOLATION: TEST_DATABASE_URL port must be 54332 (got %s)", parsed.Port())
	}
	schema := fmt.Sprintf("qa_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`create schema "` + schema + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`drop schema if exists "` + schema + `" cascade`)
		_ = admin.Close()
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	testURL := parsed.String()
	db, err := sql.Open("pgx", testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, file, _, _ := runtime.Caller(0)
	migrationPaths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "../../../supabase/migrations/*.sql"))
	if err != nil || len(migrationPaths) == 0 {
		t.Fatalf("load migrations: paths=%v err=%v", migrationPaths, err)
	}
	for _, migrationPath := range migrationPaths {
		if finalMigration != "" && filepath.Base(migrationPath) > finalMigration {
			break
		}
		migration, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(migrationPath), err)
		}
	}
	return testURL
}

func integrationEvent(id, actor, state string) model.ValidationEvent {
	return model.ValidationEvent{
		EventID: id, TenantID: "festival-thesis-demo", RunID: "festival-guided-001", ActorID: actor,
		SourceType: "human", SourceAdapter: "integration-test", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser",
		Context: map[string]string{"role": "editor", "module": "supply-list", "connectivity": "online", "scenario_id": "equipment-handoff-v1"},
		Route:   "/trips/demo/supply-list", StateFingerprint: "supply-list:item:canopy:status:" + state,
		ActionType: "supply_item_observed", Outcome: "pass", OccurredAt: time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC), StepIndex: 1,
		Privacy: model.PrivacyMetadata{Masked: true},
	}
}

func registerIntegrationBuild(t *testing.T, repository Repository, tenantID, environmentID, buildID string, sequence int64, predecessor string) {
	t.Helper()
	_, _, err := repository.RegisterBuild(context.Background(), model.BuildRegistration{TenantID: tenantID, EnvironmentID: environmentID, BuildID: buildID, SequenceNumber: sequence, PredecessorBuildID: predecessor}, "integration-controller")
	if err != nil {
		t.Fatalf("register integration build: %v", err)
	}
}

func createIntegrationRun(t *testing.T, repository Repository, runID, tenantID, buildID, environmentID string) {
	t.Helper()
	_, _, err := repository.CreateExperiment(context.Background(), model.ExperimentRegistration{ID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: environmentID, ScenarioID: "equipment-handoff-v1", Mode: "guided"}, "integration-controller")
	if err != nil {
		t.Fatalf("create integration run: %v", err)
	}
}

func assertTableCount(t *testing.T, db *sql.DB, table string, expected int) {
	t.Helper()
	var actual int
	if err := db.QueryRow(`select count(*) from ` + table).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("expected %d rows in %s, got %d", expected, table, actual)
	}
}

// Gate 2 lifecycle coverage. These exercise the Go store against the 0005
// triggers. Post-freeze denial is asserted on candidate_states and
// evidence_records rather than validation_events, because
// validation_events_immutable_update sorts before
// validation_events_writable_run and would mask run_frozen with the
// immutability error instead.

func TestPostgresExperimentCreationIsIdempotentAndRejectsConflicts(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")

	registration := model.ExperimentRegistration{
		ID: "festival-guided-001", TenantID: "festival-thesis-demo", BuildID: "festnest-demo-001",
		EnvironmentID: "festnest-local-browser", ScenarioID: "equipment-handoff-v1", Mode: "guided",
	}
	run, created, err := repository.CreateExperiment(ctx, registration, "integration-controller")
	if err != nil || !created {
		t.Fatalf("first creation must create: created=%v err=%v", created, err)
	}
	if run.CreatedBy != "integration-controller" || run.FrozenAt != nil {
		t.Fatalf("unexpected new run: createdBy=%q frozenAt=%v", run.CreatedBy, run.FrozenAt)
	}
	if _, created, err := repository.CreateExperiment(ctx, registration, "integration-controller"); err != nil || created {
		t.Fatalf("exact retry must be idempotent: created=%v err=%v", created, err)
	}
	for _, mutation := range []func(model.ExperimentRegistration) model.ExperimentRegistration{
		func(r model.ExperimentRegistration) model.ExperimentRegistration { r.Mode = "baseline"; return r },
		func(r model.ExperimentRegistration) model.ExperimentRegistration {
			r.ScenarioID = "other-scenario"
			return r
		},
		func(r model.ExperimentRegistration) model.ExperimentRegistration {
			r.BuildID = "festnest-demo-002"
			return r
		},
	} {
		if _, _, err := repository.CreateExperiment(ctx, mutation(registration), "integration-controller"); !errors.Is(err, ErrExperimentConflict) {
			t.Fatalf("conflicting re-registration must be rejected, got %v", err)
		}
	}
	assertTableCount(t, repository.db, "experiment_runs", 1)
}

func TestPostgresEvidenceIsPartitionedByRun(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")
	createIntegrationRun(t, repository, "festival-baseline-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")

	guided := integrationEvent("partition-guided", "actor-a", "packed")
	baseline := integrationEvent("partition-baseline", "actor-a", "packed")
	baseline.RunID = "festival-baseline-001"
	for _, event := range []model.ValidationEvent{guided, baseline} {
		if created, err := repository.SaveEvent(ctx, event); err != nil || !created {
			t.Fatalf("save %s: created=%v err=%v", event.EventID, created, err)
		}
	}

	// Identical state fingerprints in different runs must remain distinct rows.
	assertTableCount(t, repository.db, "candidate_states", 2)

	guidedEvidence, err := repository.Evidence(ctx, Scope{RunID: "festival-guided-001", TenantID: "festival-thesis-demo", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser"})
	if err != nil {
		t.Fatal(err)
	}
	if len(guidedEvidence.States) != 1 {
		t.Fatalf("guided run must see only its own candidate, got %d", len(guidedEvidence.States))
	}

	unknown := integrationEvent("partition-unknown", "actor-a", "packed")
	unknown.RunID = "festival-nonexistent-001"
	if _, err := repository.SaveEvent(ctx, unknown); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("unknown run must fail closed, got %v", err)
	}
}

func TestPostgresFreezeIsIdempotentAndDatabaseDeniesLaterWrites(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")
	createIntegrationRun(t, repository, "festival-baseline-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")
	if created, err := repository.SaveEvent(ctx, integrationEvent("freeze-seed", "actor-a", "packed")); err != nil || !created {
		t.Fatalf("seed event: created=%v err=%v", created, err)
	}

	frozen, created, err := repository.FreezeExperiment(ctx, "festival-guided-001", "festival-thesis-demo")
	if err != nil || !created {
		t.Fatalf("freeze must succeed: created=%v err=%v", created, err)
	}
	if frozen.FrozenAt == nil || frozen.Status != "frozen" {
		t.Fatalf("frozen metrics must be marked frozen: status=%q frozenAt=%v", frozen.Status, frozen.FrozenAt)
	}
	again, created, err := repository.FreezeExperiment(ctx, "festival-guided-001", "festival-thesis-demo")
	if err != nil || created {
		t.Fatalf("re-freeze must be idempotent: created=%v err=%v", created, err)
	}
	if again.InputReceipt.SHA256 != frozen.InputReceipt.SHA256 {
		t.Fatalf("re-freeze changed the receipt: %s vs %s", again.InputReceipt.SHA256, frozen.InputReceipt.SHA256)
	}

	// The database, not the API layer, must refuse later run-attributed writes.
	if _, err := repository.db.Exec(`update candidate_states set status='stale' where run_id='festival-guided-001'`); err == nil {
		t.Fatal("candidate_states update after freeze must be rejected by the database")
	} else if !strings.Contains(err.Error(), "run_frozen") {
		t.Fatalf("expected run_frozen, got %v", err)
	}
	if _, err := repository.db.Exec(`delete from evidence_records where run_id='festival-guided-001'`); err == nil {
		t.Fatal("evidence_records delete after freeze must be rejected by the database")
	} else if !strings.Contains(err.Error(), "run_frozen") {
		t.Fatalf("expected run_frozen, got %v", err)
	}
	if _, err := repository.SaveEvent(ctx, integrationEvent("post-freeze-event", "actor-b", "claimed")); err == nil {
		t.Fatal("ingest into a frozen run must fail")
	}

	// A concurrently open run must stay writable: the denial is run-scoped.
	stillOpen := integrationEvent("open-run-event", "actor-b", "claimed")
	stillOpen.RunID = "festival-baseline-001"
	if created, err := repository.SaveEvent(ctx, stillOpen); err != nil || !created {
		t.Fatalf("open run must remain writable after another run froze: created=%v err=%v", created, err)
	}
}

func TestPostgresFrozenSnapshotIsImmutable(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")
	if created, err := repository.SaveEvent(ctx, integrationEvent("snapshot-seed", "actor-a", "packed")); err != nil || !created {
		t.Fatalf("seed event: created=%v err=%v", created, err)
	}
	if _, _, err := repository.FreezeExperiment(ctx, "festival-guided-001", "festival-thesis-demo"); err != nil {
		t.Fatal(err)
	}

	for _, statement := range []string{
		`update experiment_metrics set definition_version='tampered'`,
		`delete from experiment_metrics`,
	} {
		if _, err := repository.db.Exec(statement); err == nil {
			t.Fatalf("%q must be rejected", statement)
		} else if !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("expected immutability rejection for %q, got %v", statement, err)
		}
	}
	// Unfreezing and identity changes must also be refused.
	for _, statement := range []string{
		`update experiment_runs set frozen_at=null where id='festival-guided-001'`,
		`update experiment_runs set mode='baseline' where id='festival-guided-001'`,
	} {
		if _, err := repository.db.Exec(statement); err == nil {
			t.Fatalf("%q must be rejected", statement)
		}
	}
	assertTableCount(t, repository.db, "experiment_metrics", 1)
}

func TestPostgresFrozenReceiptSurvivesRestart(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	registerIntegrationBuild(t, repository, "festival-thesis-demo", "festnest-local-browser", "festnest-demo-001", 1, "")
	createIntegrationRun(t, repository, "festival-guided-001", "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser")
	if created, err := repository.SaveEvent(ctx, integrationEvent("restart-seed", "actor-a", "packed")); err != nil || !created {
		t.Fatalf("seed event: created=%v err=%v", created, err)
	}
	before, _, err := repository.FreezeExperiment(ctx, "festival-guided-001", "festival-thesis-demo")
	if err != nil {
		t.Fatal(err)
	}
	repository.Close()

	restarted, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, err := restarted.Metrics(ctx, "festival-guided-001", "festival-thesis-demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.InputReceipt.SHA256 != before.InputReceipt.SHA256 {
		t.Fatalf("receipt digest changed across restart: %s vs %s", after.InputReceipt.SHA256, before.InputReceipt.SHA256)
	}
	if after.Status != "frozen" || after.FrozenAt == nil {
		t.Fatalf("run must still read frozen after restart: status=%q frozenAt=%v", after.Status, after.FrozenAt)
	}
	if after.DefinitionVersion != before.DefinitionVersion {
		t.Fatalf("definition version drifted: %s vs %s", after.DefinitionVersion, before.DefinitionVersion)
	}
}

func TestPostgresLegacyCandidateIDConflict(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	tenantID := "festival-thesis-demo"
	buildID := "festnest-demo-001"
	envID := "festnest-local-browser"
	runID := "festival-guided-001"
	role := "viewer"
	fingerprint := "supply-list:item:10000000-0000-4000-8000-000000000101:status:packed"

	// 1. Pre-0005 legacy candidate ID (hashed WITHOUT run_id)
	legacyKey := strings.Join([]string{tenantID, buildID, envID, role, fingerprint}, "|")
	digest := sha256.Sum256([]byte(legacyKey))
	legacyID := fmt.Sprintf("state-%x", digest[:12])

	registerIntegrationBuild(t, repository, tenantID, envID, buildID, 1, "")
	createIntegrationRun(t, repository, runID, tenantID, buildID, envID)

	// Insert legacy candidate row into PostgreSQL and memory
	repository.memory.states[legacyID] = model.CandidateState{
		ID: legacyID, RunID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: envID,
		Role: role, Module: "supply-list", RecordID: "10000000-0000-4000-8000-000000000101", BusinessState: "packed",
		StateFingerprint: fingerprint, Status: "strongly_represented", EvidenceCount: 1, IndependentActorCount: 1,
		SourceTypes: []string{"human"}, Conflict: false, UpdatedAt: time.Now().UTC(),
	}
	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'strongly_represented', 1, 1, '["human"]', false, now())
	`, legacyID, runID, tenantID, buildID, envID, role, "supply-list", "10000000-0000-4000-8000-000000000101", "packed", fingerprint)
	if err != nil {
		t.Fatalf("failed to insert legacy candidate row: %v", err)
	}

	// 2. Incoming event for the same scope/fingerprint
	event := model.ValidationEvent{
		EventID: "legacy-conflict-event-001", TenantID: tenantID, RunID: runID, ActorID: "actor-a",
		SourceType: "human", SourceAdapter: "chrome-mv3", BuildID: buildID, EnvironmentID: envID,
		Context: map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}, Route: "/trips/demo/supply-list", StateFingerprint: fingerprint,
		ActionType: "control_activated", Outcome: "pass", Privacy: model.PrivacyMetadata{Masked: true},
	}

	// 3. SaveEvent must succeed cleanly without SQLSTATE 23505 or 23503
	created, err := repository.SaveEvent(ctx, event)
	if err != nil || !created {
		t.Fatalf("SaveEvent failed: created=%v err=%v", created, err)
	}

	// 4. Exactly one candidate row exists for the canonical run-scoped fingerprint
	var candidateCount int
	if err := repository.db.QueryRowContext(ctx, `select count(*) from candidate_states where run_id=$1 and tenant_id=$2 and build_id=$3 and environment_id=$4 and role=$5 and state_fingerprint=$6`, runID, tenantID, buildID, envID, role, fingerprint).Scan(&candidateCount); err != nil {
		t.Fatalf("failed candidate count: %v", err)
	}
	if candidateCount != 1 {
		t.Fatalf("expected exactly 1 candidate state row, got %d", candidateCount)
	}

	// 5. Existing legacy candidate ID remains authoritative; new run-aware ID is NOT inserted
	var actualCandidateID string
	if err := repository.db.QueryRowContext(ctx, `select id from candidate_states where run_id=$1 and tenant_id=$2 and build_id=$3 and environment_id=$4 and role=$5 and state_fingerprint=$6`, runID, tenantID, buildID, envID, role, fingerprint).Scan(&actualCandidateID); err != nil {
		t.Fatalf("failed candidate id query: %v", err)
	}
	if actualCandidateID != legacyID {
		t.Fatalf("expected authoritative candidate ID %s, got %s", legacyID, actualCandidateID)
	}

	// 6. Evidence record references the legacy candidate ID
	var evidenceCandidateID string
	if err := repository.db.QueryRowContext(ctx, `select candidate_state_id from evidence_records where event_id=$1`, event.EventID).Scan(&evidenceCandidateID); err != nil {
		t.Fatalf("failed evidence record query: %v", err)
	}
	if evidenceCandidateID != legacyID {
		t.Fatalf("expected evidence candidate_state_id %s, got %s", legacyID, evidenceCandidateID)
	}

	// 7. Verification request references the legacy candidate ID (if derived)
	var requestCandidateID string
	err = repository.db.QueryRowContext(ctx, `select candidate_state_id from verification_requests where run_id=$1 limit 1`, runID).Scan(&requestCandidateID)
	if err == nil && requestCandidateID != legacyID {
		t.Fatalf("expected verification_request candidate_state_id %s, got %s", legacyID, requestCandidateID)
	}

	// Boundary Case 1: Cross-run isolation
	runID2 := "festival-baseline-001"
	createIntegrationRun(t, repository, runID2, tenantID, buildID, envID)
	eventRun2 := model.ValidationEvent{
		EventID: "legacy-crossrun-event-002", TenantID: tenantID, RunID: runID2, ActorID: "actor-a",
		SourceType: "human", SourceAdapter: "chrome-mv3", BuildID: buildID, EnvironmentID: envID,
		Context: map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}, Route: "/trips/demo/supply-list", StateFingerprint: fingerprint,
		ActionType: "control_activated", Outcome: "pass", Privacy: model.PrivacyMetadata{Masked: true},
	}
	if created2, err := repository.SaveEvent(ctx, eventRun2); err != nil || !created2 {
		t.Fatalf("SaveEvent for run 2 failed: created=%v err=%v", created2, err)
	}
	var run2CandidateID string
	if err := repository.db.QueryRowContext(ctx, `select id from candidate_states where run_id=$1 and state_fingerprint=$2`, runID2, fingerprint).Scan(&run2CandidateID); err != nil {
		t.Fatalf("failed run 2 candidate id query: %v", err)
	}
	if run2CandidateID == legacyID {
		t.Fatalf("cross-run isolation broken: run 2 reused run 1's legacy candidate ID %s", legacyID)
	}

	// Boundary Case 2: Truly novel state
	novelFingerprint := "route:/trips/demo/camp-grid:control:button:start-building"
	novelEvent := model.ValidationEvent{
		EventID: "novel-event-003", TenantID: tenantID, RunID: runID, ActorID: "actor-a",
		SourceType: "human", SourceAdapter: "chrome-mv3", BuildID: buildID, EnvironmentID: envID,
		Context: map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}, Route: "/trips/demo/camp-grid", StateFingerprint: novelFingerprint,
		ActionType: "control_activated", Outcome: "pass", Privacy: model.PrivacyMetadata{Masked: true},
	}
	if created3, err := repository.SaveEvent(ctx, novelEvent); err != nil || !created3 {
		t.Fatalf("SaveEvent for novel state failed: created=%v err=%v", created3, err)
	}
	var novelCandidateID string
	expectedNovelID := state.New().Identify(novelEvent).ID
	if err := repository.db.QueryRowContext(ctx, `select id from candidate_states where run_id=$1 and state_fingerprint=$2`, runID, novelFingerprint).Scan(&novelCandidateID); err != nil {
		t.Fatalf("failed novel candidate id query: %v", err)
	}
	if novelCandidateID != expectedNovelID {
		t.Fatalf("novel state derivation failed: expected %s, got %s", expectedNovelID, novelCandidateID)
	}
}

func TestPostgresCandidateHistoryBaselines(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	tenantID, buildID, envID, runID, role := "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser", "festival-guided-001", "viewer"
	registerIntegrationBuild(t, repository, tenantID, envID, buildID, 1, "")
	createIntegrationRun(t, repository, runID, tenantID, buildID, envID)

	// Case C: Zero replayable history in validation_events
	zeroID := "state-zero-history-001"
	zeroFP := "supply-list:item:zero-history:status:packed"
	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, 'supply-list', 'zero-history', 'packed', $7, 'strongly_represented', 5, 2, '["human","automation"]', false, now())
	`, zeroID, runID, tenantID, buildID, envID, role, zeroFP)
	if err != nil {
		t.Fatalf("failed to insert zero history row: %v", err)
	}

	// Case B: Partial replayable history (persisted count=10, 2 events in DB)
	partialID := "state-partial-history-002"
	partialFP := "supply-list:item:partial-history:status:packed"
	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, 'supply-list', 'partial-history', 'packed', $7, 'strongly_represented', 10, 3, '["human","automation"]', false, now())
	`, partialID, runID, tenantID, buildID, envID, role, partialFP)
	if err != nil {
		t.Fatalf("failed to insert partial history row: %v", err)
	}

	e1 := integrationEvent("partial-e1", "actor-1", "packed")
	e1.StateFingerprint = partialFP
	if _, err := repository.SaveEvent(ctx, e1); err != nil {
		t.Fatal(err)
	}
	e2 := integrationEvent("partial-e2", "actor-2", "packed")
	e2.StateFingerprint = partialFP
	if _, err := repository.SaveEvent(ctx, e2); err != nil {
		t.Fatal(err)
	}

	// Restart repository
	repository.Close()
	restarted, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	defer restarted.Close()

	// Assert zero-history candidate is NOT in memory.states on startup
	if _, exists := restarted.memory.states[zeroID]; exists {
		t.Fatalf("zero-history candidate %s should not be preloaded into memory.states on startup", zeroID)
	}

	// Ingest new event for zero-history candidate
	newEventZero := integrationEvent("zero-new-001", "actor-3", "packed")
	newEventZero.StateFingerprint = zeroFP
	newEventZero.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if created, err := restarted.SaveEvent(ctx, newEventZero); err != nil || !created {
		t.Fatalf("SaveEvent for zero-history failed: created=%v err=%v", created, err)
	}

	// Zero history aggregate count must be 5+1 = 6, preserving legacy ID and status
	zeroState, exists := restarted.memory.states[zeroID]
	if !exists {
		t.Fatalf("zero history state %s missing from memory.states after SaveEvent", zeroID)
	}
	if zeroState.EvidenceCount != 6 {
		t.Fatalf("expected evidence count 6 for zero-history state after new event, got %d", zeroState.EvidenceCount)
	}
	if zeroState.IndependentActorCount != 2 {
		t.Fatalf("expected actor count 2 preserved for zero-history state, got %d", zeroState.IndependentActorCount)
	}

	// Ingest new event for partial-history candidate
	newEventPartial := integrationEvent("partial-new-003", "actor-3", "packed")
	newEventPartial.StateFingerprint = partialFP
	newEventPartial.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if created, err := restarted.SaveEvent(ctx, newEventPartial); err != nil || !created {
		t.Fatalf("SaveEvent for partial-history failed: created=%v err=%v", created, err)
	}

	// Partial history aggregate count must be 10+1 = 11 (not collapsed to 3+1=4)
	partialState, exists := restarted.memory.states[partialID]
	if !exists {
		t.Fatalf("partial history state %s missing from memory.states", partialID)
	}
	if partialState.EvidenceCount != 11 {
		t.Fatalf("expected evidence count 11 for partial-history state after new event, got %d", partialState.EvidenceCount)
	}
}

func TestPostgresCollisionGuardsAndIndexCoherence(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	tenantID, buildID, envID, runID := "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser", "festival-guided-001"
	registerIntegrationBuild(t, repository, tenantID, envID, buildID, 1, "")
	createIntegrationRun(t, repository, runID, tenantID, buildID, envID)

	e1 := integrationEvent("collision-e1", "actor-a", "packed")
	e1.StateFingerprint = "supply-list:item:item-a:status:packed"
	if _, err := repository.SaveEvent(ctx, e1); err != nil {
		t.Fatal(err)
	}

	e2 := integrationEvent("collision-e2", "actor-b", "unassigned")
	e2.StateFingerprint = "supply-list:item:item-b:status:unassigned"
	if _, err := repository.SaveEvent(ctx, e2); err != nil {
		t.Fatal(err)
	}

	evidence, _ := repository.Evidence(ctx, Scope{RunID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: envID})
	if len(evidence.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(evidence.States))
	}

	var stateA, stateB model.CandidateState
	for _, s := range evidence.States {
		if s.StateFingerprint == e1.StateFingerprint {
			stateA = s
		} else if s.StateFingerprint == e2.StateFingerprint {
			stateB = s
		}
	}
	if stateA.ID == "" || stateB.ID == "" {
		t.Fatalf("expected states A and B to exist, got A=%q B=%q", stateA.ID, stateB.ID)
	}

	// Rename stateA to stateB's fingerprint -> MUST BE REJECTED BY COLLISION GUARD

	err = repository.CorrectState(ctx, "rename", stateA.ID, model.StateCorrectionRequest{
		NewFingerprint: e2.StateFingerprint, NewBusinessState: "unassigned", ActorID: "tester", Reason: "conflict test",
	})
	if err == nil || !strings.Contains(err.Error(), "renamed state already exists") {
		t.Fatalf("expected 'renamed state already exists', got err=%v", err)
	}

	// Split stateA into stateB's fingerprint -> MUST BE REJECTED BY COLLISION GUARD
	err = repository.CorrectState(ctx, "split", stateA.ID, model.StateCorrectionRequest{
		NewFingerprint: e2.StateFingerprint, NewBusinessState: "unassigned", EventIDs: []string{e1.EventID}, ActorID: "tester", Reason: "split collision test",
	})
	if err == nil || !strings.Contains(err.Error(), "split target state already exists") {
		t.Fatalf("expected 'split target state already exists', got err=%v", err)
	}
}

func TestPostgresConflictInvariantInPartialZeroHistory(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	tenantID, buildID, envID, runID, role := "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser", "festival-guided-001", "viewer"
	registerIntegrationBuild(t, repository, tenantID, envID, buildID, 1, "")
	createIntegrationRun(t, repository, runID, tenantID, buildID, envID)

	conflictID := "state-conflict-baseline-001"
	conflictFP := "supply-list:item:conflict-baseline:status:packed"

	// Insert baseline with Conflict=true and status='conflicting'
	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, 'supply-list', 'conflict-baseline', 'packed', $7, 'conflicting', 5, 2, '["human"]', true, now())
	`, conflictID, runID, tenantID, buildID, envID, role, conflictFP)
	if err != nil {
		t.Fatalf("insert conflict baseline: %v", err)
	}

	repository.Close()
	restarted, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	// Ingest a passing, non-conflicting new live event
	liveEvent := integrationEvent("live-non-conflict", "actor-9", "packed")
	liveEvent.StateFingerprint = conflictFP
	liveEvent.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if created, err := restarted.SaveEvent(ctx, liveEvent); err != nil || !created {
		t.Fatalf("SaveEvent failed: created=%v err=%v", created, err)
	}

	state, ok := restarted.memory.states[conflictID]
	if !ok {
		t.Fatalf("state %s missing after SaveEvent", conflictID)
	}

	// CONFLICT INVARIANT: Conflict=true and Status=conflicting must NEVER disagree
	if !state.Conflict || state.Status != "conflicting" {
		t.Fatalf("conflict invariant violated: Conflict=%v Status=%q (expected Conflict=true Status=conflicting)", state.Conflict, state.Status)
	}
}

func TestPostgresCorrectionsOverIncompleteBaselines(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	tenantID, buildID, envID, runID, role := "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser", "festival-guided-001", "viewer"
	registerIntegrationBuild(t, repository, tenantID, envID, buildID, 1, "")
	createIntegrationRun(t, repository, runID, tenantID, buildID, envID)

	sourceFP := "supply-list:item:inc-source:status:packed"
	targetFP := "supply-list:item:inc-target:status:unassigned"
	sourceID := state.New().IDFor(runID, tenantID, buildID, envID, role, sourceFP)
	targetID := state.New().IDFor(runID, tenantID, buildID, envID, role, targetFP)

	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, 'supply-list', 'inc-source', 'packed', $7, 'strongly_represented', 10, 2, '["human"]', false, now())
	`, sourceID, runID, tenantID, buildID, envID, role, sourceFP)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, 'supply-list', 'inc-target', 'unassigned', $7, 'needs_independent_verification', 4, 1, '["automation"]', false, now())
	`, targetID, runID, tenantID, buildID, envID, role, targetFP)
	if err != nil {
		t.Fatal(err)
	}

	repository.Close()
	restarted, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	// Ingest 2 events into source so split attempt leaves evidence in source state
	e1 := integrationEvent("inc-e1", "actor-1", "packed")
	e1.StateFingerprint = sourceFP
	e1.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := restarted.SaveEvent(ctx, e1); err != nil {
		t.Fatal(err)
	}
	e1b := integrationEvent("inc-e1b", "actor-1b", "packed")
	e1b.StateFingerprint = sourceFP
	e1b.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := restarted.SaveEvent(ctx, e1b); err != nil {
		t.Fatal(err)
	}

	// 1. SPLIT on non-replayable baseline MUST FAIL CLOSED
	err = restarted.CorrectState(ctx, "split", sourceID, model.StateCorrectionRequest{
		NewFingerprint: "supply-list:item:inc-source-split:status:reviewed", NewBusinessState: "reviewed", EventIDs: []string{e1b.EventID}, ActorID: "tester", Reason: "split attempt",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot split candidate with non-replayable historical evidence baseline") {
		t.Fatalf("expected fail closed on split of incomplete baseline, got err=%v", err)
	}

	// Ingest 1 event into target so it enters memory.states
	e2 := integrationEvent("inc-e2", "actor-2", "unassigned")
	e2.StateFingerprint = targetFP
	e2.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := restarted.SaveEvent(ctx, e2); err != nil {
		t.Fatal(err)
	}

	// 2. MERGE combining incomplete baselines (source 10 + target 4 + 3 live events = 17)
	err = restarted.CorrectState(ctx, "merge", sourceID, model.StateCorrectionRequest{
		TargetStateID: targetID, ActorID: "tester", Reason: "merge incomplete baselines",
	})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	targetState, exists := restarted.memory.states[targetID]
	if !exists {
		t.Fatalf("merged target state %s missing", targetID)
	}
	if targetState.EvidenceCount != 17 {
		t.Fatalf("expected merged EvidenceCount == 17, got %d", targetState.EvidenceCount)
	}

	// 3. RENAME preserving incomplete baseline
	renamedFP := "supply-list:item:inc-renamed:status:reviewed"
	renamedID := state.New().IDFor(runID, tenantID, buildID, envID, role, renamedFP)
	err = restarted.CorrectState(ctx, "rename", targetID, model.StateCorrectionRequest{
		NewFingerprint: renamedFP, NewBusinessState: "reviewed", ActorID: "tester", Reason: "rename test",
	})
	if err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	renamedState, exists := restarted.memory.states[renamedID]
	if !exists {
		t.Fatalf("renamed state %s missing from memory.states", renamedID)
	}
	if renamedState.EvidenceCount != 17 {
		t.Fatalf("expected preserved baseline EvidenceCount == 17 on rename, got %d", renamedState.EvidenceCount)
	}

	// 4. RESTART and assert renamed candidate uses renamed canonical ID, has EvidenceCount == 17, and no duplicate old candidate
	restarted.Close()
	restarted2, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("restart after rename failed: %v", err)
	}
	defer restarted2.Close()

	postEvidence, err := restarted2.Evidence(ctx, Scope{RunID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: envID})
	if err != nil {
		t.Fatalf("Evidence query after restart failed: %v", err)
	}

	var foundRenamed bool
	for _, s := range postEvidence.States {
		if s.ID == sourceID || s.ID == targetID {
			t.Fatalf("found duplicate old candidate %s after rename and restart", s.ID)
		}
		if s.ID == renamedID {
			foundRenamed = true
			if s.EvidenceCount != 17 {
				t.Fatalf("expected EvidenceCount == 17 after restart for renamed candidate, got %d", s.EvidenceCount)
			}
		}
	}
	if !foundRenamed {
		t.Fatalf("renamed candidate %s missing from evidence after restart", renamedID)
	}
}

func TestPostgresThresholdCrossingAndSplitProtection(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	repository, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	tenantID, buildID, envID, runID, role := "festival-thesis-demo", "festnest-demo-001", "festnest-local-browser", "festival-guided-001", "viewer"
	registerIntegrationBuild(t, repository, tenantID, envID, buildID, 1, "")
	createIntegrationRun(t, repository, runID, tenantID, buildID, envID)

	threshID := "state-thresh-001"
	threshFP := "supply-list:item:thresh:status:packed"

	// Baseline EvidenceCount = 10, startup replayable events = 2
	_, err = repository.db.ExecContext(ctx, `
		insert into candidate_states (id, run_id, tenant_id, build_id, environment_id, role, module, record_id, business_state, state_fingerprint, status, evidence_count, independent_actor_count, source_types, conflict, updated_at)
		values ($1, $2, $3, $4, $5, $6, 'supply-list', 'thresh', 'packed', $7, 'strongly_represented', 10, 2, '["human"]', false, now())
	`, threshID, runID, tenantID, buildID, envID, role, threshFP)
	if err != nil {
		t.Fatal(err)
	}

	repository.Close()
	repository, err = NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	// Ingest 2 events prior to restart (replayed at startup)
	e1 := integrationEvent("thresh-replayed-1", "actor-1", "packed")
	e1.StateFingerprint = threshFP
	e1.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := repository.SaveEvent(ctx, e1); err != nil {
		t.Fatal(err)
	}
	e2 := integrationEvent("thresh-replayed-2", "actor-2", "packed")
	e2.StateFingerprint = threshFP
	e2.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := repository.SaveEvent(ctx, e2); err != nil {
		t.Fatal(err)
	}

	// Reset DB baseline count to 10 for threshID to simulate legacy baseline with 2 replayable events
	if _, err := repository.db.ExecContext(ctx, `update candidate_states set evidence_count = 10 where id = $1`, threshID); err != nil {
		t.Fatal(err)
	}

	// Restart -> baselineReplayedEvents[threshID] = 2
	repository.Close()
	restarted, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	// Add 8 live events after restart (total stateEvents = 2 replayed + 8 live = 10)
	var lastLiveID string
	for i := 1; i <= 8; i++ {
		live := integrationEvent(fmt.Sprintf("thresh-live-%d", i), fmt.Sprintf("actor-live-%d", i), "packed")
		live.StateFingerprint = threshFP
		live.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
		if _, err := restarted.SaveEvent(ctx, live); err != nil {
			t.Fatal(err)
		}
		lastLiveID = live.EventID
	}

	// Expected EvidenceCount = 10 (baseline) + 8 (live) = 18, NOT 10!
	state, exists := restarted.memory.states[threshID]
	if !exists {
		t.Fatalf("threshold state %s missing", threshID)
	}
	if state.EvidenceCount != 18 {
		t.Fatalf("expected EvidenceCount=18 after 8 live events, got %d", state.EvidenceCount)
	}

	// Add 1 more live event (total live = 9)
	live9 := integrationEvent("thresh-live-9", "actor-live-9", "packed")
	live9.StateFingerprint = threshFP
	live9.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := restarted.SaveEvent(ctx, live9); err != nil {
		t.Fatal(err)
	}

	state = restarted.memory.states[threshID]
	if state.EvidenceCount != 19 {
		t.Fatalf("expected EvidenceCount=19 after 9 live events, got %d", state.EvidenceCount)
	}

	// SPLIT after threshold crossing MUST STILL FAIL CLOSED
	err = restarted.CorrectState(ctx, "split", threshID, model.StateCorrectionRequest{
		NewFingerprint: "supply-list:item:thresh-split:status:reviewed", NewBusinessState: "reviewed", EventIDs: []string{lastLiveID}, ActorID: "tester", Reason: "split attempt after threshold crossing",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot split candidate with non-replayable historical evidence baseline") {
		t.Fatalf("expected split to fail closed after threshold crossing, got err=%v", err)
	}

	// Verify complete-history candidate still uses normal replay-derived aggregation
	completeFP := "supply-list:item:complete:status:packed"
	ce1 := integrationEvent("comp-e1", "actor-c1", "packed")
	ce1.StateFingerprint = completeFP
	ce1.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := restarted.SaveEvent(ctx, ce1); err != nil {
		t.Fatal(err)
	}

	// Restart -> complete candidate has baseline.EvidenceCount = 1, baselineReplayedEvents = 1 -> complete history (baselineIncomplete = false)
	restarted.Close()
	restarted2, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted2.Close()

	ce2 := integrationEvent("comp-e2", "actor-c2", "packed")
	ce2.StateFingerprint = completeFP
	ce2.Context = map[string]string{"role": role, "scenario_id": "equipment-handoff-v1"}
	if _, err := restarted2.SaveEvent(ctx, ce2); err != nil {
		t.Fatal(err)
	}

	evidence, _ := restarted2.Evidence(ctx, Scope{RunID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: envID})
	var compState model.CandidateState
	for _, s := range evidence.States {
		if s.StateFingerprint == completeFP {
			compState = s
		}
	}
	if compState.ID == "" {
		t.Fatalf("complete state missing")
	}
	// Replay-derived aggregation for complete history: 2 total events -> EvidenceCount = 2
	if compState.EvidenceCount != 2 {
		t.Fatalf("expected EvidenceCount=2 for complete history candidate, got %d", compState.EvidenceCount)
	}
}

func TestPostgresClaimEvidenceLedgerAndReconciliation(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	store, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tenantID := "tenant-claim-test"
	envID := "staging"
	build1 := "build-v1"
	build2 := "build-v2"
	run1 := "run-claim-001"
	run2 := "run-claim-002"

	// Register Build 1 & 2
	if _, _, err := store.RegisterBuild(ctx, model.BuildRegistration{TenantID: tenantID, EnvironmentID: envID, BuildID: build1, SequenceNumber: 1}, "controller"); err != nil {
		t.Fatalf("register build1: %v", err)
	}
	if _, _, err := store.RegisterBuild(ctx, model.BuildRegistration{TenantID: tenantID, EnvironmentID: envID, BuildID: build2, SequenceNumber: 2, PredecessorBuildID: build1}, "controller"); err != nil {
		t.Fatalf("register build2: %v", err)
	}

	// Create Experiments
	if _, _, err := store.CreateExperiment(ctx, model.ExperimentRegistration{ID: run1, TenantID: tenantID, BuildID: build1, EnvironmentID: envID, ScenarioID: "scenario-1", Mode: "guided"}, "controller"); err != nil {
		t.Fatalf("create experiment run1: %v", err)
	}
	if _, _, err := store.CreateExperiment(ctx, model.ExperimentRegistration{ID: run2, TenantID: tenantID, BuildID: build2, EnvironmentID: envID, ScenarioID: "scenario-1", Mode: "guided"}, "controller"); err != nil {
		t.Fatalf("create experiment run2: %v", err)
	}

	// 1. Create Claim Definition (Reusable expected behavior across builds)
	claim1 := model.ClaimDefinition{
		ID:               "claim-fest-001",
		TenantID:         tenantID,
		Title:            "Participant access is revoked after removal",
		Description:      "When an editor removes a participant, their access is revoked",
		Target:           "participant_access_card",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	savedClaim, created, err := store.SaveClaimDefinition(ctx, claim1)
	if err != nil || !created {
		t.Fatalf("save claim1: created=%v, err=%v", created, err)
	}
	if savedClaim.ID != claim1.ID {
		t.Fatalf("expected claim ID %s, got %s", claim1.ID, savedClaim.ID)
	}

	// Idempotent resave
	_, created, err = store.SaveClaimDefinition(ctx, claim1)
	if err != nil || created {
		t.Fatalf("expected idempotent resave, created=%v, err=%v", created, err)
	}

	// 2. Untouched Summary Check
	scope1 := Scope{RunID: run1, TenantID: tenantID, BuildID: build1, EnvironmentID: envID}
	summary1, err := store.GetClaimEvidenceSummary(ctx, scope1, claim1.ID)
	if err != nil {
		t.Fatalf("get untouched summary: %v", err)
	}
	if summary1.Classification != "Untouched" {
		t.Fatalf("expected Untouched, got %s", summary1.Classification)
	}

	// 3. Save Evidence Context for Run 1 / Build 1
	ctx1 := model.EvidenceContext{
		ID:            "ctx-fest-001",
		TenantID:      tenantID,
		RunID:         run1,
		BuildID:       build1,
		EnvironmentID: envID,
		ScenarioID:    "scenario-1",
		FeatureFlags:  map[string]string{"camp_grid_v2": "true"},
		Device:        "desktop",
		Region:        "us-east-1",
	}
	if _, _, err := store.SaveEvidenceContext(ctx, ctx1); err != nil {
		t.Fatalf("save evidence context: %v", err)
	}

	// 4. Save Raw Validation Event + Observation 1 (Human shallow DOM click)
	event1 := integrationEvent("event-claim-001", "editor-alice", "revoked")
	event1.TenantID = tenantID
	event1.RunID = run1
	event1.BuildID = build1
	event1.EnvironmentID = envID
	event1.Context = map[string]string{"role": "editor", "scenario_id": "scenario-1"}
	if _, err := store.SaveEvent(ctx, event1); err != nil {
		t.Fatalf("save event1: %v", err)
	}

	obs1 := model.Observation{
		ID:                "obs-claim-001",
		TenantID:          tenantID,
		RunID:             run1,
		ClaimID:           claim1.ID,
		EvidenceContextID: ctx1.ID,
		EventID:           event1.EventID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-alice",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}
	if _, _, err := store.SaveObservation(ctx, obs1); err != nil {
		t.Fatalf("save observation1: %v", err)
	}

	// 5. Check Shallow Evidence -> Classification = Weak
	summaryWeak, err := store.GetClaimEvidenceSummary(ctx, scope1, claim1.ID)
	if err != nil {
		t.Fatalf("get weak summary: %v", err)
	}
	if summaryWeak.Classification != "Weak" {
		t.Fatalf("expected Weak for shallow human click, got %s", summaryWeak.Classification)
	}
	if summaryWeak.MaxVerificationDepth != 1 {
		t.Fatalf("expected depth 1, got %d", summaryWeak.MaxVerificationDepth)
	}

	// 6. Test Duplicate Same-Source Protection (10 repeated clicks from different actors using same chrome-mv3 adapter without backend verifier)
	for i := 2; i <= 10; i++ {
		evt := integrationEvent(fmt.Sprintf("event-dup-%d", i), fmt.Sprintf("editor-%d", i), "revoked")
		evt.TenantID = tenantID
		evt.RunID = run1
		evt.BuildID = build1
		evt.EnvironmentID = envID
		evt.Context = map[string]string{"role": "editor", "scenario_id": "scenario-1"}
		if _, err := store.SaveEvent(ctx, evt); err != nil {
			t.Fatalf("save dup event %d: %v", i, err)
		}
		obs := model.Observation{
			ID:                fmt.Sprintf("obs-dup-%d", i),
			TenantID:          tenantID,
			RunID:             run1,
			ClaimID:           claim1.ID,
			EvidenceContextID: ctx1.ID,
			EventID:           evt.EventID,
			SourceType:        "human",
			SourceAdapter:     "chrome-mv3",
			ActorID:           fmt.Sprintf("editor-%d", i),
			Assessment:        "supports",
			OccurredAt:        time.Now().UTC(),
		}
		if _, _, err := store.SaveObservation(ctx, obs); err != nil {
			t.Fatalf("save dup obs %d: %v", i, err)
		}
	}

	summaryDup, err := store.GetClaimEvidenceSummary(ctx, scope1, claim1.ID)
	if err != nil {
		t.Fatalf("get summary after dups: %v", err)
	}
	if summaryDup.Classification != "Weak" {
		t.Fatalf("10 repeated same-oracle observations must remain Weak, got %s", summaryDup.Classification)
	}
	if summaryDup.IndependentSourceCount != 1 {
		t.Fatalf("same shallow client oracle should count as 1 independent source, got %d", summaryDup.IndependentSourceCount)
	}

	// 7. Add Depth-3 Backend Persistence Verifier -> Classification = Solid
	obsDeep := model.Observation{
		ID:                "obs-deep-001",
		TenantID:          tenantID,
		RunID:             run1,
		ClaimID:           claim1.ID,
		EvidenceContextID: ctx1.ID,
		SourceType:        "automation",
		SourceAdapter:     "activity-log",
		ActorID:           "system",
		Assessment:        "supports",
		VerifierResult:    "database persistence check confirms access revoked",
		OccurredAt:        time.Now().UTC(),
	}
	if _, _, err := store.SaveObservation(ctx, obsDeep); err != nil {
		t.Fatalf("save obsDeep: %v", err)
	}

	summarySolid, err := store.GetClaimEvidenceSummary(ctx, scope1, claim1.ID)
	if err != nil {
		t.Fatalf("get solid summary: %v", err)
	}
	if summarySolid.Classification != "Solid" {
		t.Fatalf("expected Solid after depth-3 verifier corroboration, got %s", summarySolid.Classification)
	}
	if summarySolid.MaxVerificationDepth != 3 {
		t.Fatalf("expected depth 3, got %d", summarySolid.MaxVerificationDepth)
	}
	if summarySolid.IndependentSourceCount < 2 {
		t.Fatalf("expected at least 2 independent sources, got %d", summarySolid.IndependentSourceCount)
	}

	// 8. Test Incompatible/Older Build -> Classification = Stale
	scope2 := Scope{RunID: run2, TenantID: tenantID, BuildID: build2, EnvironmentID: envID}
	summaryStale, err := store.GetClaimEvidenceSummary(ctx, scope2, claim1.ID)
	if err != nil {
		t.Fatalf("get stale summary: %v", err)
	}
	// For run2, observations belong to build1 -> stale
	if summaryStale.Classification != "Stale" && summaryStale.Classification != "Untouched" {
		t.Fatalf("expected Stale or Untouched across runs/builds, got %s", summaryStale.Classification)
	}

	// 9. Test Conflicted Classification (Add a contradictory observation to claim2)
	claim2 := model.ClaimDefinition{
		ID:               "claim-fest-002",
		TenantID:         tenantID,
		Title:            "Item count decrements after remove",
		Target:           "item_counter",
		StateSignature:   "decremented",
		ActorContext:     "editor",
		ValidationIntent: "verify_count",
	}
	if _, _, err := store.SaveClaimDefinition(ctx, claim2); err != nil {
		t.Fatalf("save claim2: %v", err)
	}

	obsSupport := model.Observation{
		ID:                "obs-c2-support",
		TenantID:          tenantID,
		RunID:             run1,
		ClaimID:           claim2.ID,
		EvidenceContextID: ctx1.ID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "editor-alice",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}
	obsConflict := model.Observation{
		ID:                "obs-c2-conflict",
		TenantID:          tenantID,
		RunID:             run1,
		ClaimID:           claim2.ID,
		EvidenceContextID: ctx1.ID,
		SourceType:        "automation",
		SourceAdapter:     "database",
		ActorID:           "system",
		Assessment:        "invalidates",
		VerifierResult:    "database row count was not decremented",
		OccurredAt:        time.Now().UTC(),
	}
	if _, _, err := store.SaveObservation(ctx, obsSupport); err != nil {
		t.Fatalf("save obsSupport: %v", err)
	}
	if _, _, err := store.SaveObservation(ctx, obsConflict); err != nil {
		t.Fatalf("save obsConflict: %v", err)
	}

	summaryConflicted, err := store.GetClaimEvidenceSummary(ctx, scope1, claim2.ID)
	if err != nil {
		t.Fatalf("get conflicted summary: %v", err)
	}
	if summaryConflicted.Classification != "Conflicted" {
		t.Fatalf("expected Conflicted, got %s", summaryConflicted.Classification)
	}
	if summaryConflicted.ConflictCount != 1 {
		t.Fatalf("expected conflict count 1, got %d", summaryConflicted.ConflictCount)
	}
	if summaryConflicted.HeuristicScore != nil {
		t.Fatalf("conflicted summary must have nil heuristic score, got %v", *summaryConflicted.HeuristicScore)
	}
	if len(summaryConflicted.Reasons) == 0 {
		t.Fatalf("expected conflict reasons text")
	}

	// 10. Test Restart Durability
	store.Close()
	reopened, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer reopened.Close()

	listSummaries, err := reopened.ListClaimEvidenceSummaries(ctx, scope1)
	if err != nil {
		t.Fatalf("list summaries after restart: %v", err)
	}
	if len(listSummaries) != 2 {
		t.Fatalf("expected 2 claim summaries after restart, got %d", len(listSummaries))
	}
	for _, s := range listSummaries {
		if s.ClaimID == claim1.ID && s.Classification != "Solid" {
			t.Fatalf("expected claim1 to remain Solid after restart, got %s", s.Classification)
		}
		if s.ClaimID == claim2.ID && s.Classification != "Conflicted" {
			t.Fatalf("expected claim2 to remain Conflicted after restart, got %s", s.Classification)
		}
	}
}

func TestPostgresInformationValueRouterParityIdempotencyAndHistory(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	postgres, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	memory := NewMemory()

	seed := func(repository Repository) {
		t.Helper()
		if _, _, err := repository.RegisterBuild(ctx, model.BuildRegistration{
			TenantID: "tenant-router-parity", EnvironmentID: "local", BuildID: "build-router", SequenceNumber: 1,
		}, "controller"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.CreateExperiment(ctx, model.ExperimentRegistration{
			ID: "run-router-parity", TenantID: "tenant-router-parity", BuildID: "build-router",
			EnvironmentID: "local", ScenarioID: "equipment-handoff-v1", Mode: "guided",
		}, "controller"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.SaveClaimDefinition(ctx, model.ClaimDefinition{
			ID: "FEST-CLAIM-04", TenantID: "tenant-router-parity", Title: "Packed state persists",
			Target: "Supply List / canopy persistence", StateSignature: "packed before reload -> packed after reload",
			ActorContext: "permitted trip member", ValidationIntent: "DataPersistenceOnRefresh",
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed(postgres)
	seed(memory)

	scope := Scope{RunID: "run-router-parity", TenantID: "tenant-router-parity", BuildID: "build-router", EnvironmentID: "local", ScenarioID: "equipment-handoff-v1", FeatureFlags: map[string]string{"camp_grid_v2": "true"}}
	query := RecommendationQuery{Scope: scope, ActorID: "late-tester-d", Role: "editor"}
	postgresFirst, err := postgres.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	memoryFirst, err := memory.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if postgresFirst.ClaimID != memoryFirst.ClaimID || postgresFirst.Score != memoryFirst.Score || postgresFirst.Factors.DeltaUncertainty != memoryFirst.Factors.DeltaUncertainty || postgresFirst.Factors.BusinessRisk != memoryFirst.Factors.BusinessRisk || postgresFirst.Factors.DiffImpact != memoryFirst.Factors.DiffImpact || postgresFirst.Factors.SmoothedFailurePrior != memoryFirst.Factors.SmoothedFailurePrior || postgresFirst.Factors.ExpectedCost != memoryFirst.Factors.ExpectedCost || postgresFirst.EvidenceSnapshot != memoryFirst.EvidenceSnapshot {
		t.Fatalf("memory/PostgreSQL routing mismatch:\npostgres=%+v\nmemory=%+v", postgresFirst, memoryFirst)
	}
	postgresRetry, err := postgres.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatalf("retry failed with in-memory recommendation %+v: %v", postgres.memory.recommendations[postgresFirst.ID], err)
	}
	if postgresRetry.ID != postgresFirst.ID || !postgresRetry.CreatedAt.Equal(postgresFirst.CreatedAt) {
		t.Fatalf("PostgreSQL retry was not idempotent: first=%+v retry=%+v", postgresFirst, postgresRetry)
	}
	assertTableCount(t, postgres.db, "recommendation_events", 1)

	evidenceContext := model.EvidenceContext{
		ID: "ctx-router-parity", TenantID: scope.TenantID, RunID: scope.RunID,
		BuildID: scope.BuildID, EnvironmentID: scope.EnvironmentID, ScenarioID: scope.ScenarioID,
		FeatureFlags: scope.FeatureFlags,
	}
	observation := model.Observation{
		ID: "obs-router-parity", TenantID: scope.TenantID, RunID: scope.RunID,
		ClaimID: "FEST-CLAIM-04", EvidenceContextID: evidenceContext.ID,
		SourceType: "human", SourceAdapter: "chrome-mv3", ActorID: "late-tester-d",
		Assessment: "supports", OccurredAt: time.Date(2026, 8, 25, 21, 0, 0, 0, time.UTC),
	}
	for _, repository := range []Repository{postgres, memory} {
		if _, _, err := repository.SaveEvidenceContext(ctx, evidenceContext); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.SaveObservation(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	postgresAfterEvidence, err := postgres.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	memoryAfterEvidence, err := memory.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if postgresAfterEvidence.ID == postgresFirst.ID || postgresAfterEvidence.ID != memoryAfterEvidence.ID || postgresAfterEvidence.Classification != "Weak" {
		t.Fatalf("new evidence decision mismatch: postgres=%+v memory=%+v", postgresAfterEvidence, memoryAfterEvidence)
	}
	assertTableCount(t, postgres.db, "recommendation_events", 2)

	postgres.memory.routingPolicy.Version = "information-value-v0.2-test"
	memory.routingPolicy.Version = "information-value-v0.2-test"
	postgresAfterPolicy, err := postgres.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	memoryAfterPolicy, err := memory.NextRecommendation(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if postgresAfterPolicy.ID == postgresAfterEvidence.ID || postgresAfterPolicy.ID != memoryAfterPolicy.ID || !strings.Contains(postgresAfterPolicy.Rationale, "information-value-v0.2-test") {
		t.Fatalf("policy-version decision mismatch: postgres=%+v memory=%+v", postgresAfterPolicy, memoryAfterPolicy)
	}
	assertTableCount(t, postgres.db, "recommendation_events", 3)
	if _, err := postgres.db.ExecContext(ctx, `update recommendation_events set score=score+1 where id=$1`, postgresAfterPolicy.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("claim-level recommendation mutation was not rejected: %v", err)
	}

	response := model.RecommendationResponse{Response: "accepted", Reason: "Claim-level priority is useful", ActorID: query.ActorID}
	if created, err := postgres.SaveRecommendationResponse(ctx, postgresAfterPolicy.ID, response); err != nil || !created {
		t.Fatalf("save claim recommendation response: created=%v err=%v", created, err)
	}
	if created, err := postgres.SaveRecommendationResponse(ctx, postgresAfterPolicy.ID, response); err != nil || created {
		t.Fatalf("response retry compatibility failed: created=%v err=%v", created, err)
	}
	postgres.Close()

	reopened, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if created, err := reopened.SaveRecommendationResponse(ctx, postgresAfterPolicy.ID, response); err != nil || created {
		t.Fatalf("persisted response history did not survive restart: created=%v err=%v", created, err)
	}
	var claimID, candidateStateID, policyVersion, snapshot string
	var flagsRaw []byte
	if err := reopened.db.QueryRowContext(ctx, `select claim_id,coalesce(candidate_state_id,''),policy_version,evidence_snapshot,coalesce(feature_flags,'{}'::jsonb) from recommendation_events where id=$1`, postgresAfterPolicy.ID).Scan(&claimID, &candidateStateID, &policyVersion, &snapshot, &flagsRaw); err != nil {
		t.Fatal(err)
	}
	if claimID != "FEST-CLAIM-04" || candidateStateID != "" || policyVersion != "information-value-v0.2-test" || len(snapshot) != 64 {
		t.Fatalf("persisted immutable claim link is incomplete: claim=%s candidate=%s policy=%s snapshot=%s", claimID, candidateStateID, policyVersion, snapshot)
	}
	var persistedFlags map[string]string
	if err := json.Unmarshal(flagsRaw, &persistedFlags); err != nil {
		t.Fatal(err)
	}
	if persistedFlags["camp_grid_v2"] != "true" || reopened.memory.recommendations[postgresAfterPolicy.ID].FeatureFlags["camp_grid_v2"] != "true" {
		t.Fatalf("feature flag scope did not survive persistence/restart: database=%v memory=%v", persistedFlags, reopened.memory.recommendations[postgresAfterPolicy.ID].FeatureFlags)
	}
}

func TestPostgresMigration0007ForwardRepairAndBlockedObservation(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()

	// 1. Manually downgrade assessment check constraint on observations to simulate pre-M4C legacy database
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, `
		ALTER TABLE observations DROP CONSTRAINT IF EXISTS observations_assessment_check;
		ALTER TABLE observations ADD CONSTRAINT observations_assessment_check
			CHECK (assessment IN ('supports', 'weakens', 'conflicts', 'invalidates'));
	`)
	if err != nil {
		t.Fatalf("setup pre-M4C assessment constraint: %v", err)
	}

	// 2. Verify that inserting an assessment='blocked' observation fails under pre-M4C constraint
	store, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tenantID := "tenant-mig-test"
	envID := "staging"
	buildID := "build-mig-001"
	runID := "run-mig-001"

	if _, _, err := store.RegisterBuild(ctx, model.BuildRegistration{TenantID: tenantID, EnvironmentID: envID, BuildID: buildID, SequenceNumber: 1}, "controller"); err != nil {
		t.Fatalf("register build: %v", err)
	}
	if _, _, err := store.CreateExperiment(ctx, model.ExperimentRegistration{ID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: envID, ScenarioID: "scenario-1", Mode: "guided"}, "controller"); err != nil {
		t.Fatalf("create run: %v", err)
	}

	claim := model.ClaimDefinition{
		ID:               "claim-mig-001",
		TenantID:         tenantID,
		Title:            "Access revoked after removal",
		Target:           "participant_card",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	if _, _, err := store.SaveClaimDefinition(ctx, claim); err != nil {
		t.Fatalf("save claim: %v", err)
	}
	ctxRecord := model.EvidenceContext{
		ID:            "ctx-mig-001",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    "scenario-1",
	}
	if _, _, err := store.SaveEvidenceContext(ctx, ctxRecord); err != nil {
		t.Fatalf("save context: %v", err)
	}

	obsBlocked := model.Observation{
		ID:                "obs-mig-blocked-001",
		TenantID:          tenantID,
		RunID:             runID,
		ClaimID:           claim.ID,
		EvidenceContextID: ctxRecord.ID,
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "agent-mig",
		Assessment:        "blocked",
		VerifierResult:    "precondition check failed",
		OccurredAt:        time.Now().UTC(),
	}

	if _, _, err := store.SaveObservation(ctx, obsBlocked); err == nil {
		t.Fatalf("expected insert of blocked assessment to fail under pre-M4C constraint")
	}

	// 3. Apply 0007_reconciliation_v0.sql migration (forward repair)
	_, file, _, _ := runtime.Caller(0)
	migrationPath := filepath.Join(filepath.Dir(file), "../../../supabase/migrations/0007_reconciliation_v0.sql")
	migrationSQL, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read 0007 migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("apply 0007 forward repair migration: %v", err)
	}

	// 4. Verify assessment='blocked' can now be saved successfully
	if _, _, err := store.SaveObservation(ctx, obsBlocked); err != nil {
		t.Fatalf("save blocked observation after 0007 migration: %v", err)
	}

	// 5. Verify reconciliation derives Blocked summary
	scope := Scope{RunID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: envID}
	summary, err := store.GetClaimEvidenceSummary(ctx, scope, claim.ID)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if summary.Classification != "Blocked" {
		t.Fatalf("expected Blocked classification, got %s", summary.Classification)
	}

	// 6. Test idempotent re-application of 0007
	if _, err := db.ExecContext(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("re-applying 0007 migration must be idempotent: %v", err)
	}
}

func TestPostgresM6HeterogeneousVerifierWeakToSolidAndRestart(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	store, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tenantID := "festival-thesis-demo"
	runID := "festival-m6-pg-001"
	buildID := "festnest-demo-001"
	envID := "festnest-local-browser"
	scenarioID := "equipment-handoff-v1"

	// Register build & experiment run
	if _, _, err := store.RegisterBuild(ctx, model.BuildRegistration{
		TenantID:       tenantID,
		EnvironmentID:  envID,
		BuildID:        buildID,
		SequenceNumber: 1,
	}, "researcher-a"); err != nil {
		t.Fatalf("register build: %v", err)
	}

	if _, _, err := store.CreateExperiment(ctx, model.ExperimentRegistration{
		ID:            runID,
		TenantID:      tenantID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    scenarioID,
		Mode:          "guided",
	}, "researcher-a"); err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	// 1. Save Claim Definition
	claim := model.ClaimDefinition{
		ID:               "FEST-CLAIM-04",
		TenantID:         tenantID,
		Title:            "Packed canopy persists after refresh",
		Description:      "A packed canopy remains packed after refresh/requery when authoritative state is current.",
		Target:           "Supply List / canopy persistence",
		StateSignature:   "packed before reload -> packed after reload",
		ActorContext:     "permitted trip member",
		ValidationIntent: "DataPersistenceOnRefresh",
	}
	if _, _, err := store.SaveClaimDefinition(ctx, claim); err != nil {
		t.Fatalf("save claim definition: %v", err)
	}

	// 2. Save Evidence Context
	evidenceCtx := model.EvidenceContext{
		ID:            "ctx-m6-pg-001",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    scenarioID,
		CreatedAt:     time.Now().UTC().Add(-5 * time.Minute),
	}
	if _, _, err := store.SaveEvidenceContext(ctx, evidenceCtx); err != nil {
		t.Fatalf("save evidence context: %v", err)
	}

	// 3. Ingest Human Observation (Depth 1) -> Weak
	humanObs := model.Observation{
		ID:                "obs-h-pg-001",
		TenantID:          tenantID,
		RunID:             runID,
		ClaimID:           claim.ID,
		EvidenceContextID: evidenceCtx.ID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "late-tester-d",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC().Add(-4 * time.Minute),
	}
	if _, _, err := store.SaveObservation(ctx, humanObs); err != nil {
		t.Fatalf("save human observation: %v", err)
	}

	scope := Scope{
		RunID:         runID,
		TenantID:      tenantID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    scenarioID,
	}

	summaryWeak, err := store.GetClaimEvidenceSummary(ctx, scope, claim.ID)
	if err != nil {
		t.Fatalf("get weak summary: %v", err)
	}
	if summaryWeak.Classification != "Weak" {
		t.Fatalf("expected Weak before verifier, got %s", summaryWeak.Classification)
	}
	if summaryWeak.MaxVerificationDepth != 1 {
		t.Fatalf("expected max depth 1, got %d", summaryWeak.MaxVerificationDepth)
	}
	if summaryWeak.IndependentSourceCount != 1 {
		t.Fatalf("expected 1 source, got %d", summaryWeak.IndependentSourceCount)
	}

	// 4. Ingest Independent Verifier Observation (Depth 3) -> Solid
	verifierObs := model.Observation{
		ID:                "obs-v-pg-001",
		TenantID:          tenantID,
		RunID:             runID,
		ClaimID:           claim.ID,
		EvidenceContextID: evidenceCtx.ID,
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "playwright-agent-c",
		Assessment:        "supports",
		VerifierResult:    "persisted_authoritative_state_verified",
		OccurredAt:        time.Now().UTC().Add(-2 * time.Minute),
	}
	if _, _, err := store.SaveObservation(ctx, verifierObs); err != nil {
		t.Fatalf("save verifier observation: %v", err)
	}

	summarySolid, err := store.GetClaimEvidenceSummary(ctx, scope, claim.ID)
	if err != nil {
		t.Fatalf("get solid summary: %v", err)
	}
	if summarySolid.Classification != "Solid" {
		t.Fatalf("expected Solid after depth-3 observation, got %s", summarySolid.Classification)
	}
	if summarySolid.MaxVerificationDepth != 3 {
		t.Fatalf("expected max depth 3, got %d", summarySolid.MaxVerificationDepth)
	}
	if summarySolid.IndependentSourceCount != 2 {
		t.Fatalf("expected 2 independent sources, got %d", summarySolid.IndependentSourceCount)
	}
	if summarySolid.HeuristicScore == nil || *summarySolid.HeuristicScore < 0.8 {
		t.Fatalf("expected high heuristic score, got %v", summarySolid.HeuristicScore)
	}

	// 5. Restart Store (Hydrate fresh instance from PostgreSQL)
	store2, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("reconnect postgres: %v", err)
	}
	defer store2.Close()

	summaryHydrated, err := store2.GetClaimEvidenceSummary(ctx, scope, claim.ID)
	if err != nil {
		t.Fatalf("get hydrated summary: %v", err)
	}
	if summaryHydrated.Classification != "Solid" {
		t.Fatalf("expected Solid after store restart, got %s", summaryHydrated.Classification)
	}
	if summaryHydrated.MaxVerificationDepth != 3 {
		t.Fatalf("expected max depth 3 after restart, got %d", summaryHydrated.MaxVerificationDepth)
	}
	if summaryHydrated.IndependentSourceCount != 2 {
		t.Fatalf("expected 2 sources after restart, got %d", summaryHydrated.IndependentSourceCount)
	}

	// 6. Test Controlled Disagreement -> Conflicted
	conflictRunID := "festival-m6-conflict-pg-001"
	if _, _, err := store2.CreateExperiment(ctx, model.ExperimentRegistration{
		ID:            conflictRunID,
		TenantID:      tenantID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    scenarioID,
		Mode:          "guided",
	}, "researcher-a"); err != nil {
		t.Fatalf("create conflict experiment: %v", err)
	}
	conflictEC := model.EvidenceContext{
		ID:            "ctx-m6-conflict-pg",
		TenantID:      tenantID,
		RunID:         conflictRunID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    scenarioID,
		CreatedAt:     time.Now().UTC(),
	}
	if _, _, err := store2.SaveEvidenceContext(ctx, conflictEC); err != nil {
		t.Fatalf("save conflict evidence context: %v", err)
	}
	if _, _, err := store2.SaveObservation(ctx, model.Observation{
		ID:                "obs-c-human-pg",
		TenantID:          tenantID,
		RunID:             conflictRunID,
		ClaimID:           claim.ID,
		EvidenceContextID: conflictEC.ID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "late-tester-d",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC().Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("save conflict human obs: %v", err)
	}
	if _, _, err := store2.SaveObservation(ctx, model.Observation{
		ID:                "obs-c-verifier-pg",
		TenantID:          tenantID,
		RunID:             conflictRunID,
		ClaimID:           claim.ID,
		EvidenceContextID: conflictEC.ID,
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "playwright-agent-c",
		Assessment:        "conflicts",
		VerifierResult:    "persisted_state_mismatch_item_reverted_to_unassigned",
		OccurredAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save conflict verifier obs: %v", err)
	}
	conflictScope := Scope{
		RunID:         conflictRunID,
		TenantID:      tenantID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    scenarioID,
	}
	summaryConflicted, err := store2.GetClaimEvidenceSummary(ctx, conflictScope, claim.ID)
	if err != nil {
		t.Fatalf("get conflict summary: %v", err)
	}
	if summaryConflicted.Classification != "Conflicted" {
		t.Fatalf("expected Conflicted for disagreement, got %s", summaryConflicted.Classification)
	}
	if summaryConflicted.ConflictCount != 1 {
		t.Fatalf("expected 1 conflict, got %d", summaryConflicted.ConflictCount)
	}
	if summaryConflicted.HeuristicScore != nil {
		t.Fatalf("expected nil heuristic score for Conflicted, got %v", *summaryConflicted.HeuristicScore)
	}
}

func TestPostgresPostM6CorrectnessHardening(t *testing.T) {
	databaseURL := prepareIntegrationDatabase(t)
	ctx := context.Background()
	store, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tenantA, tenantB := "tenant-alpha-pg", "tenant-beta-pg"
	envID, buildID := "festnest-local-browser", "build-pg-001"
	runIDA := "run-pg-alpha-001"

	registerIntegrationBuild(t, store, tenantA, envID, buildID, 1, "")
	registerIntegrationBuild(t, store, tenantB, envID, buildID, 1, "")
	createIntegrationRun(t, store, runIDA, tenantA, buildID, envID)

	// --- 1. ClaimDefinition Global ID & Cross-Tenant Rejection ---
	baseClaim := model.ClaimDefinition{
		ID:               "FEST-CLAIM-HARD-01",
		TenantID:         tenantA,
		Title:            "Postgres hardening claim",
		Description:      "Testing postgres global claim ID and tenant isolation.",
		Target:           "Supply List / item",
		StateSignature:   "packed -> unpacked",
		ActorContext:     "permitted member",
		ValidationIntent: "DataPersistenceOnRefresh",
	}

	// Initial insert succeeds
	createdClaim, isNew, err := store.SaveClaimDefinition(ctx, baseClaim)
	if err != nil || !isNew {
		t.Fatalf("initial postgres save claim failed: new=%v err=%v", isNew, err)
	}
	if createdClaim.ID != baseClaim.ID {
		t.Fatalf("expected claim ID %s, got %s", baseClaim.ID, createdClaim.ID)
	}

	// Exact retry succeeds (idempotent)
	retryClaim, isNew, err := store.SaveClaimDefinition(ctx, baseClaim)
	if err != nil || isNew {
		t.Fatalf("exact postgres claim retry must be idempotent: new=%v err=%v", isNew, err)
	}
	if retryClaim.ID != baseClaim.ID {
		t.Fatalf("expected claim ID %s, got %s", baseClaim.ID, retryClaim.ID)
	}

	// Same ID + different tenant + otherwise identical payload conflicts
	crossTenantClaim := baseClaim
	crossTenantClaim.TenantID = tenantB
	_, _, err = store.SaveClaimDefinition(ctx, crossTenantClaim)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected ErrClaimConflict for postgres cross-tenant claim ID collision, got: %v", err)
	}

	// Same ID + same tenant + changed payload conflicts
	sameTenantModified := baseClaim
	sameTenantModified.Title = "Modified title in postgres"
	_, _, err = store.SaveClaimDefinition(ctx, sameTenantModified)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected ErrClaimConflict for postgres modified claim payload, got: %v", err)
	}

	// --- 2. EvidenceContext Canonical Feature Flags Idempotency & Conflict ---
	baseEC := model.EvidenceContext{
		ID:            "ctx-pg-hard-001",
		TenantID:      tenantA,
		RunID:         runIDA,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    "equipment-handoff-v1",
		FeatureFlags:  map[string]string{"camp_grid_v2": "true", "beta_chat": "false"},
		Device:        "desktop-chrome",
		Region:        "us-central",
	}

	// Initial insert succeeds
	_, isNew, err = store.SaveEvidenceContext(ctx, baseEC)
	if err != nil || !isNew {
		t.Fatalf("initial postgres save EC failed: new=%v err=%v", isNew, err)
	}

	// Exact retry succeeds
	_, isNew, err = store.SaveEvidenceContext(ctx, baseEC)
	if err != nil || isNew {
		t.Fatalf("exact postgres EC retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// Reordered flags retry succeeds
	reorderedEC := baseEC
	reorderedEC.FeatureFlags = map[string]string{"beta_chat": "false", "camp_grid_v2": "true"}
	_, isNew, err = store.SaveEvidenceContext(ctx, reorderedEC)
	if err != nil || isNew {
		t.Fatalf("reordered flags postgres EC retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// Changed flag value conflicts
	changedValueEC := baseEC
	changedValueEC.FeatureFlags = map[string]string{"camp_grid_v2": "false", "beta_chat": "false"}
	_, _, err = store.SaveEvidenceContext(ctx, changedValueEC)
	if !errors.Is(err, ErrEvidenceContextConflict) {
		t.Fatalf("expected ErrEvidenceContextConflict in postgres for changed flag value, got: %v", err)
	}

	// Added flag conflicts
	addedFlagEC := baseEC
	addedFlagEC.FeatureFlags = map[string]string{"camp_grid_v2": "true", "beta_chat": "false", "extra": "1"}
	_, _, err = store.SaveEvidenceContext(ctx, addedFlagEC)
	if !errors.Is(err, ErrEvidenceContextConflict) {
		t.Fatalf("expected ErrEvidenceContextConflict in postgres for added flag, got: %v", err)
	}

	// Removed flag conflicts
	removedFlagEC := baseEC
	removedFlagEC.FeatureFlags = map[string]string{"camp_grid_v2": "true"}
	_, _, err = store.SaveEvidenceContext(ctx, removedFlagEC)
	if !errors.Is(err, ErrEvidenceContextConflict) {
		t.Fatalf("expected ErrEvidenceContextConflict in postgres for removed flag, got: %v", err)
	}

	// --- 3. Observation OccurredAt Idempotency & Conflict ---
	occurredAt := time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC)
	baseObs := model.Observation{
		ID:                "obs-pg-hard-001",
		TenantID:          tenantA,
		RunID:             runIDA,
		ClaimID:           baseClaim.ID,
		EvidenceContextID: baseEC.ID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "human-a",
		Assessment:        "supports",
		OccurredAt:        occurredAt,
	}

	// Initial insert succeeds
	createdObs, isNew, err := store.SaveObservation(ctx, baseObs)
	if err != nil || !isNew {
		t.Fatalf("initial postgres save obs failed: new=%v err=%v", isNew, err)
	}
	if !createdObs.OccurredAt.Equal(occurredAt) {
		t.Fatalf("expected postgres OccurredAt %v, got %v", occurredAt, createdObs.OccurredAt)
	}

	// Exact retry succeeds
	_, isNew, err = store.SaveObservation(ctx, baseObs)
	if err != nil || isNew {
		t.Fatalf("exact postgres observation retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// Timezone-offset equivalent retry succeeds
	cstObs := baseObs
	cstObs.OccurredAt = occurredAt.In(time.FixedZone("CDT", -5*3600))
	_, isNew, err = store.SaveObservation(ctx, cstObs)
	if err != nil || isNew {
		t.Fatalf("timezone-shifted equivalent postgres observation retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// Different timestamp conflicts
	differentTimeObs := baseObs
	differentTimeObs.OccurredAt = occurredAt.Add(2 * time.Minute)
	_, _, err = store.SaveObservation(ctx, differentTimeObs)
	if !errors.Is(err, ErrObservationConflict) {
		t.Fatalf("expected ErrObservationConflict in postgres for changed OccurredAt, got: %v", err)
	}
}
