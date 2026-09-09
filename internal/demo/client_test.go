package demo_test

import (
	"context"
	"strings"
	"testing"

	"visitready/internal/demo"
	"visitready/internal/domain"
	"visitready/internal/guard"
)

func TestClientUsesExactInputQuotesAndClarifiesOnce(t *testing.T) {
	client := demo.Client{}
	first, err := client.Extract(context.Background(), "三天前开始咳嗽。晚上更明显。目前没有服药。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(first.Facts) != 3 || first.Facts[0].SourceQuote != "三天前开始咳嗽" || len(first.ClarificationQuestions) != 1 {
		t.Fatalf("first extraction = %#v", first)
	}

	second, err := client.Extract(context.Background(), "三天前开始咳嗽。晚上更明显。目前没有服药。\n补充信息：体温37.2度")
	if err != nil {
		t.Fatalf("second Extract() error = %v", err)
	}
	if len(second.ClarificationQuestions) != 0 || len(second.SearchQueries) != 1 {
		t.Fatalf("second extraction = %#v", second)
	}
}

func TestClientDeduplicatesFactsAndBuildsSymptomProfiles(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "我最近有心悸，手脚发汗，晚上睡觉发热的问题。我最近有心悸，手脚发汗，晚上睡觉发热的问题。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want one deduplicated fact", result.Facts)
	}
	if len(result.SymptomProfiles) != 3 {
		t.Fatalf("symptom profiles = %#v", result.SymptomProfiles)
	}
	if result.SymptomProfiles[0].Name != "心悸" || result.SymptomProfiles[2].Pattern != "晚上睡觉" {
		t.Fatalf("symptom profiles = %#v", result.SymptomProfiles)
	}
	if len(result.ClarificationQuestions) < 3 {
		t.Fatalf("clarification questions = %#v", result.ClarificationQuestions)
	}
	if len(result.ClarificationPrompts) != len(result.ClarificationQuestions) {
		t.Fatalf("clarification prompts = %#v", result.ClarificationPrompts)
	}
	for _, prompt := range result.ClarificationPrompts {
		if prompt.Reason == "" || prompt.Priority == "" || prompt.Category == "" {
			t.Fatalf("clarification prompt lacks guidance: %#v", prompt)
		}
	}
}

func TestClientExtractsTimelineFrequencyTriggersAndAssociatedSymptoms(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：心悸从三天前开始，每晚两次，每次约十分钟，运动后更明显，伴有头晕，没有胸痛。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(result.Timeline) == 0 || result.Timeline[0].TimeLabel != "三天前" {
		t.Fatalf("timeline = %#v", result.Timeline)
	}
	profile := findProfile(t, result.SymptomProfiles, "心悸")
	if profile.Frequency != "每晚两次" || profile.Duration != "每次约十分钟" || profile.Trigger != "运动后" {
		t.Fatalf("profile = %#v", profile)
	}
	if len(profile.AssociatedSymptoms) != 1 || profile.AssociatedSymptoms[0] != "头晕" {
		t.Fatalf("associated symptoms = %#v", profile.AssociatedSymptoms)
	}
	if len(result.RiskSignals) != 0 {
		t.Fatalf("negated chest pain must not be a risk signal: %#v", result.RiskSignals)
	}
}

