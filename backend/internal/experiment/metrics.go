package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

const DefinitionVersion = "festival-v1.1"
const ReceiptSchemaVersion = "festival-metric-input-v1"

var ErrInputInvariant = errors.New("metric input invariant failed")

type Inputs struct {
	Events               []model.ValidationEvent
	States               []model.CandidateState
	VerificationRequests []model.VerificationRequest
	Recommendations      []model.Recommendation
	Responses            []model.RecommendationResponseRecord
	Corrections          []model.StateCorrection
}

type canonicalInput struct {
	Run                  model.ExperimentRun                  `json:"run"`
	Events               []model.ValidationEvent              `json:"events"`
	States               []model.CandidateState               `json:"states"`
	VerificationRequests []model.VerificationRequest          `json:"verification_requests"`
	Recommendations      []model.Recommendation               `json:"recommendations"`
	Responses            []model.RecommendationResponseRecord `json:"responses"`
	Corrections          []model.StateCorrection              `json:"corrections"`
}

func Compute(run model.ExperimentRun, input Inputs, immutable bool) (model.ExperimentMetrics, []byte, error) {
	canonical := canonicalize(run, input)
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		return model.ExperimentMetrics{}, nil, err
	}
	receipt, err := makeReceipt(canonical, canonicalJSON, immutable)
	if err != nil {
		return model.ExperimentMetrics{}, nil, err
	}

	metrics := allUndefinedMetrics()
	breakdowns := map[string]any{
		"source_type":                 map[string]int{},
		"source_adapter":              map[string]int{},
		"dismissal_reason":            map[string]int{},
		"unclassified_finding_count": 0,
	}
	eligible := make([]model.ValidationEvent, 0, len(canonical.Events))
	for _, event := range canonical.Events {
		breakdowns["source_type"].(map[string]int)[event.SourceType]++
		breakdowns["source_adapter"].(map[string]int)[event.SourceAdapter]++
		if event.Outcome != "abandoned" {
			eligible = append(eligible, event)
		}
	}

	if len(eligible) == 0 {
		metrics["low_information_overlap_rate"] = undefined("ratio", "no_eligible_evidence", floatPtr(0), floatPtr(0))
		metrics["useful_independent_verification_preserved"] = undefined("count", "no_eligible_evidence", floatPtr(0), floatPtr(0))
	} else {
		priorActors := map[string]map[string]bool{}
		priorActorSignature := map[string]bool{}
		overlap, independent, novelOrRisk := 0, 0, 0
		for _, event := range eligible {
			signature := evidenceSignature(event)
			actors := priorActors[signature]
			if actors == nil {
				actors = map[string]bool{}
				priorActors[signature] = actors
			}
			actorKey := event.ActorID + "\x00" + signature
			if priorActorSignature[actorKey] || len(actors) >= 2 {
				overlap++
			}
			isIndependent := !actors[event.ActorID] && len(actors) == 1
			if isIndependent {
				independent++
			}
			if len(actors) == 0 || isIndependent || isAuthoritativeDenial(event) {
				novelOrRisk++
			}
			actors[event.ActorID] = true
			priorActorSignature[actorKey] = true
		}
		metrics["low_information_overlap_rate"] = ratio(float64(overlap), float64(len(eligible)))
		metrics["useful_independent_verification_preserved"] = defined("count", float64(independent), float64(independent), nil)
		novelNumerator := float64(novelOrRisk)
		metrics["novel_or_risk_relevant_evidence_per_tester_hour"] = undefined("per_tester_hour", "missing_tester_time_receipt", &novelNumerator, nil)
	}

	metrics["late_tester_time_to_next_useful_area_seconds"] = lateTesterMetric(canonical.Recommendations, eligible)
	metrics["evidence_completeness_rate"] = completenessMetric(canonical.Events)
	metrics["state_correction_rate"] = correctionMetric(canonical.Corrections, canonical.States)

	defect, evidenceYield, unclassified, err := findingYieldMetrics(canonical.Events)
	if err != nil {
		return model.ExperimentMetrics{}, nil, err
	}
	metrics["accepted_defect_yield"] = defect
	metrics["accepted_evidence_yield"] = evidenceYield
	breakdowns["unclassified_finding_count"] = unclassified

	accepted, dismissed := 0, 0
	for _, response := range canonical.Responses {
		switch response.Response {
		case "accepted":
			accepted++
		case "dismissed":
			dismissed++
			breakdowns["dismissal_reason"].(map[string]int)[response.Reason]++
		}
	}
	if len(canonical.Recommendations) == 0 {
		metrics["recommendation_acceptance_rate"] = undefined("ratio", "no_recommendations", floatPtr(0), floatPtr(0))
		metrics["recommendation_dismissal_rate"] = undefined("ratio", "no_recommendations", floatPtr(0), floatPtr(0))
	} else {
		metrics["recommendation_acceptance_rate"] = ratio(float64(accepted), float64(len(canonical.Recommendations)))
		metrics["recommendation_dismissal_rate"] = ratio(float64(dismissed), float64(len(canonical.Recommendations)))
	}

	asOf := run.StartedAt.UTC()
	for _, event := range canonical.Events {
		asOf = later(asOf, event.OccurredAt)
	}
	for _, state := range canonical.States {
		asOf = later(asOf, state.UpdatedAt)
	}
	for _, request := range canonical.VerificationRequests {
		asOf = later(asOf, request.CreatedAt)
	}
	for _, recommendation := range canonical.Recommendations {
		asOf = later(asOf, recommendation.CreatedAt)
	}
	for _, response := range canonical.Responses {
		asOf = later(asOf, response.CreatedAt)
	}
	for _, correction := range canonical.Corrections {
		asOf = later(asOf, correction.CreatedAt)
	}
	status := "preview"
	if immutable {
		status = "frozen"
	}
	return model.ExperimentMetrics{
		RunID: run.ID, Status: status, DefinitionVersion: DefinitionVersion,
		AsOf: asOf, FrozenAt: run.FrozenAt, Metrics: metrics,
		Breakdowns: breakdowns, InputReceipt: receipt,
	}, canonicalJSON, nil
}

