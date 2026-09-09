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

const EmergencyEscalationMessage = "这些表现正在发生或明显加重，请立即联系当地急救服务或前往急诊。"

// DetectEmergencySignals deterministically identifies explicitly reported,
// current red-flag symptoms. Hypothetical, historical, and negated mentions
// are excluded before the result can influence workflow control.
func DetectEmergencySignals(input string) []domain.RiskSignal {
	segments := emergencySegments(input)
	result := make([]domain.RiskSignal, 0, 3)
	seen := make(map[string]struct{})
	for segmentIndex, segment := range segments {
		for _, evidence := range positiveEmergencyTerms(segment) {
			later := segments[segmentIndex+1:]
			if laterSegmentResolvesEvidence(evidence, later) || evidenceCorrectedByLaterContext(evidence, later) {
				continue
			}
			key := normalizeEvidence(evidence)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, domain.RiskSignal{
				Priority:    domain.PriorityUrgent,
				Title:       "需要立即处理的表现",
				Evidence:    evidence,
				Guidance:    EmergencyEscalationMessage,
				SourceQuote: evidence,
			})
			if len(result) == 3 {
				return result
			}
		}
	}
	for _, evidence := range continuedHistoricalEvidence(input, segments) {
		key := normalizeEvidence(evidence)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, domain.RiskSignal{
			Priority: domain.PriorityUrgent, Title: "需要立即处理的表现", Evidence: evidence,
			Guidance: EmergencyEscalationMessage, SourceQuote: evidence,
		})
		if len(result) == 3 {
			break
		}
	}
	return result
}

func laterSegmentResolvesEvidence(evidence string, segments []string) bool {
	for _, segment := range segments {
		if !containsAny(segment, []string{"现在已经好了", "现在好了", "目前已经好了", "目前好了", "已经缓解", "已缓解", "已经消失", "已消失", "不再出现", "恢复正常", "今天正常"}) {
			continue
		}
		if containsEquivalentEmergencyTerm(segment, evidence) {
			return true
		}
		if implicitlyResolvesPreviousEvidence(segment) {
			return true
		}
	}
	return false
}

func containsEquivalentEmergencyTerm(segment, evidence string) bool {
	for _, term := range emergencyTerms() {
		if strings.Contains(segment, term) && sameEmergencyFamily(term, evidence) {
			return true
		}
	}
	return false
}

func sameEmergencyFamily(first, second string) bool {
	if first == second {
		return true
	}
	families := [][]string{
		{"胸痛", "胸口疼", "胸口剧痛"},
		{"呼吸困难", "喘不上气", "喘不过气", "气短", "无法呼吸"},
		{"晕厥", "晕倒", "差点晕", "眼前发黑", "意识不清", "神志不清"},
		{"大量出血", "止不住血", "吐血", "呕血", "咯血"},
		{"单侧无力", "一侧无力"},
		{"说话不清", "言语不清"},
	}
	for _, family := range families {
		firstInFamily, secondInFamily := false, false
		for _, term := range family {
			firstInFamily = firstInFamily || term == first
			secondInFamily = secondInFamily || term == second
		}
		if firstInFamily && secondInFamily {
			return true
		}
	}
	return false
}

func implicitlyResolvesPreviousEvidence(segment string) bool {
	segment = strings.TrimSpace(segment)
	for _, prefix := range []string{
		"现在已经好了", "现在好了", "目前已经好了", "目前好了", "已经缓解", "已缓解",
		"已经消失", "已消失", "不再出现", "恢复正常", "今天正常", "症状已经缓解",
		"症状已缓解", "症状已经消失", "症状已消失", "这个症状已经缓解", "这个症状已缓解",
		"这种症状已经缓解", "这种症状已缓解", "它已经缓解", "它已缓解",
	} {
		if strings.HasPrefix(segment, prefix) {
			return true
		}
	}
	return false
}

func evidenceCorrectedByLaterContext(evidence string, segments []string) bool {
	context := strings.Join(segments, "，")
	if !containsAny(context, []string{"不，是", "不，", "不是", "而是", "改为", "应该是"}) {
		return false
	}
	correctionIndex := -1
	for _, marker := range []string{"不，是", "不，", "不是", "而是", "改为", "应该是"} {
		if index := strings.Index(context, marker); index >= 0 && (correctionIndex < 0 || index < correctionIndex) {
			correctionIndex = index
		}
	}
	if correctionIndex < 0 {
		return false
	}
	corrected := context[correctionIndex:]
	if containsEquivalentEmergencyTerm(corrected, evidence) {
		return false
	}
	for _, term := range append(emergencyTerms(), "胃痛", "腹痛", "头痛", "咳嗽") {
		if strings.Contains(corrected, term) && !sameEmergencyFamily(term, evidence) {
			return true
		}
	}
	return false
}

