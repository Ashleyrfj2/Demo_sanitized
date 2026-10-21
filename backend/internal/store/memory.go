package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/example/qa-platform/backend/internal/evidence"
	"github.com/example/qa-platform/backend/internal/experiment"
	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/routing"
	"github.com/example/qa-platform/backend/internal/state"
	"github.com/example/qa-platform/backend/internal/sufficiency"
)

type candidateScopeKey struct {
	RunID            string
	TenantID         string
	BuildID          string
	EnvironmentID    string
	Role             string
	StateFingerprint string
}

type Memory struct {
	mu                     sync.Mutex
	builds                 map[string]model.RegisteredBuild
	runs                   map[string]model.ExperimentRun
	snapshots              map[string]model.ExperimentMetrics
	events                 map[string]model.ValidationEvent
	states                 map[string]model.CandidateState
	stateEvents            map[string][]string
	requests               map[string]model.VerificationRequest
	recommendations        map[string]model.Recommendation
	responses              map[string]model.RecommendationResponseRecord
	aliases                map[string]string
	corrections            map[string]model.StateCorrection
	candidateIndex         map[candidateScopeKey]string
	baselines              map[string]model.CandidateState
	baselineReplayedEvents map[string]int
	claims                 map[string]model.ClaimDefinition
	claimSemantics         map[string]string
	evidenceContexts       map[string]model.EvidenceContext
	observations           map[string]model.Observation
	claimObs               map[string][]string
	routingPolicy          routing.Policy
	loadCompleted          bool
}

func NewMemory() *Memory {
	return &Memory{
		builds: map[string]model.RegisteredBuild{}, runs: map[string]model.ExperimentRun{}, snapshots: map[string]model.ExperimentMetrics{},
		events: map[string]model.ValidationEvent{}, states: map[string]model.CandidateState{},
		stateEvents: map[string][]string{}, requests: map[string]model.VerificationRequest{},
		recommendations: map[string]model.Recommendation{}, responses: map[string]model.RecommendationResponseRecord{},
		aliases: map[string]string{}, corrections: map[string]model.StateCorrection{},
		candidateIndex: map[candidateScopeKey]string{}, baselines: map[string]model.CandidateState{},
		baselineReplayedEvents: map[string]int{},
		claims:                 map[string]model.ClaimDefinition{},
		claimSemantics:         map[string]string{},
		evidenceContexts:       map[string]model.EvidenceContext{},
		observations:           map[string]model.Observation{},
		claimObs:               map[string][]string{},
		routingPolicy:          routing.ActivePolicy(),
	}
}

func (memory *Memory) loadBaselineCandidate(candidate model.CandidateState) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	role := candidate.Role
	if role == "" {
		role = "unknown"
	}
	key := candidateScopeKey{
		RunID:            candidate.RunID,
		TenantID:         candidate.TenantID,
		BuildID:          candidate.BuildID,
		EnvironmentID:    candidate.EnvironmentID,
		Role:             role,
		StateFingerprint: candidate.StateFingerprint,
	}
	memory.candidateIndex[key] = candidate.ID
	memory.baselines[candidate.ID] = cloneCandidate(candidate)
}

func (memory *Memory) loadEvidenceContext(ec model.EvidenceContext) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if ec.CreatedAt.IsZero() {
		ec.CreatedAt = time.Now().UTC()
	} else {
		ec.CreatedAt = ec.CreatedAt.UTC()
	}
	memory.evidenceContexts[ec.ID] = ec
}

func (memory *Memory) loadObservation(obs model.Observation) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = time.Now().UTC()
	} else {
		obs.CreatedAt = obs.CreatedAt.UTC()
	}
	obs.OccurredAt = obs.OccurredAt.UTC().Truncate(time.Microsecond)
	claimKey := obs.TenantID + ":" + obs.ClaimID
	memory.observations[obs.ID] = obs
	memory.claimObs[claimKey] = append(memory.claimObs[claimKey], obs.ID)
}

func (memory *Memory) markLoadComplete() {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.loadCompleted = true
	for id := range memory.baselines {
		memory.baselineReplayedEvents[id] = len(memory.stateEvents[id])
	}
}

func (memory *Memory) Clone() *Memory {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	clone := NewMemory()
	for key, value := range memory.builds {
		clone.builds[key] = value
	}
	for key, value := range memory.runs {
		clone.runs[key] = value
	}
	for key, value := range memory.snapshots {
		clone.snapshots[key] = cloneMetrics(value)
	}
	for key, value := range memory.events {
		clone.events[key] = cloneEvent(value)
	}
	for key, value := range memory.states {
		clone.states[key] = cloneCandidate(value)
	}
	for key, value := range memory.stateEvents {
		clone.stateEvents[key] = append([]string(nil), value...)
	}
	for key, value := range memory.requests {
		clone.requests[key] = value
	}
	for key, value := range memory.recommendations {
		value.FeatureFlags = cloneStringMap(value.FeatureFlags)
		value.OverrideAlternatives = append([]model.RecommendationAlternative(nil), value.OverrideAlternatives...)
		clone.recommendations[key] = value
	}
	for key, value := range memory.responses {
		clone.responses[key] = value
	}
	for key, value := range memory.aliases {
		clone.aliases[key] = value
	}
	for key, value := range memory.corrections {
		clone.corrections[key] = value
	}
	for key, value := range memory.candidateIndex {
		clone.candidateIndex[key] = value
	}
	for key, value := range memory.baselines {
		clone.baselines[key] = cloneCandidate(value)
	}
	for key, value := range memory.baselineReplayedEvents {
		clone.baselineReplayedEvents[key] = value
	}
	for key, value := range memory.claims {
		clone.claims[key] = value
	}
	for key, value := range memory.claimSemantics {
		clone.claimSemantics[key] = value
	}
	for key, value := range memory.evidenceContexts {
		if value.FeatureFlags != nil {
			flags := make(map[string]string, len(value.FeatureFlags))
			for fk, fv := range value.FeatureFlags {
				flags[fk] = fv
			}
			value.FeatureFlags = flags
		}
		clone.evidenceContexts[key] = value
	}
	for key, value := range memory.observations {
		clone.observations[key] = value
	}
	for key, value := range memory.claimObs {
		clone.claimObs[key] = append([]string(nil), value...)
	}
	clone.routingPolicy = cloneRoutingPolicy(memory.routingPolicy)
	clone.loadCompleted = memory.loadCompleted
	return clone
}

