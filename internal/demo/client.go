package demo

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"visitready/internal/domain"
)

type Client struct{}

type symptomRule struct {
	name   string
	tokens []string
}

var (
	symptomRules = []symptomRule{
		{name: "心悸", tokens: []string{"心悸", "心慌", "心跳很快", "心跳加速"}},
		{name: "手脚发汗", tokens: []string{"手脚发汗", "手心出汗", "脚心出汗"}},
		{name: "发热", tokens: []string{"发热", "发烧", "体温升高"}},
		{name: "咳嗽", tokens: []string{"咳嗽"}},
		{name: "头痛", tokens: []string{"头痛", "头疼"}},
		{name: "腹痛", tokens: []string{"腹痛", "胃痛", "肚子痛"}},
		{name: "皮疹", tokens: []string{"皮疹"}},
		{name: "头晕", tokens: []string{"头晕"}},
	}
	timePattern      = regexp.MustCompile(`(?:从)?(今天|昨[天日]|前天|(?:近|最近)[一二两三四五六七八九十\d]+(?:天|周|个月|月|年)|[一二两三四五六七八九十\d]+(?:天|周|个月|月|年)(?:前|来))`)
	durationPattern  = regexp.MustCompile(`(每次(?:(?:约|大约)?持续)?(?:约|大约)?[一二两三四五六七八九十半\d]+(?:秒钟?|分钟?|小时))`)
	frequencyPattern = regexp.MustCompile(`((?:每天|每晚|每周|一周|一天)[一二两三四五六七八九十\d]+次|反复|偶尔)`)
	severityPattern  = regexp.MustCompile(`(轻微|较轻|中等|明显|严重|剧烈|难以忍受)`)
	triggerPattern   = regexp.MustCompile(`(运动后|活动后|饭后|进食后|起床后|躺下时|紧张时|熬夜后|夜间|安静坐着|安静时|静坐时|休息时|准备睡觉时|睡前)`)
	reliefPattern    = regexp.MustCompile(`((?:深呼吸|休息|坐下|喝水|进食)(?:后|时)?(?:会|可|能)?(?:缓解|减轻|好转))`)
)

func (Client) Extract(_ context.Context, input string) (domain.Extraction, error) {
	quotes := userEvidenceSentences(input)
	facts := make([]domain.Fact, 0, len(quotes))
	for _, quote := range quotes {
		facts = append(facts, domain.Fact{Category: category(quote), Content: quote, SourceQuote: quote})
		if len(facts) == 18 {
			break
		}
	}

	profiles := extractProfiles(input, quotes)
	result := domain.Extraction{
		VisitGoal:       visitGoal(profiles),
		Facts:           facts,
		SymptomProfiles: profiles,
		Timeline:        extractTimeline(quotes),
		RiskSignals:     extractRiskSignals(input, quotes),
	}
	result.MissingFields, result.ClarificationQuestions = missingDetails(profiles, input)
	result.ClarificationPrompts = clarificationPrompts(result.MissingFields)
	if !strings.Contains(input, "补充信息：") && len(result.ClarificationQuestions) > 0 {
		return result, nil
	}
	result.SearchQueries = []string{searchTopicFromProfiles(profiles, input) + " 就诊前准备"}
	return result, nil
}

func userEvidenceSentences(input string) []string {
	rawInput := input
	if marker := strings.Index(input, "轮追问："); marker >= 0 {
		if lineStart := strings.LastIndex(input[:marker], "\n"); lineStart >= 0 {
			rawInput = input[:lineStart]
		}
	}
	result := uniqueSentences(strings.ReplaceAll(rawInput, "\n补充信息：", "。"))
	seen := make(map[string]struct{}, len(result))
	for _, quote := range result {
		seen[normalize(quote)] = struct{}{}
	}
	for _, turn := range parseClarificationEvidence(input) {
		for _, quote := range uniqueSentences(turn.answer) {
			key := normalize(quote)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, quote)
		}
	}
	return result
}

