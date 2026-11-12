package swarm

import (
	"strings"
)

// ---- 硬规则三档判定位引擎(P4-63 A,问题清单 #85) ----
//
// v1.2.1 误伤实锤:URL 路径 /actuator/shutdown 与代码字符串 "truncate -2"
// 被当破坏命令、单位被杀。修法沿用 P4-50 围栏收窄的哲学——**防误伤不降
// 底线,防漂移不防规避**:这些匹配面本拦不住真越界(变形/编码/拼接一概
// 穿透),只会误伤正常测试;判定收窄到确定性动作位,真破坏形态仍拒。
//
// 三档判定位标准(判定规则语义为契约,禁按实锤样本字符串特判):
//   - 动作位:能确定性归入"命令被执行/SQL 语句被执行"的位置——系统破坏
//     类只认命令动词位(命令行首词、管道/分号/&& 后首个词、$( 与反引号
//     命令替换内、sudo/systemctl 等服务管理语境的动词位);数据库破坏类
//     只认 SQL 语句语境(SQL 关键词组合且处语句位)→ 拒绝留痕;
//   - 非动作位:URL 路径、带引号字符串字面量、参数值、注释行——词在此
//     不是被执行的动作 → 放行(不当动作,留痕走 tool_call 执行记录);
//   - 模糊形态:两档都归不进(如 echo/管道喂给解释器的词、nohup/xargs
//     后的词——是否被执行取决于前一个命令的语义,静态不可判)→ 弹门
//     人审,人即兜底。
//
// 收窄作用于组(判定引擎的匹配语义,不改规则行数据):系统破坏命令组与
// 数据库破坏组;其余组(HTTP 破坏/批量清空等)命中即动作位,语义不变。
// 组内自建规则同享组的判位语义(组即语义类)。
//
// 钉死的边界(如实):带引号字符串字面量归非动作位意味着 bash -c
// "shutdown" 这类"引号内真执行"形态放行、引号内容首的 SQL 组合词照拒
// (print "DROP TABLE x" 会被拒)——前者是操作者三档标准的字面推论,
// 与"防漂移不防规避"一致(真越界者写引号内的概率与写 base64 等同);
// 后者保 mysql -e "TRUNCATE TABLE x" 与注入分号后语句仍拒,残余误伤由
// B"拒动作不杀单位"降为一次换路成本。禁止为穷尽这些形态扩大匹配面。

// hardTier 一次硬规则命中的判定位三档。
type hardTier int

const (
	hardTierAction    hardTier = iota // 动作位:确定性拒绝
	hardTierAmbiguous                 // 模糊形态:弹门人审
	hardTierNonAction                 // 非动作位:放行(不当动作)
)

// narrowedHardGroups 收窄组(判定按三档判定位):系统破坏类只认命令动词
// 位,数据库破坏类只认 SQL 语句语境。
var narrowedHardGroups = map[string]bool{
	HardGroupSysDestroy: true,
	HardGroupDBDestroy:  true,
}

// judgeMatchPosition 判定一次命中在 value 内 matchStart 处的判定位。
// 非收窄组命中即动作位(现行语义);收窄组按组语义做位置分析。
func judgeMatchPosition(value string, matchStart int, matched string, group string) hardTier {
	if !narrowedHardGroups[group] {
		return hardTierAction
	}
	if group == HardGroupDBDestroy {
		return judgeSQLStatement(value, matchStart, matched)
	}
	return judgeCommandVerb(value, matchStart)
}

// judgeCommandVerb 系统破坏类判位:matchStart 处的词是否处在命令动词位。
func judgeCommandVerb(value string, start int) hardTier {
	if inCommentLine(value, start) {
		return hardTierNonAction
	}
	st := scanQuotes(value, start)
	if st.inSingle {
		return hardTierNonAction // 单引号字面量:内部无任何执行语义
	}
	// 双引号内的 $(...) / 反引号命令替换照常执行——剥掉外层引号语境,
	// 以替换内容的起点为"值首"重判(匹配处在替换内才发生)。
	if st.inDouble && st.substStart >= 0 {
		return judgeCommandVerb(value[st.substStart:], start-st.substStart)
	}
	j, spaced := backScan(value, start)
	if j < 0 || value[j] == '\n' {
		return hardTierAction // 命令行首词(值首/行首)
	}
	switch c := value[j]; {
	case c == '|' || c == ';' || c == '&' || c == '`':
		return hardTierAction // 管道/分号/&/命令替换后的首个词
	case c == '(' && j > 0 && value[j-1] == '$':
		return hardTierAction // $( 命令替换内的命令首词
	case c == '"' || c == '\'':
		return hardTierNonAction // 带引号字符串字面量的内容首(三档标准归非动作位)
	case isWordRune(c):
		if !spaced {
			return hardTierNonAction // 词中段:URL 路径/参数值/标识符
		}
		// 隔空白的前一个词:服务管理语境的动词位是动作;其余词
		// (echo/nohup/xargs/env…)是否执行后词取决于该命令语义,
		// 静态不可判——模糊弹门。
		if w := wordEndingAt(value, j); serviceMgrWords[w] {
			return hardTierAction
		}
		if st.inDouble {
			return hardTierNonAction // 双引号字面量中段的词
		}
		return hardTierAmbiguous
	default:
		return hardTierAmbiguous // 裸 '(' 与其余符号语境:两档都归不进
	}
}

// serviceMgrWords 服务管理语境词:其后首个词是该语境要执行的动词
// (sudo shutdown / systemctl reboot / sudo systemctl poweroff)。
var serviceMgrWords = map[string]bool{
	"sudo": true, "doas": true, "systemctl": true, "service": true,
}