func TestClientDoesNotTreatFrequencyWindowAsSymptomOnset(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：心悸一天两三次，每次约五分钟。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	profile := findProfile(t, result.SymptomProfiles, "心悸")
	if profile.Onset != "" || profile.Frequency != "一天两三次" {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestClientAppliesClarificationAnswersToTheNamedSymptom(t *testing.T) {
	client := demo.Client{}
	input := "我最近有心悸，手脚发汗，晚上睡觉发热的问题。\n" +
		"第1轮追问：\n" +
		"1. 心悸一天或一周大约发作几次，最近频率是否变化？\n" +
		"2. 心悸通常在什么情况下出现，怎样做会缓解？\n" +
		"用户回答：最近一周大约每天两三次，多在安静坐着或准备睡觉时出现，深呼吸后会缓解一些。"

	result, err := client.Extract(context.Background(), input)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	profile := findProfile(t, result.SymptomProfiles, "心悸")
	if profile.Frequency != "每天两三次" || profile.Trigger == "" || profile.RelievingFactors != "深呼吸后会缓解" {
		t.Fatalf("clarification answer was not applied to heart profile: %#v", profile)
	}
	if len(profile.EvidenceQuotes) != 1 || profile.EvidenceQuotes[0] != "最近一周大约每天两三次，多在安静坐着或准备睡觉时出现，深呼吸后会缓解一些。" {
		t.Fatalf("clarification evidence was not retained: %#v", profile.EvidenceQuotes)
	}
	for _, prompt := range result.ClarificationPrompts {
		if prompt.Category == "frequency" || prompt.Category == "trigger" {
			t.Fatalf("answered detail was asked again: %#v", result.ClarificationPrompts)
		}
	}
}

func TestClientClassifiesMissingStartTimeAsTimelineQuestion(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "我有心悸，每次约五分钟，每周两次，没有胸痛、呼吸困难或晕倒，希望准备就诊时与医生沟通。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	found := false
	for _, prompt := range result.ClarificationPrompts {
		if strings.Contains(prompt.Text, "开始时间") {
			found = true
			if prompt.Category != "timeline" {
				t.Fatalf("start-time prompt category = %q, want timeline", prompt.Category)
			}
		}
	}
	if !found {
		t.Fatalf("missing start-time clarification prompt: %#v", result.ClarificationPrompts)
	}
}

func TestClientSharesCoordinatedTimingDetailsAcrossSymptoms(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "我最近三周偶尔心悸和手心出汗，多在晚上发生，每次大约十分钟，目前没有胸痛、呼吸困难或晕厥。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}

	heart := findProfile(t, result.SymptomProfiles, "心悸")
	sweating := findProfile(t, result.SymptomProfiles, "手心出汗")
	if heart.Onset != "最近三周" || heart.Duration != "每次大约十分钟" || heart.Frequency != "偶尔" {
		t.Fatalf("heart profile missed shared details: %#v", heart)
	}
	if sweating.Onset != "最近三周" || sweating.Duration != "每次大约十分钟" || sweating.Frequency != "偶尔" {
		t.Fatalf("sweating profile missed shared details: %#v", sweating)
	}
	for _, prompt := range result.ClarificationPrompts {
		if prompt.Category == "timeline" || prompt.Category == "duration" || prompt.Category == "frequency" {
			t.Fatalf("known shared detail was asked again: %#v", result.ClarificationPrompts)
		}
	}
}

func TestClientKeepsProactiveDetailsAcrossClarificationRounds(t *testing.T) {
	client := demo.Client{}
	input := "我最近有心悸，手脚发汗，晚上睡觉发热的问题。\n" +
		"第1轮追问：\n" +
		"1. 心悸的开始时间是什么时候？\n" +
		"用户回答：心悸一天两三次，每次约五分钟。\n" +
		"第2轮追问：\n" +
		"1. 手脚发汗的开始时间是什么时候？\n" +
		"用户回答：这些情况近一周开始，心悸安静时出现，深呼吸后会缓解。"
	result, err := client.Extract(context.Background(), input)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	profile := findProfile(t, result.SymptomProfiles, "心悸")
	if profile.Onset != "近一周" || profile.Duration != "每次约五分钟" || profile.Frequency != "一天两三次" || profile.Trigger != "安静时" || profile.RelievingFactors != "深呼吸后会缓解" {
		t.Fatalf("profile lost proactive clarification details: %#v", profile)
	}
}

func TestClientDoesNotAttributeAnAnswerToAnUnaskedProfileField(t *testing.T) {
	client := demo.Client{}
	input := "我最近有心悸。\n" +
		"第1轮追问：\n" +
		"1. 心悸一天或一周大约发作几次，最近频率是否变化？\n" +
		"用户回答：每天两三次，多在安静坐着时出现。"

	result, err := client.Extract(context.Background(), input)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	profile := findProfile(t, result.SymptomProfiles, "心悸")
	if profile.Frequency != "每天两三次" || profile.Trigger != "" {
		t.Fatalf("answer crossed clarification categories: %#v", profile)
	}
	if !containsQuestionCategory(result.ClarificationPrompts, "trigger") {
		t.Fatalf("unanswered trigger was not asked next: %#v", result.ClarificationPrompts)
	}
}

func containsQuestionCategory(questions []domain.Question, category string) bool {
	for _, question := range questions {
		if question.Category == category {
			return true
		}
	}
	return false
}

func TestClientFlagsExplicitRedFlagsWithoutDiagnosing(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：现在心悸，并且胸痛、呼吸困难，差点晕倒。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(result.RiskSignals) == 0 || result.RiskSignals[0].Priority != domain.PriorityUrgent {
		t.Fatalf("risk signals = %#v", result.RiskSignals)
	}
	for _, signal := range result.RiskSignals {
		combined := signal.Title + signal.Guidance
		if strings.Contains(combined, "诊断") || strings.Contains(combined, "患有") {
			t.Fatalf("risk signal crosses medical boundary: %#v", signal)
		}
	}
}

func TestClientScopesNegationToEachSymptomMention(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：之前没有心悸。现在出现心悸。没有胸痛，现在也没有气短。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	profile := findProfile(t, result.SymptomProfiles, "心悸")
	if profile.SourceQuote != "现在出现心悸" {
		t.Fatalf("positive source quote = %q", profile.SourceQuote)
	}
	if len(result.RiskSignals) != 0 {
		t.Fatalf("negated risks = %#v", result.RiskSignals)
	}
}

func TestClientUsesPositiveQuoteForRedFlagAfterNegation(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：之前没有胸痛。现在胸痛。")
	if err != nil || len(result.RiskSignals) != 1 {
		t.Fatalf("Extract() = %#v, %v", result, err)
	}
	if result.RiskSignals[0].SourceQuote != "现在胸痛" {
		t.Fatalf("risk source quote = %q", result.RiskSignals[0].SourceQuote)
	}
}

func TestClientDoesNotShareDetailsAcrossSymptomsInOneSentence(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：我有心悸，三天前开始发热，晚上睡觉时更明显。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	heart := findProfile(t, result.SymptomProfiles, "心悸")
	fever := findProfile(t, result.SymptomProfiles, "发热")
	if heart.Onset != "" || heart.Pattern != "" {
		t.Fatalf("heart profile inherited fever details: %#v", heart)
	}
	if fever.Onset != "三天前" || fever.Pattern != "晚上睡觉" {
		t.Fatalf("fever profile = %#v", fever)
	}

	withListSeparator, err := client.Extract(context.Background(), "补充信息：我有心悸、三天前开始发热。")
	if err != nil {
		t.Fatalf("Extract() with list separator error = %v", err)
	}
	heart = findProfile(t, withListSeparator.SymptomProfiles, "心悸")
	fever = findProfile(t, withListSeparator.SymptomProfiles, "发热")
	if heart.Onset != "" || fever.Onset != "三天前" {
		t.Fatalf("list-separated profiles: heart=%#v fever=%#v", heart, fever)
	}
}

func TestClientUnderstandsLongNegationScope(t *testing.T) {
	client := demo.Client{}
	result, err := client.Extract(context.Background(), "补充信息：心悸，但没有感到特别明显的胸痛，也未见呼吸困难。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(result.RiskSignals) != 0 {
		t.Fatalf("negated risks = %#v", result.RiskSignals)
	}
}

func TestClientKeepsUrgentRiskWhenOtherQuestionInputsAreEmpty(t *testing.T) {
	client := demo.Client{}
	risk := domain.RiskSignal{Priority: domain.PriorityUrgent, Evidence: "胸痛、呼吸困难", Guidance: "立即联系当地急救服务"}
	result, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{RiskSignals: []domain.RiskSignal{risk}})
	if err != nil {
		t.Fatalf("GenerateQuestions() error = %v", err)
	}
	if len(result.Questions) == 0 || result.Questions[0].Priority != domain.PriorityUrgent {
		t.Fatalf("questions = %#v", result.Questions)
	}
	if len(result.ActionItems) == 0 || result.ActionItems[0].Priority != domain.PriorityUrgent {
		t.Fatalf("action items = %#v", result.ActionItems)
	}
}

func TestClientGeneratesPrioritizedQuestionsWithReasons(t *testing.T) {
	client := demo.Client{}
	result, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{
		VisitGoal: "准备说明心悸",
		SymptomProfiles: []domain.SymptomProfile{{
			Name: "心悸",
		}},
		MissingFields: []string{"心悸的持续时间", "是否伴随胸痛、气短或晕厥"},
	})
	if err != nil {
		t.Fatalf("GenerateQuestions() error = %v", err)
	}
	if len(result.Questions) < 3 || len(result.Questions) > 6 {
		t.Fatalf("questions = %#v", result.Questions)
	}
	if result.Questions[0].Priority != domain.PriorityUrgent || result.Questions[0].Reason == "" {
		t.Fatalf("first question = %#v", result.Questions[0])
	}
	seenDuration := false
	for _, question := range result.Questions {
		if strings.Contains(question.Text, "持续") {
			seenDuration = true
		}
		if question.Reason == "" || question.Priority == "" {
			t.Fatalf("question lacks explanation or priority: %#v", question)
		}
	}
	if !seenDuration {
		t.Fatalf("questions do not respond to missing duration: %#v", result.Questions)
	}
	if len(result.ActionItems) < 2 {
		t.Fatalf("action items = %#v", result.ActionItems)
	}
	for _, action := range result.ActionItems {
		if action.Title == "" || action.Detail == "" || action.Reason == "" {
			t.Fatalf("incomplete action item: %#v", action)
		}
	}
}

