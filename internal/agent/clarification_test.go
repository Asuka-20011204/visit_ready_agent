package agent

import (
	"regexp"
	"strings"
	"testing"

	"visitready/internal/domain"
)

// Every category the extraction can classify a missing field with must have a
// deterministic write-back path; otherwise answers are asked but never stored.
func TestEveryMissingFieldCategoryIsMergeable(t *testing.T) {
	cases := []struct {
		field    string
		category string
	}{
		{"左手腕每次持续多长时间？", "duration"},
		{"左手腕发作频率", "frequency"},
		{"左手腕开始时间", "onset"},
		{"左手腕诱因或缓解因素", "trigger"},
		{"左手腕严重程度", "severity"},
		{"左手腕伴随症状", "associated"},
	}
	for _, sample := range cases {
		question := clarificationPromptForMissingField(sample.field, sample.category, domain.Session{SymptomProfiles: []domain.SymptomProfile{{Name: "左手腕"}}})
		if question.Category == "" {
			t.Fatalf("field %q produced no question", sample.field)
		}
		if _, ok := clarificationSlotSpecFor(question.Category); !ok {
			t.Fatalf("category %q (from field %q) has no mergeable slot", question.Category, sample.field)
		}
	}
	globalCases := []struct {
		field    string
		category string
	}{
		{"是否使用止痛药、外用药", "medication"},
		{"是否有药物过敏史", "allergy"},
		{"既往糖尿病/甲状腺/关节炎/外伤史", "history"},
		{"是否伴随胸痛、气短或晕厥", "safety"},
	}
	for _, sample := range globalCases {
		question := clarificationPromptForMissingField(sample.field, sample.category, domain.Session{})
		if question.Text == "" || question.Category != sample.category {
			t.Fatalf("global field %q produced %#v", sample.field, question)
		}
		if _, ok := clarificationSlotSpecFor(question.Category); !ok {
			t.Fatalf("category %q (from field %q) has no mergeable slot", question.Category, sample.field)
		}
	}
}

func TestClarificationSlotValueExtractsTypedValues(t *testing.T) {
	cases := []struct {
		category, answer, field, value string
	}{
		{"duration", "每次大概持续10到20分钟", "duration", "每次大概持续10到20分钟"},
		{"frequency", "一周四五次，最近更频繁", "frequency", "一周四五次"},
		{"timeline", "大约三周前开始", "onset", "三周前"},
		{"severity", "用鼠标超过半小时就明显发酸", "severity", "用鼠标超过半小时就明显发酸"},
	}
	for _, sample := range cases {
		field, value := clarificationSlotValue(sample.category, sample.answer)
		if field != sample.field || value != sample.value {
			t.Fatalf("clarificationSlotValue(%q, %q) = (%q, %q), want (%q, %q)", sample.category, sample.answer, field, value, sample.field, sample.value)
		}
	}
	if _, value := clarificationSlotValue("duration", "不清楚，没注意过"); value != "" {
		t.Fatalf("uncertain answer should not produce a value, got %q", value)
	}
}

func TestTriggerAnswerSplitsTriggerAndRelieving(t *testing.T) {
	values := clarificationSlotValues("trigger", "用鼠标的时候最明显，停下来休息一会儿会好转，热敷试过一次没什么明显差别")
	if values["trigger"] != "用鼠标的时候最明显" {
		t.Fatalf("trigger = %q", values["trigger"])
	}
	if !strings.Contains(values["relieving"], "休息") || !strings.Contains(values["relieving"], "热敷") {
		t.Fatalf("relieving = %q, want rest and heat-pack clauses", values["relieving"])
	}
}

