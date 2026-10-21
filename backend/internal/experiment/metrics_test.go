package experiment

import (
	"errors"
	"testing"
	"time"

	"github.com/example/qa-platform/backend/internal/model"
)

func TestComputeFestivalV11TypedMetrics(t *testing.T) {
	start := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	run := metricRun(start)
	inputs := Inputs{
		Events: []model.ValidationEvent{
			metricEvent("event-a", "editor-a", start, "pass", "finding-defect-a", "accepted", "defect"),
			metricEvent("event-b", "agent-c", start.Add(time.Minute), "pass", "finding-defect-b", "rejected", "defect"),
			metricEvent("event-c", "viewer-b", start.Add(2*time.Minute), "pass", "finding-evidence-a", "accepted", "evidence"),
			metricEvent("event-d", "editor-a", start.Add(3*time.Minute), "pass", "finding-unclassified", "accepted", ""),
			metricEvent("event-late", "late-tester-d", start.Add(5*time.Minute), "fail", "", "", ""),
		},
		States: []model.CandidateState{{ID: "state-a", RunID: run.ID}, {ID: "state-b", RunID: run.ID}},
		Recommendations: []model.Recommendation{{ID: "rec-a", RunID: run.ID, ActorID: "late-tester-d", Area: "supply-list", CreatedAt: start.Add(4 * time.Minute)}},
		Responses: []model.RecommendationResponseRecord{{ID: "response-a", RunID: run.ID, RecommendationID: "rec-a", Response: "accepted", Reason: "highest-value area", ActorID: "late-tester-d", CreatedAt: start.Add(4*time.Minute + time.Second)}},
		Corrections: []model.StateCorrection{{ID: "correction-a", RunID: run.ID, CreatedAt: start.Add(3 * time.Minute)}},
	}
	metrics, canonical, err := Compute(run, inputs, false)
	if err != nil { t.Fatal(err) }
	if len(canonical) == 0 || metrics.Status != "preview" || metrics.DefinitionVersion != DefinitionVersion || metrics.FrozenAt != nil {
		t.Fatalf("unexpected preview envelope: %+v", metrics)
	}
	if len(metrics.Metrics) != 16 { t.Fatalf("expected all 16 metrics, got %d", len(metrics.Metrics)) }
	assertMetric(t, metrics.Metrics["low_information_overlap_rate"], 2.0/5.0, 2, 5, "")
	assertMetric(t, metrics.Metrics["useful_independent_verification_preserved"], 1, 1, -1, "")
	assertMetric(t, metrics.Metrics["accepted_defect_yield"], 0.5, 1, 2, "")
	assertMetric(t, metrics.Metrics["accepted_evidence_yield"], 1, 1, 1, "")
	assertMetric(t, metrics.Metrics["recommendation_acceptance_rate"], 1, 1, 1, "")
	assertMetric(t, metrics.Metrics["late_tester_time_to_next_useful_area_seconds"], 60, 60, -1, "")
	assertMetric(t, metrics.Metrics["novel_or_risk_relevant_evidence_per_tester_hour"], 0, 3, -1, "missing_tester_time_receipt")
	assertMetric(t, metrics.Metrics["high_risk_gap_closure_time_seconds"], 0, -1, -1, "missing_high_risk_classification")
	assertMetric(t, metrics.Metrics["capture_adapter_failure_rate"], 0, -1, -1, "missing_transport_attempt_receipt")
	if metrics.Breakdowns["unclassified_finding_count"].(int) != 1 { t.Fatalf("unexpected finding breakdown: %+v", metrics.Breakdowns) }
	if metrics.InputReceipt.SchemaVersion != ReceiptSchemaVersion || metrics.InputReceipt.Algorithm != "sha256" || metrics.InputReceipt.Immutable || len(metrics.InputReceipt.SHA256) != 64 {
		t.Fatalf("unexpected preview receipt: %+v", metrics.InputReceipt)
	}

	frozenAt := start.Add(time.Hour)
	run.FrozenAt = &frozenAt
	frozen, _, err := Compute(run, inputs, true)
	if err != nil { t.Fatal(err) }
	if frozen.Status != "frozen" || frozen.FrozenAt == nil || !frozen.InputReceipt.Immutable || frozen.InputReceipt.SHA256 != metrics.InputReceipt.SHA256 {
		t.Fatalf("unexpected frozen envelope: %+v", frozen)
	}
}