func TestClientGeneratedQuestionsPassTheOutputGuard(t *testing.T) {
	client := demo.Client{}
	result, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{
		SymptomProfiles: []domain.SymptomProfile{{Name: "发热"}},
		MissingFields:   []string{"发热时测量的体温", "发热的开始时间"},
	})
	if err != nil {
		t.Fatalf("GenerateQuestions() error = %v", err)
	}
	for _, question := range result.Questions {
		if !guard.IsSafeQuestion(question.Text) {
			t.Fatalf("generated question would be removed by output guard: %#v", question)
		}
	}
}

func findProfile(t *testing.T, profiles []domain.SymptomProfile, name string) domain.SymptomProfile {
	t.Helper()
	for _, profile := range profiles {
		if profile.Name == name {
			return profile
		}
	}
	t.Fatalf("profile %q not found in %#v", name, profiles)
	return domain.SymptomProfile{}
}

func TestClientQuestionsAreWithinMedicalBoundary(t *testing.T) {
	client := demo.Client{}
	result, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{Sources: []domain.Source{{URL: "https://who.int/a"}}})
	if err != nil || len(result.Questions) != 3 {
		t.Fatalf("GenerateQuestions() = %#v, %v", result, err)
	}
	if result.Questions[1].SourceURL != "https://who.int/a" {
		t.Fatalf("source URL = %q", result.Questions[1].SourceURL)
	}
}

