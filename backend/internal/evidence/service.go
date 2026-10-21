// Package evidence reconciles normalized observations into durable evidence.
package evidence

import (
	"crypto/sha256"
	"fmt"

	"github.com/example/qa-platform/backend/internal/model"
)

type Record struct {
	ID               string
	CandidateStateID string
	Event            model.ValidationEvent
}

type Service struct{}

func New() Service { return Service{} }

func (Service) FromEvent(candidateStateID string, event model.ValidationEvent) Record {
	digest := sha256.Sum256([]byte("evidence|" + event.RunID + "|" + event.EventID))
	return Record{ID: fmt.Sprintf("evidence-%x", digest[:12]), CandidateStateID: candidateStateID, Event: event}
}
