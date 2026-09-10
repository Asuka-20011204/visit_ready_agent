package agent

import (
	"testing"

	"visitready/internal/domain"
)

// P4: common non-numeric Chinese answers must be preserved, not dropped.
func TestClarificationSlotValuesPreservesFreeTextAnswers(t *testing.T) {
	cases := []struct {
		category string
		answer   string
		slot     string
		want     string
	}{
		{"frequency", "偶尔", "frequency", "偶尔"},
		{"duration", "半天", "duration", "半天"},
		{"timeline", "上个月开始的", "onset", "上个月开始的"},
		{"frequency", "每周两三次", "frequency", "每周两三次"},
	}
	for _, tc := range cases {
		values := clarificationSlotValues(tc.category, tc.answer)
		if got := values[tc.slot]; got == "" {
			t.Errorf("%s(%q) dropped the answer: %v", tc.category, tc.answer, values)
		}
	}
}

// P10: a single answer answering two questions must not be written whole into
// each free-text slot.
func TestMultiQuestionAnswerDoesNotContaminateSlots(t *testing.T) {
	turn := domain.ClarificationTurn{
		Questions: []domain.Question{
			{Text: "头痛大概多久发作一次？", Category: "frequency"},
			{Text: "头痛目前对日常活动有什么影响？", Category: "severity"},
		},
		Answer: "偶尔疼，影响不大",
	}
	profiles := []domain.SymptomProfile{{Name: "头痛"}}
	merged := mergeClarificationSlots(profiles, []domain.ClarificationTurn{turn})
	if len(merged) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(merged))
	}
	if got := merged[0].Severity; got != "影响不大" {
		t.Errorf("Severity = %q, want %q", got, "影响不大")
	}
	if containsAnyPhrase(merged[0].Frequency, "影响") {
		t.Errorf("Frequency absorbed severity clause: %q", merged[0].Frequency)
	}
}

// P9: a relieving factor misassigned to the trigger slot must not survive
// grounding, since it points the wrong direction.
func TestGroundedProfilesRejectMisassignedTrigger(t *testing.T) {
	input := "最近手腕活动后疼痛明显，休息后缓解。"
	profiles := []domain.SymptomProfile{{
		Name:        "手腕疼痛",
		Trigger:     "休息后缓解",
		SourceQuote: "手腕活动后疼痛明显，休息后缓解",
	}}
	grounded := groundedSymptomProfiles(input, profiles, nil)
	if len(grounded) != 1 {
		t.Fatalf("expected 1 grounded profile, got %d", len(grounded))
	}
	if grounded[0].Trigger != "" {
		t.Errorf("misassigned relieving factor survived in trigger slot: %q", grounded[0].Trigger)
	}
}
