package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/store"
)

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	NewRouter().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, response.Code)
	}
}

func TestEventPersistenceIdempotencyAndEvidence(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	event := validEvent("event-001", "human-a", "pass")
	first := performJSON(router, http.MethodPost, "/api/v1/events", event)
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d: %s", first.Code, first.Body.String())
	}
	duplicate := performJSON(router, http.MethodPost, "/api/v1/events", event)
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), "duplicate") {
		t.Fatalf("expected duplicate response, got %d: %s", duplicate.Code, duplicate.Body.String())
	}
	evidence := httptest.NewRecorder()
	router.ServeHTTP(evidence, authenticatedRequest(http.MethodGet, "/api/v1/evidence?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser", nil, "human-a"))
	if evidence.Code != http.StatusOK || !strings.Contains(evidence.Body.String(), "needs_independent_verification") {
		t.Fatalf("unexpected evidence response: %d %s", evidence.Code, evidence.Body.String())
	}
}

func TestBuildRegistrationIsControllerOwnedOrderedAndIdempotent(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	second := model.BuildRegistration{
		TenantID: "festival-thesis-demo", EnvironmentID: "festnest-local-browser",
		BuildID: "festnest-demo-002", SequenceNumber: 2, PredecessorBuildID: "festnest-demo-001",
	}
	created := performJSON(router, http.MethodPost, "/api/v1/builds", second)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"registered_by":"researcher-a"`) {
		t.Fatalf("expected controller registration, got %d: %s", created.Code, created.Body.String())
	}
	if retry := performJSON(router, http.MethodPost, "/api/v1/builds", second); retry.Code != http.StatusOK {
		t.Fatalf("exact retry must be idempotent, got %d: %s", retry.Code, retry.Body.String())
	}
	conflict := second
	conflict.SequenceNumber = 3
	if response := performJSON(router, http.MethodPost, "/api/v1/builds", conflict); response.Code != http.StatusConflict {
		t.Fatalf("conflicting build retry must fail, got %d: %s", response.Code, response.Body.String())
	}
	invalid := model.BuildRegistration{TenantID: second.TenantID, EnvironmentID: second.EnvironmentID, BuildID: "festnest-demo-003", SequenceNumber: 3, PredecessorBuildID: "missing-build"}
	if response := performJSON(router, http.MethodPost, "/api/v1/builds", invalid); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown predecessor must fail closed, got %d: %s", response.Code, response.Body.String())
	}
	nonControllerPayload, _ := json.Marshal(invalid)
	nonController := httptest.NewRecorder()
	nonControllerRequest := authenticatedRequest(http.MethodPost, "/api/v1/builds", bytes.NewReader(nonControllerPayload), "human-a")
	router.ServeHTTP(nonController, nonControllerRequest)
	if nonController.Code != http.StatusForbidden {
		t.Fatalf("source credential must not register builds, got %d", nonController.Code)
	}
	tenantMismatch := second
	tenantMismatch.TenantID = "other-tenant"
	if response := performJSON(router, http.MethodPost, "/api/v1/builds", tenantMismatch); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "tenant_mismatch") {
		t.Fatalf("controller must remain tenant-bound, got %d: %s", response.Code, response.Body.String())
	}
}

func TestUnknownBuildFailsClosedAtIngestAndRead(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	unknown := validEvent("unknown-build-event", "human-a", "pass")
	unknown.BuildID = "festnest-demo-999"
	if response := performJSON(router, http.MethodPost, "/api/v1/events", unknown); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "run_scope_mismatch") {
		t.Fatalf("event outside its registered run scope must fail closed, got %d: %s", response.Code, response.Body.String())
	}
	read := httptest.NewRecorder()
	router.ServeHTTP(read, authenticatedRequest(http.MethodGet, "/api/v1/evidence?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-999&environment_id=festnest-local-browser", nil, "human-a"))
	if read.Code != http.StatusUnprocessableEntity || !strings.Contains(read.Body.String(), "unknown_build") {
		t.Fatalf("unknown build read must fail closed, got %d: %s", read.Code, read.Body.String())
	}
	recommendation := httptest.NewRecorder()
	router.ServeHTTP(recommendation, authenticatedRequest(http.MethodGet, "/api/v1/recommendations/next?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-999&environment_id=festnest-local-browser&actor_id=late-tester-d&role=editor", nil, "late-tester-d"))
	if recommendation.Code != http.StatusUnprocessableEntity || !strings.Contains(recommendation.Body.String(), "unknown_build") {
		t.Fatalf("unknown build recommendation must fail closed, got %d: %s", recommendation.Code, recommendation.Body.String())
	}
}

func TestIndependentEvidenceBecomesStrongAndRecommendationPersistsResponse(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	if _, _, err := repository.SaveClaimDefinition(context.Background(), model.ClaimDefinition{
		ID: "claim-recommendation-001", TenantID: "festival-thesis-demo", Title: "Canopy remains packed",
		Target: "Supply List persistence", StateSignature: "packed before reload -> packed after reload",
		ActorContext: "editor", ValidationIntent: "DataPersistenceOnRefresh",
	}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []model.ValidationEvent{validEvent("event-001", "human-a", "pass"), validEvent("event-002", "agent-c", "pass")} {
		if response := performJSON(router, http.MethodPost, "/api/v1/events", event); response.Code != http.StatusAccepted {
			t.Fatalf("ingest failed: %s", response.Body.String())
		}
	}
	recommendationResponse := httptest.NewRecorder()
	router.ServeHTTP(recommendationResponse, authenticatedRequest(http.MethodGet, "/api/v1/recommendations/next?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser&actor_id=late-tester-d&role=editor&feature_flag=camp_grid_v2%3Dtrue", nil, "late-tester-d"))
	if recommendationResponse.Code != http.StatusOK {
		t.Fatalf("recommendation failed: %s", recommendationResponse.Body.String())
	}
	var recommendation model.Recommendation
	if err := json.Unmarshal(recommendationResponse.Body.Bytes(), &recommendation); err != nil {
		t.Fatal(err)
	}
	if recommendation.ClaimID != "claim-recommendation-001" || recommendation.CandidateStateID != "" || recommendation.PolicyVersion == "" || recommendation.Factors.ExpectedCost <= 0 || recommendation.FeatureFlags["camp_grid_v2"] != "true" {
		t.Fatalf("recommendation is not a typed claim-level decision: %+v", recommendation)
	}
	response := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", model.RecommendationResponse{Response: "accepted", Reason: "Freshness uncertainty is highest value", ActorID: "late-tester-d"})
	if response.Code != http.StatusCreated {
		t.Fatalf("response failed: %d %s", response.Code, response.Body.String())
	}

	invalidFlags := httptest.NewRecorder()
	router.ServeHTTP(invalidFlags, authenticatedRequest(http.MethodGet, "/api/v1/recommendations/next?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser&actor_id=late-tester-d&role=editor&feature_flag=camp_grid_v2", nil, "late-tester-d"))
	if invalidFlags.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidFlags.Body.String(), "contract_invalid") {
		t.Fatalf("malformed feature flag scope should fail closed: %d %s", invalidFlags.Code, invalidFlags.Body.String())
	}
}

func TestMalformedOversizedUnknownAndInvalidEnumsFailClosed(t *testing.T) {
	router := testRouter(store.NewMemory())
	cases := []struct {
		name, body string
		want       int
	}{
		{"malformed", `{`, http.StatusBadRequest},
		{"unknown", `{"unknown":true}`, http.StatusBadRequest},
		{"oversized", `{"padding":"` + strings.Repeat("x", maxBodyBytes) + `"}`, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := authenticatedRequest(http.MethodPost, "/api/v1/events", strings.NewReader(testCase.body), "human-a")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("expected %d, got %d", testCase.want, response.Code)
			}
		})
	}
	invalid := validEvent("event-invalid", "human-a", "not-allowed")
	if response := performJSON(router, http.MethodPost, "/api/v1/events", invalid); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", response.Code)
	}
	unmasked := validEvent("event-unmasked", "human-a", "pass")
	unmasked.Privacy.Masked = false
	if response := performJSON(router, http.MethodPost, "/api/v1/events", unmasked); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected unmasked evidence rejection, got %d", response.Code)
	}
	credentialContext := validEvent("event-secret", "human-a", "pass")
	credentialContext.Context["authorization_token"] = "redacted"
	if response := performJSON(router, http.MethodPost, "/api/v1/events", credentialContext); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected credential-shaped context rejection, got %d", response.Code)
	}
	sensitiveRoute := validEvent("event-sensitive", "human-a", "pass")
	sensitiveRoute.Route = "/trips/demo/safety"
	if response := performJSON(router, http.MethodPost, "/api/v1/events", sensitiveRoute); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected sensitive route rejection, got %d", response.Code)
	}
}

func TestAuthenticationIdentityBindingAndDuplicateConflict(t *testing.T) {
	router := testRouter(store.NewMemory())
	event := validEvent("event-auth", "human-a", "pass")
	payload, _ := json.Marshal(event)

	missing := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(missing, request)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", missing.Code)
	}

	forged := event
	forged.ActorID = "agent-c"
	forgedPayload, _ := json.Marshal(forged)
	forgedResponse := httptest.NewRecorder()
	forgedRequest := authenticatedRequest(http.MethodPost, "/api/v1/events", bytes.NewReader(forgedPayload), "human-a")
	forgedRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(forgedResponse, forgedRequest)
	if forgedResponse.Code != http.StatusForbidden {
		t.Fatalf("expected forged identity rejection, got %d", forgedResponse.Code)
	}

	if accepted := performJSON(router, http.MethodPost, "/api/v1/events", event); accepted.Code != http.StatusAccepted {
		t.Fatalf("expected accepted event, got %d", accepted.Code)
	}
	conflict := event
	conflict.Outcome = "fail"
	if response := performJSON(router, http.MethodPost, "/api/v1/events", conflict); response.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestHumanAdapterAndAgentUseTheSameAuthenticatedIngestBoundary(t *testing.T) {
	router := testRouter(store.NewMemory())
	human := validEvent("source-human", "human-a", "pass")
	adapter := validEvent("source-adapter", "editor-a", "ambiguous")
	adapter.SourceType, adapter.SourceAdapter = "automation", "festival-activity-log-v1"
	agent := validEvent("source-agent", "playwright-agent-c", "pass")
	agent.SourceType, agent.SourceAdapter = "agent", "playwright"
	for _, event := range []model.ValidationEvent{human, adapter, agent} {
		if response := performJSON(router, http.MethodPost, "/api/v1/events", event); response.Code != http.StatusAccepted {
			t.Fatalf("%s source rejected: %d %s", event.SourceType, response.Code, response.Body.String())
		}
	}
}

func TestStateRenameCorrectionPreservesEventHistory(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	event := validEvent("event-correction", "human-a", "pass")
	if response := performJSON(router, http.MethodPost, "/api/v1/events", event); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	evidence, err := repository.Evidence(context.Background(), store.Scope{RunID: event.RunID, TenantID: event.TenantID, BuildID: event.BuildID, EnvironmentID: event.EnvironmentID})
	if err != nil || len(evidence.States) != 1 {
		t.Fatalf("unexpected evidence: %+v %v", evidence, err)
	}
	stateID := evidence.States[0].ID
	response := performJSON(router, http.MethodPost, "/api/v1/states/"+stateID+"/rename", model.StateCorrectionRequest{
		NewFingerprint: "supply-list:item:canopy:status:reconciled", NewBusinessState: "reconciled",
		ActorID: "researcher-a", Reason: "Normalize the reviewed business state",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", response.Code, response.Body.String())
	}
	after, _ := repository.Evidence(context.Background(), store.Scope{RunID: event.RunID, TenantID: event.TenantID, BuildID: event.BuildID, EnvironmentID: event.EnvironmentID})
	if len(after.States) != 1 || after.States[0].BusinessState != "reconciled" || len(after.States[0].EventIDs) != 1 || after.States[0].EventIDs[0] != event.EventID {
		t.Fatalf("correction rewrote or lost event history: %+v", after)
	}
}

func TestOldBuildEvidenceBecomesStale(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	oldEvent := validEvent("old-event", "human-a", "pass")
	if response := performJSON(router, http.MethodPost, "/api/v1/events", oldEvent); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	registration := model.BuildRegistration{TenantID: oldEvent.TenantID, EnvironmentID: oldEvent.EnvironmentID, BuildID: "festnest-demo-002", SequenceNumber: 2, PredecessorBuildID: oldEvent.BuildID}
	if response := performJSON(router, http.MethodPost, "/api/v1/builds", registration); response.Code != http.StatusCreated {
		t.Fatalf("build registration failed: %d %s", response.Code, response.Body.String())
	}
	oldEvidence, err := repository.Evidence(context.Background(), store.Scope{RunID: oldEvent.RunID, TenantID: oldEvent.TenantID, BuildID: oldEvent.BuildID, EnvironmentID: oldEvent.EnvironmentID})
	if err != nil || len(oldEvidence.States) != 1 || oldEvidence.States[0].Status != "stale" {
		t.Fatalf("registration alone must stale older evidence: %#v %v", oldEvidence, err)
	}
	newEvent := validEvent("new-event", "human-a", "pass")
	newEvent.BuildID = "festnest-demo-002"
	newEvent.RunID = "festival-guided-002"
	if _, _, err := repository.CreateExperiment(context.Background(), model.ExperimentRegistration{ID: newEvent.RunID, TenantID: newEvent.TenantID, BuildID: newEvent.BuildID, EnvironmentID: newEvent.EnvironmentID, ScenarioID: newEvent.Context["scenario_id"], Mode: "guided"}, "researcher-a"); err != nil {
		t.Fatal(err)
	}
	if response := performJSON(router, http.MethodPost, "/api/v1/events", newEvent); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	oldEvidence, err = repository.Evidence(context.Background(), store.Scope{RunID: oldEvent.RunID, TenantID: oldEvent.TenantID, BuildID: oldEvent.BuildID, EnvironmentID: oldEvent.EnvironmentID})
	if err != nil || len(oldEvidence.States) != 1 || oldEvidence.States[0].Status != "stale" {
		t.Fatalf("expected stale old evidence: %#v %v", oldEvidence, err)
	}
	delayed := validEvent("delayed-old-event", "agent-c", "pass")
	delayed.OccurredAt = newEvent.OccurredAt.Add(time.Minute)
	if response := performJSON(router, http.MethodPost, "/api/v1/events", delayed); response.Code != http.StatusAccepted {
		t.Fatalf("delayed registered old-build event should be retained: %d %s", response.Code, response.Body.String())
	}
	oldEvidence, err = repository.Evidence(context.Background(), store.Scope{RunID: oldEvent.RunID, TenantID: oldEvent.TenantID, BuildID: oldEvent.BuildID, EnvironmentID: oldEvent.EnvironmentID})
	if err != nil || len(oldEvidence.States) != 1 || oldEvidence.States[0].Status != "stale" || oldEvidence.States[0].EvidenceCount != 2 {
		t.Fatalf("delayed event must not make an older build current: %#v %v", oldEvidence, err)
	}
	newEvidence, err := repository.Evidence(context.Background(), store.Scope{RunID: newEvent.RunID, TenantID: newEvent.TenantID, BuildID: newEvent.BuildID, EnvironmentID: newEvent.EnvironmentID})
	if err != nil || len(newEvidence.States) != 1 || newEvidence.States[0].Status == "stale" {
		t.Fatalf("newer build must remain current after delayed old evidence: %#v %v", newEvidence, err)
	}
}

func TestExperimentCreationIsolationPreviewFreezeAndFrozenWrites(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	registration := model.ExperimentRegistration{ID: "festival-guided-extra", TenantID: "festival-thesis-demo", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser", ScenarioID: "equipment-handoff-v1", Mode: "guided"}
	if response := performJSON(router, http.MethodPost, "/api/v1/experiments", registration); response.Code != http.StatusCreated {
		t.Fatalf("experiment create failed: %d %s", response.Code, response.Body.String())
	}
	if response := performJSON(router, http.MethodPost, "/api/v1/experiments", registration); response.Code != http.StatusOK {
		t.Fatalf("exact experiment retry failed: %d %s", response.Code, response.Body.String())
	}
	conflict := registration
	conflict.Mode = "baseline"
	if response := performJSON(router, http.MethodPost, "/api/v1/experiments", conflict); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "experiment_conflict") {
		t.Fatalf("conflicting experiment retry must fail: %d %s", response.Code, response.Body.String())
	}

	baseline := validEvent("baseline-isolated", "human-a", "pass")
	baseline.RunID = "festival-baseline-001"
	guided := validEvent("guided-isolated", "human-a", "pass")
	if response := performJSON(router, http.MethodPost, "/api/v1/events", baseline); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if response := performJSON(router, http.MethodPost, "/api/v1/events", guided); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	baselineEvidence, err := repository.Evidence(context.Background(), store.Scope{RunID: baseline.RunID, TenantID: baseline.TenantID, BuildID: baseline.BuildID, EnvironmentID: baseline.EnvironmentID})
	if err != nil || len(baselineEvidence.States) != 1 {
		t.Fatalf("baseline evidence: %+v %v", baselineEvidence, err)
	}
	guidedEvidence, err := repository.Evidence(context.Background(), store.Scope{RunID: guided.RunID, TenantID: guided.TenantID, BuildID: guided.BuildID, EnvironmentID: guided.EnvironmentID})
	if err != nil || len(guidedEvidence.States) != 1 {
		t.Fatalf("guided evidence: %+v %v", guidedEvidence, err)
	}
	if baselineEvidence.States[0].ID == guidedEvidence.States[0].ID || baselineEvidence.States[0].RunID == guidedEvidence.States[0].RunID {
		t.Fatalf("baseline/guided state identity mixed: baseline=%+v guided=%+v", baselineEvidence.States[0], guidedEvidence.States[0])
	}

	preview := httptest.NewRecorder()
	router.ServeHTTP(preview, authenticatedRequest(http.MethodGet, "/api/v1/experiments/festival-guided-001/metrics", nil, "human-a"))
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"status":"preview"`) || !strings.Contains(preview.Body.String(), `"definition_version":"festival-v1.1"`) || !strings.Contains(preview.Body.String(), `"missing_tester_time_receipt"`) {
		t.Fatalf("unexpected preview: %d %s", preview.Code, preview.Body.String())
	}
	freeze := httptest.NewRecorder()
	router.ServeHTTP(freeze, authenticatedRequest(http.MethodPost, "/api/v1/experiments/festival-guided-001/freeze", nil, "controller-researcher-a"))
	if freeze.Code != http.StatusCreated || !strings.Contains(freeze.Body.String(), `"status":"frozen"`) {
		t.Fatalf("freeze failed: %d %s", freeze.Code, freeze.Body.String())
	}
	retry := httptest.NewRecorder()
	router.ServeHTTP(retry, authenticatedRequest(http.MethodPost, "/api/v1/experiments/festival-guided-001/freeze", nil, "controller-researcher-a"))
	if retry.Code != http.StatusOK || retry.Body.String() != freeze.Body.String() {
		t.Fatalf("freeze retry not byte stable: first=%s retry=%s", freeze.Body.String(), retry.Body.String())
	}

	late := validEvent("after-freeze", "human-a", "pass")
	if response := performJSON(router, http.MethodPost, "/api/v1/events", late); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "run_frozen") {
		t.Fatalf("frozen event accepted: %d %s", response.Code, response.Body.String())
	}
	recommendation := httptest.NewRecorder()
	router.ServeHTTP(recommendation, authenticatedRequest(http.MethodGet, "/api/v1/recommendations/next?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser&actor_id=late-tester-d&role=editor", nil, "late-tester-d"))
	if recommendation.Code != http.StatusConflict || !strings.Contains(recommendation.Body.String(), "run_frozen") {
		t.Fatalf("frozen recommendation created: %d %s", recommendation.Code, recommendation.Body.String())
	}
	stateID := guidedEvidence.States[0].ID
	correction := performJSON(router, http.MethodPost, "/api/v1/states/"+stateID+"/rename", model.StateCorrectionRequest{NewFingerprint: "renamed", NewBusinessState: "renamed", ActorID: "researcher-a", Reason: "after freeze"})
	if correction.Code != http.StatusConflict || !strings.Contains(correction.Body.String(), "run_frozen") {
		t.Fatalf("frozen correction accepted: %d %s", correction.Code, correction.Body.String())
	}
}

