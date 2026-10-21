package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/store"
)

func seedFeedbackClaims(t *testing.T, repository store.Repository, claimIDs ...string) {
	t.Helper()
	for _, claimID := range claimIDs {
		if _, _, err := repository.SaveClaimDefinition(context.Background(), model.ClaimDefinition{
			ID: claimID, TenantID: "festival-thesis-demo", Title: claimID, Target: "target " + claimID,
			StateSignature: "before -> after " + claimID, ActorContext: "editor", ValidationIntent: "TransitionSucceeds",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func fetchLateTesterRecommendation(t *testing.T, router http.Handler) model.Recommendation {
	t.Helper()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, authenticatedRequest(http.MethodGet,
		"/api/v1/recommendations/next?run_id=festival-guided-001&tenant_id=festival-thesis-demo&build_id=festnest-demo-001&environment_id=festnest-local-browser&scenario_id=equipment-handoff-v1&actor_id=late-tester-d&role=editor",
		nil, "late-tester-d"))
	if response.Code != http.StatusOK {
		t.Fatalf("recommendation failed: %d %s", response.Code, response.Body.String())
	}
	var recommendation model.Recommendation
	if err := json.Unmarshal(response.Body.Bytes(), &recommendation); err != nil {
		t.Fatal(err)
	}
	return recommendation
}

func TestRecommendationOverrideAPIRecordsAlternativeAndExactRetry(t *testing.T) {
	repository := store.NewMemory()
	seedFeedbackClaims(t, repository, "claim-a", "claim-b")
	router := testRouter(repository)
	recommendation := fetchLateTesterRecommendation(t, router)
	if len(recommendation.OverrideAlternatives) != 1 || recommendation.OverrideAlternatives[0].ClaimID == recommendation.ClaimID {
		t.Fatalf("recommendation payload omitted backend-approved alternatives: %+v", recommendation)
	}
	overrideClaimID := "claim-b"
	if recommendation.ClaimID == overrideClaimID {
		overrideClaimID = "claim-a"
	}

	feedback := model.RecommendationResponse{
		Response: "override", Reason: "the alternate claim is more useful now",
		ActorID: "late-tester-d", OverrideClaimID: overrideClaimID,
	}
	first := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", feedback)
	if first.Code != http.StatusCreated {
		t.Fatalf("override failed: %d %s", first.Code, first.Body.String())
	}
	retry := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", feedback)
	if retry.Code != http.StatusOK {
		t.Fatalf("exact override retry must be idempotent: %d %s", retry.Code, retry.Body.String())
	}
	next := fetchLateTesterRecommendation(t, router)
	if next.ID == recommendation.ID || next.ClaimID != overrideClaimID || next.EvidenceSnapshot != recommendation.EvidenceSnapshot || next.ProgressionSnapshot == recommendation.ProgressionSnapshot || next.PromotedByResponseID == "" {
		t.Fatalf("override did not advance to the promoted immutable step: first=%+v next=%+v", recommendation, next)
	}
}

func TestRecommendationAPIAdvancesAfterRespondedRecommendation(t *testing.T) {
	for _, responseType := range []string{"accepted", "dismissed"} {
		t.Run(responseType, func(t *testing.T) {
			repository := store.NewMemory()
			seedFeedbackClaims(t, repository, "claim-a", "claim-b")
			router := testRouter(repository)
			first := fetchLateTesterRecommendation(t, router)
			response := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+first.ID+"/responses", model.RecommendationResponse{
				Response: responseType, Reason: "advance after response", ActorID: "late-tester-d",
			})
			if response.Code != http.StatusCreated {
				t.Fatalf("feedback failed: %d %s", response.Code, response.Body.String())
			}
			next := fetchLateTesterRecommendation(t, router)
			if next.ID == first.ID || next.ClaimID == first.ClaimID || next.EvidenceSnapshot != first.EvidenceSnapshot || next.ProgressionSnapshot == first.ProgressionSnapshot {
				t.Fatalf("responded recommendation was re-served: first=%+v next=%+v", first, next)
			}
		})
	}
}

func TestRecommendationOverrideAPIFailsClosedForInvalidShapeAndClaim(t *testing.T) {
	repository := store.NewMemory()
	seedFeedbackClaims(t, repository, "claim-a", "claim-b")
	router := testRouter(repository)
	recommendation := fetchLateTesterRecommendation(t, router)

	cases := []struct {
		name string
		body model.RecommendationResponse
		code string
	}{
		{"missing replacement", model.RecommendationResponse{Response: "override", Reason: "missing", ActorID: "late-tester-d"}, "contract_invalid"},
		{"same replacement", model.RecommendationResponse{Response: "override", Reason: "same", ActorID: "late-tester-d", OverrideClaimID: recommendation.ClaimID}, "override_claim_invalid"},
		{"unknown replacement", model.RecommendationResponse{Response: "override", Reason: "unknown", ActorID: "late-tester-d", OverrideClaimID: "claim-missing"}, "override_claim_invalid"},
		{"override field on accept", model.RecommendationResponse{Response: "accepted", Reason: "invalid shape", ActorID: "late-tester-d", OverrideClaimID: "claim-b"}, "contract_invalid"},
		{"missing reason", model.RecommendationResponse{Response: "dismissed", ActorID: "late-tester-d"}, "contract_invalid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", testCase.body)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"`+testCase.code+`"`) {
				t.Fatalf("expected 422 %s, got %d %s", testCase.code, response.Code, response.Body.String())
			}
		})
	}
}

func TestRecommendationOverrideAPIRejectsRoleInfeasibleReplacement(t *testing.T) {
	repository := store.NewMemory()
	seedFeedbackClaims(t, repository, "FEST-CLAIM-04", "FEST-CLAIM-05")
	router := testRouter(repository)
	recommendation := fetchLateTesterRecommendation(t, router)
	if recommendation.ClaimID != "FEST-CLAIM-04" {
		t.Fatalf("unexpected recommendation: %+v", recommendation)
	}
	for _, alternative := range recommendation.OverrideAlternatives {
		if alternative.ClaimID == "FEST-CLAIM-05" {
			t.Fatalf("role-infeasible alternative leaked into API payload: %+v", recommendation.OverrideAlternatives)
		}
	}
	response := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", model.RecommendationResponse{
		Response: "override", Reason: "viewer-only claim", ActorID: "late-tester-d", OverrideClaimID: "FEST-CLAIM-05",
	})
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "override_claim_invalid") {
		t.Fatalf("role-infeasible override must fail closed: %d %s", response.Code, response.Body.String())
	}
}

func TestRecommendationOverrideAPIEvidenceDriftRetryAndConflict(t *testing.T) {
	repository := store.NewMemory()
	seedFeedbackClaims(t, repository, "claim-a", "claim-b")
	router := testRouter(repository)
	recommendation := fetchLateTesterRecommendation(t, router)
	overrideClaimID := "claim-b"
	if recommendation.ClaimID == overrideClaimID {
		overrideClaimID = "claim-a"
	}

	feedback := model.RecommendationResponse{
		Response: "override", Reason: "the alternate claim is chosen",
		ActorID: "late-tester-d", OverrideClaimID: overrideClaimID,
	}
	first := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", feedback)
	if first.Code != http.StatusCreated {
		t.Fatalf("first override failed: %d %s", first.Code, first.Body.String())
	}

	contextRecord := model.EvidenceContext{
		ID: "ctx-http-drift", TenantID: recommendation.TenantID, RunID: recommendation.RunID,
		BuildID: recommendation.BuildID, EnvironmentID: recommendation.EnvironmentID, ScenarioID: recommendation.ScenarioID,
	}
	if _, _, err := repository.SaveEvidenceContext(context.Background(), contextRecord); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.SaveObservation(context.Background(), model.Observation{
		ID: "obs-http-drift-blocked", TenantID: recommendation.TenantID, RunID: recommendation.RunID,
		ClaimID: overrideClaimID, EvidenceContextID: contextRecord.ID,
		SourceType: "agent", SourceAdapter: "playwright", ActorID: "agent-a",
		Assessment: "blocked", VerifierResult: "fixture unavailable",
		OccurredAt: time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	retry := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", feedback)
	if retry.Code != http.StatusOK {
		t.Fatalf("exact override retry after evidence drift must return 200 OK, got %d %s", retry.Code, retry.Body.String())
	}

	conflictingReason := model.RecommendationResponse{
		Response: "override", Reason: "different reason",
		ActorID: "late-tester-d", OverrideClaimID: overrideClaimID,
	}
	conflictingResp := performJSON(router, http.MethodPost, "/api/v1/recommendations/"+recommendation.ID+"/responses", conflictingReason)
	if conflictingResp.Code != http.StatusConflict || !strings.Contains(conflictingResp.Body.String(), "response_conflict") {
		t.Fatalf("conflicting retry after evidence drift must return 409 Conflict, got %d %s", conflictingResp.Code, conflictingResp.Body.String())
	}
}
