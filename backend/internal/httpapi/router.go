package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/example/qa-platform/backend/internal/ingest"
	"github.com/example/qa-platform/backend/internal/model"
	"github.com/example/qa-platform/backend/internal/store"
)

const maxBodyBytes = 64 << 10

func NewRouter(repositories ...store.Repository) http.Handler {
	return NewAuthenticatedRouter(NewStaticAuthenticator(nil), repositories...)
}

func NewAuthenticatedRouter(authenticator Authenticator, repositories ...store.Repository) http.Handler {
	repository := store.Repository(store.NewMemory())
	if len(repositories) > 0 && repositories[0] != nil {
		repository = repositories[0]
	}
	validator := ingest.New()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := repository.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/builds", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("controller") {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "insufficient_capability", "error": "controller capability is required"})
			return
		}
		var registration model.BuildRegistration
		if err := decodeJSON(w, r, &registration); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if registration.TenantID != identity.TenantID {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "tenant_mismatch", "error": "build tenant does not match authenticated controller"})
			return
		}
		registered, created, err := repository.RegisterBuild(r.Context(), registration, identity.ActorID)
		switch {
		case errors.Is(err, store.ErrBuildRegistrationConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"code": "build_registration_conflict", "error": err.Error()})
			return
		case errors.Is(err, store.ErrInvalidBuildChain):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "invalid_build_chain", "error": err.Error()})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build registration failed"})
			return
		}
		status := http.StatusCreated
		if !created {
			status = http.StatusOK
		}
		writeJSON(w, status, registered)
	})
	mux.HandleFunc("POST /api/v1/experiments", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("controller") {
			writeAPIError(w, http.StatusForbidden, "insufficient_capability", "controller capability is required")
			return
		}
		var registration model.ExperimentRegistration
		if err := decodeJSON(w, r, &registration); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
			return
		}
		if registration.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "experiment tenant does not match authenticated controller")
			return
		}
		run, created, err := repository.CreateExperiment(r.Context(), registration, identity.ActorID)
		switch {
		case errors.Is(err, store.ErrExperimentConflict):
			writeAPIError(w, http.StatusConflict, "experiment_conflict", err.Error())
		case errors.Is(err, store.ErrContractInvalid):
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
		case errors.Is(err, store.ErrBuildNotRegistered):
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_build", err.Error())
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, "experiment_creation_failed", "experiment creation failed")
		default:
			status := http.StatusCreated
			if !created {
				status = http.StatusOK
			}
			writeJSON(w, status, run)
		}
	})
	mux.HandleFunc("POST /api/v1/experiments/{id}/freeze", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("controller") {
			writeAPIError(w, http.StatusForbidden, "insufficient_capability", "controller capability is required")
			return
		}
		if body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes)); len(strings.TrimSpace(string(body))) != 0 {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", "freeze does not accept a request body")
			return
		}
		metrics, created, err := repository.FreezeExperiment(r.Context(), r.PathValue("id"), identity.TenantID)
		switch {
		case errors.Is(err, store.ErrUnknownRun):
			writeAPIError(w, http.StatusNotFound, "experiment_not_found", "experiment not found")
		case errors.Is(err, store.ErrRunScopeMismatch):
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "experiment tenant does not match authenticated controller")
		case errors.Is(err, store.ErrFreezeInvariant):
			writeAPIError(w, http.StatusConflict, "freeze_invariant_failed", err.Error())
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, "experiment_freeze_failed", "experiment freeze failed")
		default:
			status := http.StatusCreated
			if !created {
				status = http.StatusOK
			}
			writeJSON(w, status, metrics)
		}
	})
	mux.HandleFunc("POST /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("source") {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "insufficient_capability", "error": "source capability is required"})
			return
		}
		var event model.ValidationEvent
		if err := decodeJSON(w, r, &event); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
			return
		}
		if err := bindEventIdentity(&event, identity); err != nil {
			writeAPIError(w, http.StatusForbidden, "identity_mismatch", err.Error())
			return
		}
		if strings.TrimSpace(event.RunID) == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", store.ErrUnknownRun.Error())
			return
		}
		if err := validator.Validate(event); err != nil {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
			return
		}
		created, err := repository.SaveEvent(r.Context(), event)
		if errors.Is(err, store.ErrBuildNotRegistered) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "unknown_build", "error": err.Error()})
			return
		}
		if writeRunWriteError(w, err) {
			return
		}
		if errors.Is(err, store.ErrEventConflict) {
			writeAPIError(w, http.StatusConflict, "event_id_conflict", "event_id already exists with a different payload")
			return
		}
		if err != nil {
			log.Print("SaveEvent persistence error: details suppressed to protect request data")
			writeAPIError(w, http.StatusInternalServerError, "event_persistence_failed", "event persistence failed")
			return
		}
		if !created {
			writeJSON(w, http.StatusOK, map[string]string{"event_id": event.EventID, "status": "duplicate"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"event_id": event.EventID, "status": "accepted"})
	})
	mux.HandleFunc("GET /api/v1/evidence", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		scope, ok := scopeFromQuery(r)
		if scope.RunID == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", store.ErrUnknownRun.Error())
			return
		}
		if !ok {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "run_id, tenant_id, build_id, and environment_id are required")
			return
		}
		if scope.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "tenant scope does not match authenticated source")
			return
		}
		if err := repository.RequireRegisteredBuild(r.Context(), scope.TenantID, scope.EnvironmentID, scope.BuildID); errors.Is(err, store.ErrBuildNotRegistered) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "unknown_build", "error": err.Error()})
			return
		} else if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "build_registry_lookup_failed", "build registry lookup failed")
			return
		}
		response, err := repository.Evidence(r.Context(), scope)
		if errors.Is(err, store.ErrUnknownRun) {
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", err.Error())
			return
		}
		if errors.Is(err, store.ErrRunScopeMismatch) {
			writeAPIError(w, http.StatusUnprocessableEntity, "run_scope_mismatch", err.Error())
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "evidence_retrieval_failed", "evidence retrieval failed")
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("GET /api/v1/claims/evidence", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		scope, ok := scopeFromQuery(r)
		if scope.RunID == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", store.ErrUnknownRun.Error())
			return
		}
		if !ok {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "run_id, tenant_id, build_id, and environment_id are required")
			return
		}
		if scope.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "tenant scope does not match authenticated source")
			return
		}
		if err := repository.RequireRegisteredBuild(r.Context(), scope.TenantID, scope.EnvironmentID, scope.BuildID); errors.Is(err, store.ErrBuildNotRegistered) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "unknown_build", "error": err.Error()})
			return
		} else if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "build_registry_lookup_failed", "build registry lookup failed")
			return
		}

		claimID := r.URL.Query().Get("claim_id")
		if claimID != "" {
			summary, err := repository.GetClaimEvidenceSummary(r.Context(), scope, claimID)
			if errors.Is(err, store.ErrClaimNotFound) {
				writeAPIError(w, http.StatusNotFound, "claim_not_found", "claim definition not found")
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "claim_evidence_summary_failed", "claim evidence summary failed")
				return
			}
			writeJSON(w, http.StatusOK, summary)
			return
		}

		summaries, err := repository.ListClaimEvidenceSummaries(r.Context(), scope)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "claim_evidence_summaries_failed", "claim evidence summaries failed")
			return
		}
		response := model.ClaimEvidenceSummariesResponse{
			RunID:         scope.RunID,
			TenantID:      scope.TenantID,
			BuildID:       scope.BuildID,
			EnvironmentID: scope.EnvironmentID,
			Summaries:     summaries,
		}
		writeJSON(w, http.StatusOK, response)
	})
	mux.HandleFunc("GET /api/v1/claims/{id}/evidence", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		scope, ok := scopeFromQuery(r)
		if scope.RunID == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", store.ErrUnknownRun.Error())
			return
		}
		if !ok {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "run_id, tenant_id, build_id, and environment_id are required")
			return
		}
		if scope.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "tenant scope does not match authenticated source")
			return
		}
		claimID := r.PathValue("id")
		summary, err := repository.GetClaimEvidenceSummary(r.Context(), scope, claimID)
		if errors.Is(err, store.ErrClaimNotFound) {
			writeAPIError(w, http.StatusNotFound, "claim_not_found", "claim definition not found")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "claim_evidence_summary_failed", "claim evidence summary failed")
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})
	mux.HandleFunc("POST /api/v1/claims", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("controller") && !identity.HasCapability("source") {
			writeAPIError(w, http.StatusForbidden, "insufficient_capability", "controller or source capability is required")
			return
		}
		var claim model.ClaimDefinition
		if err := decodeJSON(w, r, &claim); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
			return
		}
		if claim.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "claim tenant does not match authenticated actor")
			return
		}
		saved, created, err := repository.SaveClaimDefinition(r.Context(), claim)
		switch {
		case errors.Is(err, store.ErrClaimConflict):
			writeAPIError(w, http.StatusConflict, "claim_conflict", err.Error())
		case errors.Is(err, store.ErrContractInvalid):
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, "claim_persistence_failed", "claim persistence failed")
		default:
			status := http.StatusCreated
			if !created {
				status = http.StatusOK
			}
			writeJSON(w, status, saved)
		}
	})
	mux.HandleFunc("POST /api/v1/evidence-contexts", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("controller") && !identity.HasCapability("source") {
			writeAPIError(w, http.StatusForbidden, "insufficient_capability", "controller or source capability is required")
			return
		}
		var ec model.EvidenceContext
		if err := decodeJSON(w, r, &ec); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
			return
		}
		if ec.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "evidence context tenant does not match authenticated actor")
			return
		}
		saved, created, err := repository.SaveEvidenceContext(r.Context(), ec)
		if writeRunWriteError(w, err) {
			return
		}
		switch {
		case errors.Is(err, store.ErrEvidenceContextConflict):
			writeAPIError(w, http.StatusConflict, "evidence_context_conflict", err.Error())
		case errors.Is(err, store.ErrContractInvalid):
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, "evidence_context_persistence_failed", "evidence context persistence failed")
		default:
			status := http.StatusCreated
			if !created {
				status = http.StatusOK
			}
			writeJSON(w, status, saved)
		}
	})
	mux.HandleFunc("POST /api/v1/observations", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		if !identity.HasCapability("source") && !identity.HasCapability("controller") {
			writeAPIError(w, http.StatusForbidden, "insufficient_capability", "source capability is required")
			return
		}
		var obs model.Observation
		if err := decodeJSON(w, r, &obs); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
			return
		}
		if obs.TenantID != identity.TenantID {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "observation tenant does not match authenticated source")
			return
		}
		if obs.ActorID != "" && obs.ActorID != identity.ActorID {
			writeAPIError(w, http.StatusForbidden, "identity_mismatch", "observation actor does not match authenticated source")
			return
		}
		if obs.SourceType != "" && obs.SourceType != identity.SourceType {
			writeAPIError(w, http.StatusForbidden, "identity_mismatch", "observation source_type does not match authenticated source")
			return
		}
		if obs.SourceAdapter != "" && obs.SourceAdapter != identity.SourceAdapter {
			writeAPIError(w, http.StatusForbidden, "identity_mismatch", "observation source_adapter does not match authenticated source")
			return
		}
		obs.ActorID = identity.ActorID
		obs.SourceType = identity.SourceType
		obs.SourceAdapter = identity.SourceAdapter

		saved, created, err := repository.SaveObservation(r.Context(), obs)
		if writeRunWriteError(w, err) {
			return
		}
		switch {
		case errors.Is(err, store.ErrObservationConflict):
			writeAPIError(w, http.StatusConflict, "observation_conflict", err.Error())
		case errors.Is(err, store.ErrClaimNotFound):
			writeAPIError(w, http.StatusNotFound, "claim_not_found", err.Error())
		case errors.Is(err, store.ErrContractInvalid):
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
		case errors.Is(err, store.ErrUnknownRun):
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", err.Error())
		case errors.Is(err, store.ErrRunScopeMismatch):
			writeAPIError(w, http.StatusUnprocessableEntity, "run_scope_mismatch", err.Error())
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, "observation_persistence_failed", "observation persistence failed")
		default:
			status := http.StatusCreated
			if !created {
				status = http.StatusOK
			}
			writeJSON(w, status, saved)
		}
	})
	mux.HandleFunc("GET /api/v1/recommendations/next", func(w http.ResponseWriter, r *http.Request) {
		identity, authenticated := authenticate(w, r, authenticator)
		if !authenticated {
			return
		}
		scope, ok := scopeFromQuery(r)
		actorID, role := r.URL.Query().Get("actor_id"), r.URL.Query().Get("role")
		if scope.RunID == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", store.ErrUnknownRun.Error())
			return
		}
		if !ok || actorID == "" || role == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "run_id, actor_id, role, tenant_id, build_id, and environment_id are required")
			return
		}
		if scope.TenantID != identity.TenantID || actorID != identity.ActorID || role != identity.Role {
			writeAPIError(w, http.StatusForbidden, "identity_mismatch", "recommendation identity does not match authenticated source")
			return
		}
		if err := repository.RequireRegisteredBuild(r.Context(), scope.TenantID, scope.EnvironmentID, scope.BuildID); errors.Is(err, store.ErrBuildNotRegistered) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "unknown_build", "error": err.Error()})
			return
		} else if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "build_registry_lookup_failed", "build registry lookup failed")
			return
		}
		recommendation, err := repository.NextRecommendation(r.Context(), store.RecommendationQuery{Scope: scope, ActorID: actorID, Role: role})
		if writeRunWriteError(w, err) {
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "recommendation_not_found", "no recommendation available")
			return
		}
		if errors.Is(err, store.ErrRecommendationProgressionInvalid) {
			writeAPIError(w, http.StatusConflict, "recommendation_progression_invalid", err.Error())
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "recommendation_failed", "recommendation failed")
			return
		}
		writeJSON(w, http.StatusOK, recommendation)
	})
	mux.HandleFunc("POST /api/v1/recommendations/{id}/responses", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		var response model.RecommendationResponse
		if err := decodeJSON(w, r, &response); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
			return
		}
		if response.ActorID != "" && response.ActorID != identity.ActorID {
			writeAPIError(w, http.StatusForbidden, "identity_mismatch", "response actor does not match authenticated source")
			return
		}
		response.ActorID = identity.ActorID
		response.Response = strings.TrimSpace(response.Response)
		response.Reason = strings.TrimSpace(response.Reason)
		response.OverrideClaimID = strings.TrimSpace(response.OverrideClaimID)
		if response.Reason == "" || response.Response != "accepted" && response.Response != "dismissed" && response.Response != "override" {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "response must be accepted, dismissed, or override and reason is required")
			return
		}
		if response.Response == "override" && response.OverrideClaimID == "" || response.Response != "override" && response.OverrideClaimID != "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "override_claim_id is required only for override responses")
			return
		}
		created, err := repository.SaveRecommendationFeedback(r.Context(), r.PathValue("id"), response)
		if errors.Is(err, store.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "recommendation_not_found", "recommendation not found")
			return
		} else if writeRunWriteError(w, err) {
			return
		} else if errors.Is(err, store.ErrResponseConflict) {
			writeAPIError(w, http.StatusConflict, "response_conflict", err.Error())
			return
		} else if errors.Is(err, store.ErrRecommendationActorMismatch) {
			writeAPIError(w, http.StatusForbidden, "recommendation_actor_mismatch", err.Error())
			return
		} else if errors.Is(err, store.ErrRecommendationOverrideInvalid) {
			writeAPIError(w, http.StatusUnprocessableEntity, "override_claim_invalid", err.Error())
			return
		} else if errors.Is(err, store.ErrContractInvalid) {
			writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
			return
		} else if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "response_persistence_failed", "response persistence failed")
			return
		}
		status := http.StatusCreated
		if !created {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]string{"status": "recorded"})
	})
	for _, correctionType := range []string{"merge", "split", "rename"} {
		kind := correctionType
		mux.HandleFunc("POST /api/v1/states/{id}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			identity, ok := authenticate(w, r, authenticator)
			if !ok {
				return
			}
			var request model.StateCorrectionRequest
			if err := decodeJSON(w, r, &request); err != nil {
				writeAPIError(w, http.StatusBadRequest, "malformed_json", err.Error())
				return
			}
			if request.ActorID != "" && request.ActorID != identity.ActorID {
				writeAPIError(w, http.StatusForbidden, "identity_mismatch", "correction actor does not match authenticated source")
				return
			}
			request.ActorID = identity.ActorID
			if strings.TrimSpace(request.Reason) == "" {
				writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", "reason is required")
				return
			}
			if err := repository.CorrectState(r.Context(), kind, r.PathValue("id"), request); errors.Is(err, store.ErrNotFound) {
				writeAPIError(w, http.StatusNotFound, "state_not_found", "state not found")
				return
			} else if writeRunWriteError(w, err) {
				return
			} else if err != nil {
				writeAPIError(w, http.StatusUnprocessableEntity, "contract_invalid", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "corrected", "correction_type": kind})
		})
	}
	mux.HandleFunc("GET /api/v1/experiments/{id}/metrics", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authenticate(w, r, authenticator)
		if !ok {
			return
		}
		metrics, err := repository.Metrics(r.Context(), r.PathValue("id"), identity.TenantID)
		if errors.Is(err, store.ErrUnknownRun) {
			writeAPIError(w, http.StatusNotFound, "experiment_not_found", "experiment not found")
			return
		}
		if errors.Is(err, store.ErrRunScopeMismatch) {
			writeAPIError(w, http.StatusForbidden, "tenant_mismatch", "experiment tenant does not match authenticated source")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "metrics_preview_failed", "metrics preview failed")
			return
		}
		writeJSON(w, http.StatusOK, metrics)
	})

	return localCORS(mux)
}