func (Client) GenerateQuestions(_ context.Context, input domain.QuestionInput) (domain.QuestionSet, error) {
	if len(input.SymptomProfiles) == 0 && len(input.MissingFields) == 0 && len(input.RiskSignals) == 0 {
		questions := []domain.Question{
			{Text: "这些症状的时间变化中，哪些细节最需要向您说明？", Reason: "帮助医生快速理解症状过程", Priority: domain.PriorityHigh, Category: "timeline"},
			{Text: "是否需要进一步检查，检查前需要做哪些准备？", Reason: "提前确认检查要求", Priority: domain.PriorityNormal, Category: "visit_preparation"},
			{Text: "我目前记录的信息还有哪些重要遗漏？", Reason: "请医生补充判断所需信息", Priority: domain.PriorityNormal, Category: "completeness"},
		}
		if len(input.Sources) > 0 {
			questions[1].SourceURL = input.Sources[0].URL
		}
		return domain.QuestionSet{Questions: questions, ActionItems: defaultActions(nil, nil)}, nil
	}

	questions := make([]domain.Question, 0, 6)
	seen := make(map[string]struct{})
	add := func(question domain.Question) {
		if len(questions) == 6 {
			return
		}
		key := normalize(question.Text)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		questions = append(questions, question)
	}
	for _, risk := range input.RiskSignals {
		if risk.Priority != domain.PriorityUrgent {
			continue
		}
		evidence := strings.TrimSpace(risk.Evidence)
		if evidence == "" {
			evidence = "已提到的安全信号"
		}
		add(domain.Question{
			Text:     "我提到的" + evidence + "是否需要您优先处理？",
			Reason:   "这些已明确出现的伴随表现可能影响就医紧迫程度",
			Priority: domain.PriorityUrgent,
			Category: "safety",
		})
	}
	for _, urgentOnly := range []bool{true, false} {
		for _, field := range input.MissingFields {
			question := questionForMissingField(field)
			if (question.Priority == domain.PriorityUrgent) == urgentOnly {
				add(question)
			}
		}
	}
	for _, profile := range input.SymptomProfiles {
		if profile.Onset == "" {
			add(domain.Question{Text: profile.Name + "最早在什么时候出现，之后是持续存在还是间歇发作？", Reason: "建立症状时间线并了解变化趋势", Priority: domain.PriorityHigh, Category: "timeline"})
		}
	}
	add(domain.Question{Text: "就诊时应优先提供哪些既往疾病、用药和过敏信息？", Reason: "这些背景可能影响医生对检查与处置的安排", Priority: domain.PriorityNormal, Category: "history"})
	add(domain.Question{Text: "在就诊前，我应该记录哪些发作数据或携带哪些已有检查资料？", Reason: "把有限的就诊时间用于更有效的沟通", Priority: domain.PriorityNormal, Category: "visit_preparation"})
	if len(input.Sources) > 0 && len(questions) > 0 {
		questions[len(questions)-1].SourceURL = input.Sources[0].URL
	}
	return domain.QuestionSet{Questions: questions, ActionItems: defaultActions(input.SymptomProfiles, input.RiskSignals)}, nil
}

func clarificationPrompts(fields []string) []domain.Question {
	prompts := make([]domain.Question, 0, len(fields))
	for _, field := range fields {
		prompts = append(prompts, questionForMissingField(field))
	}
	return prompts
}

