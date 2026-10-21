package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func setupTestRun(t *testing.T, memory *Memory, tenantID, runID string) {
	t.Helper()
	ctx := context.Background()
	_, _, err := memory.RegisterBuild(ctx, model.BuildRegistration{
		TenantID:       tenantID,
		EnvironmentID:  "local",
		BuildID:        "build-001",
		SequenceNumber: 1,
	}, "controller-a")
	if err != nil && !errors.Is(err, ErrBuildRegistrationConflict) {
		t.Fatalf("register build: %v", err)
	}

	_, _, err = memory.CreateExperiment(ctx, model.ExperimentRegistration{
		ID:            runID,
		TenantID:      tenantID,
		BuildID:       "build-001",
		EnvironmentID: "local",
		ScenarioID:    "scenario-001",
		Mode:          "guided",
	}, "controller-a")
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
}

func TestMemoryClaimDefinitionGlobalIDAndTenantIsolation(t *testing.T) {
	memory := NewMemory()
	ctx := context.Background()

	baseClaim := model.ClaimDefinition{
		ID:               "FEST-CLAIM-01",
		TenantID:         "tenant-alpha",
		Title:            "Canopy persists after refresh",
		Description:      "A packed canopy remains packed after refresh.",
		Target:           "Supply List / canopy",
		StateSignature:   "packed -> packed",
		ActorContext:     "permitted member",
		ValidationIntent: "DataPersistenceOnRefresh",
	}

	// 1. Initial creation succeeds
	created, isNew, err := memory.SaveClaimDefinition(ctx, baseClaim)
	if err != nil || !isNew {
		t.Fatalf("initial save claim failed: new=%v err=%v", isNew, err)
	}
	if created.ID != baseClaim.ID {
		t.Fatalf("expected claim ID %s, got %s", baseClaim.ID, created.ID)
	}

	// 2. Exact same-tenant retry succeeds (idempotent)
	retry, isNew, err := memory.SaveClaimDefinition(ctx, baseClaim)
	if err != nil || isNew {
		t.Fatalf("exact retry must be idempotent: new=%v err=%v", isNew, err)
	}
	if retry.ID != baseClaim.ID {
		t.Fatalf("expected claim ID %s, got %s", baseClaim.ID, retry.ID)
	}

	// 3. Same ID + different tenant + otherwise identical payload must conflict (mirroring Postgres globally unique primary key)
	crossTenantIdentical := baseClaim
	crossTenantIdentical.TenantID = "tenant-beta"
	_, _, err = memory.SaveClaimDefinition(ctx, crossTenantIdentical)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected ErrClaimConflict for cross-tenant claim ID collision, got: %v", err)
	}

	// 4. Same ID + different tenant + changed payload must also conflict
	crossTenantDifferent := baseClaim
	crossTenantDifferent.TenantID = "tenant-beta"
	crossTenantDifferent.Title = "Completely different title"
	_, _, err = memory.SaveClaimDefinition(ctx, crossTenantDifferent)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected ErrClaimConflict for cross-tenant claim ID collision, got: %v", err)
	}

	// 5. Same ID + same tenant + changed semantic payload must conflict
	sameTenantModified := baseClaim
	sameTenantModified.Title = "Modified title"
	_, _, err = memory.SaveClaimDefinition(ctx, sameTenantModified)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected ErrClaimConflict for same-tenant modified claim payload, got: %v", err)
	}

	// 6. Different ID + same tenant + duplicate semantic identity must conflict
	duplicateSemantics := baseClaim
	duplicateSemantics.ID = "FEST-CLAIM-02"
	_, _, err = memory.SaveClaimDefinition(ctx, duplicateSemantics)
	if !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("expected ErrClaimConflict for duplicate semantic identity, got: %v", err)
	}
}