// judgeSQLStatement 数据库破坏类判位:命中是否构成 SQL 语句语境——
// SQL 关键词组合(相邻两关键词)且处语句位(值首/行首/分号后/引号内容
// 首)。组合缺位即非语句形态(print 标签/切片/标识符),放行。
func judgeSQLStatement(value string, start int, matched string) hardTier {
	if inCommentLine(value, start) {
		return hardTierNonAction
	}
	if !hasSQLKeywordPair(matched) {
		return hardTierNonAction // 不是 SQL 关键词组合:代码字符串/标签/切片不算
	}
	st := scanQuotes(value, start)
	if st.inDouble && st.substStart >= 0 {
		return judgeSQLStatement(value[st.substStart:], start-st.substStart, matched)
	}
	j, spaced := backScan(value, start)
	if j < 0 || value[j] == '\n' || value[j] == ';' {
		return hardTierAction // 语句位:脚本/多行语句首、分号后第二条
	}
	switch c := value[j]; {
	case c == '"' || c == '\'':
		return hardTierAction // 语句位:引号内容首(mysql -e "TRUNCATE…"/HTTP 包体语句)
	case isWordRune(c):
		if !spaced {
			return hardTierNonAction // 词中段:参数值/URL(query=DROP 形态按参数值放行)
		}
		if st.inSingle || st.inDouble {
			return hardTierNonAction // 字符串字面量中段的组合词(句中提及不是语句)
		}
		return hardTierAmbiguous // echo "…" | mysql 类:喂给解释器与否不可静态判定
	default:
		return hardTierAmbiguous
	}
}

// sqlKeywords SQL 语句/结构关键词集合(类级词表):命中文本里存在相邻两词
// 均属此表,才算 SQL 关键词组合。覆盖内置五条(DROP TABLE/TRUNCATE
// TABLE/DROP DATABASE/DELETE FROM/ALTER TABLE…DROP)与组内自建规则
// (DROP INDEX/RENAME TABLE 等)。
var sqlKeywords = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true,
	"drop": true, "truncate": true, "alter": true, "create": true,
	"replace": true, "rename": true, "grant": true, "revoke": true,
	"table": true, "tables": true, "database": true, "schema": true,
	"index": true, "view": true, "from": true, "into": true,
	"values": true, "where": true, "set": true,
}

// hasSQLKeywordPair 命中文本分词后是否存在相邻两词均为 SQL 关键词。
func hasSQLKeywordPair(matched string) bool {
	words := strings.FieldsFunc(strings.ToLower(matched), func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	for i := 1; i < len(words); i++ {
		if sqlKeywords[words[i-1]] && sqlKeywords[words[i]] {
			return true
		}
	}
	return false
}

// quoteState 前向扫描到某位置时的引号语境。
type quoteState struct {
	inSingle   bool // 处在未闭合单引号内
	inDouble   bool // 处在未闭合双引号内
	substStart int  // 双引号区域内未闭合 $( / 反引号 命令替换的内容起点;-1=无
}

// scanQuotes 前向扫描 value 至 idx,得出该位置的引号语境(未闭合即视为
// 跨到结尾,任意输入都有确定输出)。双引号内 $ 与反引号开启命令替换、
// ) 闭合;$ 转义按双引号内反斜杠处理;单引号内一切字面。
func scanQuotes(value string, idx int) quoteState {
	st := quoteState{substStart: -1}
	depth := 0 // 命令替换嵌套深度(仅双引号内有效)
	for i := 0; i < idx; i++ {
		c := value[i]
		switch {
		case st.inSingle:
			if c == '\'' {
				st.inSingle = false
			}
		case st.inDouble:
			switch {
			case c == '\\' && i+1 < idx:
				i++ // 双引号内反斜杠转义下一字符
			case c == '"':
				st.inDouble = false
			case depth > 0 && c == ')':
				depth--
				if depth == 0 {
					st.substStart = -1
				}
			case depth == 0 && (c == '`' || c == '$' && i+1 < idx && value[i+1] == '('):
				depth = 1
				if c == '`' {
					st.substStart = i + 1
				} else {
					st.substStart = i + 2
					i++ // 跳过 (
				}
			}
		default:
			switch c {
			case '\'':
				st.inSingle = true
			case '"':
				st.inDouble = true
			}
		}
	}
	return st
}

// backScan 从 start-1 向前跳过空格与制表符,返回最近的非空白位置与是否
// 隔了空白(j<0 = 值首)。
func backScan(value string, start int) (j int, spaced bool) {
	j = start - 1
	for j >= 0 && (value[j] == ' ' || value[j] == '\t') {
		j--
		spaced = true
	}
	return j, spaced
}

// wordEndingAt 取 value[<=j] 处向前到空白/换行/引号/命令分隔符为止的词。
func wordEndingAt(value string, j int) string {
	k := j
	for k >= 0 {
		c := value[k]
		if c == ' ' || c == '\t' || c == '\n' || c == '|' || c == ';' || c == '&' ||
			c == '(' || c == ')' || c == '"' || c == '\'' || c == '`' {
			break
		}
		k--
	}
	return value[k+1 : j+1]
}

// isWordRune 词字符(命令词与路径/参数值共用字符集:字母数字与
// _ - . : = / ~ + , % ? @ # ! * [ ] { } < > ^)。URL 路径(/actuator/
// shutdown)、参数值(--flag=值)、标识符(token_id)的词内字符。
func isWordRune(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("_-.:=/~+,%?@#!*[]{}<>^", c) >= 0
}

// inCommentLine idx 是否落在注释行内:行首(# 之后)首个非空白字符为 #
// 或 //(shell/Python 注释与 JS/Java 注释),该行整体不是动作。
func inCommentLine(value string, idx int) bool {
	lineStart := strings.LastIndexByte(value[:idx], '\n') + 1
	rest := value[lineStart:]
	trimmed := strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//")
}