func defaultActions(profiles []domain.SymptomProfile, risks []domain.RiskSignal) []domain.ActionItem {
	actions := make([]domain.ActionItem, 0, 4)
	if len(risks) > 0 {
		actions = append(actions, domain.ActionItem{
			Title:    "优先处理安全信号",
			Detail:   risks[0].Guidance,
			Reason:   "用户已明确提到可能影响就医紧迫程度的伴随表现",
			Priority: domain.PriorityUrgent,
			Category: "safety",
		})
	}
	for _, profile := range profiles {
		if profile.Name == "心悸" {
			actions = append(actions, domain.ActionItem{
				Title:    "记录一次完整发作",
				Detail:   "记录开始时间、持续多久、当时在做什么、脉搏或心率数值以及伴随表现；不要为了记录而延误求助。",
				Reason:   "连续的客观记录比单次回忆更利于就诊沟通",
				Priority: domain.PriorityHigh,
				Category: "tracking",
			})
			break
		}
	}
	actions = append(actions,
		domain.ActionItem{
			Title:    "整理用药与过敏信息",
			Detail:   "列出正在使用的药物、保健品、剂量与频率，并记录已知过敏；不要自行停药或改剂量。",
			Reason:   "帮助医生了解可能影响检查和处置安排的信息",
			Priority: domain.PriorityNormal,
			Category: "medication_history",
		},
		domain.ActionItem{
			Title:    "准备已有资料",
			Detail:   "携带与本次情况相关的既往检查报告、近期测量记录和病历资料。",
			Reason:   "减少重复询问并让就诊信息更连贯",
			Priority: domain.PriorityNormal,
			Category: "records",
		},
	)
	return actions
}

func questionForMissingField(field string) domain.Question {
	subject := symptomFromMissingField(field)
	switch {
	case containsAny(field, "胸痛", "气短", "呼吸困难", "晕厥"):
		return domain.Question{Text: "发作时是否同时出现胸痛、呼吸困难、接近晕倒或已经晕倒？", Reason: "这些伴随表现会影响就医紧迫程度，需要优先确认", Priority: domain.PriorityUrgent, Category: "safety"}
	case containsAny(field, "开始", "起病"):
		return domain.Question{Text: "关于" + subject + "的开始时间，你还能补充哪些信息？", Reason: "建立症状时间线并了解变化趋势", Priority: domain.PriorityHigh, Category: "timeline"}
	case strings.Contains(field, "持续"):
		return domain.Question{Text: subject + "每次发作通常持续多久，是突然出现和缓解，还是逐渐变化？", Reason: "持续时间和起止方式有助于医生判断下一步需要记录或检查什么", Priority: domain.PriorityHigh, Category: "duration"}
	case strings.Contains(field, "频率"):
		return domain.Question{Text: subject + "一天或一周大约发作几次，最近频率是否变化？", Reason: "频率变化能帮助医生理解症状趋势", Priority: domain.PriorityHigh, Category: "frequency"}
	case containsAny(field, "诱因", "缓解"):
		return domain.Question{Text: subject + "通常在什么活动、体位、进食或情绪下出现，怎样做会缓解？", Reason: "诱发与缓解因素可提升就诊沟通效率", Priority: domain.PriorityNormal, Category: "trigger"}
	case strings.Contains(field, "体温"):
		return domain.Question{Text: "最近是否测量过体温；如果测过，数值、时间和测量方式分别是什么？", Reason: "客观测量比主观发热感更便于医生评估", Priority: domain.PriorityHigh, Category: "measurement"}
	default:
		return domain.Question{Text: "关于" + field + "，你还能补充哪些信息？", Reason: "这是当前就诊信息中的关键空缺", Priority: domain.PriorityNormal, Category: "missing_detail"}
	}
}

func symptomFromMissingField(field string) string {
	for _, rule := range symptomRules {
		if strings.Contains(field, rule.name) {
			return rule.name
		}
	}
	return "症状"
}

