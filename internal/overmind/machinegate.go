package overmind

// P4-24 ②(问题清单 #39):模式闸门——机器语"特征"扫描,替代词表维护。
// 作用域 = 定稿冻结的模型文本(复现步骤描述 P4-08 ④ / 来龙去脉叙述
// P4-13),词表管不住模型新写的句子,特征制管得住:内部 ID、解释编号、
// 裸版本号数字、程序报错原文、英文状态词、无动词压缩名词堆。只判定
// 不改文;不合格由调用方退回同会话重写一次,再不合格如实标注
// (Unpolished / NarrativeUnpolished)不阻塞定稿。确定性、零 LLM、
// 可单测;静态界面串的同类特征由构建闸门(web/scripts/check-ui-words)
// 承载,这里管运行时模型文本。

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// talkHit 一次机器语特征命中:特征名(操作者语言)+ 命中样例(截断,
// 喂改写指令与日志,不整段回显)。
type talkHit struct {
	Kind   string
	Sample string
}

// machineTalkPatterns 特征模式一:带形状的 token(内部 ID / 解释编号 /
// 裸版本号数字 / 程序报错串)。纯 ASCII 模式与 CJK 模式混排;英文状态
// 词单独扫(machineTalkStatusWords,先掩掉 URL 与路径再判,防
// "?status=…"、"/api/status" 类误伤)。
var machineTalkPatterns = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`\btc-(?:chain-)?[0-9a-z]{6,}`), "内部编号"},
	{regexp.MustCompile(`\balt\d+\b`), "解释编号"},
	{regexp.MustCompile(`版本\s*\d`), "裸版本号数字"},
	{regexp.MustCompile(`\bEOF\b`), "程序报错原文"},
	{regexp.MustCompile(`Get "|Post "`), "程序报错原文"},
	{regexp.MustCompile(`(?i)context deadline exceeded|connection refused|dial tcp|no such host|panic:`), "程序报错原文"},
}

// machineTalkURLRe 英文状态词判定前掩掉的 URL / 路径 token(含 "/" 的
// 连续 ASCII 段;掩掉后按整词判状态词)。
var machineTalkURLRe = regexp.MustCompile(`[A-Za-z0-9_.?=&%:@~+-]*\/[A-Za-z0-9_.?=&%:@~+-]*`)

// machineTalkStatusRe 英文状态词(整词;系统状态机的操作者不可读原词,
// 界面词表已有人话对照,叙述里出现即机器语)。active/done 这类高误伤
// 词不入表(URL / 产品名常撞);先掩 URL 与路径再判。
var machineTalkStatusRe = regexp.MustCompile(
	`(?i)\b(status|running|dispatched|confirmed|pending|exhausted|blocked|denied|failed|success|error|timeout|completed|awaiting_round|testing)\b`)

// barePortSubjectRe 句首裸端口数字(P4-27 ③,问题清单 #44):2–5 位
// 数字直接落在主语位置(句首/分句首),脱离上下文读不出说的是哪个
// 目标。候选命中后再由 barePortKind 排除误伤(IP 起头/量词计数);
// 冒号不作引导符——host:port 形态的端口数字天然跟在冒号后。
var barePortSubjectRe = regexp.MustCompile(`(?:^|[。！？；\n!?;][ \t]*|[，,][ \t]*)(\d{2,5})[ \t]*`)

// barePortCounterWords 量词计数与时间词——句首数字带这些量词时是正常
// 计数("3 个端口开放"),不是裸端口主语。
const barePortCounterWords = "个条次轮步项张份秒分时天字处种位名次台条"

// barePortKind 判定一段文本里是否有句首裸端口。RE2 无环视,候选由
// 正则抓出、排除规则在代码里做:跳过尾随空白后是量词/计数词的放过
// ("20 个测试全部通过"是正常计数),紧邻 "."(IP 地址段起头,如
// 10.0.0.5)或 ":"(host:port 形态)的放过。
func barePortKind(text string) (talkHit, bool) {
	for _, loc := range barePortSubjectRe.FindAllStringSubmatchIndex(text, -1) {
		digitsStart, digitsEnd := loc[2], loc[3]
		if digitsEnd < len(text) {
			after := rune(text[digitsEnd])
			if after == '.' || after == ':' {
				continue // IP 段起头(10.0.0.5)或 host:port 已带冒号形态
			}
		}
		rest := digitsEnd
		for rest < len(text) && (text[rest] == ' ' || text[rest] == '\t') {
			rest++
		}
		if rest < len(text) {
			if r, _ := utf8.DecodeRuneInString(text[rest:]); strings.ContainsRune(barePortCounterWords, r) {
				continue // 计数量词("20 个测试"),不是端口主语
			}
		} else {
			continue
		}
		sampleEnd := rest + 10
		if sampleEnd > len(text) {
			sampleEnd = len(text)
		}
		return talkHit{Kind: "句首裸端口数字", Sample: clipLine(text[digitsStart:sampleEnd], 40)}, true
	}
	return talkHit{}, false
}

// talkFuncChars 功能字集(的/了/是/在/与…):自然中文短句几乎必含其一;
// 长段零功能字 = 电报体压缩名词堆的判定信号(见 machineTalkPiles)。
// 只收近乎纯功能用的字:方位字(中/上/下/里/前后)会误护"命中/登录
// 跳转"类动词复合词、"对"会误护"比对/核对/校对",均不入集;量词"一"
// 入集(一轮/一条是自然计数句)。
const talkFuncChars = "的了是在有和与及或者且而并从被把让向往跟给于由以为因所之其这那各每某一已未无没也很就都还再又只才即地得着过会能可要需该应等"

// talkPileMin 压缩名词堆判定的最短汉字段长(更短的零功能字段是正常
// 紧凑中文,如"仲裁者判定成立";≥8 字零功能字才视为电报体)。
const talkPileMin = 8

// ScanMachineTalk 扫一段定稿文本,返回全部机器语特征命中(顺序:带
// 形状 token → 英文状态词 → 句首裸端口 → 压缩名词堆);零命中 = 过闸。
func ScanMachineTalk(text string) []talkHit {
	var hits []talkHit
	for _, p := range machineTalkPatterns {
		if m := p.re.FindString(text); m != "" {
			hits = append(hits, talkHit{Kind: p.kind, Sample: clipLine(m, 40)})
		}
	}
	if m := machineTalkStatusRe.FindString(machineTalkURLRe.ReplaceAllString(text, " ")); m != "" {
		hits = append(hits, talkHit{Kind: "英文状态词", Sample: clipLine(m, 40)})
	}
	if h, hit := barePortKind(text); hit {
		hits = append(hits, h)
	}
	hits = append(hits, machineTalkPiles(text)...)
	return hits
}

// machineTalkPiles 无动词压缩名词堆:按标点与空白切段,段内汉字数
// ≥talkPileMin 且全程零功能字(无"的/了/是/与…"任一)视为电报体。
// 判据是"长而零功能字"——短的无功能字段是正常中文紧凑句,不误伤。
func machineTalkPiles(text string) []talkHit {
	var hits []talkHit
	for _, seg := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		han := 0
		for _, r := range seg {
			if unicode.Is(unicode.Han, r) {
				han++
			}
		}
		if han < talkPileMin {
			continue
		}
		if !strings.ContainsAny(seg, talkFuncChars) {
			hits = append(hits, talkHit{Kind: "无动词压缩名词堆", Sample: clipLine(seg, 40)})
		}
	}
	return hits
}

// talkHitLines 命中清单 → 改写指令用的逐条行("特征名:样例")。
func talkHitLines(hits []talkHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Kind+": "+h.Sample)
	}
	return out
}
