package sufficiency

import (
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func TestAmbiguousStateRequiresTwoFreshIndependentVerifications(t *testing.T) {
	start := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	events := []model.ValidationEvent{
		{ActorID: "editor-a", SourceType: "adapter", Outcome: "ambiguous", OccurredAt: start, Context: map[string]string{"connectivity": "stale"}},
		{ActorID: "editor-a", SourceType: "human", Outcome: "pass", OccurredAt: start.Add(time.Minute), Context: map[string]string{"connectivity": "online"}},
	}
	result := New().Evaluate(events)
	if result.Status != Conflicting {
		t.Fatalf("one fresh actor must not close stale uncertainty: got %s", result.Status)
	}
	events = append(events, model.ValidationEvent{ActorID: "viewer-b", SourceType: "human", Outcome: "pass", OccurredAt: start.Add(2 * time.Minute), Context: map[string]string{"connectivity": "online"}})
	result = New().Evaluate(events)
	if result.Status != Strong || result.Conflict {
		t.Fatalf("two fresh independent actors should close uncertainty: %+v", result)
	}
}

func TestPermissionDenialIsStrongRoleContextEvidence(t *testing.T) {
	authoritative := model.ValidationEvent{
		ActorID: "viewer-b", SourceType: "automation", SourceAdapter: "festival-activity-log-v1",
		ActionType: "supply_item_permission_denied", Outcome: "blocked",
		VerifierResult: "database_authorization_denied", OccurredAt: time.Now().UTC(),
		Context: map[string]string{"role": "viewer", "attempted_action": "delete", "provenance": "festival-db-supply-audit-v1"},
	}
	result := New().Evaluate([]model.ValidationEvent{authoritative})
	if result.Status != Strong {
		t.Fatalf("authoritative permission denial should be strongly represented, got %s", result.Status)
	}

	lookalikes := []struct {
		name   string
		mutate func(*model.ValidationEvent)
	}{
		{"client action alias", func(event *model.ValidationEvent) { event.ActionType = "supply_item_delete_denied" }},
		{"human source", func(event *model.ValidationEvent) { event.SourceType = "human" }},
		{"wrong adapter", func(event *model.ValidationEvent) { event.SourceAdapter = "chrome-mv3" }},
		{"passing outcome", func(event *model.ValidationEvent) { event.Outcome = "pass" }},
		{"wrong verifier", func(event *model.ValidationEvent) { event.VerifierResult = "rls_delete_denied" }},
		{"missing action", func(event *model.ValidationEvent) { delete(event.Context, "attempted_action") }},
		{"wrong provenance", func(event *model.ValidationEvent) { event.Context["provenance"] = "client_asserted" }},
	}
	for _, testCase := range lookalikes {
		t.Run(testCase.name, func(t *testing.T) {
			event := authoritative
			event.Context = map[string]string{}
			for key, value := range authoritative.Context {
				event.Context[key] = value
			}
			testCase.mutate(&event)
			result := New().Evaluate([]model.ValidationEvent{event})
			if result.Status == Strong {
				t.Fatalf("lookalike denial must not become strong: %+v", event)
			}
		})
	}
	twoActorLookalike := authoritative
	twoActorLookalike.SourceType = "human"
	secondActor := twoActorLookalike
	secondActor.ActorID = "viewer-c"
	result = New().Evaluate([]model.ValidationEvent{twoActorLookalike, secondActor})
	if result.Status == Strong {
		t.Fatalf("two client-asserted denial lookalikes must not become strong: %+v", result)
	}
}