func continuedHistoricalEvidence(input string, segments []string) []string {
	result := make([]string, 0, 1)
	for _, evidence := range emergencyTerms() {
		if !strings.Contains(input, evidence) || !containsAny(input, []string{"以前", "曾经", "既往", "过去", "半年前", "一年前", "多年前"}) {
			continue
		}
		for index, segment := range segments {
			if !strings.Contains(segment, evidence) || !containsAny(segment, []string{"以前", "曾经", "既往", "过去", "半年前", "一年前", "多年前"}) {
				continue
			}
			if explicitlyContinuesHistoricalSymptom(evidence, segments[index+1:]) {
				result = append(result, evidence)
			}
		}
	}
	return result
}

func explicitlyContinuesHistoricalSymptom(evidence string, segments []string) bool {
	later := strings.Join(segments, "，")
	for _, segment := range segments {
		for _, term := range positiveEmergencyTerms(segment) {
			if sameEmergencyFamily(term, evidence) {
				return true
			}
		}
	}
	if containsAny(later, emergencyTerms()) || containsAny(later, []string{"头痛", "胃痛", "腹痛", "咳嗽", "发热", "发烧", "心悸"}) {
		return false
	}
	if sameEmergencyFamily(evidence, "胸痛") {
		return containsAny(later, []string{"现在还在痛", "目前还在痛", "现在仍在痛", "目前仍在痛", "现在仍然痛", "目前仍然痛", "至今还痛"})
	}
	return containsAny(later, []string{"现在仍在发作", "目前仍在发作", "现在还在发作", "目前还在发作", "至今未缓解"})
}

func positiveEmergencyTerms(segment string) []string {
	positive := make([]string, 0, 2)
	for _, term := range emergencyTerms() {
		for offset := 0; offset < len(segment); {
			relative := strings.Index(segment[offset:], term)
			if relative < 0 {
				break
			}
			index := offset + relative
			prefix := runeWindowBefore(segment, index, 24)
			suffix := runeWindowAfter(segment, index+len(term), 24)
			offset = index + len(term)
			if emergencyMentionExcluded(prefix, suffix) || isAmbiguousEmergencyTerm(term) && !hasCurrentEmergencyCue(segment) {
				continue
			}
			positive = append(positive, term)
			break
		}
	}
	return positive
}

func emergencyMentionExcluded(prefix, suffix string) bool {
	trimmedPrefix := emergencyScopePrefix(strings.TrimSpace(prefix))
	trimmedSuffix := strings.TrimSpace(suffix)
	if directlyNegatesEmergencyTerm(trimmedPrefix) || coordinatedEmergencyNegation(trimmedPrefix) {
		return true
	}
	if containsAny(trimmedPrefix, []string{"如果", "假如", "假设", "要是", "一旦", "以后出现", "将来出现"}) {
		return true
	}
	if containsAny(trimmedPrefix, []string{"是否", "会不会"}) && !hasCurrentEmergencyCue(trimmedPrefix) {
		return true
	}
	if containsAny(trimmedPrefix, []string{"不确定", "不清楚是不是", "不清楚是否", "不知道是不是", "不知道是否"}) && !hasCurrentEmergencyCue(trimmedPrefix) {
		return true
	}
	if containsAny(trimmedSuffix, []string{"是否", "会不会", "算不算", "是不是"}) && !hasCurrentEmergencyCue(trimmedPrefix) {
		return true
	}
	if containsAny(trimmedPrefix, []string{"昨天", "前天", "半年前", "一年前", "三年前", "多年前", "以前", "曾经", "既往", "过去", "出现过"}) &&
		!hasCurrentEmergencyCue(trimmedPrefix) && !containsAny(trimmedSuffix, []string{"一直", "持续", "仍然", "还在", "未缓解", "加重"}) {
		return true
	}
	return containsAny(trimmedSuffix, []string{
		"都没有出现", "均未出现", "均没有出现", "并没有出现", "没有这些表现", "这些表现都没有",
		"已缓解", "已经缓解", "已消失", "已经消失", "现在好了", "已经好了", "不再出现",
	})
}