func (memory *Memory) RegisterBuild(_ context.Context, registration model.BuildRegistration, registeredBy string) (model.RegisteredBuild, bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.registerBuild(registration, registeredBy, time.Now().UTC())
}

func (memory *Memory) registerBuild(registration model.BuildRegistration, registeredBy string, registeredAt time.Time) (model.RegisteredBuild, bool, error) {
	key := buildKey(registration.TenantID, registration.EnvironmentID, registration.BuildID)
	if existing, ok := memory.builds[key]; ok {
		if existing.SequenceNumber == registration.SequenceNumber && existing.PredecessorBuildID == registration.PredecessorBuildID {
			return existing, false, nil
		}
		return model.RegisteredBuild{}, false, ErrBuildRegistrationConflict
	}
	if registration.TenantID == "" || registration.EnvironmentID == "" || registration.BuildID == "" || registration.SequenceNumber < 1 || registeredBy == "" {
		return model.RegisteredBuild{}, false, ErrInvalidBuildChain
	}
	for _, existing := range memory.builds {
		if existing.TenantID == registration.TenantID && existing.EnvironmentID == registration.EnvironmentID && existing.SequenceNumber == registration.SequenceNumber {
			return model.RegisteredBuild{}, false, ErrBuildRegistrationConflict
		}
	}
	if registration.SequenceNumber == 1 {
		if registration.PredecessorBuildID != "" {
			return model.RegisteredBuild{}, false, ErrInvalidBuildChain
		}
	} else {
		predecessor, ok := memory.builds[buildKey(registration.TenantID, registration.EnvironmentID, registration.PredecessorBuildID)]
		if !ok || predecessor.SequenceNumber != registration.SequenceNumber-1 {
			return model.RegisteredBuild{}, false, ErrInvalidBuildChain
		}
	}
	registered := model.RegisteredBuild{BuildRegistration: registration, RegisteredBy: registeredBy, RegisteredAt: registeredAt.UTC()}
	memory.builds[key] = registered
	memory.applyBuildFreshness(registration.TenantID, registration.EnvironmentID)
	return registered, true, nil
}

func (memory *Memory) RequireRegisteredBuild(_ context.Context, tenantID, environmentID, buildID string) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if _, ok := memory.builds[buildKey(tenantID, environmentID, buildID)]; !ok {
		return ErrBuildNotRegistered
	}
	return nil
}

func (memory *Memory) CreateExperiment(_ context.Context, registration model.ExperimentRegistration, createdBy string) (model.ExperimentRun, bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.createExperiment(registration, createdBy, time.Now().UTC())
}

func (memory *Memory) createExperiment(registration model.ExperimentRegistration, createdBy string, startedAt time.Time) (model.ExperimentRun, bool, error) {
	if existing, ok := memory.runs[registration.ID]; ok {
		if existing.TenantID == registration.TenantID && existing.BuildID == registration.BuildID && existing.EnvironmentID == registration.EnvironmentID && existing.ScenarioID == registration.ScenarioID && existing.Mode == registration.Mode {
			return existing, false, nil
		}
		return model.ExperimentRun{}, false, ErrExperimentConflict
	}
	if strings.TrimSpace(registration.ID) == "" || strings.TrimSpace(registration.TenantID) == "" || strings.TrimSpace(registration.BuildID) == "" || strings.TrimSpace(registration.EnvironmentID) == "" || strings.TrimSpace(registration.ScenarioID) == "" || strings.TrimSpace(createdBy) == "" || registration.Mode != "baseline" && registration.Mode != "guided" {
		return model.ExperimentRun{}, false, ErrContractInvalid
	}
	if _, ok := memory.builds[buildKey(registration.TenantID, registration.EnvironmentID, registration.BuildID)]; !ok {
		return model.ExperimentRun{}, false, ErrBuildNotRegistered
	}
	run := model.ExperimentRun{ID: registration.ID, TenantID: registration.TenantID, BuildID: registration.BuildID, EnvironmentID: registration.EnvironmentID, ScenarioID: registration.ScenarioID, Mode: registration.Mode, StartedAt: startedAt.UTC(), CreatedBy: createdBy}
	memory.runs[run.ID] = run
	return run, true, nil
}

func (memory *Memory) writableRun(runID string) (model.ExperimentRun, error) {
	if strings.TrimSpace(runID) == "" {
		return model.ExperimentRun{}, ErrUnknownRun
	}
	run, ok := memory.runs[runID]
	if !ok {
		return model.ExperimentRun{}, ErrUnknownRun
	}
	if run.FrozenAt != nil {
		return model.ExperimentRun{}, ErrRunFrozen
	}
	return run, nil
}

func runMatchesEvent(run model.ExperimentRun, event model.ValidationEvent) bool {
	return run.TenantID == event.TenantID && run.BuildID == event.BuildID && run.EnvironmentID == event.EnvironmentID && run.ScenarioID == event.Context["scenario_id"]
}

func runMatchesScope(run model.ExperimentRun, scope Scope) bool {
	return run.TenantID == scope.TenantID && run.BuildID == scope.BuildID && run.EnvironmentID == scope.EnvironmentID &&
		(scope.ScenarioID == "" || run.ScenarioID == scope.ScenarioID)
}

