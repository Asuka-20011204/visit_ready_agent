package guard_test

import (
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
