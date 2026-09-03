package guard

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var piiPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "email", pattern: regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)},
	{name: "mobile_phone", pattern: regexp.MustCompile(`(?:^|[^0-9])(?:\+?86[\s-]?)?1[3-9][0-9][\s-]?[0-9]{4}[\s-]?[0-9]{4}(?:$|[^0-9])`)},
	{name: "china_identity_card", pattern: regexp.MustCompile(`(?:^|[^0-9])[1-9][0-9]{5}(?:18|19|20)[0-9]{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx](?:$|[^0-9A-Za-z])`)},
	{name: "explicit_name", pattern: regexp.MustCompile(`(?:我叫|姓名\s*[:：]?\s*|就诊人\s*[:：]?\s*|联系人\s*[:：]?\s*)[\p{Han}]{2,4}`)},
	{name: "street_address", pattern: regexp.MustCompile(`(?:住址|家庭地址|详细地址|地址\s*[:：])\s*[:：]?\s*[\p{Han}A-Za-z0-9\-]{6,}`)},
	{name: "landline_phone", pattern: regexp.MustCompile(`(?:^|[^0-9])0[0-9]{2,3}-?[0-9]{7,8}(?:$|[^0-9])`)},
	{name: "medical_record_number", pattern: regexp.MustCompile(`(?:就诊卡|医保卡|病历号|检查号|住院号)\s*[:：]?\s*[A-Za-z0-9\-]{6,}`)},
	{name: "wechat_id", pattern: regexp.MustCompile(`(?i)(?:微信|微信号|wechat)\s*[:：]?\s*[a-z][-_a-z0-9]{5,19}`)},
	{name: "passport_number", pattern: regexp.MustCompile(`(?i)(?:护照号?|passport)\s*[:：]?\s*[a-z][a-z0-9]{7,8}`)},
	{name: "residential_address", pattern: regexp.MustCompile(`(?:我住在|现住|居住在)\s*[\p{Han}A-Za-z0-9\-]{6,}`)},
}

const commonSingleSurnames = `赵钱孙李周吴郑王冯陈褚卫蒋沈韩杨朱秦尤许何吕施张孔曹严华金魏陶姜戚谢邹喻柏窦章云苏潘葛奚范彭郎鲁韦昌马苗方俞任袁柳鲍史唐费廉岑薛雷贺倪汤滕殷罗毕郝邬安常乐于时傅皮卞齐康伍余元卜顾孟平黄穆萧尹姚邵汪祁毛米贝明臧计伏成戴谈宋茅庞熊纪舒屈项祝董梁杜阮蓝闵席季麻强贾路娄危江童颜郭梅盛林刁钟徐邱骆高夏蔡田樊胡凌霍虞万柯管卢莫房解应宗丁宣邓郁单杭洪包诸左石崔吉龚程裴陆荣翁荀羊惠甄曲家封储靳段富巫乌焦巴牧山谷车侯全班仰秋仲伊宫宁仇栾甘厉祖武符刘景詹束龙叶司韶郜黎白怀蒲鄂索赖卓蔺屠蒙池乔翟谭贡劳姬申冉雍桑桂牛寿边燕尚农温别庄晏柴瞿阎连茹艾鱼容向古易廖终居步都耿满弘匡国文寇广东欧沃利蔚越师巩聂晁敖融冷辛那简饶曾沙鞠丰关相查红游权盖益桓公`

var patientNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`患者\s*[:：]?\s*([` + commonSingleSurnames + `][\p{Han}]{1,3})(?:$|[\s，。！？、；：,])`),
	regexp.MustCompile(`患者\s*[:：]?\s*((?:欧阳|太史|端木|上官|司马|东方|独孤|南宫|万俟|闻人|夏侯|诸葛|尉迟|公羊|赫连|澹台|皇甫|宗政|濮阳|公冶|太叔|申屠|公孙|慕容|仲孙|钟离|长孙|宇文|司徒|鲜于|司空|闾丘|子车|亓官|司寇|巫马|公西|颛孙|壤驷|公良|漆雕|乐正|宰父|谷梁|拓跋|夹谷|轩辕|令狐|段干|百里|呼延|东郭|南门|羊舌|微生)[\p{Han}]{1,2})(?:$|[\s，。！？、；：,])`),
}

var patientConditionMarkers = []string{
	"血压", "血糖", "病", "症", "炎", "癌", "瘤", "痛", "热", "咳", "喘", "龄", "感染",
}

var generalMedicalTopics = []string{
	"呼吸困难", "关节痛", "偏头痛", "咳嗽", "咳痰", "发热", "头痛", "腹痛", "胃痛",
	"胸痛", "皮疹", "瘙痒", "腹泻", "便秘", "恶心", "呕吐", "眩晕", "头晕", "心悸",
	"失眠", "腰痛", "背痛", "气短", "鼻塞", "流涕", "咽痛", "血压", "血糖", "尿频", "水肿",
}

var controlPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+instructions?`),
	regexp.MustCompile(`忽略(?:之前|以上|所有)?(?:的)?(?:指令|要求|提示词)`),
	regexp.MustCompile(`(?:系统|开发者)提示词`),
}

func ScanPII(input string) []string {
	findings := make([]string, 0, len(piiPatterns))
	for _, candidate := range piiPatterns {
		if candidate.pattern.MatchString(input) {
			findings = append(findings, candidate.name)
		}
	}
	for _, pattern := range patientNamePatterns {
		for _, match := range pattern.FindAllStringSubmatch(input, -1) {
			if len(match) == 2 && !containsAny(match[1], patientConditionMarkers) {
				findings = append(findings, "explicit_name")
				return findings
			}
		}
	}
	return findings
}

func containsAny(input string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(input, candidate) {
			return true
		}
	}
	return false
}

func SanitizeSearchQuery(input string, maxRunes int) string {
	cleaned := input
	for _, candidate := range piiPatterns {
		cleaned = candidate.pattern.ReplaceAllString(cleaned, " ")
	}
	for _, pattern := range controlPatterns {
		cleaned = pattern.ReplaceAllString(cleaned, " ")
	}
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if maxRunes <= 0 || utf8.RuneCountInString(cleaned) <= maxRunes {
		return cleaned
	}
	return string([]rune(cleaned)[:maxRunes])
}

// GeneralizeSearchQuery reduces an untrusted model proposal to approved broad
// health topics, preventing patient narratives and record numbers from leaving the service.
func GeneralizeSearchQuery(input string, maxTopics int) string {
	if maxTopics <= 0 {
		return ""
	}
	cleaned := SanitizeSearchQuery(input, 120)
	topics := make([]string, 0, maxTopics)
	for _, topic := range generalMedicalTopics {
		if strings.Contains(cleaned, topic) {
			topics = append(topics, topic)
			if len(topics) == maxTopics {
				break
			}
		}
	}
	if len(topics) == 0 {
		return ""
	}
	return strings.Join(topics, " ") + " 就诊准备"
}