func validEvent(id, actor, outcome string) model.ValidationEvent {
	return model.ValidationEvent{
		EventID: id, TenantID: "festival-thesis-demo", RunID: "festival-guided-001", ActorID: actor,
		SourceType: "human", SourceAdapter: "test", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser",
		Context: map[string]string{"role": "editor", "module": "supply-list", "connectivity": "online", "scenario_id": "equipment-handoff-v1"},
		Route:   "/trips/demo/supply-list", StateFingerprint: "supply-list:item:canopy:status:packed", ActionType: "supply_item_observed", Outcome: outcome,
		OccurredAt: time.Now().UTC(), StepIndex: 1, Privacy: model.PrivacyMetadata{Masked: true},
	}
}

func performJSON(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	actorID := "human-a"
	switch value := body.(type) {
	case model.ValidationEvent:
		actorID = value.ActorID
		if value.SourceType == "automation" {
			actorID = "adapter-" + value.ActorID
		}
		if value.SourceType == "agent" {
			actorID = "playwright-" + value.ActorID
		}
	case model.RecommendationResponse:
		actorID = value.ActorID
	case model.StateCorrectionRequest:
		actorID = value.ActorID
	case model.BuildRegistration:
		actorID = "controller-researcher-a"
	case model.ExperimentRegistration:
		actorID = "controller-researcher-a"
	case model.ClaimDefinition:
		actorID = "controller-researcher-a"
	case model.EvidenceContext:
		actorID = "controller-researcher-a"
	case model.Observation:
		actorID = value.ActorID
		if value.SourceType == "agent" {
			actorID = "playwright-" + value.ActorID
		}
	}
	request := authenticatedRequest(method, path, bytes.NewReader(payload), actorID)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func testRouter(repository store.Repository) http.Handler {
	if _, _, err := repository.RegisterBuild(context.Background(), model.BuildRegistration{TenantID: "festival-thesis-demo", EnvironmentID: "festnest-local-browser", BuildID: "festnest-demo-001", SequenceNumber: 1}, "researcher-a"); err != nil {
		panic(err)
	}
	for _, registration := range []model.ExperimentRegistration{
		{ID: "festival-baseline-001", TenantID: "festival-thesis-demo", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser", ScenarioID: "equipment-handoff-v1", Mode: "baseline"},
		{ID: "festival-guided-001", TenantID: "festival-thesis-demo", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser", ScenarioID: "equipment-handoff-v1", Mode: "guided"},
	} {
		if _, _, err := repository.CreateExperiment(context.Background(), registration, "researcher-a"); err != nil {
			panic(err)
		}
	}
	identities := map[string]SourceIdentity{}
	for _, actor := range []struct{ id, source, adapter, role string }{
		{"human-a", "human", "test", "editor"},
		{"agent-c", "human", "test", "editor"},
		{"researcher-a", "human", "test", "researcher"},
		{"late-tester-d", "human", "test", "editor"},
	} {
		identities["token-"+actor.id] = SourceIdentity{TenantID: "festival-thesis-demo", ActorID: actor.id, SourceType: actor.source, SourceAdapter: actor.adapter, Role: actor.role}
	}
	identities["token-adapter-editor-a"] = SourceIdentity{TenantID: "festival-thesis-demo", ActorID: "editor-a", SourceType: "automation", SourceAdapter: "festival-activity-log-v1", Role: "editor"}
	identities["token-playwright-playwright-agent-c"] = SourceIdentity{TenantID: "festival-thesis-demo", ActorID: "playwright-agent-c", SourceType: "agent", SourceAdapter: "playwright", Role: "editor"}
	identities["token-controller-researcher-a"] = SourceIdentity{TenantID: "festival-thesis-demo", ActorID: "researcher-a", SourceType: "human", SourceAdapter: "controller", Role: "researcher", Capabilities: map[string]bool{"controller": true}}
	return NewAuthenticatedRouter(NewStaticAuthenticator(identities), repository)
}

func authenticatedRequest(method, path string, body io.Reader, actorID string) *http.Request {
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer token-"+actorID)
	return request
}

type failingSaveRepository struct {
	store.Repository
}

func (f *failingSaveRepository) SaveEvent(ctx context.Context, event model.ValidationEvent) (bool, error) {
	return false, errors.New("sensitive internal database stack trace error detail")
}

func TestEventPersistenceErrorRemainsGeneric(t *testing.T) {
	mem := store.NewMemory()
	failingRepo := &failingSaveRepository{Repository: mem}
	router := testRouter(failingRepo)

	event := validEvent("event-fail-001", "human-a", "pass")
	resp := performJSON(router, http.MethodPost, "/api/v1/events", event)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"code":"event_persistence_failed"`) || !strings.Contains(body, `"error":"event persistence failed"`) {
		t.Fatalf("expected generic event_persistence_failed response, got %s", body)
	}
	if strings.Contains(body, "sensitive internal database") {
		t.Fatalf("internal error detail leaked to HTTP caller: %s", body)
	}
}

func TestClaimEvidenceSummariesReadAPI(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	ctx := context.Background()

	tenantID := "festival-thesis-demo"
	envID := "festnest-local-browser"
	buildID := "festnest-demo-001"
	runID := "festival-guided-001"

	// Add claim definition
	claim := model.ClaimDefinition{
		ID:               "claim-fest-001",
		TenantID:         tenantID,
		Title:            "Participant access is revoked after removal",
		Target:           "participant_access_card",
		StateSignature:   "revoked",
		ActorContext:     "editor",
		ValidationIntent: "verify_revocation",
	}
	if _, _, err := repository.SaveClaimDefinition(ctx, claim); err != nil {
		t.Fatalf("save claim: %v", err)
	}

	// 1. Read single claim summary (Untouched)
	rec := httptest.NewRecorder()
	req := authenticatedRequest(http.MethodGet, "/api/v1/claims/claim-fest-001/evidence?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser", nil, "human-a")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var singleSummary model.ClaimEvidenceSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &singleSummary); err != nil {
		t.Fatalf("unmarshal single summary: %v", err)
	}
	if singleSummary.Classification != "Untouched" {
		t.Fatalf("expected Untouched, got %s", singleSummary.Classification)
	}

	// 2. Add Context and Observation (Human shallow click)
	ec := model.EvidenceContext{
		ID:            "ctx-fest-001",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    "equipment-handoff-v1",
	}
	if _, _, err := repository.SaveEvidenceContext(ctx, ec); err != nil {
		t.Fatalf("save ec: %v", err)
	}
	obs := model.Observation{
		ID:                "obs-fest-001",
		TenantID:          tenantID,
		RunID:             runID,
		ClaimID:           claim.ID,
		EvidenceContextID: ec.ID,
		SourceType:        "human",
		SourceAdapter:     "chrome-mv3",
		ActorID:           "human-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}
	if _, _, err := repository.SaveObservation(ctx, obs); err != nil {
		t.Fatalf("save obs: %v", err)
	}

	// 3. Read list of claim summaries (Weak)
	recList := httptest.NewRecorder()
	reqList := authenticatedRequest(http.MethodGet, "/api/v1/claims/evidence?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser", nil, "human-a")
	router.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recList.Code, recList.Body.String())
	}
	var listResp model.ClaimEvidenceSummariesResponse
	if err := json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listResp.Summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(listResp.Summaries))
	}
	if listResp.Summaries[0].Classification != "Weak" {
		t.Fatalf("expected Weak, got %s", listResp.Summaries[0].Classification)
	}

	// 4. Test Tenant Mismatch Forbidden
	recForbidden := httptest.NewRecorder()
	reqForbidden := authenticatedRequest(http.MethodGet, "/api/v1/claims/evidence?run_id=festival-guided-001&tenant_id=other-tenant&build_id=festnest-demo-001&environment_id=festnest-local-browser", nil, "human-a")
	router.ServeHTTP(recForbidden, reqForbidden)
	if recForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for tenant mismatch, got %d", recForbidden.Code)
	}
}

func TestClaimAndObservationAPIEndpoints(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)

	claim := model.ClaimDefinition{
		ID:               "FEST-CLAIM-04",
		TenantID:         "festival-thesis-demo",
		Title:            "Packed canopy persists after refresh",
		Description:      "A packed canopy remains packed after refresh/requery when authoritative state is current.",
		Target:           "Supply List / canopy persistence",
		StateSignature:   "packed before reload -> packed after reload",
		ActorContext:     "permitted trip member",
		ValidationIntent: "DataPersistenceOnRefresh",
	}
	recClaim := performJSON(router, http.MethodPost, "/api/v1/claims", claim)
	if recClaim.Code != http.StatusCreated {
		t.Fatalf("expected 201 for POST /api/v1/claims, got %d: %s", recClaim.Code, recClaim.Body.String())
	}

	ec := model.EvidenceContext{
		ID:            "ctx-m6-001",
		TenantID:      "festival-thesis-demo",
		RunID:         "festival-guided-001",
		BuildID:       "festnest-demo-001",
		EnvironmentID: "festnest-local-browser",
		ScenarioID:    "equipment-handoff-v1",
	}
	recEC := performJSON(router, http.MethodPost, "/api/v1/evidence-contexts", ec)
	if recEC.Code != http.StatusCreated {
		t.Fatalf("expected 201 for POST /api/v1/evidence-contexts, got %d: %s", recEC.Code, recEC.Body.String())
	}

	humanObs := model.Observation{
		ID:                "obs-h-001",
		TenantID:          "festival-thesis-demo",
		RunID:             "festival-guided-001",
		ClaimID:           "FEST-CLAIM-04",
		EvidenceContextID: "ctx-m6-001",
		SourceType:        "human",
		SourceAdapter:     "test",
		ActorID:           "human-a",
		Assessment:        "supports",
		OccurredAt:        time.Now().UTC(),
	}
	recHumanObs := performJSON(router, http.MethodPost, "/api/v1/observations", humanObs)
	if recHumanObs.Code != http.StatusCreated {
		t.Fatalf("expected 201 for human observation, got %d: %s", recHumanObs.Code, recHumanObs.Body.String())
	}

	scopeQuery := "/api/v1/claims/evidence?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser&claim_id=FEST-CLAIM-04"
	recWeak := httptest.NewRecorder()
	reqWeak := authenticatedRequest(http.MethodGet, scopeQuery, nil, "human-a")
	router.ServeHTTP(recWeak, reqWeak)
	if recWeak.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recWeak.Code, recWeak.Body.String())
	}
	var summaryWeak model.ClaimEvidenceSummary
	if err := json.Unmarshal(recWeak.Body.Bytes(), &summaryWeak); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summaryWeak.Classification != "Weak" {
		t.Fatalf("expected Weak before deeper verifier, got %s", summaryWeak.Classification)
	}

	verifierObs := model.Observation{
		ID:                "obs-v-001",
		TenantID:          "festival-thesis-demo",
		RunID:             "festival-guided-001",
		ClaimID:           "FEST-CLAIM-04",
		EvidenceContextID: "ctx-m6-001",
		SourceType:        "agent",
		SourceAdapter:     "playwright",
		ActorID:           "playwright-agent-c",
		Assessment:        "supports",
		VerifierResult:    "persisted_authoritative_state_verified",
		OccurredAt:        time.Now().UTC(),
	}
	recVerifierObs := performJSON(router, http.MethodPost, "/api/v1/observations", verifierObs)
	if recVerifierObs.Code != http.StatusCreated {
		t.Fatalf("expected 201 for verifier observation, got %d: %s", recVerifierObs.Code, recVerifierObs.Body.String())
	}

	recSolid := httptest.NewRecorder()
	reqSolid := authenticatedRequest(http.MethodGet, scopeQuery, nil, "human-a")
	router.ServeHTTP(recSolid, reqSolid)
	if recSolid.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recSolid.Code, recSolid.Body.String())
	}
	var summarySolid model.ClaimEvidenceSummary
	if err := json.Unmarshal(recSolid.Body.Bytes(), &summarySolid); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summarySolid.Classification != "Solid" {
		t.Fatalf("expected Solid after depth-3 verifier observation, got %s", summarySolid.Classification)
	}
	if summarySolid.MaxVerificationDepth != 3 {
		t.Fatalf("expected max depth 3, got %d", summarySolid.MaxVerificationDepth)
	}
	if summarySolid.IndependentSourceCount != 2 {
		t.Fatalf("expected 2 independent sources, got %d", summarySolid.IndependentSourceCount)
	}
}

func TestEvidenceContextFrozenRunRejection(t *testing.T) {
	repository := store.NewMemory()
	router := testRouter(repository)
	ctx := context.Background()

	tenantID := "festival-thesis-demo"
	runID := "run-freeze-test-001"
	buildID := "festnest-demo-001"
	envID := "festnest-local-browser"

	// Create run
	_, _, err := repository.CreateExperiment(ctx, model.ExperimentRegistration{
		ID:            runID,
		TenantID:      tenantID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    "equipment-handoff-v1",
		Mode:          "guided",
	}, "controller-a")
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	// 1. Open run: write EvidenceContext succeeds (201)
	openEC := model.EvidenceContext{
		ID:            "ctx-open-001",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    "equipment-handoff-v1",
	}
	recOpen := performJSON(router, http.MethodPost, "/api/v1/evidence-contexts", openEC)
	if recOpen.Code != http.StatusCreated {
		t.Fatalf("expected 201 for EvidenceContext on open run, got %d: %s", recOpen.Code, recOpen.Body.String())
	}

	// Freeze the run
	freezeReq := authenticatedRequest(http.MethodPost, "/api/v1/experiments/"+runID+"/freeze", nil, "controller-researcher-a")
	recFreeze := httptest.NewRecorder()
	router.ServeHTTP(recFreeze, freezeReq)
	if recFreeze.Code != http.StatusCreated {
		t.Fatalf("expected 201 for freeze, got %d: %s", recFreeze.Code, recFreeze.Body.String())
	}

	// 2. Frozen run: write EvidenceContext returns 409 run_frozen
	frozenEC := model.EvidenceContext{
		ID:            "ctx-frozen-002",
		TenantID:      tenantID,
		RunID:         runID,
		BuildID:       buildID,
		EnvironmentID: envID,
		ScenarioID:    "equipment-handoff-v1",
	}
	recFrozen := performJSON(router, http.MethodPost, "/api/v1/evidence-contexts", frozenEC)
	if recFrozen.Code != http.StatusConflict {
		t.Fatalf("expected 409 for EvidenceContext on frozen run, got %d: %s", recFrozen.Code, recFrozen.Body.String())
	}
	if !strings.Contains(recFrozen.Body.String(), `"code":"run_frozen"`) {
		t.Fatalf("expected error code run_frozen, got: %s", recFrozen.Body.String())
	}
}