func (memory *Memory) SaveEvent(_ context.Context, event model.ValidationEvent) (bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	run, err := memory.writableRun(event.RunID)
	if err != nil {
		return false, err
	}
	if !runMatchesEvent(run, event) {
		return false, ErrRunScopeMismatch
	}
	if _, ok := memory.builds[buildKey(event.TenantID, event.EnvironmentID, event.BuildID)]; !ok {
		return false, ErrBuildNotRegistered
	}
	if existing, exists := memory.events[event.EventID]; exists {
		if sameEvent(existing, event) {
			return false, nil
		}
		return false, ErrEventConflict
	}
	memory.events[event.EventID] = event
	identity := state.New().Identify(event)
	identity.ID = memory.canonicalStateIDForEvent(event)
	role := strings.TrimSpace(event.Context["role"])
	if role == "" {
		role = "unknown"
	}
	key := candidateScopeKey{
		RunID:            event.RunID,
		TenantID:         event.TenantID,
		BuildID:          event.BuildID,
		EnvironmentID:    event.EnvironmentID,
		Role:             role,
		StateFingerprint: event.StateFingerprint,
	}
	memory.candidateIndex[key] = identity.ID

	candidate := memory.states[identity.ID]
	if candidate.ID == "" {
		if baseline, ok := memory.baselines[identity.ID]; ok {
			candidate = cloneCandidate(baseline)
		} else {
			candidate = model.CandidateState{ID: identity.ID, RunID: event.RunID, TenantID: event.TenantID, BuildID: event.BuildID, EnvironmentID: event.EnvironmentID, Role: identity.Role, Module: identity.Module, RecordID: identity.RecordID, BusinessState: identity.BusinessState, StateFingerprint: event.StateFingerprint}
		}
	}
	memory.stateEvents[identity.ID] = append(memory.stateEvents[identity.ID], event.EventID)
	memory.recompute(identity.ID, &candidate)
	memory.states[identity.ID] = candidate
	memory.recomputeCorrelated(candidate)
	memory.applyBuildFreshness(event.TenantID, event.EnvironmentID)
	candidate = memory.states[identity.ID]
	if candidate.Status == string(sufficiency.Conflicting) || candidate.Status == string(sufficiency.NeedsIndependentVerification) {
		requestID := stableID("verification", event.RunID+"|"+identity.ID)
		memory.requests[requestID] = model.VerificationRequest{ID: requestID, RunID: event.RunID, CandidateStateID: identity.ID, RequestedRole: "viewer", Rationale: "Conflicting or single-context evidence requires an independent reconnect verification.", Status: "open", CreatedAt: candidate.UpdatedAt}
	}
	return true, nil
}

func (memory *Memory) recomputeCorrelated(changed model.CandidateState) {
	correlatedIDs := []string{}
	combinedEvents := []model.ValidationEvent{}
	for id, candidate := range memory.states {
		if candidate.RunID == changed.RunID && candidate.TenantID == changed.TenantID && candidate.BuildID == changed.BuildID && candidate.EnvironmentID == changed.EnvironmentID && candidate.RecordID == changed.RecordID && candidate.BusinessState == changed.BusinessState {
			correlatedIDs = append(correlatedIDs, id)
			for _, eventID := range memory.stateEvents[id] {
				combinedEvents = append(combinedEvents, memory.events[eventID])
			}
		}
	}
	evaluation := sufficiency.New().Evaluate(combinedEvents)
	if evaluation.Status != sufficiency.Strong {
		return
	}
	for _, id := range correlatedIDs {
		candidate := memory.states[id]
		candidate.Status, candidate.Conflict, candidate.UpdatedAt = string(sufficiency.Strong), false, latestOccurredAt(combinedEvents)
		memory.states[id] = candidate
		requestID := stableID("verification", candidate.RunID+"|"+id)
		if request, ok := memory.requests[requestID]; ok {
			request.Status = "completed"
			memory.requests[requestID] = request
		}
	}
}

func (memory *Memory) recompute(id string, candidate *model.CandidateState) {
	events := make([]model.ValidationEvent, 0, len(memory.stateEvents[id]))
	for _, eventID := range memory.stateEvents[id] {
		events = append(events, memory.events[eventID])
	}
	evaluation := sufficiency.New().Evaluate(events)

	baseline, hasBaseline := memory.baselines[id]
	baselineIncomplete := hasBaseline && memory.baselineReplayedEvents[id] < baseline.EvidenceCount

	if baselineIncomplete {
		baselineReplayed := memory.baselineReplayedEvents[id]
		newLiveEvents := 0
		if memory.loadCompleted && len(events) > baselineReplayed {
			newLiveEvents = len(events) - baselineReplayed
		}

		candidate.EvidenceCount = baseline.EvidenceCount + newLiveEvents
		candidate.IndependentActorCount = maxInt(baseline.IndependentActorCount, evaluation.ActorCount)

		sourceSet := map[string]bool{}
		for _, s := range baseline.SourceTypes {
			sourceSet[s] = true
		}
		for _, s := range evaluation.SourceTypes {
			sourceSet[s] = true
		}
		mergedSources := make([]string, 0, len(sourceSet))
		for s := range sourceSet {
			mergedSources = append(mergedSources, s)
		}
		sort.Strings(mergedSources)
		candidate.SourceTypes = mergedSources

		if evaluation.Conflict || baseline.Conflict {
			candidate.Status = string(sufficiency.Conflicting)
			candidate.Conflict = true
		} else {
			candidate.Conflict = false
			candidate.Status = maxStatus(baseline.Status, string(evaluation.Status))
		}

		candidate.UpdatedAt = latestTime(baseline.UpdatedAt, latestOccurredAt(events))
	} else {
		if hasBaseline && baseline.Conflict {
			candidate.Status = string(sufficiency.Conflicting)
			candidate.Conflict = true
		} else {
			candidate.Status, candidate.Conflict = string(evaluation.Status), evaluation.Conflict
		}
		candidate.EvidenceCount, candidate.IndependentActorCount = len(events), evaluation.ActorCount
		candidate.SourceTypes, candidate.UpdatedAt = evaluation.SourceTypes, latestOccurredAt(events)
	}
	candidate.EventIDs = append([]string(nil), memory.stateEvents[id]...)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func statusRank(status string) int {
	switch status {
	case string(sufficiency.Strong):
		return 4
	case string(sufficiency.NeedsIndependentVerification):
		return 3
	case string(sufficiency.Conflicting):
		return 2
	case string(sufficiency.Weak):
		return 1
	case string(sufficiency.Stale):
		return 0
	default:
		return 1
	}
}

func maxStatus(a, b string) string {
	if statusRank(a) >= statusRank(b) {
		return a
	}
	return b
}

func latestTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a.UTC()
	}
	return b.UTC()
}