func extractProfiles(input string, quotes []string) []domain.SymptomProfile {
	profiles := make([]domain.SymptomProfile, 0, len(symptomRules))
	for _, rule := range symptomRules {
		if !containsAny(input, rule.tokens...) || isNegated(input, rule.tokens...) {
			continue
		}
		quote := positiveContainingQuote(quotes, rule.tokens...)
		name := firstPositiveToken(input, rule.tokens...)
		if name == "" {
			continue
		}
		profile := domain.SymptomProfile{Name: name, SourceQuote: quote}
		scope := symptomScope(quote, rule)
		profile.Onset = firstMatch(timePattern, scope)
		profile.Duration = firstMatch(durationPattern, scope)
		profile.Frequency = firstMatch(frequencyPattern, scope)
		profile.Severity = firstMatch(severityPattern, scope)
		profile.Trigger = firstMatch(triggerPattern, scope)
		profile.RelievingFactors = firstMatch(reliefPattern, scope)
		if containsPositive(scope, "头晕") && rule.name != "头晕" {
			profile.AssociatedSymptoms = append(profile.AssociatedSymptoms, "头晕")
		}
		profile.Pattern = firstContained(scope, "晚上睡觉", "夜间", "晚上", "睡觉")
		applySharedSymptomDetails(&profile, coordinatedSymptomScope(quote, rule))
		profiles = append(profiles, profile)
	}
	applyClarificationDetails(profiles, parseClarificationEvidence(input))
	return profiles
}

type clarificationEvidence struct {
	questions []string
	answer    string
}

func parseClarificationEvidence(input string) []clarificationEvidence {
	var result []clarificationEvidence
	var current *clarificationEvidence
	collectingAnswer := false
	for _, rawLine := range strings.Split(input, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "第") && strings.Contains(line, "轮追问：") {
			if current != nil {
				result = append(result, *current)
			}
			current = &clarificationEvidence{}
			collectingAnswer = false
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "用户回答：") {
			current.answer = strings.TrimSpace(strings.TrimPrefix(line, "用户回答："))
			collectingAnswer = true
			continue
		}
		if collectingAnswer {
			current.answer = strings.TrimSpace(current.answer + "\n" + line)
			continue
		}
		if separator := strings.Index(line, ". "); separator > 0 {
			current.questions = append(current.questions, strings.TrimSpace(line[separator+2:]))
		}
	}
	if current != nil {
		result = append(result, *current)
	}
	return result
}

func applyClarificationDetails(profiles []domain.SymptomProfile, turns []clarificationEvidence) {
	for profileIndex := range profiles {
		profile := &profiles[profileIndex]
		for _, turn := range turns {
			questions := strings.Join(turn.questions, "\n")
			askedAboutProfile := containsAny(questions, profile.Name)
			before := profileDetailKey(*profile)
			profileScope := clarificationProfileScope(turn.answer, profile.Name)
			onset := firstMatch(timePattern, profileScope)
			if profile.Onset == "" && (askedAboutProfile && clarificationAsks(questions, "timeline") || clarificationDetailTargetsProfile(turn.answer, onset, profile.Name, profiles)) {
				profile.Onset = onset
			}
			duration := firstMatch(durationPattern, profileScope)
			if profile.Duration == "" && (askedAboutProfile && clarificationAsks(questions, "duration") || clarificationDetailTargetsProfile(turn.answer, duration, profile.Name, profiles)) {
				profile.Duration = duration
			}
			frequency := firstMatch(frequencyPattern, profileScope)
			if profile.Frequency == "" && (askedAboutProfile && clarificationAsks(questions, "frequency") || clarificationDetailTargetsProfile(turn.answer, frequency, profile.Name, profiles)) {
				profile.Frequency = frequency
			}
			severity := firstMatch(severityPattern, profileScope)
			if profile.Severity == "" && (askedAboutProfile && clarificationAsks(questions, "severity") || clarificationDetailTargetsProfile(turn.answer, severity, profile.Name, profiles)) {
				profile.Severity = severity
			}
			trigger := firstMatch(triggerPattern, profileScope)
			if profile.Trigger == "" && (askedAboutProfile && clarificationAsks(questions, "trigger") || clarificationDetailTargetsProfile(turn.answer, trigger, profile.Name, profiles)) {
				profile.Trigger = trigger
			}
			relief := firstMatch(reliefPattern, profileScope)
			if profile.RelievingFactors == "" && (askedAboutProfile && clarificationAsks(questions, "trigger") || clarificationDetailTargetsProfile(turn.answer, relief, profile.Name, profiles)) {
				profile.RelievingFactors = relief
			}
			if turn.answer != "" && before != profileDetailKey(*profile) {
				profile.EvidenceQuotes = appendUnique(profile.EvidenceQuotes, turn.answer)
			}
		}
	}
}

