package ingest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/example/qa-platform/backend/internal/model"
)

// Service validates the normalized contract before later pipeline stages use it.
type Service struct{}

func New() Service { return Service{} }

func (Service) Validate(event model.ValidationEvent) error {
	required := map[string]string{
		"event_id": event.EventID, "tenant_id": event.TenantID,
		"run_id": event.RunID,
		"actor_id": event.ActorID, "source_type": event.SourceType,
		"source_adapter": event.SourceAdapter, "build_id": event.BuildID,
		"environment_id": event.EnvironmentID, "route": event.Route,
		"state_fingerprint": event.StateFingerprint, "action_type": event.ActionType,
		"outcome": event.Outcome,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return errors.New(field + " is required")
		}
		if len(value) > 512 {
			return fmt.Errorf("%s exceeds 512 characters", field)
		}
	}
	if event.StepIndex < 0 {
		return errors.New("step_index must be non-negative")
	}
	if event.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}
	if !event.Privacy.Masked {
		return errors.New("privacy.masked must be true")
	}
	lowerRoute := strings.ToLower(event.Route)
	if strings.Contains(lowerRoute, "/safety") || strings.Contains(lowerRoute, "/emergency") {
		return errors.New("safety and emergency routes are outside the V1 evidence boundary")
	}
	if !allowed(event.SourceType, "human", "agent", "automation") {
		return errors.New("source_type is not allowed")
	}
	if !allowed(event.Outcome, "pass", "fail", "blocked", "ambiguous", "abandoned") {
		return errors.New("outcome is not allowed")
	}
	if len(event.Context) > 24 {
		return errors.New("context exceeds 24 entries")
	}
	for key, value := range event.Context {
		if len(key) > 80 || len(value) > 240 {
			return errors.New("context key or value exceeds allowed length")
		}
		lowerKey := strings.ToLower(key)
		for _, forbidden := range []string{"password", "token", "cookie", "authorization", "body", "description", "freeform", "safety", "emergency"} {
			if strings.Contains(lowerKey, forbidden) {
				return fmt.Errorf("context key %q is not allowed", key)
			}
		}
	}
	return nil
}

func allowed(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