func (memory *Memory) Evidence(_ context.Context, scope Scope) (model.EvidenceResponse, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	run, ok := memory.runs[scope.RunID]
	if !ok {
		return model.EvidenceResponse{}, ErrUnknownRun
	}
	if !runMatchesScope(run, scope) {
		return model.EvidenceResponse{}, ErrRunScopeMismatch
	}
	if _, ok := memory.builds[buildKey(scope.TenantID, scope.EnvironmentID, scope.BuildID)]; !ok {
		return model.EvidenceResponse{}, ErrBuildNotRegistered
	}
	result := model.EvidenceResponse{RunID: scope.RunID, TenantID: scope.TenantID, BuildID: scope.BuildID, EnvironmentID: scope.EnvironmentID, States: []model.CandidateState{}, VerificationRequests: []model.VerificationRequest{}}
	for _, candidate := range memory.states {
		if candidate.RunID == scope.RunID && candidate.TenantID == scope.TenantID && candidate.BuildID == scope.BuildID && candidate.EnvironmentID == scope.EnvironmentID {
			result.States = append(result.States, memory.withBuildFreshness(candidate))
		}
	}
	for _, request := range memory.requests {
		if request.Status != "open" {
			continue
		}
		if request.RunID == scope.RunID {
			result.VerificationRequests = append(result.VerificationRequests, request)
		}
	}
	sort.Slice(result.States, func(i, j int) bool { return result.States[i].UpdatedAt.After(result.States[j].UpdatedAt) })
	return result, nil
}

func (memory *Memory) NextRecommendation(_ context.Context, query RecommendationQuery) (model.Recommendation, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	run, err := memory.writableRun(query.RunID)
	if err != nil {
		return model.Recommendation{}, err
	}
	if !runMatchesScope(run, query.Scope) {
		return model.Recommendation{}, ErrRunScopeMismatch
	}
	if _, ok := memory.builds[buildKey(query.TenantID, query.EnvironmentID, query.BuildID)]; !ok {
		return model.Recommendation{}, ErrBuildNotRegistered
	}
	summaries := memory.claimEvidenceSummaries(query.Scope)
	snapshot, err := routing.EvidenceSnapshot(summaries)
	if err != nil {
		return model.Recommendation{}, err
	}
	progression, err := memory.recommendationProgression(query, snapshot)
	if err != nil {
		return model.Recommendation{}, err
	}
	for _, existing := range memory.recommendations {
		if recommendationMatchesQuery(existing, query, memory.routingPolicy.Version, snapshot) &&
			existing.ProgressionSnapshot == progression.Snapshot {
			return existing, nil
		}
	}
	candidates, err := memory.eligibleRecommendationCandidates(summaries, query.Role, progression.ExcludedClaims)
	if err != nil {
		return model.Recommendation{}, err
	}
	if len(candidates) == 0 {
		return model.Recommendation{}, ErrNotFound
	}
	selected := candidates[0]
	promotedByResponseID := ""
	if progression.LatestResponse != nil && progression.LatestResponse.Response == "override" {
		promoted := false
		for _, candidate := range candidates {
			if candidate.Summary.ClaimID == progression.LatestResponse.OverrideClaimID {
				selected = candidate
				promotedByResponseID = progression.LatestResponse.ID
				promoted = true
				break
			}
		}
		if !promoted {
			return model.Recommendation{}, ErrRecommendationProgressionInvalid
		}
	}
	id := stableID("recommendation", strings.Join([]string{
		query.RunID, query.TenantID, query.BuildID, query.EnvironmentID, query.ScenarioID,
		query.Device, query.Region, canonicalStringMap(query.FeatureFlags), query.ActorID, query.Role,
		memory.routingPolicy.Version, snapshot, progression.Snapshot,
	}, "|"))
	if existing, ok := memory.recommendations[id]; ok {
		return existing, nil
	}
	recommendation := model.Recommendation{
		ID: id, TenantID: query.TenantID, ClaimID: selected.Summary.ClaimID, RunID: query.RunID,
		ActorID: query.ActorID, Role: query.Role, BuildID: query.BuildID, EnvironmentID: query.EnvironmentID,
		ScenarioID: query.ScenarioID, Device: query.Device, Region: query.Region,
		FeatureFlags: cloneStringMap(query.FeatureFlags),
		Area:         selected.Area, Classification: selected.Summary.Classification,
		MaxVerificationDepth:   selected.Summary.MaxVerificationDepth,
		IndependentSourceCount: selected.Summary.IndependentSourceCount,
		ConflictCount:          selected.Summary.ConflictCount,
		Rationale:              selected.Rationale, Score: selected.Score,
		Factors: model.RecommendationFactors{
			DeltaUncertainty:     selected.Factors.DeltaUncertainty,
			BusinessRisk:         selected.Factors.BusinessRisk,
			DiffImpact:           selected.Factors.DiffImpact,
			SmoothedFailurePrior: selected.Factors.SmoothedFailurePrior,
			ExpectedCost:         selected.Factors.ExpectedCost,
		},
		PolicyVersion: memory.routingPolicy.Version, EvidenceSnapshot: snapshot,
		ProgressionSnapshot:  progression.Snapshot,
		OverrideAlternatives: memory.recommendationAlternatives(candidates, selected.Summary.ClaimID, query.TenantID),
		PromotedByResponseID: promotedByResponseID,
		CreatedAt:            time.Now().UTC(),
	}
	memory.recommendations[id] = recommendation
	return recommendation, nil
}