func directlyNegatesEmergencyTerm(prefix string) bool {
	if strings.HasSuffix(prefix, "既不") || strings.HasSuffix(prefix, "也不") {
		return true
	}
	for _, ending := range []string{"没有", "没有明显", "否认", "未出现", "未见", "不伴", "不伴有", "无", "无明显", "并非", "不是"} {
		if strings.HasSuffix(prefix, ending) {
			return true
		}
	}
	return false
}

func coordinatedEmergencyNegation(prefix string) bool {
	lastNegation := -1
	for _, marker := range []string{"没有", "否认", "未出现", "未见", "不伴", "无明显", "并非", "不是", "无"} {
		if index := strings.LastIndex(prefix, marker); index > lastNegation {
			lastNegation = index + len(marker)
		}
	}
	if lastNegation < 0 || lastNegation >= len(prefix) {
		return false
	}
	scope := prefix[lastNegation:]
	if !containsAny(scope, []string{"和", "或", "及", "以及", "、"}) {
		return false
	}
	return containsAny(scope, emergencyTerms())
}

func emergencyScopePrefix(prefix string) string {
	lastIndex := -1
	preserveCue := false
	for _, boundary := range []string{"但是", "然而", "不过", "但", "。", "！", "？", "；", "，", ","} {
		if index := strings.LastIndex(prefix, boundary); index > lastIndex {
			lastIndex = index + len(boundary)
			preserveCue = false
		}
	}
	for _, cue := range []string{"现在", "目前", "正在", "此刻", "刚刚", "突然"} {
		if index := strings.LastIndex(prefix, cue); index > lastIndex {
			lastIndex = index
			preserveCue = true
		}
	}
	if lastIndex < 0 {
		return prefix
	}
	if !preserveCue && lastIndex >= len(prefix) {
		return ""
	}
	return prefix[lastIndex:]
}

func hasCurrentEmergencyCue(value string) bool {
	return containsAny(value, []string{"现在", "目前", "正在", "此刻", "刚刚", "突然", "持续", "一直", "加重", "越来越", "严重", "止不住", "喘不上"})
}

func isAmbiguousEmergencyTerm(term string) bool {
	switch term {
	case "气短", "眼前发黑", "差点晕":
		return true
	default:
		return false
	}
}

func runeWindowBefore(value string, byteIndex, limit int) string {
	runes := []rune(value[:byteIndex])
	if len(runes) > limit {
		runes = runes[len(runes)-limit:]
	}
	return string(runes)
}

func runeWindowAfter(value string, byteIndex, limit int) string {
	runes := []rune(value[byteIndex:])
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func emergencySegments(input string) []string {
	for _, pivot := range []string{"但是", "然而", "但"} {
		input = strings.ReplaceAll(input, pivot, "。")
	}
	return strings.FieldsFunc(input, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,\n", char)
	})
}

func emergencyTerms() []string {
	return []string{
		"胸痛", "胸口疼", "胸口剧痛", "呼吸困难", "喘不上气", "喘不过气", "气短", "晕厥", "晕倒", "差点晕", "眼前发黑", "意识不清", "神志不清", "失去意识",
		"单侧无力", "一侧无力", "一侧脸歪", "口角歪斜", "说话不清", "言语不清", "大量出血", "止不住血", "吐血", "呕血", "咯血", "抽搐", "持续惊厥",
		"无法呼吸", "喉咙肿胀", "严重过敏", "服毒", "中毒", "想自杀", "伤害自己",
	}
}

// DetectEmergencyAnswer resolves a short affirmative answer against the
// safety question that produced it, without retaining the full answer.
func DetectEmergencyAnswer(answer string, questions []domain.Question) []domain.RiskSignal {
	answer = strings.TrimSpace(answer)
	if answer == "" || explicitlyResolvesCurrentEmergency(answer) || !hasAffirmativeEmergencyClause(answer) {
		return nil
	}
	safetyIndexes := make([]int, 0, len(questions))
	for index, question := range questions {
		if isSafetyQuestion(question) {
			safetyIndexes = append(safetyIndexes, index)
		}
	}
	if len(safetyIndexes) == 0 {
		return nil
	}
	if answerIndex, explicit := referencedQuestionIndex(answer); explicit {
		if answerIndex < 0 || answerIndex >= len(questions) || !isSafetyQuestion(questions[answerIndex]) {
			return nil
		}
	} else if len(questions) != 1 {
		return nil
	}
	evidence := minimalAffirmativeEvidence(answer)
	return []domain.RiskSignal{{
		Priority: domain.PriorityUrgent, Title: "安全追问得到肯定回答", Evidence: evidence,
		Guidance: EmergencyEscalationMessage, SourceQuote: evidence,
	}}
}