func clarificationProfileScope(answer, profileName string) string {
	clauses := strings.FieldsFunc(answer, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,\n", char)
	})
	selected := make([]string, 0, len(clauses))
	includeContinuation := false
	for _, clause := range clauses {
		if strings.Contains(clause, profileName) || containsAny(clause, "这些情况", "这些症状", "上述情况", "上述症状", "以上情况", "以上症状") {
			selected = append(selected, clause)
			includeContinuation = true
			continue
		}
		if mentionsDifferentSymptom(clause, profileName) {
			includeContinuation = false
			continue
		}
		if includeContinuation {
			selected = append(selected, clause)
		}
	}
	if len(selected) == 0 {
		return answer
	}
	return strings.Join(selected, "。")
}

func mentionsDifferentSymptom(value, profileName string) bool {
	for _, rule := range symptomRules {
		if rule.name != profileName && containsAny(value, rule.tokens...) {
			return true
		}
	}
	return false
}

func clarificationDetailTargetsProfile(answer, value, profileName string, profiles []domain.SymptomProfile) bool {
	if value == "" {
		return false
	}
	clauses := strings.FieldsFunc(answer, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,、\n", char)
	})
	for index, clause := range clauses {
		if !strings.Contains(clause, value) {
			continue
		}
		if containsAny(clause, "这些情况", "这些症状", "上述情况", "上述症状", "以上情况", "以上症状") || strings.Contains(clause, profileName) {
			return true
		}
		if containsOtherProfile(clause, profileName, profiles) {
			return false
		}
		if index > 0 && strings.Contains(clauses[index-1], profileName) && !containsOtherProfile(clauses[index-1], profileName, profiles) {
			return true
		}
	}
	return false
}

func containsOtherProfile(value, profileName string, profiles []domain.SymptomProfile) bool {
	for _, profile := range profiles {
		if profile.Name != profileName && strings.Contains(value, profile.Name) {
			return true
		}
	}
	return false
}

func profileDetailKey(profile domain.SymptomProfile) string {
	return strings.Join([]string{
		profile.Onset, profile.Duration, profile.Frequency, profile.Severity,
		profile.Pattern, profile.Trigger, profile.RelievingFactors,
	}, "\x1f")
}

func clarificationAsks(questions, category string) bool {
	switch category {
	case "timeline":
		return containsAny(questions, "什么时候", "开始时间", "最早")
	case "duration":
		return containsAny(questions, "持续多久", "持续时间")
	case "frequency":
		return containsAny(questions, "发作几次", "频率")
	case "severity":
		return containsAny(questions, "严重程度", "不适程度", "有多严重")
	case "trigger":
		return containsAny(questions, "诱发", "缓解", "什么活动", "什么情况下")
	default:
		return false
	}
}

func extractTimeline(quotes []string) []domain.TimelineEvent {
	timeline := make([]domain.TimelineEvent, 0, 3)
	for _, quote := range quotes {
		label := firstMatch(timePattern, quote)
		if label == "" {
			continue
		}
		timeline = append(timeline, domain.TimelineEvent{TimeLabel: label, Event: quote, SourceQuote: quote})
		if len(timeline) == 3 {
			break
		}
	}
	return timeline
}

