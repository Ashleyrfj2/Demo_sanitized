package store

import (
	"context"
	"errors"

	"github.com/example/qa-platform/backend/internal/model"
)

var ErrNotFound = errors.New("not found")
var ErrEventConflict = errors.New("event id payload conflict")
var ErrBuildNotRegistered = errors.New("build is not registered")
var ErrBuildRegistrationConflict = errors.New("build registration conflicts with the existing registry")
var ErrInvalidBuildChain = errors.New("invalid build registration chain")
var ErrExperimentConflict = errors.New("experiment id conflicts with an existing run")
var ErrContractInvalid = errors.New("experiment contract is invalid")
var ErrUnknownRun = errors.New("experiment run is unknown")
var ErrRunScopeMismatch = errors.New("experiment run scope does not match")
var ErrRunFrozen = errors.New("experiment run is frozen")
var ErrFreezeInvariant = errors.New("experiment input invariant prevents freezing")
var ErrResponseConflict = errors.New("recommendation already has a different response")
var ErrRecommendationActorMismatch = errors.New("recommendation response actor does not match its target")
var ErrRecommendationOverrideInvalid = errors.New("recommendation override claim is invalid for this recommendation scope")
var ErrRecommendationProgressionInvalid = errors.New("recommendation progression is invalid for the current routing state")
var ErrClaimConflict = errors.New("claim definition semantic identity conflict")
var ErrClaimNotFound = errors.New("claim definition not found")
var ErrEvidenceContextConflict = errors.New("evidence context conflict")
var ErrObservationConflict = errors.New("observation conflict")

type Scope = model.Scope

type RecommendationQuery struct {
	Scope
	ActorID, Role string
}

type Repository interface {
	RegisterBuild(context.Context, model.BuildRegistration, string) (model.RegisteredBuild, bool, error)
	RequireRegisteredBuild(context.Context, string, string, string) error
	CreateExperiment(context.Context, model.ExperimentRegistration, string) (model.ExperimentRun, bool, error)
	SaveEvent(context.Context, model.ValidationEvent) (bool, error)
	Evidence(context.Context, Scope) (model.EvidenceResponse, error)
	NextRecommendation(context.Context, RecommendationQuery) (model.Recommendation, error)
	SaveRecommendationResponse(context.Context, string, model.RecommendationResponse) (bool, error)
	SaveRecommendationFeedback(context.Context, string, model.RecommendationResponse) (bool, error)
	CorrectState(context.Context, string, string, model.StateCorrectionRequest) error
	Metrics(context.Context, string, string) (model.ExperimentMetrics, error)
	FreezeExperiment(context.Context, string, string) (model.ExperimentMetrics, bool, error)
	SaveClaimDefinition(context.Context, model.ClaimDefinition) (model.ClaimDefinition, bool, error)
	GetClaimDefinition(context.Context, string, string) (model.ClaimDefinition, error)
	SaveEvidenceContext(context.Context, model.EvidenceContext) (model.EvidenceContext, bool, error)
	SaveObservation(context.Context, model.Observation) (model.Observation, bool, error)
	GetClaimEvidence(context.Context, string, string) (model.ClaimEvidence, error)
	GetClaimEvidenceSummary(context.Context, Scope, string) (model.ClaimEvidenceSummary, error)
	ListClaimEvidenceSummaries(context.Context, Scope) ([]model.ClaimEvidenceSummary, error)
	Ping(context.Context) error
	Close()
}