func canonicalStringMap(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func (memory *Memory) withBuildFreshness(candidate model.CandidateState) model.CandidateState {
	if run, ok := memory.runs[candidate.RunID]; ok && run.FrozenAt != nil {
		return candidate
	}
	registered, ok := memory.builds[buildKey(candidate.TenantID, candidate.EnvironmentID, candidate.BuildID)]
	if !ok {
		return candidate
	}
	for _, other := range memory.builds {
		if other.TenantID == registered.TenantID && other.EnvironmentID == registered.EnvironmentID && other.SequenceNumber > registered.SequenceNumber {
			candidate.Status = string(sufficiency.Stale)
			return candidate
		}
	}
	return candidate
}

func (memory *Memory) applyBuildFreshness(tenantID, environmentID string) {
	for id, candidate := range memory.states {
		if candidate.TenantID == tenantID && candidate.EnvironmentID == environmentID {
			if run, ok := memory.runs[candidate.RunID]; ok && run.FrozenAt != nil {
				continue
			}
			memory.states[id] = memory.withBuildFreshness(candidate)
		}
	}
}

func buildKey(tenantID, environmentID, buildID string) string {
	return strings.Join([]string{tenantID, environmentID, buildID}, "\x00")
}

func (memory *Memory) SaveRecommendationResponse(_ context.Context, id string, response model.RecommendationResponse) (bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	recommendation, exists := memory.recommendations[id]
	if !exists {
		return false, ErrNotFound
	}
	if _, err := memory.writableRun(recommendation.RunID); err != nil {
		return false, err
	}
	if response.ActorID != recommendation.ActorID {
		return false, ErrRecommendationActorMismatch
	}
	if existing, ok := memory.responses[id]; ok {
		if existing.Response == response.Response && existing.Reason == response.Reason && existing.ActorID == response.ActorID {
			return false, nil
		}
		return false, ErrResponseConflict
	}
	record := model.RecommendationResponseRecord{
		ID:    stableID("response", recommendation.RunID+"|"+id+"|"+response.ActorID+"|"+time.Now().UTC().Format(time.RFC3339Nano)),
		RunID: recommendation.RunID, RecommendationID: id, Response: response.Response,
		Reason: response.Reason, ActorID: response.ActorID, CreatedAt: time.Now().UTC(),
	}
	memory.responses[id] = record
	return true, nil
}

func (memory *Memory) CorrectState(_ context.Context, kind, id string, request model.StateCorrectionRequest) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	candidate, exists := memory.states[id]
	if !exists {
		return ErrNotFound
	}
	if _, err := memory.writableRun(candidate.RunID); err != nil {
		return err
	}
	before := cloneCandidate(candidate)
	switch kind {
	case "rename":
		if request.NewFingerprint == "" || request.NewBusinessState == "" {
			return fmt.Errorf("new_fingerprint and new_business_state are required")
		}
		newID := state.New().IDFor(candidate.RunID, candidate.TenantID, candidate.BuildID, candidate.EnvironmentID, candidate.Role, request.NewFingerprint)
		if existing, exists := memory.states[newID]; exists && existing.ID != id {
			return fmt.Errorf("renamed state already exists")
		}
		oldKey := candidateScopeKey{
			RunID: candidate.RunID, TenantID: candidate.TenantID, BuildID: candidate.BuildID, EnvironmentID: candidate.EnvironmentID,
			Role: candidate.Role, StateFingerprint: candidate.StateFingerprint,
		}
		newKey := candidateScopeKey{
			RunID: candidate.RunID, TenantID: candidate.TenantID, BuildID: candidate.BuildID, EnvironmentID: candidate.EnvironmentID,
			Role: candidate.Role, StateFingerprint: request.NewFingerprint,
		}
		delete(memory.candidateIndex, oldKey)
		memory.candidateIndex[newKey] = newID
		if baseline, ok := memory.baselines[id]; ok {
			baseline.ID, baseline.StateFingerprint, baseline.BusinessState = newID, request.NewFingerprint, request.NewBusinessState
			memory.baselines[newID] = baseline
			if newID != id {
				delete(memory.baselines, id)
			}
		}
		candidate.ID, candidate.StateFingerprint, candidate.BusinessState = newID, request.NewFingerprint, request.NewBusinessState
		memory.stateEvents[newID] = append([]string(nil), memory.stateEvents[id]...)
		if newID != id {
			delete(memory.states, id)
			delete(memory.stateEvents, id)
			memory.aliases[id] = newID
		}
		memory.recompute(newID, &candidate)
		memory.states[newID] = candidate
	case "merge":
		if request.TargetStateID == id {
			return fmt.Errorf("a state cannot be merged into itself")
		}
		target, ok := memory.states[request.TargetStateID]
		if !ok {
			return ErrNotFound
		}
		if candidate.RunID != target.RunID || candidate.TenantID != target.TenantID || candidate.BuildID != target.BuildID || candidate.EnvironmentID != target.EnvironmentID {
			return ErrRunScopeMismatch
		}
		oldKey := candidateScopeKey{
			RunID: candidate.RunID, TenantID: candidate.TenantID, BuildID: candidate.BuildID, EnvironmentID: candidate.EnvironmentID,
			Role: candidate.Role, StateFingerprint: candidate.StateFingerprint,
		}
		delete(memory.candidateIndex, oldKey)
		sourceBaseline, sourceHasBaseline := memory.baselines[id]
		targetBaseline, targetHasBaseline := memory.baselines[target.ID]
		if sourceHasBaseline || targetHasBaseline {
			mergedBaseline := targetBaseline
			if !targetHasBaseline {
				mergedBaseline = sourceBaseline
				mergedBaseline.ID = target.ID
			} else if sourceHasBaseline {
				mergedBaseline.EvidenceCount = targetBaseline.EvidenceCount + sourceBaseline.EvidenceCount
				mergedBaseline.IndependentActorCount = maxInt(targetBaseline.IndependentActorCount, sourceBaseline.IndependentActorCount)

				sourceSet := map[string]bool{}
				for _, s := range targetBaseline.SourceTypes {
					sourceSet[s] = true
				}
				for _, s := range sourceBaseline.SourceTypes {
					sourceSet[s] = true
				}
				mergedSources := make([]string, 0, len(sourceSet))
				for s := range sourceSet {
					mergedSources = append(mergedSources, s)
				}
				sort.Strings(mergedSources)
				mergedBaseline.SourceTypes = mergedSources

				if targetBaseline.Conflict || sourceBaseline.Conflict {
					mergedBaseline.Conflict = true
					mergedBaseline.Status = string(sufficiency.Conflicting)
				} else {
					mergedBaseline.Conflict = false
					mergedBaseline.Status = maxStatus(targetBaseline.Status, sourceBaseline.Status)
				}
				mergedBaseline.UpdatedAt = latestTime(targetBaseline.UpdatedAt, sourceBaseline.UpdatedAt)
			}
			memory.baselines[target.ID] = mergedBaseline
			memory.baselineReplayedEvents[target.ID] = memory.baselineReplayedEvents[target.ID] + memory.baselineReplayedEvents[id]
			delete(memory.baselines, id)
			delete(memory.baselineReplayedEvents, id)
		}
		memory.stateEvents[target.ID] = append(memory.stateEvents[target.ID], memory.stateEvents[id]...)
		memory.recompute(target.ID, &target)
		memory.states[target.ID] = target
		delete(memory.states, id)
		delete(memory.stateEvents, id)
		memory.aliases[id] = target.ID
	case "split":
		if request.NewFingerprint == "" || request.NewBusinessState == "" || len(request.EventIDs) == 0 {
			return fmt.Errorf("new_fingerprint, new_business_state, and event_ids are required")
		}
		if baseline, hasBaseline := memory.baselines[id]; hasBaseline && memory.loadCompleted && memory.baselineReplayedEvents[id] < baseline.EvidenceCount {
			return fmt.Errorf("cannot split candidate with non-replayable historical evidence baseline")
		}
		newID := state.New().IDFor(candidate.RunID, candidate.TenantID, candidate.BuildID, candidate.EnvironmentID, candidate.Role, request.NewFingerprint)
		if _, exists := memory.states[newID]; exists {
			return fmt.Errorf("split target state already exists")
		}
		newKey := candidateScopeKey{
			RunID: candidate.RunID, TenantID: candidate.TenantID, BuildID: candidate.BuildID, EnvironmentID: candidate.EnvironmentID,
			Role: candidate.Role, StateFingerprint: request.NewFingerprint,
		}
		memory.candidateIndex[newKey] = newID
		moved := map[string]struct{}{}
		for _, eventID := range request.EventIDs {
			moved[eventID] = struct{}{}
		}
		remaining, split := []string{}, []string{}
		for _, eventID := range memory.stateEvents[id] {
			if _, ok := moved[eventID]; ok {
				split = append(split, eventID)
			} else {
				remaining = append(remaining, eventID)
			}
		}
		if len(split) == 0 {
			return fmt.Errorf("none of the event_ids belong to the state")
		}
		if len(remaining) == 0 {
			return fmt.Errorf("split must leave evidence in the source state")
		}
		memory.stateEvents[id], memory.stateEvents[newID] = remaining, split
		newCandidate := candidate
		newCandidate.ID, newCandidate.StateFingerprint, newCandidate.BusinessState = newID, request.NewFingerprint, request.NewBusinessState
		memory.recompute(id, &candidate)
		memory.recompute(newID, &newCandidate)
		memory.states[id], memory.states[newID] = candidate, newCandidate
	default:
		return fmt.Errorf("unknown correction type")
	}
	createdAt := time.Now().UTC()
	correctionID := stableID("correction", candidate.RunID+"|"+id+"|"+kind+"|"+request.ActorID+"|"+request.Reason+"|"+createdAt.Format(time.RFC3339Nano))
	memory.corrections[correctionID] = model.StateCorrection{ID: correctionID, RunID: candidate.RunID, Type: kind, StateID: id, BeforeState: before, Request: request, CorrectedBy: request.ActorID, Reason: request.Reason, CreatedAt: createdAt}
	return nil
}

