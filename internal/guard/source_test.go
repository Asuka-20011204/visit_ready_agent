package guard_test

import (
	"strings"
	"testing"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

func TestIsAllowedSourceURL(t *testing.T) {
	allowed := []string{"who.int", "medlineplus.gov"}
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "exact host", url: "https://who.int/news/item", want: true},
		{name: "subdomain", url: "https://www.who.int/news/item", want: true},
		{name: "suffix attack", url: "https://who.int.attacker.example/page", want: false},
		{name: "insecure scheme", url: "http://who.int/page", want: false},
		{name: "credentials", url: "https://who.int@attacker.example/page", want: false},
		{name: "localhost", url: "https://localhost/page", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guard.IsAllowedSourceURL(tt.url, allowed); got != tt.want {
				t.Fatalf("IsAllowedSourceURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestPartitionGroundedFactsRejectsUnsupportedClaims(t *testing.T) {
	input := "三天前开始咳嗽，晚上更明显。没有测体温。"
	facts := []domain.Fact{
		{Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽"},
		{Category: "temperature", Content: "体温39度", SourceQuote: "体温39度"},
	}

	accepted, rejected := guard.PartitionGroundedFacts(input, facts)
	if len(accepted) != 1 || accepted[0].Content != "咳嗽三天" {
		t.Fatalf("accepted = %#v", accepted)
	}
	if len(rejected) != 1 || rejected[0].Content != "体温39度" {
		t.Fatalf("rejected = %#v", rejected)
	}
}

func TestPartitionGroundedFactsRejectsQuoteTruncatedOutOfNegation(t *testing.T) {
	input := "我没有胸痛，但最近一直咳嗽。"
	facts := []domain.Fact{
		{Category: "symptom", Content: "胸痛", SourceQuote: "胸痛"},
		{Category: "symptom", Content: "没有胸痛", SourceQuote: "没有胸痛"},
		{Category: "symptom", Content: "咳嗽", SourceQuote: "一直咳嗽"},
	}
	accepted, rejected := guard.PartitionGroundedFacts(input, facts)
	if len(accepted) != 2 || len(rejected) != 1 || rejected[0].SourceQuote != "胸痛" {
		t.Fatalf("accepted = %#v, rejected = %#v", accepted, rejected)
	}
}

func TestDetectEmergencySignalsDistinguishesCurrentFromNegatedHistoricalAndHypothetical(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "current", input: "我现在胸痛并且呼吸困难，症状还在加重。", want: true},
		{name: "mixed", input: "我没有胸痛，但是现在呼吸困难。", want: true},
		{name: "negated", input: "我现在没有胸痛，也没有呼吸困难。", want: false},
		{name: "historical", input: "我以前有过胸痛，但现在没有胸痛。", want: false},
		{name: "hypothetical", input: "如果以后出现胸痛，我应该准备哪些资料？", want: false},
		{name: "question", input: "心悸的时候是否会出现胸痛或呼吸困难？", want: false},
		{name: "current question", input: "我现在胸痛呼吸困难是否需要去急诊", want: true},
		{name: "resolved", input: "三年前出现过胸痛，已经好了", want: false},
		{name: "chronic ambiguous", input: "活动后气短半年，想整理资料", want: false},
		{name: "active bleeding", input: "我现在一直吐血，情况正在加重", want: true},
		{name: "no obvious trigger", input: "我现在无明显诱因地出现呼吸困难", want: true},
		{name: "unrelated negation", input: "没有咳嗽伴胸痛", want: true},
		{name: "question after symptom", input: "胸痛会不会很危险？", want: false},
		{name: "yesterday resolved", input: "昨天胸痛，今天正常", want: false},
		{name: "historical after current symptom", input: "我现在咳嗽，半年前活动后气短", want: false},
		{name: "other symptom resolved", input: "现在胸痛，之前的呼吸困难已经缓解", want: true},
		{name: "unrelated ordinary symptom resolved", input: "我现在胸痛，头痛已经缓解", want: true},
		{name: "unlisted symptom resolved", input: "我现在胸痛，皮疹已经缓解", want: true},
		{name: "unrelated body part resolved", input: "我现在胸痛，胃已经好了", want: true},
		{name: "not relieved", input: "胸痛一直都没有缓解", want: true},
		{name: "resolved after contrast", input: "我胸痛，但是现在已经好了", want: false},
		{name: "resolved with anaphoric subject", input: "我胸痛，症状已经缓解", want: false},
		{name: "uncertain symptom", input: "不确定是不是胸痛", want: false},
		{name: "corrected symptom", input: "胸痛？不，是胃痛", want: false},
		{name: "corrected symptom with explanation", input: "胸痛？不，我是说胃痛", want: false},
		{name: "historical symptom still active", input: "我以前有胸痛，但是现在还在痛", want: true},
		{name: "unrelated historical symptom still active", input: "我以前有胸痛，但是现在咳嗽还在", want: false},
		{name: "assumption", input: "假设出现胸痛该怎么办", want: false},
		{name: "breathing synonym", input: "我现在喘不过气", want: true},
		{name: "chest pain synonym", input: "我现在胸口剧痛", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signals := guard.DetectEmergencySignals(test.input)
			if got := len(signals) > 0; got != test.want {
				t.Fatalf("DetectEmergencySignals(%q) = %#v", test.input, signals)
			}
			for _, signal := range signals {
				if signal.Priority != domain.PriorityUrgent || signal.Guidance != guard.EmergencyEscalationMessage {
					t.Fatalf("signal = %#v", signal)
				}
			}
		})
	}
}

func TestDetectEmergencyAnswerUsesSafetyContextWithoutInventingSymptoms(t *testing.T) {
	questions := []domain.Question{{Text: "发作时是否伴有胸痛或呼吸困难？", Category: "safety"}}
	for _, answer := range []string{"有，但没有加重", "第一个没有，第二个有，而且越来越严重"} {
		signals := guard.DetectEmergencyAnswer(answer, questions)
		if len(signals) != 1 || strings.Contains(signals[0].Evidence, "胸痛") || strings.Contains(signals[0].Evidence, "呼吸困难") {
			t.Fatalf("DetectEmergencyAnswer(%q) = %#v", answer, signals)
		}
	}
	if signals := guard.DetectEmergencyAnswer("都没有", questions); len(signals) != 0 {
		t.Fatalf("negative answer produced signals: %#v", signals)
	}
	if signals := guard.DetectEmergencyAnswer("有发热", []domain.Question{{Text: "最近是否发热？", Category: "duration"}}); len(signals) != 0 {
		t.Fatalf("non-safety context produced signals: %#v", signals)
	}
	multiple := []domain.Question{
		{Text: "最近是否发热？", Category: "duration"},
		{Text: "现在是否有胸痛或呼吸困难？", Category: "safety"},
		{Text: "是否记录过体温？", Category: "measurement"},
	}
	if signals := guard.DetectEmergencyAnswer("第三个：有发热", multiple); len(signals) != 0 {
		t.Fatalf("answer to ordinary question produced signals: %#v", signals)
	}
	if signals := guard.DetectEmergencyAnswer("第二个确实存在", multiple); len(signals) != 1 {
		t.Fatalf("numbered safety answer was missed: %#v", signals)
	}
	if signals := guard.DetectEmergencyAnswer("嗯，对", questions); len(signals) != 1 {
		t.Fatalf("colloquial affirmative answer was missed: %#v", signals)
	}
	if signals := guard.DetectEmergencyAnswer("以前有过，不过现在没有了", questions); len(signals) != 0 {
		t.Fatalf("resolved historical answer produced signals: %#v", signals)
	}
}

func TestDetectEmergencySignalsKeepsEachDistinctTrigger(t *testing.T) {
	signals := guard.DetectEmergencySignals("我现在胸痛并且呼吸困难，症状正在加重。")
	if len(signals) != 2 || signals[0].SourceQuote != "胸痛" || signals[1].SourceQuote != "呼吸困难" {
		t.Fatalf("signals = %#v", signals)
	}
}

func TestContainsMedicalOverreach(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{text: "建议向医生询问是否需要进一步检查", want: false},
		{text: "你被诊断为肺炎", want: true},
		{text: "你患有肺炎，是否需要服药？", want: true},
		{text: "你得了肺炎，是否需要进一步检查？", want: true},
		{text: "你这就是肺炎", want: true},
		{text: "这就是肺结节", want: true},
		{text: "是否就是骨折？", want: true},
		{text: "已经确定是肺炎", want: true},
		{text: "已经确定是抑郁症", want: true},
		{text: "是否已确定是肿瘤？", want: true},
		{text: "检查后确认为肺炎", want: true},
		{text: "建议停用目前的药物", want: true},
		{text: "无需就医，在家观察即可", want: true},
		{text: "禁食三天观察症状是否缓解", want: true},
		{text: "检查前是否需要禁食？", want: false},
	}

	for _, tt := range tests {
		if got := guard.ContainsMedicalOverreach(tt.text); got != tt.want {
			t.Errorf("ContainsMedicalOverreach(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestIsSafeQuestionRejectsMedicalAdviceSynonyms(t *testing.T) {
	unsafe := []string{
		"你应该马上吃阿莫西林吗？",
		"你患有肺炎，是否需要服药？",
		"你得了肺炎，是否需要进一步检查？",
		"请停掉现在的药，好吗？",
		"这很可能是肺炎吗？",
		"是否需要自行换药？",
	}
	for _, question := range unsafe {
		if guard.IsSafeQuestion(question) {
			t.Fatalf("IsSafeQuestion(%q) = true", question)
		}
	}
	if !guard.IsSafeQuestion("是否需要进一步检查，检查前需要做哪些准备？") {
		t.Fatal("safe preparation question was rejected")
	}
	if !guard.IsSafeQuestion("是否需要向医生确定是否患有肺炎？") {
		t.Fatal("diagnostic clarification question was rejected")
	}
}

func TestIsSafeQuestionAcceptsDurationClarification(t *testing.T) {
	question := "心悸每次发作通常持续多久，是突然出现和缓解，还是逐渐变化？"
	if !guard.IsSafeQuestion(question) {
		t.Fatalf("safe duration clarification was rejected: %q", question)
	}
}