func canonicalize(run model.ExperimentRun, input Inputs) canonicalInput {
	result := canonicalInput{
		Run: run,
		Events: append([]model.ValidationEvent(nil), input.Events...),
		States: append([]model.CandidateState(nil), input.States...),
		VerificationRequests: append([]model.VerificationRequest(nil), input.VerificationRequests...),
		Recommendations: append([]model.Recommendation(nil), input.Recommendations...),
		Responses: append([]model.RecommendationResponseRecord(nil), input.Responses...),
		Corrections: append([]model.StateCorrection(nil), input.Corrections...),
	}
	result.Run.FrozenAt = nil
	sort.Slice(result.Events, func(i, j int) bool {
		if result.Events[i].OccurredAt.Equal(result.Events[j].OccurredAt) {
			return result.Events[i].EventID < result.Events[j].EventID
		}
		return result.Events[i].OccurredAt.Before(result.Events[j].OccurredAt)
	})
	sort.Slice(result.States, func(i, j int) bool { return result.States[i].ID < result.States[j].ID })
	sort.Slice(result.VerificationRequests, func(i, j int) bool { return result.VerificationRequests[i].ID < result.VerificationRequests[j].ID })
	sort.Slice(result.Recommendations, func(i, j int) bool { return result.Recommendations[i].ID < result.Recommendations[j].ID })
	sort.Slice(result.Responses, func(i, j int) bool { return result.Responses[i].ID < result.Responses[j].ID })
	sort.Slice(result.Corrections, func(i, j int) bool { return result.Corrections[i].ID < result.Corrections[j].ID })
	return result
}

func makeReceipt(input canonicalInput, canonicalJSON []byte, immutable bool) (model.MetricInputReceipt, error) {
	digest := sha256.Sum256(canonicalJSON)
	receipt := model.MetricInputReceipt{
		SchemaVersion: ReceiptSchemaVersion, Algorithm: "sha256", SHA256: hex.EncodeToString(digest[:]), Immutable: immutable,
		Counts: model.InputCounts{Events: len(input.Events), States: len(input.States), VerificationRequests: len(input.VerificationRequests), Recommendations: len(input.Recommendations), Responses: len(input.Responses), Corrections: len(input.Corrections)},
		RowDigests: map[string][]model.RowDigest{"events": {}, "states": {}, "verification_requests": {}, "recommendations": {}, "responses": {}, "corrections": {}},
	}
	sets := []struct {
		name string
		rows []struct{ id string; value any }
	}{
		{name: "events", rows: eventRows(input.Events)},
		{name: "states", rows: stateRows(input.States)},
		{name: "verification_requests", rows: requestRows(input.VerificationRequests)},
		{name: "recommendations", rows: recommendationRows(input.Recommendations)},
		{name: "responses", rows: responseRows(input.Responses)},
		{name: "corrections", rows: correctionRows(input.Corrections)},
	}
	for _, set := range sets {
		for _, row := range set.rows {
			encoded, err := json.Marshal(row.value)
			if err != nil {
				return model.MetricInputReceipt{}, err
			}
			hash := sha256.Sum256(encoded)
			receipt.RowDigests[set.name] = append(receipt.RowDigests[set.name], model.RowDigest{ID: row.id, SHA256: hex.EncodeToString(hash[:])})
		}
	}
	return receipt, nil
}