func (memory *Memory) canonicalStateID(id string) string {
	seen := map[string]bool{}
	for memory.aliases[id] != "" && !seen[id] {
		seen[id] = true
		id = memory.aliases[id]
	}
	return id
}

func (memory *Memory) canonicalStateIDForEvent(event model.ValidationEvent) string {
	identity := state.New().Identify(event)
	id := memory.canonicalStateID(identity.ID)
	role := strings.TrimSpace(event.Context["role"])
	if role == "" {
		role = "unknown"
	}
	key := candidateScopeKey{
		RunID:            event.RunID,
		TenantID:         event.TenantID,
		BuildID:          event.BuildID,
		EnvironmentID:    event.EnvironmentID,
		Role:             role,
		StateFingerprint: event.StateFingerprint,
	}
	if indexedID, ok := memory.candidateIndex[key]; ok {
		return memory.canonicalStateID(indexedID)
	}
	for _, existing := range memory.states {
		if existing.RunID == event.RunID && existing.TenantID == event.TenantID && existing.BuildID == event.BuildID && existing.EnvironmentID == event.EnvironmentID && existing.Role == role && existing.StateFingerprint == event.StateFingerprint {
			return memory.canonicalStateID(existing.ID)
		}
	}
	return id
}

func latestOccurredAt(events []model.ValidationEvent) time.Time {
	var latest time.Time
	for _, event := range events {
		if event.OccurredAt.After(latest) {
			latest = event.OccurredAt
		}
	}
	return latest.UTC()
}

func sameEvent(left, right model.ValidationEvent) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneEvent(event model.ValidationEvent) model.ValidationEvent {
	event.Context = cloneStringMap(event.Context)
	event.Privacy.RedactedFields = append([]string(nil), event.Privacy.RedactedFields...)
	return event
}

