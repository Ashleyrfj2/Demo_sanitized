package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/example/qa-platform/backend/internal/model"
)

func TestTrajectoryFixturesConformToGoValidation(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	pattern := filepath.Join(filepath.Dir(file), "../../../testdata/trajectories/*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		t.Fatalf("load contract fixtures: paths=%v err=%v", paths, err)
	}
	validator := New()
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var event model.ValidationEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if err := validator.Validate(event); err != nil {
				t.Fatalf("fixture rejected by Go validation: %v", err)
			}
		})
	}
}