func extractRiskSignals(input string, quotes []string) []domain.RiskSignal {
	type redFlag struct {
		label  string
		tokens []string
	}
	flags := []redFlag{
		{label: "胸痛", tokens: []string{"胸痛", "胸口疼"}},
		{label: "呼吸困难", tokens: []string{"呼吸困难", "喘不上气", "气短"}},
		{label: "晕厥或接近晕厥", tokens: []string{"晕倒", "晕厥", "差点晕", "眼前发黑"}},
		{label: "意识异常", tokens: []string{"意识不清", "神志不清"}},
	}
	found := make([]string, 0, len(flags))
	quote := ""
	for _, flag := range flags {
		if containsPositive(input, flag.tokens...) {
			found = append(found, flag.label)
			if quote == "" {
				quote = positiveContainingQuote(quotes, flag.tokens...)
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	evidence := strings.Join(found, "、")
	return []domain.RiskSignal{{
		Priority:    domain.PriorityUrgent,
		Title:       "发现需要优先处理的伴随表现",
		Evidence:    evidence,
		Guidance:    "如果这些表现正在发生、明显加重或让你感到无法安全等待，请立即联系当地急救服务；否则也应尽快告知医护人员。",
		SourceQuote: quote,
	}}
}

func missingDetails(profiles []domain.SymptomProfile, input string) ([]string, []string) {
	if len(profiles) == 0 {
		if !strings.Contains(input, "体温") {
			return []string{"是否测量过体温"}, []string{"最近是否测量过体温？如有，请填写数值和时间。"}
		}
		return nil, nil
	}
	fields := make([]string, 0, 6)
	for _, profile := range profiles {
		if profile.Onset == "" {
			fields = appendUnique(fields, profile.Name+"的开始时间")
		}
		if profile.Duration == "" && containsAny(profile.Name, "心悸", "头痛", "腹痛") {
			fields = appendUnique(fields, profile.Name+"每次持续时间")
		}
		if profile.Frequency == "" && profile.Name == "心悸" {
			fields = appendUnique(fields, profile.Name+"的发作频率")
		}
		if profile.Trigger == "" && profile.Name == "心悸" {
			fields = appendUnique(fields, profile.Name+"的诱因与缓解因素")
		}
	}
	if containsAny(input, "心悸", "心慌", "心跳很快") && !mentionsAny(input, "胸痛", "气短", "呼吸困难", "晕倒", "晕厥") {
		fields = append([]string{"是否伴随胸痛、气短或晕厥"}, fields...)
	}
	if containsAny(input, "发热", "发烧") && !strings.Contains(input, "体温") {
		fields = appendUnique(fields, "发热时测量的体温")
	}
	if containsAny(input, "咳嗽") && !strings.Contains(input, "体温") {
		fields = appendUnique(fields, "是否测量过体温")
	}
	if len(fields) > 6 {
		fields = fields[:6]
	}
	questions := make([]string, 0, len(fields))
	for _, field := range fields {
		questions = append(questions, questionForMissingField(field).Text)
	}
	return fields, questions
}

func visitGoal(profiles []domain.SymptomProfile) string {
	if len(profiles) == 0 {
		return "把症状变化和已有信息清楚地告诉医生"
	}
	names := make([]string, 0, 3)
	for _, profile := range profiles {
		names = append(names, profile.Name)
		if len(names) == 3 {
			break
		}
	}
	return "梳理" + strings.Join(names, "、") + "的时间变化、伴随表现和就诊重点"
}

func uniqueSentences(input string) []string {
	parts := sentences(input)
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		part = strings.TrimSpace(strings.TrimPrefix(part, "补充信息："))
		key := normalize(part)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, part)
	}
	return result
}

func sentences(input string) []string {
	return strings.FieldsFunc(input, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;\n", char)
	})
}

func category(value string) string {
	switch {
	case containsAny(value, "药", "服用", "剂量"):
		return "medication"
	case containsAny(value, "过敏"):
		return "allergy"
	case containsAny(value, "检查", "化验", "CT", "核磁"):
		return "test"
	case containsAny(value, "以前", "既往", "病史"):
		return "history"
	case timePattern.MatchString(value):
		return "timeline"
	default:
		return "symptom"
	}
}

