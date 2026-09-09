package evaluation

import (
	"context"
	"testing"
	"time"

	"visitready/internal/domain"
)

type fakeRunner struct{ sessions []domain.Session }

func (f *fakeRunner) Start(context.Context, string, bool) (domain.Session, error) {
	item := f.sessions[0]
	f.sessions = f.sessions[1:]
	return item, nil
}

func TestRunScoresFactsGroundingDuplicatesEmergencyAndLatency(t *testing.T) {
	cases := []Case{
		{Name: "normal", Input: "咳嗽三天", ExpectedFacts: []string{"咳嗽", "三天"}},
		{Name: "emergency", Input: "现在胸痛", Emergency: true, ExpectedFacts: nil},
	}
	runner := &fakeRunner{sessions: []domain.Session{
		{Status: domain.StatusWaitingReview, Facts: []domain.Fact{{Content: "咳嗽三天", SourceQuote: "咳嗽三天"}, {Content: "咳嗽三天", SourceQuote: "咳嗽三天"}}},
		{Status: domain.StatusEmergency},
	}}
	report := Run(context.Background(), runner, cases, func() time.Time { return time.Now() })
	if report.CaseCount != 2 || report.EmergencyAccuracy != 1 {
		t.Fatalf("report = %#v", report)
	}
	if report.FactRecall != 1 || report.FactPrecision != 1 || report.GroundingRate != 1 {
		t.Fatalf("fact metrics = precision %.2f recall %.2f grounding %.2f", report.FactPrecision, report.FactRecall, report.GroundingRate)
	}
	if report.DuplicateFacts != 1 || report.P95Milliseconds < 0 {
		t.Fatalf("duplicate/latency metrics = %#v", report)
	}
}

func TestLoadCasesRequiresAtLeastTwentyValidCases(t *testing.T) {
	cases, err := LoadCases("testdata/visit_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 20 {
		t.Fatalf("case count = %d", len(cases))
	}
	for _, item := range cases {
		if item.Name == "" || item.Input == "" {
			t.Fatalf("invalid case: %#v", item)
		}
	}
}
