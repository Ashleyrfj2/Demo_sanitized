package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/example/qa-platform/backend/internal/evidence"
	"github.com/example/qa-platform/backend/internal/experiment"
	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/state"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Postgres struct {
	mu                        sync.Mutex
	db                        *sql.DB
	memory                    *Memory
	failAfterRawInsertForTest func() error
}

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	store := &Postgres{db: db, memory: NewMemory()}
	if err := store.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.load(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Postgres) load(ctx context.Context) error {
	buildRows, err := store.db.QueryContext(ctx, `select tenant_id,environment_id,build_id,sequence_number,coalesce(predecessor_build_id,''),registered_by,registered_at from build_registry order by tenant_id,environment_id,sequence_number`)
	if err != nil {
		return err
	}
	for buildRows.Next() {
		var build model.RegisteredBuild
		if err := buildRows.Scan(&build.TenantID, &build.EnvironmentID, &build.BuildID, &build.SequenceNumber, &build.PredecessorBuildID, &build.RegisteredBy, &build.RegisteredAt); err != nil {
			buildRows.Close()
			return err
		}
		if _, _, err := store.memory.registerBuild(build.BuildRegistration, build.RegisteredBy, build.RegisteredAt); err != nil {
			buildRows.Close()
			return fmt.Errorf("load build registry: %w", err)
		}
	}
	if err := buildRows.Close(); err != nil {
		return err
	}
	frozenRuns := map[string]time.Time{}
	runRows, err := store.db.QueryContext(ctx, `select id,tenant_id,build_id,environment_id,scenario_id,mode,started_at,frozen_at,created_by from experiment_runs order by started_at,id`)
	if err != nil {
		return err
	}
	for runRows.Next() {
		var run model.ExperimentRun
		var frozenAt sql.NullTime
		if err := runRows.Scan(&run.ID, &run.TenantID, &run.BuildID, &run.EnvironmentID, &run.ScenarioID, &run.Mode, &run.StartedAt, &frozenAt, &run.CreatedBy); err != nil {
			runRows.Close()
			return err
		}
		registration := model.ExperimentRegistration{ID: run.ID, TenantID: run.TenantID, BuildID: run.BuildID, EnvironmentID: run.EnvironmentID, ScenarioID: run.ScenarioID, Mode: run.Mode}
		if _, _, err := store.memory.createExperiment(registration, run.CreatedBy, run.StartedAt); err != nil {
			runRows.Close()
			return fmt.Errorf("load experiment: %w", err)
		}
		if frozenAt.Valid {
			frozenRuns[run.ID] = frozenAt.Time.UTC()
		}
	}
	if err := runRows.Close(); err != nil {
		return err
	}

	candidateRows, err := store.db.QueryContext(ctx, `select id,run_id,tenant_id,build_id,environment_id,role,module,record_id,business_state,state_fingerprint,status,evidence_count,independent_actor_count,source_types,conflict,updated_at from candidate_states order by updated_at, id`)
	if err != nil {
		return fmt.Errorf("load candidate_states: %w", err)
	}
	for candidateRows.Next() {
		var candidate model.CandidateState
		var sources []byte
		if err := candidateRows.Scan(&candidate.ID, &candidate.RunID, &candidate.TenantID, &candidate.BuildID, &candidate.EnvironmentID, &candidate.Role, &candidate.Module, &candidate.RecordID, &candidate.BusinessState, &candidate.StateFingerprint, &candidate.Status, &candidate.EvidenceCount, &candidate.IndependentActorCount, &sources, &candidate.Conflict, &candidate.UpdatedAt); err != nil {
			candidateRows.Close()
			return fmt.Errorf("scan candidate state: %w", err)
		}
		if err := json.Unmarshal(sources, &candidate.SourceTypes); err != nil {
			candidateRows.Close()
			return fmt.Errorf("decode candidate source types for %s: %w", candidate.ID, err)
		}
		store.memory.loadBaselineCandidate(candidate)
	}
	if err := candidateRows.Err(); err != nil {
		candidateRows.Close()
		return fmt.Errorf("candidate_states rows error: %w", err)
	}
	if err := candidateRows.Close(); err != nil {
		return err
	}

	rows, err := store.db.QueryContext(ctx, `select raw_event from validation_events order by created_at, event_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var event model.ValidationEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode persisted event: %w", err)
		}
		if _, err := store.memory.SaveEvent(ctx, event); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	correctionRows, err := store.db.QueryContext(ctx, `select id,run_id,correction_type,state_id,before_state,after_state,corrected_by,reason,created_at from state_corrections order by created_at,id`)
	if err != nil {
		return err
	}
	for correctionRows.Next() {
		var record model.StateCorrection
		var beforeRaw, afterRaw []byte
		if err := correctionRows.Scan(&record.ID, &record.RunID, &record.Type, &record.StateID, &beforeRaw, &afterRaw, &record.CorrectedBy, &record.Reason, &record.CreatedAt); err != nil {
			correctionRows.Close()
			return err
		}
		var request model.StateCorrectionRequest
		if err := json.Unmarshal(beforeRaw, &record.BeforeState); err != nil || json.Unmarshal(afterRaw, &request) != nil {
			correctionRows.Close()
			return err
		}
		record.Request = request
		beforeKeys := map[string]bool{}
		for key := range store.memory.corrections {
			beforeKeys[key] = true
		}
		if err := store.memory.CorrectState(ctx, record.Type, record.StateID, request); err != nil {
			correctionRows.Close()
			return fmt.Errorf("replay correction: %w", err)
		}
		for key := range store.memory.corrections {
			if !beforeKeys[key] {
				delete(store.memory.corrections, key)
			}
		}
		store.memory.corrections[record.ID] = record
	}
	if err := correctionRows.Close(); err != nil {
		return err
	}
	store.memory.markLoadComplete()
	recommendationRows, err := store.db.QueryContext(ctx, `select id,tenant_id,coalesce(candidate_state_id,''),coalesce(claim_id,''),run_id,actor_id,role,build_id,environment_id,coalesce(scenario_id,''),coalesce(device,''),coalesce(region,''),area,coalesce(classification,''),coalesce(max_verification_depth,0),coalesce(independent_source_count,0),coalesce(conflict_count,0),rationale,score,factors,coalesce(policy_version,''),coalesce(evidence_snapshot,''),coalesce(progression_snapshot,''),coalesce(override_alternatives,'[]'::jsonb),coalesce(promoted_by_response_id,''),coalesce(feature_flags,'{}'::jsonb),created_at from recommendation_events order by created_at,id`)
	if err != nil {
		return err
	}
	for recommendationRows.Next() {
		var recommendation model.Recommendation
		var factors, alternatives, featureFlags []byte
		if err := recommendationRows.Scan(&recommendation.ID, &recommendation.TenantID, &recommendation.CandidateStateID, &recommendation.ClaimID, &recommendation.RunID, &recommendation.ActorID, &recommendation.Role, &recommendation.BuildID, &recommendation.EnvironmentID, &recommendation.ScenarioID, &recommendation.Device, &recommendation.Region, &recommendation.Area, &recommendation.Classification, &recommendation.MaxVerificationDepth, &recommendation.IndependentSourceCount, &recommendation.ConflictCount, &recommendation.Rationale, &recommendation.Score, &factors, &recommendation.PolicyVersion, &recommendation.EvidenceSnapshot, &recommendation.ProgressionSnapshot, &alternatives, &recommendation.PromotedByResponseID, &featureFlags, &recommendation.CreatedAt); err != nil {
			recommendationRows.Close()
			return err
		}
		if err := json.Unmarshal(factors, &recommendation.Factors); err != nil {
			recommendationRows.Close()
			return err
		}
		if err := json.Unmarshal(alternatives, &recommendation.OverrideAlternatives); err != nil {
			recommendationRows.Close()
			return err
		}
		if err := json.Unmarshal(featureFlags, &recommendation.FeatureFlags); err != nil {
			recommendationRows.Close()
			return err
		}
		store.memory.recommendations[recommendation.ID] = recommendation
	}
	if err := recommendationRows.Close(); err != nil {
		return err
	}
	responseRows, err := store.db.QueryContext(ctx, `select id,run_id,recommendation_id,response,reason,actor_id,coalesce(override_claim_id,''),created_at from recommendation_responses order by created_at,id`)
	if err != nil {
		return err
	}
	for responseRows.Next() {
		var response model.RecommendationResponseRecord
		if err := responseRows.Scan(&response.ID, &response.RunID, &response.RecommendationID, &response.Response, &response.Reason, &response.ActorID, &response.OverrideClaimID, &response.CreatedAt); err != nil {
			responseRows.Close()
			return err
		}
		store.memory.responses[response.RecommendationID] = response
	}
	if err := responseRows.Close(); err != nil {
		return err
	}
	for runID, frozenAt := range frozenRuns {
		var snapshotRaw []byte
		if err := store.db.QueryRowContext(ctx, `select snapshot from experiment_metrics where run_id=$1 and definition_version=$2`, runID, experiment.DefinitionVersion).Scan(&snapshotRaw); err != nil {
			return fmt.Errorf("load frozen snapshot for %s: %w", runID, err)
		}
		var snapshot model.ExperimentMetrics
		if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
			return err
		}
		run := store.memory.runs[runID]
		run.FrozenAt = &frozenAt
		store.memory.runs[runID] = run
		store.memory.snapshots[runID] = snapshot
	}

	claimRows, err := store.db.QueryContext(ctx, `select id, tenant_id, title, coalesce(description, ''), target, state_signature, actor_context, validation_intent, created_at from claim_definitions order by created_at, id`)
	if err != nil {
		return fmt.Errorf("load claim_definitions: %w", err)
	}
	for claimRows.Next() {
		var claim model.ClaimDefinition
		if err := claimRows.Scan(&claim.ID, &claim.TenantID, &claim.Title, &claim.Description, &claim.Target, &claim.StateSignature, &claim.ActorContext, &claim.ValidationIntent, &claim.CreatedAt); err != nil {
			claimRows.Close()
			return fmt.Errorf("scan claim_definition: %w", err)
		}
		if _, _, err := store.memory.SaveClaimDefinition(ctx, claim); err != nil {
			claimRows.Close()
			return fmt.Errorf("load claim in memory: %w", err)
		}
	}
	if err := claimRows.Close(); err != nil {
		return err
	}

	ctxRows, err := store.db.QueryContext(ctx, `select id, tenant_id, run_id, build_id, environment_id, coalesce(scenario_id, ''), feature_flags, coalesce(device, ''), coalesce(region, ''), created_at from evidence_contexts order by created_at, id`)
	if err != nil {
		return fmt.Errorf("load evidence_contexts: %w", err)
	}
	for ctxRows.Next() {
		var ec model.EvidenceContext
		var flagsRaw []byte
		if err := ctxRows.Scan(&ec.ID, &ec.TenantID, &ec.RunID, &ec.BuildID, &ec.EnvironmentID, &ec.ScenarioID, &flagsRaw, &ec.Device, &ec.Region, &ec.CreatedAt); err != nil {
			ctxRows.Close()
			return fmt.Errorf("scan evidence context: %w", err)
		}
		if len(flagsRaw) > 0 {
			_ = json.Unmarshal(flagsRaw, &ec.FeatureFlags)
		}
		store.memory.loadEvidenceContext(ec)
	}
	if err := ctxRows.Close(); err != nil {
		return err
	}

	obsRows, err := store.db.QueryContext(ctx, `select id, tenant_id, run_id, claim_id, evidence_context_id, coalesce(event_id, ''), source, source_adapter, actor_id, assessment, coalesce(verifier_result, ''), occurred_at, created_at from observations order by created_at, id`)
	if err != nil {
		return fmt.Errorf("load observations: %w", err)
	}
	for obsRows.Next() {
		var obs model.Observation
		if err := obsRows.Scan(&obs.ID, &obs.TenantID, &obs.RunID, &obs.ClaimID, &obs.EvidenceContextID, &obs.EventID, &obs.SourceType, &obs.SourceAdapter, &obs.ActorID, &obs.Assessment, &obs.VerifierResult, &obs.OccurredAt, &obs.CreatedAt); err != nil {
			obsRows.Close()
			return fmt.Errorf("scan observation: %w", err)
		}
		store.memory.loadObservation(obs)
	}
	if err := obsRows.Close(); err != nil {
		return err
	}

	return nil
}

func (store *Postgres) RegisterBuild(ctx context.Context, registration model.BuildRegistration, registeredBy string) (model.RegisteredBuild, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if registration.TenantID == "" || registration.EnvironmentID == "" || registration.BuildID == "" || registration.SequenceNumber < 1 || registeredBy == "" {
		return model.RegisteredBuild{}, false, ErrInvalidBuildChain
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RegisteredBuild{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into tenants (id,name) values ($1,$1) on conflict (id) do nothing`, registration.TenantID); err != nil {
		return model.RegisteredBuild{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '|' || $2, 0))`, registration.TenantID, registration.EnvironmentID); err != nil {
		return model.RegisteredBuild{}, false, err
	}
	var existing model.RegisteredBuild
	err = tx.QueryRowContext(ctx, `select tenant_id,environment_id,build_id,sequence_number,coalesce(predecessor_build_id,''),registered_by,registered_at from build_registry where tenant_id=$1 and environment_id=$2 and build_id=$3`, registration.TenantID, registration.EnvironmentID, registration.BuildID).Scan(&existing.TenantID, &existing.EnvironmentID, &existing.BuildID, &existing.SequenceNumber, &existing.PredecessorBuildID, &existing.RegisteredBy, &existing.RegisteredAt)
	if err == nil {
		if existing.SequenceNumber != registration.SequenceNumber || existing.PredecessorBuildID != registration.PredecessorBuildID {
			return model.RegisteredBuild{}, false, ErrBuildRegistrationConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.RegisteredBuild{}, false, err
	}

	var sequenceBuildID string
	err = tx.QueryRowContext(ctx, `select build_id from build_registry where tenant_id=$1 and environment_id=$2 and sequence_number=$3`, registration.TenantID, registration.EnvironmentID, registration.SequenceNumber).Scan(&sequenceBuildID)
	if err == nil {
		return model.RegisteredBuild{}, false, ErrBuildRegistrationConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.RegisteredBuild{}, false, err
	}
	if registration.SequenceNumber == 1 {
		if registration.PredecessorBuildID != "" {
			return model.RegisteredBuild{}, false, ErrInvalidBuildChain
		}
	} else {
		var predecessorSequence int64
		err = tx.QueryRowContext(ctx, `select sequence_number from build_registry where tenant_id=$1 and environment_id=$2 and build_id=$3`, registration.TenantID, registration.EnvironmentID, registration.PredecessorBuildID).Scan(&predecessorSequence)
		if errors.Is(err, sql.ErrNoRows) || err == nil && predecessorSequence != registration.SequenceNumber-1 {
			return model.RegisteredBuild{}, false, ErrInvalidBuildChain
		}
		if err != nil {
			return model.RegisteredBuild{}, false, err
		}
	}
	registered := model.RegisteredBuild{BuildRegistration: registration, RegisteredBy: registeredBy}
	err = tx.QueryRowContext(ctx, `insert into build_registry (tenant_id,environment_id,build_id,sequence_number,predecessor_build_id,registered_by) values ($1,$2,$3,$4,nullif($5,''),$6) returning registered_at`, registration.TenantID, registration.EnvironmentID, registration.BuildID, registration.SequenceNumber, registration.PredecessorBuildID, registeredBy).Scan(&registered.RegisteredAt)
	if err != nil {
		return model.RegisteredBuild{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `update candidate_states states set status='stale' from build_registry registered, experiment_runs runs where states.tenant_id=$1 and states.environment_id=$2 and registered.tenant_id=states.tenant_id and registered.environment_id=states.environment_id and registered.build_id=states.build_id and registered.sequence_number < $3 and runs.id=states.run_id and runs.frozen_at is null`, registration.TenantID, registration.EnvironmentID, registration.SequenceNumber); err != nil {
		return model.RegisteredBuild{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.RegisteredBuild{}, false, err
	}
	if _, _, err := store.memory.registerBuild(registration, registeredBy, registered.RegisteredAt); err != nil {
		return model.RegisteredBuild{}, false, err
	}
	return registered, true, nil
}

func (store *Postgres) RequireRegisteredBuild(ctx context.Context, tenantID, environmentID, buildID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.memory.RequireRegisteredBuild(ctx, tenantID, environmentID, buildID)
}

func (store *Postgres) CreateExperiment(ctx context.Context, registration model.ExperimentRegistration, createdBy string) (model.ExperimentRun, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	staged := store.memory.Clone()
	if _, _, err := staged.createExperiment(registration, createdBy, time.Now().UTC()); err != nil {
		return model.ExperimentRun{}, false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ExperimentRun{}, false, err
	}
	defer tx.Rollback()
	var existing model.ExperimentRun
	var frozenAt sql.NullTime
	err = tx.QueryRowContext(ctx, `select id,tenant_id,build_id,environment_id,scenario_id,mode,started_at,frozen_at,created_by from experiment_runs where id=$1 for update`, registration.ID).Scan(&existing.ID, &existing.TenantID, &existing.BuildID, &existing.EnvironmentID, &existing.ScenarioID, &existing.Mode, &existing.StartedAt, &frozenAt, &existing.CreatedBy)
	if err == nil {
		if frozenAt.Valid {
			value := frozenAt.Time.UTC()
			existing.FrozenAt = &value
		}
		if existing.TenantID != registration.TenantID || existing.BuildID != registration.BuildID || existing.EnvironmentID != registration.EnvironmentID || existing.ScenarioID != registration.ScenarioID || existing.Mode != registration.Mode {
			return model.ExperimentRun{}, false, ErrExperimentConflict
		}
		store.memory.runs[existing.ID] = existing
		if existing.FrozenAt != nil {
			var snapshotRaw []byte
			if err := tx.QueryRowContext(ctx, `select snapshot from experiment_metrics where run_id=$1 and definition_version=$2`, existing.ID, experiment.DefinitionVersion).Scan(&snapshotRaw); err != nil {
				return model.ExperimentRun{}, false, err
			}
			var snapshot model.ExperimentMetrics
			if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
				return model.ExperimentRun{}, false, err
			}
			store.memory.snapshots[existing.ID] = snapshot
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.ExperimentRun{}, false, err
	}
	if err := store.memory.RequireRegisteredBuild(ctx, registration.TenantID, registration.EnvironmentID, registration.BuildID); err != nil {
		return model.ExperimentRun{}, false, err
	}
	var startedAt time.Time
	err = tx.QueryRowContext(ctx, `insert into experiment_runs (id,tenant_id,build_id,environment_id,scenario_id,mode,created_by) values ($1,$2,$3,$4,$5,$6,$7) returning started_at`, registration.ID, registration.TenantID, registration.BuildID, registration.EnvironmentID, registration.ScenarioID, registration.Mode, createdBy).Scan(&startedAt)
	if err != nil {
		return model.ExperimentRun{}, false, mapRunError(err)
	}
	run := model.ExperimentRun{ID: registration.ID, TenantID: registration.TenantID, BuildID: registration.BuildID, EnvironmentID: registration.EnvironmentID, ScenarioID: registration.ScenarioID, Mode: registration.Mode, StartedAt: startedAt.UTC(), CreatedBy: createdBy}
	if err := tx.Commit(); err != nil {
		return model.ExperimentRun{}, false, err
	}
	store.memory.runs[run.ID] = run
	return run, true, nil
}

func lockWritableRunTx(ctx context.Context, tx *sql.Tx, runID string) (model.ExperimentRun, error) {
	if strings.TrimSpace(runID) == "" {
		return model.ExperimentRun{}, ErrUnknownRun
	}
	var run model.ExperimentRun
	var frozenAt sql.NullTime
	err := tx.QueryRowContext(ctx, `select id,tenant_id,build_id,environment_id,scenario_id,mode,started_at,frozen_at,created_by from experiment_runs where id=$1 for update`, runID).Scan(&run.ID, &run.TenantID, &run.BuildID, &run.EnvironmentID, &run.ScenarioID, &run.Mode, &run.StartedAt, &frozenAt, &run.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ExperimentRun{}, ErrUnknownRun
	}
	if err != nil {
		return model.ExperimentRun{}, err
	}
	if frozenAt.Valid {
		return model.ExperimentRun{}, ErrRunFrozen
	}
	return run, nil
}

func mapRunError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "unknown_run"):
		return ErrUnknownRun
	case strings.Contains(message, "run_scope_mismatch"):
		return ErrRunScopeMismatch
	case strings.Contains(message, "run_frozen"):
		return ErrRunFrozen
	default:
		return err
	}
}

func (store *Postgres) SaveEvent(ctx context.Context, event model.ValidationEvent) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	staged := store.memory.Clone()
	created, err := staged.SaveEvent(ctx, event)
	if err != nil || !created {
		return created, err
	}
	raw, _ := json.Marshal(event)
	contextJSON, _ := json.Marshal(event.Context)
	privacyJSON, _ := json.Marshal(event.Privacy)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	run, err := lockWritableRunTx(ctx, tx, event.RunID)
	if err != nil {
		return false, err
	}
	if !runMatchesEvent(run, event) {
		return false, ErrRunScopeMismatch
	}
	if _, err = tx.ExecContext(ctx, `insert into tenants (id,name) values ($1,$1) on conflict (id) do nothing`, event.TenantID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `insert into actors (tenant_id,id,display_name) values ($1,$2,$2) on conflict (tenant_id,id) do update set display_name=excluded.display_name`, event.TenantID, event.ActorID); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		insert into validation_events (
			event_id,tenant_id,actor_id,session_id,run_id,source,source_adapter,build_id,environment_id,
			context,route,state_fingerprint,action_type,outcome,verifier_result,finding_id,verification_status,
			occurred_at,step_index,privacy,raw_event
		) values ($1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,nullif($15,''),nullif($16,''),nullif($17,''),$18,$19,$20,$21)
		on conflict (event_id) do nothing`,
		event.EventID, event.TenantID, event.ActorID, event.SessionID, event.RunID, event.SourceType, event.SourceAdapter,
		event.BuildID, event.EnvironmentID, contextJSON, event.Route, event.StateFingerprint, event.ActionType, event.Outcome,
		event.VerifierResult, event.FindingID, event.Verification, event.OccurredAt, event.StepIndex, privacyJSON, raw)
	if err != nil {
		return false, mapRunError(err)
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		var existingRaw []byte
		if err := tx.QueryRowContext(ctx, `select raw_event from validation_events where event_id=$1`, event.EventID).Scan(&existingRaw); err != nil {
			return false, err
		}
		var existing model.ValidationEvent
		if err := json.Unmarshal(existingRaw, &existing); err != nil {
			return false, err
		}
		if !sameEvent(existing, event) {
			return false, ErrEventConflict
		}
		return false, nil
	}
	if store.failAfterRawInsertForTest != nil {
		if err := store.failAfterRawInsertForTest(); err != nil {
			return false, err
		}
	}
	if err := store.persistDerivedTx(ctx, tx, staged, event); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	store.memory = staged
	return true, nil
}