func cloneCandidate(candidate model.CandidateState) model.CandidateState {
	candidate.SourceTypes = append([]string(nil), candidate.SourceTypes...)
	candidate.EventIDs = append([]string(nil), candidate.EventIDs...)
	return candidate
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneRoutingPolicy(source routing.Policy) routing.Policy {
	raw, _ := json.Marshal(source)
	var result routing.Policy
	_ = json.Unmarshal(raw, &result)
	return result
}

func cloneMetrics(value model.ExperimentMetrics) model.ExperimentMetrics {
	raw, _ := json.Marshal(value)
	var result model.ExperimentMetrics
	_ = json.Unmarshal(raw, &result)
	return result
}

func (memory *Memory) Metrics(_ context.Context, runID, tenantID string) (model.ExperimentMetrics, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	run, ok := memory.runs[runID]
	if !ok {
		return model.ExperimentMetrics{}, ErrUnknownRun
	}
	if run.TenantID != tenantID {
		return model.ExperimentMetrics{}, ErrRunScopeMismatch
	}
	if run.FrozenAt != nil {
		snapshot, ok := memory.snapshots[runID]
		if !ok {
			return model.ExperimentMetrics{}, ErrFreezeInvariant
		}
		return cloneMetrics(snapshot), nil
	}
	metrics, _, err := experiment.Compute(run, memory.metricInputs(runID), false)
	return metrics, err
}

func (memory *Memory) FreezeExperiment(_ context.Context, runID, tenantID string) (model.ExperimentMetrics, bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	run, ok := memory.runs[runID]
	if !ok {
		return model.ExperimentMetrics{}, false, ErrUnknownRun
	}
	if run.TenantID != tenantID {
		return model.ExperimentMetrics{}, false, ErrRunScopeMismatch
	}
	if run.FrozenAt != nil {
		snapshot, ok := memory.snapshots[runID]
		if !ok {
			return model.ExperimentMetrics{}, false, ErrFreezeInvariant
		}
		return cloneMetrics(snapshot), false, nil
	}
	frozenAt := time.Now().UTC()
	run.FrozenAt = &frozenAt
	metrics, _, err := experiment.Compute(run, memory.metricInputs(runID), true)
	if errors.Is(err, experiment.ErrInputInvariant) {
		return model.ExperimentMetrics{}, false, ErrFreezeInvariant
	}
	if err != nil {
		return model.ExperimentMetrics{}, false, err
	}
	memory.runs[runID] = run
	memory.snapshots[runID] = cloneMetrics(metrics)
	return metrics, true, nil
}

func (memory *Memory) metricInputs(runID string) experiment.Inputs {
	events := []model.ValidationEvent{}
	for _, event := range memory.events {
		if event.RunID == runID {
			events = append(events, cloneEvent(event))
		}
	}
	states := []model.CandidateState{}
	for _, candidate := range memory.states {
		if candidate.RunID == runID {
			states = append(states, cloneCandidate(candidate))
		}
	}
	requests := []model.VerificationRequest{}
	for _, request := range memory.requests {
		if request.RunID == runID {
			requests = append(requests, request)
		}
	}
	recommendations := []model.Recommendation{}
	for _, recommendation := range memory.recommendations {
		if recommendation.RunID == runID {
			recommendations = append(recommendations, recommendation)
		}
	}
	responses := []model.RecommendationResponseRecord{}
	for _, response := range memory.responses {
		if response.RunID == runID {
			responses = append(responses, response)
		}
	}
	corrections := []model.StateCorrection{}
	for _, correction := range memory.corrections {
		if correction.RunID == runID {
			corrections = append(corrections, correction)
		}
	}
	return experiment.Inputs{Events: events, States: states, VerificationRequests: requests, Recommendations: recommendations, Responses: responses, Corrections: corrections}
}

func (*Memory) Ping(context.Context) error { return nil }
func (*Memory) Close()                     {}

func claimSemanticKey(tenantID, target, stateSignature, actorContext, validationIntent string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s", tenantID, target, stateSignature, actorContext, validationIntent)
}

func (memory *Memory) SaveClaimDefinition(_ context.Context, claim model.ClaimDefinition) (model.ClaimDefinition, bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	if strings.TrimSpace(claim.ID) == "" || strings.TrimSpace(claim.TenantID) == "" || strings.TrimSpace(claim.Target) == "" ||
		strings.TrimSpace(claim.StateSignature) == "" || strings.TrimSpace(claim.ActorContext) == "" || strings.TrimSpace(claim.ValidationIntent) == "" ||
		strings.TrimSpace(claim.Title) == "" {
		return model.ClaimDefinition{}, false, ErrContractInvalid
	}

	semKey := claimSemanticKey(claim.TenantID, claim.Target, claim.StateSignature, claim.ActorContext, claim.ValidationIntent)
	key := claim.TenantID + ":" + claim.ID

	for _, existing := range memory.claims {
		if existing.ID == claim.ID {
			if existing.TenantID != claim.TenantID {
				return model.ClaimDefinition{}, false, ErrClaimConflict
			}
			if existing.Title == claim.Title && existing.Description == claim.Description &&
				existing.Target == claim.Target && existing.StateSignature == claim.StateSignature &&
				existing.ActorContext == claim.ActorContext && existing.ValidationIntent == claim.ValidationIntent {
				return existing, false, nil
			}
			return model.ClaimDefinition{}, false, ErrClaimConflict
		}
	}

	if existingID, exists := memory.claimSemantics[semKey]; exists {
		if existingID != claim.ID {
			return model.ClaimDefinition{}, false, ErrClaimConflict
		}
	}

	if claim.CreatedAt.IsZero() {
		claim.CreatedAt = time.Now().UTC()
	} else {
		claim.CreatedAt = claim.CreatedAt.UTC()
	}

	memory.claims[key] = claim
	memory.claimSemantics[semKey] = claim.ID
	return claim, true, nil
}

func (memory *Memory) GetClaimDefinition(_ context.Context, tenantID, claimID string) (model.ClaimDefinition, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	claim, ok := memory.claims[tenantID+":"+claimID]
	if !ok {
		return model.ClaimDefinition{}, ErrClaimNotFound
	}
	return claim, nil
}

func (memory *Memory) SaveEvidenceContext(_ context.Context, ec model.EvidenceContext) (model.EvidenceContext, bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	if strings.TrimSpace(ec.ID) == "" || strings.TrimSpace(ec.TenantID) == "" || strings.TrimSpace(ec.RunID) == "" ||
		strings.TrimSpace(ec.BuildID) == "" || strings.TrimSpace(ec.EnvironmentID) == "" {
		return model.EvidenceContext{}, false, ErrContractInvalid
	}

	run, ok := memory.runs[ec.RunID]
	if !ok {
		return model.EvidenceContext{}, false, ErrUnknownRun
	}
	if run.TenantID != ec.TenantID || run.BuildID != ec.BuildID || run.EnvironmentID != ec.EnvironmentID {
		return model.EvidenceContext{}, false, ErrRunScopeMismatch
	}
	if run.FrozenAt != nil {
		return model.EvidenceContext{}, false, ErrRunFrozen
	}

	if _, ok := memory.builds[buildKey(ec.TenantID, ec.EnvironmentID, ec.BuildID)]; !ok {
		return model.EvidenceContext{}, false, ErrBuildNotRegistered
	}

	if existing, exists := memory.evidenceContexts[ec.ID]; exists {
		if existing.TenantID == ec.TenantID && existing.RunID == ec.RunID && existing.BuildID == ec.BuildID &&
			existing.EnvironmentID == ec.EnvironmentID && existing.ScenarioID == ec.ScenarioID &&
			canonicalStringMap(existing.FeatureFlags) == canonicalStringMap(ec.FeatureFlags) &&
			existing.Device == ec.Device && existing.Region == ec.Region {
			return existing, false, nil
		}
		return model.EvidenceContext{}, false, ErrEvidenceContextConflict
	}

	if ec.CreatedAt.IsZero() {
		ec.CreatedAt = time.Now().UTC()
	} else {
		ec.CreatedAt = ec.CreatedAt.UTC()
	}
	if ec.FeatureFlags == nil {
		ec.FeatureFlags = make(map[string]string)
	}

	memory.evidenceContexts[ec.ID] = ec
	return ec, true, nil
}

func (memory *Memory) SaveObservation(_ context.Context, obs model.Observation) (model.Observation, bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	if strings.TrimSpace(obs.ID) == "" || strings.TrimSpace(obs.TenantID) == "" || strings.TrimSpace(obs.RunID) == "" ||
		strings.TrimSpace(obs.ClaimID) == "" || strings.TrimSpace(obs.EvidenceContextID) == "" ||
		strings.TrimSpace(obs.SourceType) == "" || strings.TrimSpace(obs.SourceAdapter) == "" ||
		strings.TrimSpace(obs.ActorID) == "" || strings.TrimSpace(obs.Assessment) == "" {
		return model.Observation{}, false, ErrContractInvalid
	}

	validAssessments := map[string]bool{"supports": true, "weakens": true, "conflicts": true, "invalidates": true, "blocked": true}
	if !validAssessments[strings.ToLower(obs.Assessment)] {
		return model.Observation{}, false, ErrContractInvalid
	}

	run, ok := memory.runs[obs.RunID]
	if !ok {
		return model.Observation{}, false, ErrUnknownRun
	}
	if run.TenantID != obs.TenantID {
		return model.Observation{}, false, ErrRunScopeMismatch
	}
	if run.FrozenAt != nil {
		return model.Observation{}, false, ErrRunFrozen
	}

	claimKey := obs.TenantID + ":" + obs.ClaimID
	if _, ok := memory.claims[claimKey]; !ok {
		return model.Observation{}, false, ErrClaimNotFound
	}

	ec, ok := memory.evidenceContexts[obs.EvidenceContextID]
	if !ok {
		return model.Observation{}, false, ErrContractInvalid
	}
	if ec.TenantID != obs.TenantID || ec.RunID != obs.RunID {
		return model.Observation{}, false, ErrRunScopeMismatch
	}

	if obs.EventID != "" {
		event, ok := memory.events[obs.EventID]
		if !ok || event.RunID != obs.RunID || event.TenantID != obs.TenantID {
			return model.Observation{}, false, ErrRunScopeMismatch
		}
	}

	normalizedOccurredAt := obs.OccurredAt.UTC().Truncate(time.Microsecond)
	obs.OccurredAt = normalizedOccurredAt

	if existing, exists := memory.observations[obs.ID]; exists {
		if existing.TenantID == obs.TenantID && existing.RunID == obs.RunID && existing.ClaimID == obs.ClaimID &&
			existing.EvidenceContextID == obs.EvidenceContextID && existing.EventID == obs.EventID &&
			existing.SourceType == obs.SourceType && existing.SourceAdapter == obs.SourceAdapter &&
			existing.ActorID == obs.ActorID && existing.Assessment == obs.Assessment &&
			existing.VerifierResult == obs.VerifierResult &&
			existing.OccurredAt.Equal(normalizedOccurredAt) {
			return existing, false, nil
		}
		return model.Observation{}, false, ErrObservationConflict
	}

	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = time.Now().UTC()
	} else {
		obs.CreatedAt = obs.CreatedAt.UTC()
	}

	memory.observations[obs.ID] = obs
	memory.claimObs[claimKey] = append(memory.claimObs[claimKey], obs.ID)
	return obs, true, nil
}

