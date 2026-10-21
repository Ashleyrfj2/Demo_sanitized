// Package sufficiency evaluates evidence strength, conflict, staleness, and gaps.
package sufficiency

import (
	"sort"
	"strings"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

type Status string

const (
	Strong                       Status = "strongly_represented"
	Weak                         Status = "weak_or_uncertain"
	Conflicting                  Status = "conflicting"
	Stale                        Status = "stale"
	Blocked                      Status = "blocked"
	Untouched                    Status = "untouched"
	NeedsIndependentVerification Status = "needs_independent_verification"
)

type Service struct{}

func New() Service { return Service{} }

type Evaluation struct {
	Status      Status
	Conflict    bool
	ActorCount  int
	SourceTypes []string
}

func (Service) Evaluate(events []model.ValidationEvent) Evaluation {
	if len(events) == 0 {
		return Evaluation{Status: Untouched}
	}
	actors := map[string]struct{}{}
	sources := map[string]struct{}{}
	outcomes := map[string]struct{}{}
	needsVerification := false
	permissionDenied := false
	permissionDenialClaimed := false
	var latestAmbiguous time.Time
	for _, event := range events {
		actors[event.ActorID] = struct{}{}
		sources[event.SourceType] = struct{}{}
		outcomes[event.Outcome] = struct{}{}
		if event.Outcome == "ambiguous" || event.Context["connectivity"] == "stale" || event.Context["connectivity"] == "offline" {
			needsVerification = true
			if event.OccurredAt.After(latestAmbiguous) {
				latestAmbiguous = event.OccurredAt
			}
		}
		if event.ActionType == "supply_item_permission_denied" &&
			event.SourceType == "automation" &&
			event.SourceAdapter == "festival-activity-log-v1" &&
			event.Outcome == "blocked" &&
			event.VerifierResult == "database_authorization_denied" &&
			event.Context["provenance"] == "festival-db-supply-audit-v1" &&
			event.Context["attempted_action"] != "" {
			permissionDenied = true
		}
		if strings.Contains(event.ActionType, "permission_denied") ||
			strings.Contains(event.ActionType, "delete_denied") ||
			strings.Contains(event.StateFingerprint, "permission-denied") {
			permissionDenialClaimed = true
		}
	}
	result := Evaluation{ActorCount: len(actors)}
	for source := range sources {
		result.SourceTypes = append(result.SourceTypes, source)
	}
	sort.Strings(result.SourceTypes)
	freshVerificationActors := map[string]struct{}{}
	if !latestAmbiguous.IsZero() {
		for _, event := range events {
			if event.OccurredAt.After(latestAmbiguous) && event.Outcome == "pass" && event.Context["connectivity"] == "online" {
				freshVerificationActors[event.ActorID] = struct{}{}
			}
		}
	}
	if len(freshVerificationActors) >= 2 {
		needsVerification = false
		outcomes = map[string]struct{}{"pass": {}}
	}
	switch {
	case permissionDenied:
		result.Status = Strong
	case len(outcomes) > 1 || needsVerification:
		result.Status, result.Conflict = Conflicting, true
	case permissionDenialClaimed:
		result.Status = NeedsIndependentVerification
	case len(actors) >= 2:
		result.Status = Strong
	case len(actors) == 1:
		result.Status = NeedsIndependentVerification
	default:
		result.Status = Weak
	}
	return result
}