func (store *Postgres) persistDerivedTx(ctx context.Context, tx *sql.Tx, memory *Memory, event model.ValidationEvent) error {
	identity := state.New().Identify(event)
	identity.ID = memory.canonicalStateIDForEvent(event)
	response, err := memory.Evidence(ctx, Scope{RunID: event.RunID, TenantID: event.TenantID, BuildID: event.BuildID, EnvironmentID: event.EnvironmentID})
	if err != nil {
		return err
	}
	var candidate model.CandidateState
	for _, value := range response.States {
		if value.ID == identity.ID {
			candidate = value
			break
		}
	}
	if candidate.ID == "" {
		return errors.New("derived candidate state missing")
	}
	for _, derived := range response.States {
		sources, _ := json.Marshal(derived.SourceTypes)
		_, err = tx.ExecContext(ctx, `
			insert into candidate_states (id,run_id,tenant_id,build_id,environment_id,role,module,record_id,business_state,state_fingerprint,status,evidence_count,independent_actor_count,source_types,conflict,updated_at)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			on conflict (id) do update set state_fingerprint=excluded.state_fingerprint,business_state=excluded.business_state,status=excluded.status,evidence_count=excluded.evidence_count,independent_actor_count=excluded.independent_actor_count,source_types=excluded.source_types,conflict=excluded.conflict,updated_at=excluded.updated_at`,
			derived.ID, derived.RunID, derived.TenantID, derived.BuildID, derived.EnvironmentID, derived.Role, derived.Module, derived.RecordID, derived.BusinessState, derived.StateFingerprint, derived.Status, derived.EvidenceCount, derived.IndependentActorCount, sources, derived.Conflict, derived.UpdatedAt)
		if err != nil {
			return err
		}
	}
	record := evidence.New().FromEvent(candidate.ID, event)
	_, err = tx.ExecContext(ctx, `insert into evidence_records (id,run_id,tenant_id,candidate_state_id,event_id,actor_id,source,outcome,occurred_at) values ($1,$2,$3,$4,$5,$6,$7,$8,$9) on conflict (event_id) do nothing`, record.ID, event.RunID, event.TenantID, candidate.ID, event.EventID, event.ActorID, event.SourceType, event.Outcome, event.OccurredAt)
	if err != nil {
		return err
	}
	for _, request := range response.VerificationRequests {
		if request.CandidateStateID != candidate.ID {
			continue
		}
		_, err = tx.ExecContext(ctx, `insert into verification_requests (id,run_id,tenant_id,candidate_state_id,build_id,environment_id,requested_role,rationale,status,created_at) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) on conflict (id) do update set rationale=excluded.rationale,status='open'`, request.ID, event.RunID, event.TenantID, candidate.ID, event.BuildID, event.EnvironmentID, request.RequestedRole, request.Rationale, request.Status, request.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func (store *Postgres) Evidence(ctx context.Context, scope Scope) (model.EvidenceResponse, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.memory.Evidence(ctx, scope)
}

func (store *Postgres) NextRecommendation(ctx context.Context, query RecommendationQuery) (model.Recommendation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	staged := store.memory.Clone()
	recommendation, err := staged.NextRecommendation(ctx, query)
	if err != nil {
		return recommendation, err
	}
	if persisted, ok := store.memory.recommendations[recommendation.ID]; ok {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return model.Recommendation{}, err
		}
		defer tx.Rollback()
		run, err := lockWritableRunTx(ctx, tx, query.RunID)
		if err != nil {
			return model.Recommendation{}, err
		}
		if !runMatchesScope(run, query.Scope) || persisted.ProgressionSnapshot != recommendation.ProgressionSnapshot {
			return model.Recommendation{}, ErrRunScopeMismatch
		}
		if err := tx.Commit(); err != nil {
			return model.Recommendation{}, err
		}
		return persisted, nil
	}
	factors, err := json.Marshal(recommendation.Factors)
	if err != nil {
		return model.Recommendation{}, err
	}
	featureFlags, err := json.Marshal(recommendation.FeatureFlags)
	if err != nil {
		return model.Recommendation{}, err
	}
	alternatives, err := json.Marshal(recommendation.OverrideAlternatives)
	if err != nil {
		return model.Recommendation{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Recommendation{}, err
	}
	defer tx.Rollback()
	run, err := lockWritableRunTx(ctx, tx, query.RunID)
	if err != nil {
		return model.Recommendation{}, err
	}
	if !runMatchesScope(run, query.Scope) {
		return model.Recommendation{}, ErrRunScopeMismatch
	}
	var persistedAt time.Time
	err = tx.QueryRowContext(ctx, `insert into recommendation_events (id,tenant_id,candidate_state_id,claim_id,actor_id,role,build_id,environment_id,run_id,scenario_id,device,region,area,classification,max_verification_depth,independent_source_count,conflict_count,rationale,score,factors,policy_version,evidence_snapshot,progression_snapshot,override_alternatives,promoted_by_response_id,feature_flags,created_at) values ($1,$2,nullif($3,''),nullif($4,''),$5,$6,$7,$8,$9,nullif($10,''),nullif($11,''),nullif($12,''),$13,nullif($14,''),$15,$16,$17,$18,$19,$20,nullif($21,''),nullif($22,''),nullif($23,''),$24,nullif($25,''),$26,$27) on conflict (id) do nothing returning created_at`, recommendation.ID, query.TenantID, recommendation.CandidateStateID, recommendation.ClaimID, query.ActorID, query.Role, query.BuildID, query.EnvironmentID, recommendation.RunID, recommendation.ScenarioID, recommendation.Device, recommendation.Region, recommendation.Area, recommendation.Classification, recommendation.MaxVerificationDepth, recommendation.IndependentSourceCount, recommendation.ConflictCount, recommendation.Rationale, recommendation.Score, factors, recommendation.PolicyVersion, recommendation.EvidenceSnapshot, recommendation.ProgressionSnapshot, alternatives, recommendation.PromotedByResponseID, featureFlags, recommendation.CreatedAt).Scan(&persistedAt)
	if errors.Is(err, sql.ErrNoRows) {
		var persisted model.Recommendation
		var persistedFactors, persistedAlternatives, persistedFeatureFlags []byte
		err = tx.QueryRowContext(ctx, `select id,tenant_id,coalesce(candidate_state_id,''),coalesce(claim_id,''),run_id,actor_id,role,build_id,environment_id,coalesce(scenario_id,''),coalesce(device,''),coalesce(region,''),area,coalesce(classification,''),coalesce(max_verification_depth,0),coalesce(independent_source_count,0),coalesce(conflict_count,0),rationale,score,factors,coalesce(policy_version,''),coalesce(evidence_snapshot,''),coalesce(progression_snapshot,''),coalesce(override_alternatives,'[]'::jsonb),coalesce(promoted_by_response_id,''),coalesce(feature_flags,'{}'::jsonb),created_at from recommendation_events where id=$1`, recommendation.ID).Scan(&persisted.ID, &persisted.TenantID, &persisted.CandidateStateID, &persisted.ClaimID, &persisted.RunID, &persisted.ActorID, &persisted.Role, &persisted.BuildID, &persisted.EnvironmentID, &persisted.ScenarioID, &persisted.Device, &persisted.Region, &persisted.Area, &persisted.Classification, &persisted.MaxVerificationDepth, &persisted.IndependentSourceCount, &persisted.ConflictCount, &persisted.Rationale, &persisted.Score, &persistedFactors, &persisted.PolicyVersion, &persisted.EvidenceSnapshot, &persisted.ProgressionSnapshot, &persistedAlternatives, &persisted.PromotedByResponseID, &persistedFeatureFlags, &persisted.CreatedAt)
		if err != nil {
			return model.Recommendation{}, err
		}
		if err := json.Unmarshal(persistedFactors, &persisted.Factors); err != nil {
			return model.Recommendation{}, err
		}
		if err := json.Unmarshal(persistedAlternatives, &persisted.OverrideAlternatives); err != nil {
			return model.Recommendation{}, err
		}
		if err := json.Unmarshal(persistedFeatureFlags, &persisted.FeatureFlags); err != nil {
			return model.Recommendation{}, err
		}
		if persisted.RunID != query.RunID || persisted.ActorID != query.ActorID || persisted.ClaimID != recommendation.ClaimID || persisted.PolicyVersion != recommendation.PolicyVersion || persisted.EvidenceSnapshot != recommendation.EvidenceSnapshot || persisted.ProgressionSnapshot != recommendation.ProgressionSnapshot {
			return model.Recommendation{}, ErrRunScopeMismatch
		}
		recommendation = persisted
		staged.recommendations[persisted.ID] = persisted
	} else if err != nil {
		return model.Recommendation{}, mapRunError(err)
	} else {
		recommendation.CreatedAt = persistedAt.UTC()
		staged.recommendations[recommendation.ID] = recommendation
	}
	if err := tx.Commit(); err != nil {
		return model.Recommendation{}, err
	}
	store.memory = staged
	return recommendation, nil
}

func (store *Postgres) SaveRecommendationResponse(ctx context.Context, id string, response model.RecommendationResponse) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if response.Response != "accepted" && response.Response != "dismissed" {
		return false, fmt.Errorf("response must be accepted or dismissed")
	}
	if strings.TrimSpace(response.Reason) == "" || strings.TrimSpace(response.ActorID) == "" {
		return false, fmt.Errorf("reason and actor_id are required")
	}
	recommendation, ok := store.memory.recommendations[id]
	if !ok {
		return false, ErrNotFound
	}
	staged := store.memory.Clone()
	created, err := staged.SaveRecommendationResponse(ctx, id, response)
	if err != nil {
		return false, err
	}
	if _, alreadyRecorded := store.memory.responses[id]; alreadyRecorded {
		return false, nil
	}
	record := staged.responses[id]
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := lockWritableRunTx(ctx, tx, recommendation.RunID); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `insert into recommendation_responses (id,run_id,recommendation_id,response,reason,actor_id,created_at) values ($1,$2,$3,$4,$5,$6,$7)`, record.ID, record.RunID, id, response.Response, response.Reason, response.ActorID, record.CreatedAt)
	if err != nil {
		return false, mapRunError(err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	store.memory = staged
	return created, nil
}

func (store *Postgres) CorrectState(ctx context.Context, kind, id string, request model.StateCorrectionRequest) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if strings.TrimSpace(request.ActorID) == "" || strings.TrimSpace(request.Reason) == "" {
		return fmt.Errorf("actor_id and reason are required")
	}
	current := store.memory.Clone()
	candidate, exists := current.states[id]
	if !exists {
		return ErrNotFound
	}
	beforeCorrectionIDs := map[string]bool{}
	for key := range store.memory.corrections {
		beforeCorrectionIDs[key] = true
	}
	staged := store.memory.Clone()
	if err := staged.CorrectState(ctx, kind, id, request); err != nil {
		return err
	}
	var correction model.StateCorrection
	for key, value := range staged.corrections {
		if !beforeCorrectionIDs[key] {
			correction = value
			break
		}
	}
	if correction.ID == "" {
		return fmt.Errorf("correction audit record missing")
	}
	before, _ := json.Marshal(correction.BeforeState)
	after, _ := json.Marshal(correction.Request)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := lockWritableRunTx(ctx, tx, candidate.RunID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `insert into state_corrections (id,run_id,tenant_id,build_id,environment_id,correction_type,state_id,before_state,after_state,corrected_by,reason,created_at) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, correction.ID, candidate.RunID, candidate.TenantID, candidate.BuildID, candidate.EnvironmentID, kind, id, before, after, request.ActorID, request.Reason, correction.CreatedAt)
	if err != nil {
		return mapRunError(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("correction audit row was not persisted")
	}
	scope := Scope{RunID: candidate.RunID, TenantID: candidate.TenantID, BuildID: candidate.BuildID, EnvironmentID: candidate.EnvironmentID}
	if err := store.persistScopeTx(ctx, tx, staged, scope); err != nil {
		return err
	}
	if _, stillExists := staged.states[id]; !stillExists {
		replacementID := staged.canonicalStateID(id)
		if replacementID != id {
			if _, err := tx.ExecContext(ctx, `update recommendation_events set candidate_state_id=$1 where run_id=$2 and candidate_state_id=$3`, replacementID, candidate.RunID, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `delete from verification_requests where run_id=$1 and candidate_state_id=$2`, candidate.RunID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from candidate_states where run_id=$1 and id=$2`, candidate.RunID, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	store.memory = staged
	return nil
}

func (store *Postgres) persistScopeTx(ctx context.Context, tx *sql.Tx, memory *Memory, scope Scope) error {
	response, err := memory.Evidence(ctx, scope)
	if err != nil {
		return err
	}
	for _, candidate := range response.States {
		sources, _ := json.Marshal(candidate.SourceTypes)
		if _, err := tx.ExecContext(ctx, `insert into candidate_states (id,run_id,tenant_id,build_id,environment_id,role,module,record_id,business_state,state_fingerprint,status,evidence_count,independent_actor_count,source_types,conflict,updated_at) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) on conflict (id) do update set business_state=excluded.business_state,state_fingerprint=excluded.state_fingerprint,status=excluded.status,evidence_count=excluded.evidence_count,independent_actor_count=excluded.independent_actor_count,source_types=excluded.source_types,conflict=excluded.conflict,updated_at=excluded.updated_at`, candidate.ID, candidate.RunID, candidate.TenantID, candidate.BuildID, candidate.EnvironmentID, candidate.Role, candidate.Module, candidate.RecordID, candidate.BusinessState, candidate.StateFingerprint, candidate.Status, candidate.EvidenceCount, candidate.IndependentActorCount, sources, candidate.Conflict, candidate.UpdatedAt); err != nil {
			return err
		}
		for _, eventID := range candidate.EventIDs {
			if _, err := tx.ExecContext(ctx, `update evidence_records set candidate_state_id=$1 where run_id=$2 and event_id=$3`, candidate.ID, scope.RunID, eventID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from verification_requests where run_id=$1 and tenant_id=$2 and build_id=$3 and environment_id=$4`, scope.RunID, scope.TenantID, scope.BuildID, scope.EnvironmentID); err != nil {
		return err
	}
	for _, request := range response.VerificationRequests {
		if _, err := tx.ExecContext(ctx, `insert into verification_requests (id,run_id,tenant_id,candidate_state_id,build_id,environment_id,requested_role,rationale,status,created_at) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, request.ID, scope.RunID, scope.TenantID, request.CandidateStateID, scope.BuildID, scope.EnvironmentID, request.RequestedRole, request.Rationale, request.Status, request.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

type sqlQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadMetricInputs(ctx context.Context, query sqlQuerier, runID string) (experiment.Inputs, error) {
	input := experiment.Inputs{}
	eventRows, err := query.QueryContext(ctx, `select raw_event from validation_events where run_id=$1 order by occurred_at,event_id`, runID)
	if err != nil {
		return input, err
	}
	for eventRows.Next() {
		var raw []byte
		if err := eventRows.Scan(&raw); err != nil {
			eventRows.Close()
			return input, err
		}
		var event model.ValidationEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			eventRows.Close()
			return input, err
		}
		input.Events = append(input.Events, event)
	}
	if err := eventRows.Close(); err != nil {
		return input, err
	}

	stateRows, err := query.QueryContext(ctx, `select id,run_id,tenant_id,build_id,environment_id,role,module,record_id,business_state,state_fingerprint,status,evidence_count,independent_actor_count,source_types,conflict,updated_at from candidate_states where run_id=$1 order by id`, runID)
	if err != nil {
		return input, err
	}
	for stateRows.Next() {
		var stateValue model.CandidateState
		var sourceTypes []byte
		if err := stateRows.Scan(&stateValue.ID, &stateValue.RunID, &stateValue.TenantID, &stateValue.BuildID, &stateValue.EnvironmentID, &stateValue.Role, &stateValue.Module, &stateValue.RecordID, &stateValue.BusinessState, &stateValue.StateFingerprint, &stateValue.Status, &stateValue.EvidenceCount, &stateValue.IndependentActorCount, &sourceTypes, &stateValue.Conflict, &stateValue.UpdatedAt); err != nil {
			stateRows.Close()
			return input, err
		}
		if err := json.Unmarshal(sourceTypes, &stateValue.SourceTypes); err != nil {
			stateRows.Close()
			return input, err
		}
		input.States = append(input.States, stateValue)
	}
	if err := stateRows.Close(); err != nil {
		return input, err
	}
	for index := range input.States {
		rows, err := query.QueryContext(ctx, `select event_id from evidence_records where run_id=$1 and candidate_state_id=$2 order by occurred_at,event_id`, runID, input.States[index].ID)
		if err != nil {
			return input, err
		}
		for rows.Next() {
			var eventID string
			if err := rows.Scan(&eventID); err != nil {
				rows.Close()
				return input, err
			}
			input.States[index].EventIDs = append(input.States[index].EventIDs, eventID)
		}
		if err := rows.Close(); err != nil {
			return input, err
		}
	}

	requestRows, err := query.QueryContext(ctx, `select id,run_id,candidate_state_id,coalesce(requested_role,''),rationale,status,created_at from verification_requests where run_id=$1 order by id`, runID)
	if err != nil {
		return input, err
	}
	for requestRows.Next() {
		var value model.VerificationRequest
		if err := requestRows.Scan(&value.ID, &value.RunID, &value.CandidateStateID, &value.RequestedRole, &value.Rationale, &value.Status, &value.CreatedAt); err != nil {
			requestRows.Close()
			return input, err
		}
		input.VerificationRequests = append(input.VerificationRequests, value)
	}
	if err := requestRows.Close(); err != nil {
		return input, err
	}

	recommendationRows, err := query.QueryContext(ctx, `select id,tenant_id,coalesce(candidate_state_id,''),coalesce(claim_id,''),run_id,actor_id,role,build_id,environment_id,coalesce(scenario_id,''),coalesce(device,''),coalesce(region,''),area,coalesce(classification,''),coalesce(max_verification_depth,0),coalesce(independent_source_count,0),coalesce(conflict_count,0),rationale,score,factors,coalesce(policy_version,''),coalesce(evidence_snapshot,''),coalesce(progression_snapshot,''),coalesce(override_alternatives,'[]'::jsonb),coalesce(promoted_by_response_id,''),coalesce(feature_flags,'{}'::jsonb),created_at from recommendation_events where run_id=$1 order by id`, runID)
	if err != nil {
		return input, err
	}
	for recommendationRows.Next() {
		var value model.Recommendation
		var factors, alternatives, featureFlags []byte
		if err := recommendationRows.Scan(&value.ID, &value.TenantID, &value.CandidateStateID, &value.ClaimID, &value.RunID, &value.ActorID, &value.Role, &value.BuildID, &value.EnvironmentID, &value.ScenarioID, &value.Device, &value.Region, &value.Area, &value.Classification, &value.MaxVerificationDepth, &value.IndependentSourceCount, &value.ConflictCount, &value.Rationale, &value.Score, &factors, &value.PolicyVersion, &value.EvidenceSnapshot, &value.ProgressionSnapshot, &alternatives, &value.PromotedByResponseID, &featureFlags, &value.CreatedAt); err != nil {
			recommendationRows.Close()
			return input, err
		}
		if err := json.Unmarshal(factors, &value.Factors); err != nil {
			recommendationRows.Close()
			return input, err
		}
		if err := json.Unmarshal(alternatives, &value.OverrideAlternatives); err != nil {
			recommendationRows.Close()
			return input, err
		}
		if err := json.Unmarshal(featureFlags, &value.FeatureFlags); err != nil {
			recommendationRows.Close()
			return input, err
		}
		input.Recommendations = append(input.Recommendations, value)
	}
	if err := recommendationRows.Close(); err != nil {
		return input, err
	}

	responseRows, err := query.QueryContext(ctx, `select id,run_id,recommendation_id,response,reason,actor_id,coalesce(override_claim_id,''),created_at from recommendation_responses where run_id=$1 order by id`, runID)
	if err != nil {
		return input, err
	}
	for responseRows.Next() {
		var value model.RecommendationResponseRecord
		if err := responseRows.Scan(&value.ID, &value.RunID, &value.RecommendationID, &value.Response, &value.Reason, &value.ActorID, &value.OverrideClaimID, &value.CreatedAt); err != nil {
			responseRows.Close()
			return input, err
		}
		input.Responses = append(input.Responses, value)
	}
	if err := responseRows.Close(); err != nil {
		return input, err
	}

	correctionRows, err := query.QueryContext(ctx, `select id,run_id,correction_type,state_id,before_state,after_state,corrected_by,reason,created_at from state_corrections where run_id=$1 order by id`, runID)
	if err != nil {
		return input, err
	}
	for correctionRows.Next() {
		var value model.StateCorrection
		var beforeRaw, afterRaw []byte
		if err := correctionRows.Scan(&value.ID, &value.RunID, &value.Type, &value.StateID, &beforeRaw, &afterRaw, &value.CorrectedBy, &value.Reason, &value.CreatedAt); err != nil {
			correctionRows.Close()
			return input, err
		}
		if err := json.Unmarshal(beforeRaw, &value.BeforeState); err != nil {
			correctionRows.Close()
			return input, err
		}
		if err := json.Unmarshal(afterRaw, &value.Request); err != nil {
			correctionRows.Close()
			return input, err
		}
		input.Corrections = append(input.Corrections, value)
	}
	if err := correctionRows.Close(); err != nil {
		return input, err
	}
	return input, nil
}

func (store *Postgres) Metrics(ctx context.Context, runID, tenantID string) (model.ExperimentMetrics, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var run model.ExperimentRun
	var frozenAt sql.NullTime
	err := store.db.QueryRowContext(ctx, `select id,tenant_id,build_id,environment_id,scenario_id,mode,started_at,frozen_at,created_by from experiment_runs where id=$1`, runID).Scan(&run.ID, &run.TenantID, &run.BuildID, &run.EnvironmentID, &run.ScenarioID, &run.Mode, &run.StartedAt, &frozenAt, &run.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ExperimentMetrics{}, ErrUnknownRun
	}
	if err != nil {
		return model.ExperimentMetrics{}, err
	}
	if run.TenantID != tenantID {
		return model.ExperimentMetrics{}, ErrRunScopeMismatch
	}
	if frozenAt.Valid {
		var raw []byte
		if err := store.db.QueryRowContext(ctx, `select snapshot from experiment_metrics where run_id=$1 and definition_version=$2`, runID, experiment.DefinitionVersion).Scan(&raw); err != nil {
			return model.ExperimentMetrics{}, err
		}
		var snapshot model.ExperimentMetrics
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return model.ExperimentMetrics{}, err
		}
		return snapshot, nil
	}
	input, err := loadMetricInputs(ctx, store.db, runID)
	if err != nil {
		return model.ExperimentMetrics{}, err
	}
	metrics, _, err := experiment.Compute(run, input, false)
	return metrics, err
}

// FreezeExperiment inserts the metrics snapshot BEFORE setting frozen_at, in the
// same transaction. That ordering is load-bearing: experiment_metrics carries the
// experiment_metrics_writable_run trigger, which calls the same
// assert_run_attributed_write_is_open() check as every other run-attributed table.
// Setting frozen_at first would make the snapshot insert fail with run_frozen and
// freezing would be impossible. Do not reorder these two statements.
func (store *Postgres) FreezeExperiment(ctx context.Context, runID, tenantID string) (model.ExperimentMetrics, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	defer tx.Rollback()
	var run model.ExperimentRun
	var frozenAt sql.NullTime
	err = tx.QueryRowContext(ctx, `select id,tenant_id,build_id,environment_id,scenario_id,mode,started_at,frozen_at,created_by from experiment_runs where id=$1 for update`, runID).Scan(&run.ID, &run.TenantID, &run.BuildID, &run.EnvironmentID, &run.ScenarioID, &run.Mode, &run.StartedAt, &frozenAt, &run.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ExperimentMetrics{}, false, ErrUnknownRun
	}
	if err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	if run.TenantID != tenantID {
		return model.ExperimentMetrics{}, false, ErrRunScopeMismatch
	}
	if frozenAt.Valid {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `select snapshot from experiment_metrics where run_id=$1 and definition_version=$2`, runID, experiment.DefinitionVersion).Scan(&raw); err != nil {
			return model.ExperimentMetrics{}, false, ErrFreezeInvariant
		}
		var snapshot model.ExperimentMetrics
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return model.ExperimentMetrics{}, false, err
		}
		return snapshot, false, nil
	}
	var frozen time.Time
	if err := tx.QueryRowContext(ctx, `select transaction_timestamp()`).Scan(&frozen); err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	frozen = frozen.UTC()
	run.FrozenAt = &frozen
	input, err := loadMetricInputs(ctx, tx, runID)
	if err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	metrics, canonicalInput, err := experiment.Compute(run, input, true)
	if errors.Is(err, experiment.ErrInputInvariant) {
		return model.ExperimentMetrics{}, false, ErrFreezeInvariant
	}
	if err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	metricsRaw, _ := json.Marshal(metrics.Metrics)
	receiptRaw, _ := json.Marshal(metrics.InputReceipt)
	snapshotRaw, _ := json.Marshal(metrics)
	_, err = tx.ExecContext(ctx, `insert into experiment_metrics (id,run_id,definition_version,metrics,computed_at,canonical_input,input_receipt,snapshot) values ($1,$2,$3,$4,$5,$6,$7,$8)`, stableID("metrics", runID+"|"+experiment.DefinitionVersion), runID, experiment.DefinitionVersion, metricsRaw, metrics.AsOf, canonicalInput, receiptRaw, snapshotRaw)
	if err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `update experiment_runs set frozen_at=$2 where id=$1 and frozen_at is null`, runID, frozen); err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	store.memory.runs[runID] = run
	store.memory.snapshots[runID] = cloneMetrics(metrics)
	return metrics, true, nil
}

func (store *Postgres) SaveClaimDefinition(ctx context.Context, claim model.ClaimDefinition) (model.ClaimDefinition, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	staged := store.memory.Clone()
	saved, created, err := staged.SaveClaimDefinition(ctx, claim)
	if err != nil {
		return model.ClaimDefinition{}, false, err
	}
	if !created {
		return saved, false, nil
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ClaimDefinition{}, false, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `insert into tenants (id, name) values ($1, $1) on conflict (id) do nothing`, claim.TenantID); err != nil {
		return model.ClaimDefinition{}, false, err
	}

	var existingID string
	err = tx.QueryRowContext(ctx, `select id from claim_definitions where id = $1`, claim.ID).Scan(&existingID)
	if err == nil {
		var existing model.ClaimDefinition
		err = tx.QueryRowContext(ctx, `select id, tenant_id, title, coalesce(description, ''), target, state_signature, actor_context, validation_intent, created_at from claim_definitions where id = $1`, claim.ID).Scan(
			&existing.ID, &existing.TenantID, &existing.Title, &existing.Description, &existing.Target, &existing.StateSignature, &existing.ActorContext, &existing.ValidationIntent, &existing.CreatedAt,
		)
		if err == nil {
			if existing.TenantID == claim.TenantID &&
				existing.Title == claim.Title && existing.Description == claim.Description &&
				existing.Target == claim.Target && existing.StateSignature == claim.StateSignature &&
				existing.ActorContext == claim.ActorContext && existing.ValidationIntent == claim.ValidationIntent {
				return existing, false, nil
			}
			return model.ClaimDefinition{}, false, ErrClaimConflict
		}
		return model.ClaimDefinition{}, false, err
	}

	var insertedCreatedAt time.Time
	err = tx.QueryRowContext(ctx, `insert into claim_definitions (id, tenant_id, title, description, target, state_signature, actor_context, validation_intent) values ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8) returning created_at`,
		claim.ID, claim.TenantID, claim.Title, claim.Description, claim.Target, claim.StateSignature, claim.ActorContext, claim.ValidationIntent,
	).Scan(&insertedCreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "claim_definitions_semantic_identity_unique") || strings.Contains(err.Error(), "23505") {
			return model.ClaimDefinition{}, false, ErrClaimConflict
		}
		return model.ClaimDefinition{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return model.ClaimDefinition{}, false, err
	}

	saved.CreatedAt = insertedCreatedAt
	staged.claims[saved.TenantID+":"+saved.ID] = saved
	store.memory = staged
	return saved, true, nil
}

func (store *Postgres) GetClaimDefinition(ctx context.Context, tenantID, claimID string) (model.ClaimDefinition, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.memory.GetClaimDefinition(ctx, tenantID, claimID)
}

func (store *Postgres) SaveEvidenceContext(ctx context.Context, ec model.EvidenceContext) (model.EvidenceContext, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	staged := store.memory.Clone()
	saved, created, err := staged.SaveEvidenceContext(ctx, ec)
	if err != nil {
		return model.EvidenceContext{}, false, err
	}
	if !created {
		return saved, false, nil
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.EvidenceContext{}, false, err
	}
	defer tx.Rollback()

	flagsRaw, _ := json.Marshal(ec.FeatureFlags)
	if ec.FeatureFlags == nil {
		flagsRaw = []byte("{}")
	}

	var insertedCreatedAt time.Time
	err = tx.QueryRowContext(ctx, `insert into evidence_contexts (id, tenant_id, run_id, build_id, environment_id, scenario_id, feature_flags, device, region) values ($1, $2, $3, $4, $5, nullif($6, ''), $7, nullif($8, ''), nullif($9, '')) returning created_at`,
		ec.ID, ec.TenantID, ec.RunID, ec.BuildID, ec.EnvironmentID, ec.ScenarioID, flagsRaw, ec.Device, ec.Region,
	).Scan(&insertedCreatedAt)
	if err != nil {
		mapped := mapRunError(err)
		if strings.Contains(err.Error(), "23505") {
			return model.EvidenceContext{}, false, ErrEvidenceContextConflict
		}
		return model.EvidenceContext{}, false, mapped
	}

	if err := tx.Commit(); err != nil {
		return model.EvidenceContext{}, false, err
	}

	saved.CreatedAt = insertedCreatedAt
	staged.evidenceContexts[saved.ID] = saved
	store.memory = staged
	return saved, true, nil
}

func (store *Postgres) SaveObservation(ctx context.Context, obs model.Observation) (model.Observation, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	staged := store.memory.Clone()
	saved, created, err := staged.SaveObservation(ctx, obs)
	if err != nil {
		return model.Observation{}, false, err
	}
	if !created {
		return saved, false, nil
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Observation{}, false, err
	}
	defer tx.Rollback()

	var insertedCreatedAt time.Time
	err = tx.QueryRowContext(ctx, `insert into observations (id, tenant_id, run_id, claim_id, evidence_context_id, event_id, source, source_adapter, actor_id, assessment, verifier_result, occurred_at) values ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10, nullif($11, ''), $12) returning created_at`,
		obs.ID, obs.TenantID, obs.RunID, obs.ClaimID, obs.EvidenceContextID, obs.EventID, obs.SourceType, obs.SourceAdapter, obs.ActorID, obs.Assessment, obs.VerifierResult, obs.OccurredAt,
	).Scan(&insertedCreatedAt)
	if err != nil {
		mapped := mapRunError(err)
		if strings.Contains(err.Error(), "23505") {
			return model.Observation{}, false, ErrObservationConflict
		}
		return model.Observation{}, false, mapped
	}

	if err := tx.Commit(); err != nil {
		return model.Observation{}, false, err
	}

	saved.CreatedAt = insertedCreatedAt
	saved.OccurredAt = saved.OccurredAt.UTC()
	staged.observations[saved.ID] = saved
	store.memory = staged
	return saved, true, nil
}

func (store *Postgres) GetClaimEvidence(ctx context.Context, tenantID, claimID string) (model.ClaimEvidence, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.memory.GetClaimEvidence(ctx, tenantID, claimID)
}

func (store *Postgres) GetClaimEvidenceSummary(ctx context.Context, scope Scope, claimID string) (model.ClaimEvidenceSummary, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.memory.GetClaimEvidenceSummary(ctx, scope, claimID)
}

func (store *Postgres) ListClaimEvidenceSummaries(ctx context.Context, scope Scope) ([]model.ClaimEvidenceSummary, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.memory.ListClaimEvidenceSummaries(ctx, scope)
}

func (store *Postgres) Ping(ctx context.Context) error { return store.db.PingContext(ctx) }
func (store *Postgres) Close()                         { _ = store.db.Close() }