func TestClientCoversSupportedCategoriesAndTopics(t *testing.T) {
	client := demo.Client{}
	inputs := []string{
		"补充信息：每天服用一种药物。已知青霉素过敏。做过CT检查。以前有相关病史。",
		"补充信息：出现皮疹并且瘙痒。",
		"补充信息：饭后有一种说不清楚的不舒服。",
	}
	for _, input := range inputs {
		result, err := client.Extract(context.Background(), input)
		if err != nil || len(result.Facts) == 0 || len(result.SearchQueries) == 0 {
			t.Fatalf("Extract(%q) = %#v, %v", input, result, err)
		}
	}
}

func TestClarificationAttributesTriggersToTheNamedSymptom(t *testing.T) {
	client := demo.Client{}
	input := "我最近有心悸和夜间发热，希望准备就诊沟通。\n" +
		"第1轮追问：\n1. 心悸通常在什么情况下出现？\n用户回答：发热感主要在夜间。心悸安静时也会出现，深呼吸后会缓解。"

	result, err := client.Extract(context.Background(), input)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	heart := findProfile(t, result.SymptomProfiles, "心悸")
	if heart.Trigger != "安静时" || heart.RelievingFactors != "深呼吸后会缓解" {
		t.Fatalf("heart trigger attribution = %#v", heart)
	}
	if heart.Trigger == "夜间" {
		t.Fatal("fever timing was attributed to palpitation")
	}
}

func TestClarificationAttributesDifferentOnsetsToNamedSymptoms(t *testing.T) {
	client := demo.Client{}
	input := "我最近有心悸和头痛，希望准备就诊沟通。\n" +
		"第1轮追问：\n1. 心悸和头痛分别什么时候开始？\n用户回答：心悸近一周开始，头痛两天前开始。"

	result, err := client.Extract(context.Background(), input)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	heart := findProfile(t, result.SymptomProfiles, "心悸")
	headache := findProfile(t, result.SymptomProfiles, "头痛")
	if heart.Onset != "近一周" || headache.Onset != "两天前" {
		t.Fatalf("onsets were crossed: heart=%#v headache=%#v", heart, headache)
	}
}

func TestExtractClassifiesExplicitDurationWithoutInventingFrequency(t *testing.T) {
	result, err := (demo.Client{}).Extract(context.Background(), "最近头痛，每次持续两小时，希望整理后向医生说明。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	profile := findProfile(t, result.SymptomProfiles, "头痛")
	if profile.Duration != "每次持续两小时" || profile.Frequency == "持续" {
		t.Fatalf("duration/frequency = %#v", profile)
	}
}

func TestExtractUsesClarificationAnswersAsFactsWithoutProtocolLines(t *testing.T) {
	input := "最近反复心悸，希望准备就诊沟通。\n第1轮追问：\n1. 心悸每次持续多久？\n用户回答：心悸每次约五分钟。"
	result, err := (demo.Client{}).Extract(context.Background(), input)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	foundAnswer := false
	for _, fact := range result.Facts {
		if fact.SourceQuote == "心悸每次约五分钟" {
			foundAnswer = true
		}
		if strings.Contains(fact.SourceQuote, "轮追问") || strings.Contains(fact.SourceQuote, "用户回答") {
			t.Fatalf("protocol line leaked into fact: %#v", fact)
		}
	}
	if !foundAnswer {
		t.Fatalf("clarification answer missing from facts: %#v", result.Facts)
	}
}