func TestMemoryEvidenceContextFeatureFlagsIdempotencyAndConflict(t *testing.T) {
	memory := NewMemory()
	ctx := context.Background()
	tenantID, runID := "tenant-alpha", "run-ec-001"
	setupTestRun(t, memory, tenantID, runID)

	baseEC := model.EvidenceContext{
		ID:            "ctx-001",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       "build-001",
		EnvironmentID: "local",
		ScenarioID:    "scenario-001",
		FeatureFlags:  map[string]string{"camp_grid_v2": "true", "beta_chat": "false"},
		Device:        "desktop-chrome",
		Region:        "us-central",
	}

	// 1. Initial save succeeds
	_, isNew, err := memory.SaveEvidenceContext(ctx, baseEC)
	if err != nil || !isNew {
		t.Fatalf("initial save EC failed: new=%v err=%v", isNew, err)
	}

	// 2. Exact retry with same feature flags succeeds (idempotent)
	exactRetry := baseEC
	retryRes, isNew, err := memory.SaveEvidenceContext(ctx, exactRetry)
	if err != nil || isNew {
		t.Fatalf("exact EC retry must succeed idempotently: new=%v err=%v", isNew, err)
	}
	if retryRes.ID != baseEC.ID {
		t.Fatalf("expected EC ID %s, got %s", baseEC.ID, retryRes.ID)
	}

	// 3. Same flags but inserted/ordered differently succeeds (idempotent)
	reorderedEC := baseEC
	reorderedEC.FeatureFlags = map[string]string{"beta_chat": "false", "camp_grid_v2": "true"}
	_, isNew, err = memory.SaveEvidenceContext(ctx, reorderedEC)
	if err != nil || isNew {
		t.Fatalf("reordered flags EC retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// 4. Changed flag value conflicts
	changedValueEC := baseEC
	changedValueEC.FeatureFlags = map[string]string{"camp_grid_v2": "false", "beta_chat": "false"}
	_, _, err = memory.SaveEvidenceContext(ctx, changedValueEC)
	if !errors.Is(err, ErrEvidenceContextConflict) {
		t.Fatalf("expected ErrEvidenceContextConflict for changed flag value, got: %v", err)
	}

	// 5. Added flag conflicts
	addedFlagEC := baseEC
	addedFlagEC.FeatureFlags = map[string]string{"camp_grid_v2": "true", "beta_chat": "false", "extra_flag": "1"}
	_, _, err = memory.SaveEvidenceContext(ctx, addedFlagEC)
	if !errors.Is(err, ErrEvidenceContextConflict) {
		t.Fatalf("expected ErrEvidenceContextConflict for added flag, got: %v", err)
	}

	// 6. Removed flag conflicts
	removedFlagEC := baseEC
	removedFlagEC.FeatureFlags = map[string]string{"camp_grid_v2": "true"}
	_, _, err = memory.SaveEvidenceContext(ctx, removedFlagEC)
	if !errors.Is(err, ErrEvidenceContextConflict) {
		t.Fatalf("expected ErrEvidenceContextConflict for removed flag, got: %v", err)
	}

	// 7. Nil flags vs empty map equivalence
	emptyEC := model.EvidenceContext{
		ID:            "ctx-nil-empty",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       "build-001",
		EnvironmentID: "local",
		FeatureFlags:  nil,
	}
	_, isNew, err = memory.SaveEvidenceContext(ctx, emptyEC)
	if err != nil || !isNew {
		t.Fatalf("initial nil-flags EC save failed: %v", err)
	}
	emptyMapEC := emptyEC
	emptyMapEC.FeatureFlags = map[string]string{}
	_, isNew, err = memory.SaveEvidenceContext(ctx, emptyMapEC)
	if err != nil || isNew {
		t.Fatalf("empty map retry of nil-flags EC must succeed idempotently: new=%v err=%v", isNew, err)
	}
}

func TestMemoryObservationOccurredAtIdempotencyAndConflict(t *testing.T) {
	memory := NewMemory()
	ctx := context.Background()
	tenantID, runID := "tenant-alpha", "run-obs-001"
	setupTestRun(t, memory, tenantID, runID)

	claim := model.ClaimDefinition{
		ID:               "FEST-CLAIM-OBS-01",
		TenantID:         tenantID,
		Title:            "Observation test claim",
		Description:      "Test observation idempotency with timestamp",
		Target:           "target-obs",
		StateSignature:   "sig-obs",
		ActorContext:     "actor-obs",
		ValidationIntent: "DataPersistenceOnRefresh",
	}
	if _, _, err := memory.SaveClaimDefinition(ctx, claim); err != nil {
		t.Fatalf("save claim: %v", err)
	}

	ec := model.EvidenceContext{
		ID:            "ctx-obs-001",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       "build-001",
		EnvironmentID: "local",
	}
	if _, _, err := memory.SaveEvidenceContext(ctx, ec); err != nil {
		t.Fatalf("save ec: %v", err)
	}

	occurredTime := time.Date(2026, 8, 27, 20, 30, 0, 123456789, time.UTC)
	normalizedOccurredTime := occurredTime.Truncate(time.Microsecond)
	baseObs := model.Observation{
		ID:                "obs-001",
		TenantID:          tenantID,
		RunID:             runID,
		ClaimID:           claim.ID,
		EvidenceContextID: ec.ID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "human-a",
		Assessment:        "supports",
		OccurredAt:        occurredTime,
	}

	// 1. Initial save succeeds and canonicalizes to PostgreSQL/pgx microsecond precision.
	created, isNew, err := memory.SaveObservation(ctx, baseObs)
	if err != nil || !isNew {
		t.Fatalf("initial observation save failed: new=%v err=%v", isNew, err)
	}
	if !created.OccurredAt.Equal(normalizedOccurredTime) {
		t.Fatalf("expected microsecond-normalized OccurredAt %v, got %v", normalizedOccurredTime, created.OccurredAt)
	}

	// 2. Exact retry with the original nanosecond timestamp succeeds (idempotent).
	exactRetry := baseObs
	retryRes, isNew, err := memory.SaveObservation(ctx, exactRetry)
	if err != nil || isNew {
		t.Fatalf("exact observation retry must succeed idempotently: new=%v err=%v", isNew, err)
	}
	if retryRes.ID != baseObs.ID {
		t.Fatalf("expected observation ID %s, got %s", baseObs.ID, retryRes.ID)
	}

	// 3. Retry with equivalent timestamp in different timezone succeeds (idempotent).
	cstLocation := time.FixedZone("CDT", -5*3600)
	cstObs := baseObs
	cstObs.OccurredAt = occurredTime.In(cstLocation)
	_, isNew, err = memory.SaveObservation(ctx, cstObs)
	if err != nil || isNew {
		t.Fatalf("timezone-shifted equivalent OccurredAt retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// 4. Simulate PostgreSQL hydration/restart and retry the original nanosecond timestamp.
	reloaded := NewMemory()
	setupTestRun(t, reloaded, tenantID, runID)
	if _, _, err := reloaded.SaveClaimDefinition(ctx, claim); err != nil {
		t.Fatalf("save claim in reloaded memory: %v", err)
	}
	if _, _, err := reloaded.SaveEvidenceContext(ctx, ec); err != nil {
		t.Fatalf("save ec in reloaded memory: %v", err)
	}
	reloaded.loadObservation(created)
	_, isNew, err = reloaded.SaveObservation(ctx, baseObs)
	if err != nil || isNew {
		t.Fatalf("post-hydration nanosecond retry must succeed idempotently: new=%v err=%v", isNew, err)
	}

	// 5. Retry with a materially different OccurredAt conflicts.
	differentTimeObs := baseObs
	differentTimeObs.OccurredAt = occurredTime.Add(5 * time.Minute)
	_, _, err = memory.SaveObservation(ctx, differentTimeObs)
	if !errors.Is(err, ErrObservationConflict) {
		t.Fatalf("expected ErrObservationConflict for changed OccurredAt, got: %v", err)
	}
}