func allUndefinedMetrics() map[string]model.MetricValue {
	return map[string]model.MetricValue{
		"low_information_overlap_rate": undefined("ratio", "no_eligible_evidence", nil, nil),
		"useful_independent_verification_preserved": undefined("count", "no_eligible_evidence", nil, nil),
		"novel_or_risk_relevant_evidence_per_tester_hour": undefined("per_tester_hour", "missing_tester_time_receipt", nil, nil),
		"late_tester_time_to_next_useful_area_seconds": undefined("seconds", "no_late_tester_recommendation", nil, nil),
		"high_risk_gap_closure_time_seconds": undefined("seconds", "missing_high_risk_classification", nil, nil),
		"accepted_defect_yield": undefined("ratio", "no_adjudicated_defects", nil, nil),
		"accepted_evidence_yield": undefined("ratio", "no_adjudicated_evidence", nil, nil),
		"recommendation_acceptance_rate": undefined("ratio", "no_recommendations", nil, nil),
		"recommendation_dismissal_rate": undefined("ratio", "no_recommendations", nil, nil),
		"seeded_condition_discovery_rate": undefined("ratio", "missing_seeded_ground_truth_receipt", nil, nil),
		"evidence_completeness_rate": undefined("ratio", "no_events", nil, nil),
		"state_correction_rate": undefined("ratio", "no_candidate_states", nil, nil),
		"false_merge_split_rate": undefined("ratio", "missing_correction_adjudication", nil, nil),
		"capture_adapter_failure_rate": undefined("ratio", "missing_transport_attempt_receipt", nil, nil),
		"capture_adapter_latency_p50_seconds": undefined("seconds", "missing_transport_latency_receipt", nil, nil),
		"capture_adapter_latency_p95_seconds": undefined("seconds", "missing_transport_latency_receipt", nil, nil),
	}
}

func findingYieldMetrics(events []model.ValidationEvent) (model.MetricValue, model.MetricValue, int, error) {
	type finding struct{ kinds, statuses map[string]bool }
	findings := map[string]*finding{}
	for _, event := range events {
		if strings.TrimSpace(event.FindingID) == "" {
			continue
		}
		entry := findings[event.FindingID]
		if entry == nil {
			entry = &finding{kinds: map[string]bool{}, statuses: map[string]bool{}}
			findings[event.FindingID] = entry
		}
		entry.kinds[event.Context["finding_kind"]] = true
		if event.Verification == "accepted" || event.Verification == "rejected" {
			entry.statuses[event.Verification] = true
		}
	}
	accepted := map[string]int{"defect": 0, "evidence": 0}
	adjudicated := map[string]int{"defect": 0, "evidence": 0}
	unclassified := 0
	for _, entry := range findings {
		if len(entry.kinds) > 1 || len(entry.statuses) > 1 {
			return model.MetricValue{}, model.MetricValue{}, 0, ErrInputInvariant
		}
		kind := ""
		for value := range entry.kinds {
			kind = value
		}
		if kind != "defect" && kind != "evidence" {
			unclassified++
			continue
		}
		status := ""
		for value := range entry.statuses {
			status = value
		}
		if status == "accepted" || status == "rejected" {
			adjudicated[kind]++
			if status == "accepted" {
				accepted[kind]++
			}
		}
	}
	defect := undefined("ratio", "no_adjudicated_defects", floatPtr(float64(accepted["defect"])), floatPtr(float64(adjudicated["defect"])))
	if adjudicated["defect"] > 0 {
		defect = ratio(float64(accepted["defect"]), float64(adjudicated["defect"]))
	}
	evidence := undefined("ratio", "no_adjudicated_evidence", floatPtr(float64(accepted["evidence"])), floatPtr(float64(adjudicated["evidence"])))
	if adjudicated["evidence"] > 0 {
		evidence = ratio(float64(accepted["evidence"]), float64(adjudicated["evidence"]))
	}
	return defect, evidence, unclassified, nil
}

func lateTesterMetric(recommendations []model.Recommendation, events []model.ValidationEvent) model.MetricValue {
	recommendations = append([]model.Recommendation(nil), recommendations...)
	sort.Slice(recommendations, func(i, j int) bool {
		if recommendations[i].CreatedAt.Equal(recommendations[j].CreatedAt) {
			return recommendations[i].ID < recommendations[j].ID
		}
		return recommendations[i].CreatedAt.Before(recommendations[j].CreatedAt)
	})
	var target *model.Recommendation
	for index := range recommendations {
		if recommendations[index].ActorID == "late-tester-d" {
			target = &recommendations[index]
			break
		}
	}
	if target == nil {
		return undefined("seconds", "no_late_tester_recommendation", nil, nil)
	}
	for _, event := range events {
		if event.ActorID == "late-tester-d" && event.OccurredAt.After(target.CreatedAt) && eventArea(event) == target.Area {
			seconds := event.OccurredAt.Sub(target.CreatedAt).Seconds()
			return defined("seconds", seconds, seconds, nil)
		}
	}
	return undefined("seconds", "no_later_useful_area_event", nil, nil)
}

