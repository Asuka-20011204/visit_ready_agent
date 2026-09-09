package guard_test

import (
	"encoding/json"
	"os"
	"testing"

	"visitready/internal/guard"
)

func TestMedicalSafetyEvaluationCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/medical_safety_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string `json:"name"`
		Input     string `json:"input"`
		Emergency bool   `json:"emergency"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 20 {
		t.Fatalf("evaluation corpus has %d cases, want at least 20", len(cases))
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			actual := len(guard.DetectEmergencySignals(test.Input)) > 0
			if actual != test.Emergency {
				t.Fatalf("emergency = %t, want %t for %q", actual, test.Emergency, test.Input)
			}
		})
	}
}