func authenticate(w http.ResponseWriter, r *http.Request, authenticator Authenticator) (SourceIdentity, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bearer authentication required"})
		return SourceIdentity{}, false
	}
	identity, ok := authenticator.Authenticate(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid bearer credential"})
		return SourceIdentity{}, false
	}
	return identity, true
}

func bindEventIdentity(event *model.ValidationEvent, identity SourceIdentity) error {
	if event.Context == nil {
		event.Context = map[string]string{}
	}
	assertions := []struct{ label, received, expected string }{
		{"tenant_id", event.TenantID, identity.TenantID},
		{"actor_id", event.ActorID, identity.ActorID},
		{"source_type", event.SourceType, identity.SourceType},
		{"source_adapter", event.SourceAdapter, identity.SourceAdapter},
		{"context.role", event.Context["role"], identity.Role},
	}
	for _, assertion := range assertions {
		if assertion.received != assertion.expected {
			return errors.New(assertion.label + " does not match authenticated source")
		}
	}
	event.TenantID, event.ActorID = identity.TenantID, identity.ActorID
	event.SourceType, event.SourceAdapter = identity.SourceType, identity.SourceAdapter
	event.Context["role"] = identity.Role
	return nil
}

func scopeFromQuery(r *http.Request) (store.Scope, bool) {
	featureFlags, featureFlagsValid := featureFlagsFromQuery(r.URL.Query()["feature_flag"])
	scope := store.Scope{
		RunID:         strings.TrimSpace(r.URL.Query().Get("run_id")),
		TenantID:      strings.TrimSpace(r.URL.Query().Get("tenant_id")),
		BuildID:       strings.TrimSpace(r.URL.Query().Get("build_id")),
		EnvironmentID: strings.TrimSpace(r.URL.Query().Get("environment_id")),
		ScenarioID:    strings.TrimSpace(r.URL.Query().Get("scenario_id")),
		Device:        strings.TrimSpace(r.URL.Query().Get("device")),
		Region:        strings.TrimSpace(r.URL.Query().Get("region")),
		FeatureFlags:  featureFlags,
	}
	return scope, featureFlagsValid && scope.RunID != "" && scope.TenantID != "" && scope.BuildID != "" && scope.EnvironmentID != ""
}

func featureFlagsFromQuery(values []string) (map[string]string, bool) {
	if len(values) == 0 {
		return nil, true
	}
	flags := make(map[string]string, len(values))
	for _, raw := range values {
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 {
			return nil, false
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if key == "" || value == "" {
			return nil, false
		}
		if _, duplicate := flags[key]; duplicate {
			return nil, false
		}
		flags[key] = value
	}
	return flags, true
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}

func writeRunWriteError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrUnknownRun):
		writeAPIError(w, http.StatusUnprocessableEntity, "unknown_run", err.Error())
		return true
	case errors.Is(err, store.ErrRunScopeMismatch):
		writeAPIError(w, http.StatusUnprocessableEntity, "run_scope_mismatch", err.Error())
		return true
	case errors.Is(err, store.ErrRunFrozen):
		writeAPIError(w, http.StatusConflict, "run_frozen", err.Error())
		return true
	}
	return false
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid JSON payload")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("payload must contain one JSON object")
	}
	return nil
}

func localCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "http://127.0.0.1:5173" || origin == "http://localhost:5173" || origin == "http://127.0.0.1:4173" || origin == "http://localhost:4173" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
