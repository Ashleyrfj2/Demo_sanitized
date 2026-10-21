package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecommendationFactorsPreserveLegacyHistoryAndCanonicalShape(t *testing.T) {
	legacy := `{"risk":0.55,"uncertainty":0.8,"novelty":0.25}`
	var decoded RecommendationFactors
	if err := json.Unmarshal([]byte(legacy), &decoded); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"risk", "uncertainty", "novelty"} {
		if !strings.Contains(string(roundTrip), `"`+field+`"`) {
			t.Fatalf("legacy factor %s was not preserved: %s", field, roundTrip)
		}
	}

	canonical := RecommendationFactors{
		DeltaUncertainty: 0.8, BusinessRisk: 1, DiffImpact: 0.5,
		SmoothedFailurePrior: 0.25, ExpectedCost: 1,
	}
	roundTrip, err = json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"delta_uncertainty", "business_risk", "diff_impact", "smoothed_failure_prior", "expected_cost"} {
		if !strings.Contains(string(roundTrip), `"`+field+`"`) {
			t.Fatalf("canonical factor %s is missing: %s", field, roundTrip)
		}
	}
}