func TestMergeClarificationSlotsWritesFreeTextAndTightEvidence(t *testing.T) {
	profiles := []domain.SymptomProfile{{Name: "左手腕", SourceQuote: "这三周左手腕偶尔会酸胀"}}
	turns := []domain.ClarificationTurn{{
		Questions: []domain.Question{{Text: "左手腕在什么情况下更明显，怎样会缓解？", Category: "trigger"}},
		Answer:    "用鼠标的时候最明显，停下来休息一会儿会好转",
	}}
	merged := mergeClarificationSlots(profiles, turns)
	if len(merged) != 1 {
		t.Fatalf("profiles = %d", len(merged))
	}
	if merged[0].Trigger != "用鼠标的时候最明显" {
		t.Fatalf("trigger = %q", merged[0].Trigger)
	}
	if !strings.Contains(merged[0].RelievingFactors, "休息") {
		t.Fatalf("relieving = %q", merged[0].RelievingFactors)
	}
	for _, quote := range merged[0].EvidenceQuotes {
		if strings.Contains(quote, "，") {
			t.Fatalf("evidence quote %q should be a single clause, not the whole answer", quote)
		}
	}
}

func TestMergeSymptomProfileKeepsPriorUnlessCorrection(t *testing.T) {
	prior := domain.SymptomProfile{Name: "心悸", Duration: "五分钟", Onset: "最近", Frequency: "每天两次"}
	incoming := domain.SymptomProfile{Name: "心悸", Duration: "三周", Onset: "偶尔", Frequency: "最近几天更明显", SourceQuote: "最近心悸"}

	merged := mergeSymptomProfile(prior, incoming, nil)
	if merged.Duration != "五分钟" || merged.Onset != "最近" || merged.Frequency != "每天两次" {
		t.Fatalf("non-correction merge overwrote prior slots: %#v", merged)
	}

	corrected := mergeSymptomProfile(prior, incoming, map[string]bool{"duration": true})
	if corrected.Duration != "三周" {
		t.Fatalf("correction merge kept prior duration: %#v", corrected)
	}
	if corrected.Onset != "最近" || corrected.Frequency != "每天两次" {
		t.Fatalf("scoped correction overwrote unrelated slots: %#v", corrected)
	}
}

func TestTypedProfileValueBlanksMismatchedSlots(t *testing.T) {
	cases := []struct {
		value   string
		pattern *regexp.Regexp
		want    string
	}{
		{"十分钟", clarificationDurationValuePattern, "十分钟"},
		{"三周", clarificationDurationValuePattern, ""},
		{"半天", clarificationDurationValuePattern, "半天"},
		{"一周四五次", frequencyValidationPattern, "一周四五次"},
		{"偶尔", frequencyValidationPattern, "偶尔"},
		{"三周前", onsetValidationPattern, "三周前"},
		{"近一周", onsetValidationPattern, "近一周"},
		{"上个月开始的", onsetValidationPattern, "上个月"},
		{"最近几天更明显", onsetValidationPattern, "最近几天"},
		{"一天两三次", onsetValidationPattern, ""},
	}
	for _, sample := range cases {
		got := typedProfileValue(sample.value, sample.pattern)
		if got != sample.want {
			t.Fatalf("typedProfileValue(%q, %s) = %q, want %q", sample.value, sample.pattern, got, sample.want)
		}
	}
}

func TestGlobalsRecordDenialsFromExtraction(t *testing.T) {
	session := domain.Session{}
	extraction := domain.Extraction{
		DeniedConditions: []domain.ExtractedGlobal{
			{Value: "没有糖尿病", Category: "history", SourceQuote: "没有糖尿病"},
			{Value: "没在吃药", Category: "medication", SourceQuote: "没在吃药"},
			{Value: "没有已知过敏", Category: "allergy", SourceQuote: "没有已知过敏"},
		},
	}
	merged := applyGlobals(session, extraction)
	if len(merged.Medications) != 0 {
		t.Fatalf("medications should stay empty for denial, got %#v", merged.Medications)
	}
	if len(merged.DeniedConditions) != 3 {
		t.Fatalf("denials = %#v, want 3 entries", merged.DeniedConditions)
	}
	if !missingFieldCovered("是否使用止痛药、外用药", "medication", nil, merged) {
		t.Fatalf("medication field should be covered by denial")
	}
	if !missingFieldCovered("是否有药物过敏史", "allergy", nil, merged) {
		t.Fatalf("allergy field should be covered by denial")
	}
	if !missingFieldCovered("既往糖尿病/甲状腺/关节炎/外伤史", "history", nil, merged) {
		t.Fatalf("history field should be covered by denial")
	}
}

