// Package state owns coarse, correctable candidate-state identity.
package state

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/example/qa-platform/backend/internal/model"
)

type Identity struct {
	ID            string
	Role          string
	Module        string
	RecordID      string
	BusinessState string
}

type Service struct{}

func New() Service { return Service{} }

func (Service) Identify(event model.ValidationEvent) Identity {
	role := valueOr(event.Context["role"], "unknown")
	module := valueOr(event.Context["module"], moduleFromRoute(event.Route))
	recordID, businessState := fingerprintParts(event.StateFingerprint)
	key := strings.Join([]string{event.RunID, event.TenantID, event.BuildID, event.EnvironmentID, role, event.StateFingerprint}, "|")
	digest := sha256.Sum256([]byte(key))
	return Identity{
		ID: fmt.Sprintf("state-%x", digest[:12]), Role: role, Module: module,
		RecordID: recordID, BusinessState: businessState,
	}
}

// IDFor returns the canonical candidate-state ID for an explicit scope and
// fingerprint. Corrections use the same identity function as future ingest.
func (service Service) IDFor(runID, tenantID, buildID, environmentID, role, fingerprint string) string {
	return service.Identify(model.ValidationEvent{
		RunID: runID, TenantID: tenantID, BuildID: buildID, EnvironmentID: environmentID,
		Context: map[string]string{"role": role}, StateFingerprint: fingerprint,
	}).ID
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func moduleFromRoute(route string) string {
	if strings.Contains(route, "supply-list") {
		return "supply-list"
	}
	return "unknown"
}

func fingerprintParts(fingerprint string) (string, string) {
	parts := strings.Split(fingerprint, ":")
	recordID, businessState := "unknown", "unknown"
	for index, part := range parts {
		if part == "item" && index+1 < len(parts) {
			recordID = parts[index+1]
		}
		if part == "status" && index+1 < len(parts) {
			businessState = parts[index+1]
		}
	}
	return recordID, businessState
}
