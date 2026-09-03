package guard

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"visitready/internal/domain"
)

var diagnosticAssertionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:你|患者|病人)(?:这)?就是`),
	regexp.MustCompile(`(?:你|患者|病人)(?:已经)?得了`),
	regexp.MustCompile(`(?:^|[\s，。！？；：])(?:这|那)?就是`),
	regexp.MustCompile(`(?:是否|是不是)就是`),
	regexp.MustCompile(`就是[\p{Han}]{1,12}(?:炎|癌|病|症|瘤|冒|感染|综合征|结石|梗死|哮喘|高血压|糖尿病)`),
}

func IsAllowedSourceURL(rawURL string, allowedDomains []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || net.ParseIP(host) != nil {
		return false
	}
	return MatchAllowedDomain(host, allowedDomains) != ""
}

func MatchAllowedDomain(host string, allowedDomains []string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, candidate := range allowedDomains {
		allowed := strings.ToLower(strings.Trim(strings.TrimSpace(candidate), "."))
		if allowed != "" && (host == allowed || strings.HasSuffix(host, "."+allowed)) {
			return allowed
		}
	}
	return ""
}

func PartitionGroundedFacts(input string, facts []domain.Fact) ([]domain.Fact, []domain.Fact) {
	normalizedInput := normalizeEvidence(input)
	accepted := make([]domain.Fact, 0, len(facts))
	rejected := make([]domain.Fact, 0)
	for _, fact := range facts {
		quote := normalizeEvidence(fact.SourceQuote)
		if len([]rune(quote)) >= 2 && strings.Contains(normalizedInput, quote) {
			accepted = append(accepted, fact)
			continue
		}
		rejected = append(rejected, fact)
	}
	return accepted, rejected
}

func ContainsMedicalOverreach(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{
		"诊断为", "可以确诊", "已经确诊", "很可能是", "可能患有", "你患有", "确认为", "疑似", "考虑为", "疾病概率",
		"无需就医", "不需要就医", "不用就医", "别去医院",
		"建议停药", "建议停用", "建议服用", "自行服用", "应该吃", "需要吃", "马上吃", "停掉",
		"调整剂量", "增加剂量", "减少剂量", "加大剂量", "减小剂量", "换药",
		"you are diagnosed", "stop taking", "change the dose", "no need to see a doctor",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	for _, pattern := range diagnosticAssertionPatterns {
		if pattern.MatchString(lower) {
			return true
		}
	}
	return containsDefinitiveDiagnosis(lower)
}

func containsDefinitiveDiagnosis(text string) bool {
	for _, questionForm := range []string{"确定是否", "确定是什么", "确定是不是"} {
		text = strings.ReplaceAll(text, questionForm, "")
	}
	return strings.Contains(text, "确定是")
}

func IsSafeQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 140 || ContainsMedicalOverreach(text) {
		return false
	}
	if !strings.HasSuffix(text, "？") && !strings.HasSuffix(text, "?") {
		return false
	}
	allowedIntents := []string{"是否", "哪些", "什么", "如何", "还需要", "需要记录", "需要说明"}
	for _, intent := range allowedIntents {
		if strings.Contains(text, intent) {
			return true
		}
	}
	return false
}

func normalizeEvidence(input string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, input))
}