func TestCanonicalReceiptIgnoresInputAndMapOrderButDetectsPayloadChanges(t *testing.T) {
	start := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	run := metricRun(start)
	first := metricEvent("event-a", "editor-a", start, "pass", "", "", "")
	first.Context = map[string]string{"role": "editor", "browser": "chrome", "connectivity": "online", "module": "supply-list"}
	second := metricEvent("event-b", "agent-c", start.Add(time.Minute), "pass", "", "", "")
	ordered, _, err := Compute(run, Inputs{Events: []model.ValidationEvent{first, second}, States: []model.CandidateState{{ID: "state-b"}, {ID: "state-a"}}}, false)
	if err != nil { t.Fatal(err) }
	first.Context = map[string]string{"module": "supply-list", "connectivity": "online", "browser": "chrome", "role": "editor"}
	reversed, _, err := Compute(run, Inputs{Events: []model.ValidationEvent{second, first}, States: []model.CandidateState{{ID: "state-a"}, {ID: "state-b"}}}, false)
	if err != nil { t.Fatal(err) }
	if ordered.InputReceipt.SHA256 != reversed.InputReceipt.SHA256 { t.Fatalf("canonical receipt changed with insertion/map order") }
	second.Outcome = "fail"
	changed, _, err := Compute(run, Inputs{Events: []model.ValidationEvent{first, second}}, false)
	if err != nil { t.Fatal(err) }
	if ordered.InputReceipt.SHA256 == changed.InputReceipt.SHA256 { t.Fatalf("receipt did not change with payload") }
}

func TestConflictingFindingAdjudicationFailsInvariant(t *testing.T) {
	start := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	first := metricEvent("event-a", "editor-a", start, "pass", "finding-a", "accepted", "defect")
	second := metricEvent("event-b", "agent-c", start.Add(time.Minute), "pass", "finding-a", "rejected", "defect")
	if _, _, err := Compute(metricRun(start), Inputs{Events: []model.ValidationEvent{first, second}}, true); !errors.Is(err, ErrInputInvariant) {
		t.Fatalf("expected invariant failure, got %v", err)
	}
}

func metricRun(start time.Time) model.ExperimentRun {
	return model.ExperimentRun{ID: "festival-guided-001", TenantID: "festival-thesis-demo", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser", ScenarioID: "equipment-handoff-v1", Mode: "guided", StartedAt: start, CreatedBy: "researcher-a"}
}

func metricEvent(id, actor string, occurredAt time.Time, outcome, findingID, status, kind string) model.ValidationEvent {
	return model.ValidationEvent{EventID: id, RunID: "festival-guided-001", TenantID: "festival-thesis-demo", ActorID: actor, SourceType: "human", SourceAdapter: "chrome-mv3", BuildID: "festnest-demo-001", EnvironmentID: "festnest-local-browser", Context: map[string]string{"role": "editor", "browser": "chrome", "connectivity": "online", "module": "supply-list", "finding_kind": kind}, Route: "/trips/demo/supply-list", StateFingerprint: "same-signature", ActionType: "supply_item_observed", Outcome: outcome, VerifierResult: "observed", FindingID: findingID, Verification: status, OccurredAt: occurredAt, StepIndex: 1, Privacy: model.PrivacyMetadata{Masked: true}}
}

func assertMetric(t *testing.T, metric model.MetricValue, value, numerator, denominator float64, undefinedReason string) {
	t.Helper()
	if undefinedReason != "" {
		if metric.Value != nil || metric.UndefinedReason == nil || *metric.UndefinedReason != undefinedReason { t.Fatalf("unexpected undefined metric: %+v", metric) }
	} else if metric.Value == nil || *metric.Value != value || metric.UndefinedReason != nil {
		t.Fatalf("unexpected defined metric: %+v", metric)
	}
	if numerator >= 0 && (metric.Numerator == nil || *metric.Numerator != numerator) { t.Fatalf("unexpected numerator: %+v", metric) }
	if denominator >= 0 && (metric.Denominator == nil || *metric.Denominator != denominator) { t.Fatalf("unexpected denominator: %+v", metric) }
}