func searchTopicFromProfiles(profiles []domain.SymptomProfile, input string) string {
	if len(profiles) > 0 {
		names := make([]string, 0, 3)
		for _, profile := range profiles {
			names = append(names, profile.Name)
			if len(names) == 3 {
				break
			}
		}
		return strings.Join(names, " ")
	}
	return searchTopic(input)
}

func searchTopic(input string) string {
	for _, candidate := range []string{"咳嗽", "头痛", "胃痛", "腹痛", "皮疹", "发热"} {
		if strings.Contains(input, candidate) {
			return candidate
		}
	}
	for _, field := range strings.FieldsFunc(input, func(char rune) bool {
		return unicode.IsSpace(char) || strings.ContainsRune("，。！？；,", char)
	}) {
		if len([]rune(field)) >= 2 && len([]rune(field)) <= 10 {
			return field
		}
	}
	return "门诊沟通"
}

func positiveContainingQuote(quotes []string, tokens ...string) string {
	for _, quote := range quotes {
		if containsPositive(quote, tokens...) {
			return quote
		}
	}
	return ""
}

func symptomScope(quote string, target symptomRule) string {
	segments := strings.FieldsFunc(quote, func(char rune) bool {
		return strings.ContainsRune("，,、；;和", char)
	})
	for index, segment := range segments {
		if !containsPositive(segment, target.tokens...) {
			continue
		}
		scope := []string{strings.TrimSpace(segment)}
		for next := index + 1; next < len(segments); next++ {
			candidate := strings.TrimSpace(segments[next])
			if hasDifferentSymptom(candidate, target) && !isAssociationClause(candidate) {
				break
			}
			scope = append(scope, candidate)
		}
		return strings.Join(scope, "，")
	}
	return quote
}

func hasDifferentSymptom(value string, target symptomRule) bool {
	for _, rule := range symptomRules {
		if rule.name != target.name && containsPositive(value, rule.tokens...) {
			return true
		}
	}
	return false
}

func isAssociationClause(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "伴有") || strings.HasPrefix(value, "伴随") ||
		strings.HasPrefix(value, "同时") || strings.HasPrefix(value, "并伴")
}

func firstMatch(pattern *regexp.Regexp, value string) string {
	match := pattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func firstPositiveToken(value string, candidates ...string) string {
	for _, candidate := range candidates {
		if containsPositive(value, candidate) {
			return candidate
		}
	}
	return ""
}

func firstContained(value string, candidates ...string) string {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return candidate
		}
	}
	return ""
}

func mentionsAny(value string, candidates ...string) bool {
	return containsAny(value, candidates...)
}

func containsPositive(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		remaining := value
		for {
			index := strings.Index(remaining, candidate)
			if index < 0 {
				break
			}
			if !negatedByPrefix(remaining[:index]) {
				return true
			}
			remaining = remaining[index+len(candidate):]
		}
	}
	return false
}

func isNegated(value string, candidates ...string) bool {
	mentioned := false
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			mentioned = true
		}
	}
	return mentioned && !containsPositive(value, candidates...)
}

func negatedByPrefix(prefix string) bool {
	clauseStart := 0
	for _, delimiter := range []string{"，", ",", "。", "；", ";", "！", "!", "？", "?", "但", "不过", "然而", "却"} {
		if index := strings.LastIndex(prefix, delimiter); index >= clauseStart {
			clauseStart = index + len(delimiter)
		}
	}
	clause := prefix[clauseStart:]
	for _, negation := range []string{"没有", "无", "否认", "未出现", "不伴", "未见", "未感到", "从未", "不存在"} {
		if strings.Contains(clause, negation) {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func normalize(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) || strings.ContainsRune("，。！？；,.!?;：:", char) {
			return -1
		}
		return unicode.ToLower(char)
	}, strings.TrimSpace(value))
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