func (memory *Memory) GetClaimEvidence(_ context.Context, tenantID, claimID string) (model.ClaimEvidence, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	claimKey := tenantID + ":" + claimID
	claim, ok := memory.claims[claimKey]
	if !ok {
		return model.ClaimEvidence{}, ErrClaimNotFound
	}

	obsIDs := memory.claimObs[claimKey]
	observations := make([]model.Observation, 0, len(obsIDs))
	contexts := make(map[string]model.EvidenceContext)

	for _, id := range obsIDs {
		if obs, ok := memory.observations[id]; ok {
			observations = append(observations, obs)
			if ec, ok := memory.evidenceContexts[obs.EvidenceContextID]; ok {
				contexts[ec.ID] = ec
			}
		}
	}

	return model.ClaimEvidence{
		Claim:        claim,
		Observations: observations,
		Contexts:     contexts,
	}, nil
}

func (memory *Memory) GetClaimEvidenceSummary(_ context.Context, scope Scope, claimID string) (model.ClaimEvidenceSummary, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	claimKey := scope.TenantID + ":" + claimID
	claim, ok := memory.claims[claimKey]
	if !ok {
		return model.ClaimEvidenceSummary{}, ErrClaimNotFound
	}

	obsIDs := memory.claimObs[claimKey]
	observations := make([]model.Observation, 0, len(obsIDs))
	contexts := make(map[string]model.EvidenceContext)

	for _, id := range obsIDs {
		if obs, ok := memory.observations[id]; ok {
			observations = append(observations, obs)
			if ec, ok := memory.evidenceContexts[obs.EvidenceContextID]; ok {
				contexts[ec.ID] = ec
			}
		}
	}

	return evidence.ReconcileClaim(claim, scope, observations, contexts), nil
}

func (memory *Memory) ListClaimEvidenceSummaries(_ context.Context, scope Scope) ([]model.ClaimEvidenceSummary, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.claimEvidenceSummaries(scope), nil
}

func (memory *Memory) claimEvidenceSummaries(scope Scope) []model.ClaimEvidenceSummary {
	summaries := make([]model.ClaimEvidenceSummary, 0, len(memory.claims))
	for _, claim := range memory.claims {
		if claim.TenantID != scope.TenantID {
			continue
		}
		claimKey := claim.TenantID + ":" + claim.ID
		obsIDs := memory.claimObs[claimKey]
		observations := make([]model.Observation, 0, len(obsIDs))
		contexts := make(map[string]model.EvidenceContext)

		for _, id := range obsIDs {
			if obs, ok := memory.observations[id]; ok {
				observations = append(observations, obs)
				if ec, ok := memory.evidenceContexts[obs.EvidenceContextID]; ok {
					contexts[ec.ID] = ec
				}
			}
		}

		summaries = append(summaries, evidence.ReconcileClaim(claim, scope, observations, contexts))
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ClaimID < summaries[j].ClaimID
	})

	return summaries
}

func stableID(prefix, value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-%x", prefix, digest[:12])
}