func completenessMetric(events []model.ValidationEvent) model.MetricValue {
	if len(events) == 0 {
		return undefined("ratio", "no_events", floatPtr(0), floatPtr(0))
	}
	complete := 0
	for _, event := range events {
		if event.TenantID != "" && event.BuildID != "" && event.EnvironmentID != "" && event.ActorID != "" && event.SourceType != "" && event.SourceAdapter != "" && strings.TrimSpace(event.Context["role"]) != "" && !event.OccurredAt.IsZero() {
			complete++
		}
	}
	return ratio(float64(complete), float64(len(events)))
}

func correctionMetric(corrections []model.StateCorrection, states []model.CandidateState) model.MetricValue {
	if len(states) == 0 {
		return undefined("ratio", "no_candidate_states", floatPtr(float64(len(corrections))), floatPtr(0))
	}
	return ratio(float64(len(corrections)), float64(len(states)))
}

func evidenceSignature(event model.ValidationEvent) string {
	return strings.Join([]string{event.StateFingerprint, event.Context["role"], event.Context["browser"], event.Context["connectivity"], event.Outcome, event.VerifierResult}, "|")
}

func isAuthoritativeDenial(event model.ValidationEvent) bool {
	return event.ActionType == "supply_item_permission_denied" && event.SourceType == "automation" && event.SourceAdapter == "festival-activity-log-v1" && event.Outcome == "blocked" && event.VerifierResult == "database_authorization_denied" && event.Context["provenance"] == "festival-db-supply-audit-v1" && strings.TrimSpace(event.Context["attempted_action"]) != ""
}

func eventArea(event model.ValidationEvent) string {
	if event.Context["module"] != "" {
		return event.Context["module"]
	}
	if strings.Contains(event.Route, "supply-list") {
		return "supply-list"
	}
	return "unknown"
}

func ratio(numerator, denominator float64) model.MetricValue {
	value := numerator / denominator
	return defined("ratio", value, numerator, &denominator)
}

func defined(unit string, value, numerator float64, denominator *float64) model.MetricValue {
	return model.MetricValue{Value: &value, Unit: unit, Numerator: &numerator, Denominator: denominator}
}

func undefined(unit, reason string, numerator, denominator *float64) model.MetricValue {
	return model.MetricValue{Unit: unit, Numerator: numerator, Denominator: denominator, UndefinedReason: &reason}
}

func floatPtr(value float64) *float64 { return &value }
func later(left, right time.Time) time.Time {
	if right.After(left) {
		return right.UTC()
	}
	return left.UTC()
}

func eventRows(values []model.ValidationEvent) []struct{ id string; value any } {
	rows := make([]struct{ id string; value any }, 0, len(values))
	for _, value := range values { rows = append(rows, struct{ id string; value any }{value.EventID, value}) }
	return rows
}
func stateRows(values []model.CandidateState) []struct{ id string; value any } {
	rows := make([]struct{ id string; value any }, 0, len(values))
	for _, value := range values { rows = append(rows, struct{ id string; value any }{value.ID, value}) }
	return rows
}
func requestRows(values []model.VerificationRequest) []struct{ id string; value any } {
	rows := make([]struct{ id string; value any }, 0, len(values))
	for _, value := range values { rows = append(rows, struct{ id string; value any }{value.ID, value}) }
	return rows
}
func recommendationRows(values []model.Recommendation) []struct{ id string; value any } {
	rows := make([]struct{ id string; value any }, 0, len(values))
	for _, value := range values { rows = append(rows, struct{ id string; value any }{value.ID, value}) }
	return rows
}
func responseRows(values []model.RecommendationResponseRecord) []struct{ id string; value any } {
	rows := make([]struct{ id string; value any }, 0, len(values))
	for _, value := range values { rows = append(rows, struct{ id string; value any }{value.ID, value}) }
	return rows
}
func correctionRows(values []model.StateCorrection) []struct{ id string; value any } {
	rows := make([]struct{ id string; value any }, 0, len(values))
	for _, value := range values { rows = append(rows, struct{ id string; value any }{value.ID, value}) }
	return rows
}
