package guard_test

import (
	"strings"
	"testing"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

// These cases specify escalation routing, not a diagnosis or a guarantee of safety.
func TestDetectEmergencySignalsLocalScopeRegression(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"P1_last_week_resolved", "上周胸痛，现在已经不疼了", false},
		{"P1_last_month_resolved", "一个月前胸痛过一次，后来自己好了", false},
		{"P2_chest_tightness_cold_sweat", "我胸口发闷，还出了很多冷汗", true},
		{"P2_left_chest_persistent_pain", "我左胸疼痛，一直没缓解", true},
		{"current_without_now_keyword", "我胸痛并且呼吸困难", true},
		{"chest_pain_variant", "我胸部疼痛，一直没有缓解", true},
		{"chest_pressure_with_sweat", "我胸口有压迫感，还在冒冷汗", true},
		{"tightness_with_breathlessness", "我胸闷，气喘不过来", true},
		{"breathlessness_variant", "我呼吸很困难", true},
		{"air_hunger_variant", "我透不过气", true},
		{"speech_variant", "突然讲话含糊不清", true},
		{"unilateral_weakness_variant", "突然左半边身体使不上劲", true},
		{"negated_synonym_cluster", "没有胸部疼痛，也没有呼吸很困难", false},
		{"negated_tightness_and_sweat", "没有胸口发闷，也没有出冷汗", false},
		{"hypothetical_synonym_cluster", "如果胸口发闷并且冒冷汗怎么办？", false},
		{"hypothetical_across_clause", "如果胸痛，还呼吸困难，该怎么办？", false},
		{"historical_synonym_cluster", "上个月胸口发闷并冒冷汗，后来自己好了", false},
		{"historical_breathlessness", "两周前呼吸困难，后来恢复了", false},
		{"history_then_explicit_recurrence", "上周胸痛已经好了，今天又胸痛，一直没缓解", true},
		{"history_then_elliptical_recurrence", "上周胸痛，现在又开始疼了", true},
		{"history_then_same_clause_recurrence", "以前胸痛后来好了现在又胸痛", true},
		{"history_then_synonym_recurrence", "一个月前胸痛过一次，后来自己好了，今天左胸疼痛一直没缓解", true},
		{"history_then_new_current_symptom", "上周胸痛已经好了，我现在喘不过气", true},
		{"historical_onset_still_persistent", "上周开始胸痛，一直没缓解", true},
		{"hypothetical_then_current", "如果胸痛该怎么办？不过我现在呼吸困难", true},
		{"negated_then_current_synonym", "我没有胸痛，但是我透不过气", true},
		{"unrelated_negation_with_synonym", "没有咳嗽但左胸疼痛一直没缓解", true},
		{"other_symptom_resolved", "我左胸疼痛，头痛已经好了", true},
		{"other_symptom_resolved_same_clause", "我胸痛而头痛已经缓解", true},
		{"other_symptom_resolution_not_anaphoric", "我胸痛，皮疹不再出现了", true},
		{"other_symptom_negation_not_correction", "我胸痛，不是头痛", true},
		{"resolved_then_recurred", "胸痛已经缓解，但现在又胸痛", true},
		{"recurrence_then_resolved", "以前胸痛，现在又胸痛，但现在已经好了", false},
		{"negated_resolution", "我胸痛，并没有已经缓解", true},
		{"historical_ordinary_symptom_not_recurrence", "上周胸痛已经好了，现在头痛又发作了", false},
		{"chronic_isolated_finger_numbness", "手指麻木半年了，打字后明显", false},
		{"positional_arm_numbness", "睡觉压着胳膊后手臂麻木，活动后好了", false},
		{"isolated_leg_numbness", "久坐以后腿麻，走动就好了", false},
		{"numbness_with_focal_red_flag", "突然左半边身体麻木无力，讲话含糊不清", true},
		{"negated_focal_red_flag", "没有单侧无力，也没有讲话含糊不清", false},
		{"historical_focal_red_flag", "一年前曾经讲话含糊不清，后来好了", false},
		{"hypothetical_focal_red_flag", "假如突然左半边身体使不上劲应该怎么办？", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signals := guard.DetectEmergencySignals(tt.input)
			if got := len(signals) > 0; got != tt.want {
				t.Fatalf("DetectEmergencySignals(%q) urgent=%v, want %v; signals=%#v", tt.input, got, tt.want, signals)
			}
			for _, signal := range signals {
				if signal.Priority != domain.PriorityUrgent || signal.Guidance != guard.EmergencyEscalationMessage {
					t.Errorf("unexpected escalation contract: %#v", signal)
				}
				if signal.SourceQuote == "" || !strings.Contains(tt.input, signal.SourceQuote) {
					t.Errorf("source quote must be grounded in the actual input: %#v", signal)
				}
				if guard.ContainsMedicalOverreach(signal.Title + signal.Guidance) {
					t.Errorf("routing guidance must not assert a diagnosis: %#v", signal)
				}
			}
		})
	}
}