func TestGlobalsCapturePositiveMedication(t *testing.T) {
	session := domain.Session{}
	extraction := domain.Extraction{
		Medications: []domain.ExtractedGlobal{{Value: "在用布洛芬", SourceQuote: "在用布洛芬"}},
	}
	merged := applyGlobals(session, extraction)
	if len(merged.Medications) != 1 || !strings.Contains(merged.Medications[0], "布洛芬") {
		t.Fatalf("medications = %#v", merged.Medications)
	}
	if len(merged.DeniedConditions) != 0 {
		t.Fatalf("denials = %#v, want none", merged.DeniedConditions)
	}
	if !missingFieldCovered("是否使用止痛药、外用药", "medication", nil, merged) {
		t.Fatalf("medication field should be covered")
	}
}

func TestGlobalsCaptureMeasurementsAndTests(t *testing.T) {
	session := domain.Session{}
	extraction := domain.Extraction{
		Measurements: []domain.ExtractedGlobal{{Value: "体温38度", SourceQuote: "体温38度"}},
		Tests:        []domain.ExtractedGlobal{{Value: "血常规", SourceQuote: "做过血常规"}},
	}
	merged := applyGlobals(session, extraction)
	if len(merged.Measurements) != 1 || !strings.Contains(merged.Measurements[0], "体温38度") {
		t.Fatalf("measurements = %#v", merged.Measurements)
	}
	if len(merged.Tests) != 1 || !strings.Contains(merged.Tests[0], "血常规") {
		t.Fatalf("tests = %#v", merged.Tests)
	}
	if !missingFieldCovered("体温情况", "measurement", nil, merged) {
		t.Fatalf("measurement field should be covered")
	}
	if !missingFieldCovered("检查结果", "test", nil, merged) {
		t.Fatalf("test field should be covered")
	}
}

// Coverage must follow the category, not keywords in the field text.
func TestMissingFieldCoveredUsesCategoryNotKeywords(t *testing.T) {
	session := domain.Session{Medications: []string{"在用布洛芬"}}
	// The field text contains no medication keyword; only the category says so.
	if !missingFieldCovered("用户正在服用的药物名称", "medication", nil, session) {
		t.Fatalf("category-based coverage failed")
	}
	if missingFieldCovered("用户正在服用的药物名称", "history", nil, session) {
		t.Fatalf("wrong category should not be covered")
	}
	if !missingFieldCovered("既往病史相关情况", "history", nil, domain.Session{DeniedConditions: []string{"否认既往疾病：没有糖尿病"}}) {
		t.Fatalf("denial-based history coverage failed")
	}
}

func TestConfirmedSummaryGroupsDetailsPerProfile(t *testing.T) {
	session := domain.Session{SymptomProfiles: []domain.SymptomProfile{{
		Name: "左手腕酸胀", Onset: "三周前", Duration: "三周", Frequency: "偶尔", Severity: "最近几天更明显",
	}}}
	summary, _, _ := deriveConversationSignals(session)
	if len(summary.Confirmed) != 1 {
		t.Fatalf("confirmed = %#v, want one grouped entry", summary.Confirmed)
	}
	entry := summary.Confirmed[0]
	if !strings.HasPrefix(entry, "左手腕酸胀：") || strings.Count(entry, "左手腕酸胀") != 1 {
		t.Fatalf("confirmed entry %q should name the profile once", entry)
	}
	if !strings.Contains(entry, "三周前") || !strings.Contains(entry, "最近几天更明显") {
		t.Fatalf("confirmed entry %q missing details", entry)
	}
}