func isSafetyQuestion(question domain.Question) bool {
	return question.Category == "safety" || containsAny(question.Text, emergencyTerms())
}

func referencedQuestionIndex(answer string) (int, bool) {
	for index, markers := range [][]string{
		{"第一个", "第一项", "问题一", "1."},
		{"第二个", "第二项", "问题二", "2."},
		{"第三个", "第三项", "问题三", "3."},
	} {
		if containsAny(answer, markers) {
			return index, true
		}
	}
	return 0, false
}

func explicitlyResolvesCurrentEmergency(answer string) bool {
	return containsAny(answer, []string{"现在没有了", "目前没有了", "现在已经好了", "目前已经好了", "已经好了", "已经消失", "不再出现", "恢复正常"})
}

func hasAffirmativeEmergencyClause(answer string) bool {
	for _, pivot := range []string{"但是", "然而", "但"} {
		answer = strings.ReplaceAll(answer, pivot, "。")
	}
	for _, clause := range strings.FieldsFunc(answer, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,\n", char)
	}) {
		if containsAny(clause, []string{"没有", "不是", "否认", "未出现", "不伴", "不会", "不清楚", "不知道"}) {
			continue
		}
		if containsAny(clause, []string{"有", "是", "会", "出现", "存在", "确实", "伴有", "正在", "加重", "严重", "越来越", "嗯", "对"}) {
			return true
		}
	}
	return false
}

func minimalAffirmativeEvidence(answer string) string {
	for _, candidate := range []string{"越来越严重", "正在加重", "加重", "确实存在", "存在", "伴有", "出现", "有", "是", "对"} {
		if strings.Contains(answer, candidate) {
			return candidate
		}
	}
	return "肯定回答"
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
		if len([]rune(quote)) >= 2 && strings.Contains(normalizedInput, quote) && quoteHasCompleteContext(input, fact.SourceQuote) {
			accepted = append(accepted, fact)
			continue
		}
		rejected = append(rejected, fact)
	}
	return accepted, rejected
}

func quoteHasCompleteContext(input, quote string) bool {
	quote = strings.TrimFunc(strings.TrimSpace(quote), unicode.IsPunct)
	if quote == "" || containsAny(quote, []string{"没有", "否认", "未出现", "未见", "不伴", "无明显", "并非", "不是", "以前", "曾经", "过去", "既往"}) {
		return true
	}
	compactInput := compactEvidence(input)
	compactQuote := compactEvidence(quote)
	found := false
	for offset := 0; offset < len(compactInput); {
		relative := strings.Index(compactInput[offset:], compactQuote)
		if relative < 0 {
			break
		}
		found = true
		index := offset + relative
		prefix := scopedPrefix(runeWindowBefore(compactInput, index, 12))
		suffix := runeWindowAfter(compactInput, index+len(compactQuote), 12)
		offset = index + len(compactQuote)
		if !containsAny(prefix, []string{"没有", "否认", "未出现", "未见", "不伴", "无明显", "并非", "不是", "无"}) &&
			!containsAny(suffix, []string{"都没有", "均无", "均没有", "并没有"}) {
			return true
		}
	}
	return !found
}

func compactEvidence(value string) string {
	return strings.ToLower(strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) {
			return -1
		}
		return char
	}, value))
}

func scopedPrefix(prefix string) string {
	for _, boundary := range []string{"但是", "然而", "不过", "但", "。", "！", "？", "；", "，", ","} {
		if index := strings.LastIndex(prefix, boundary); index >= 0 {
			prefix = prefix[index+len(boundary):]
		}
	}
	return prefix
}

func ContainsMedicalOverreach(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{
		"诊断为", "可以确诊", "已经确诊", "很可能是", "可能患有", "你患有", "确认为", "疑似", "考虑为", "疾病概率",
		"无需就医", "不需要就医", "不用就医", "别去医院",
		"建议停药", "建议停用", "建议服用", "自行服用", "应该吃", "需要吃", "马上吃", "停掉",
		"调整剂量", "增加剂量", "减少剂量", "加大剂量", "减小剂量", "换药",
		"禁食观察", "断食观察", "绝食观察", "禁食三天", "禁食3天", "断食三天", "断食3天",
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
	allowedIntents := []string{"是否", "哪些", "什么", "如何", "多久", "还需要", "需要记录", "需要说明"}
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
