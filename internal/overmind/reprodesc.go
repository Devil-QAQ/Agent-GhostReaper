package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// P4-08 ④:收割定稿的复现步骤描述改指令口径——确认裁决落账前,对保留
// 步骤融合请求与响应事实,由模型写一句可照做的自然语言指令("向 X 发
// GET /路径,无需凭据;返回 200 ＋ …即复现"),冻结入账本;重开再确认
// 才重新生成(AssembleRepro 随新裁决重跑,本生成随之重跑)。报告导出
// 与面板渲染零 LLM(R17 不破:智能发生在写回时;描述是包装不是证据,
// 与机器字段矛盾时以原文为准)。
//
// P4-35 ①②(问题清单 #57):同一次生成扩为最小集选择 + 三层结构——
// 模型从去同型候选池里挑出构成复现流程的必要步骤(一步漏洞恰 1 条;
// 链式复现 N 条;对比交换/反证变体/枚举试错不选),并写每步短名
// ("① 拿会话凭据"式标题段)与作用(这步在复现里干什么);未选中的
// 交换只在 Process 引用清单留档(仓库与证据包全量,面板不进)。
//
// P4-62 A(问题清单 #83:兜底直出废除):挑选失败与空选不再倒池——
// 复现区只呈现模型挑选过的最小集。模型调用/解析失败重试 1 次(同会话
// 二次请求),再失败如实标注"复现包未生成(挑选失败)";模型空选 =
// 池中无可证明漏洞的包,如实呈现"无可用复现包＋原因";候选池为空时
// 直接给确定性原因句(零外呼)。任何情况下未经模型挑选的交换不进
// 复现区(确定性装配只建池与过程清单)。

// reproDescClip 单步描述(做什么)的落账上限(超长截断;面板与报告同文)。
const reproDescClip = 400

// reproShortClip / reproPurposeClip 短名与作用的落账上限(P4-35 ②:
// 短名是标题段 ≤8 字、作用是一句,超长截断)。
const reproShortClip = 24

const reproPurposeClip = 160

// reproNoReproFailed 挑选失败(重试一次后仍失败)的固定标注(子计划
// P4-62 定稿文案)。
const reproNoReproFailed = "复现包未生成（挑选失败）"

// reproNoReproPrefix 无可用复现包原因句前缀。
const reproNoReproPrefix = "无可用复现包："

// reproFactAskCap 喂给模型的候选交换上限(账次序取首尾各半——枚举型
// 反证候选可达数百条,链式流程的关键交换在两端;超出的候选模型选不
// 到,如实按挑选失败处理,不倒池)。
const reproFactAskCap = 60

// reproFactBodyClip 喂给模型的请求/响应正文头截取(指令只需关键内容,
// 全文在 blob)。
const reproFactBodyClip = 256

// reproDescStep 模型回复契约里的单步选择与三层描述。
type reproDescStep struct {
	Seq     int    `json:"seq"`
	Short   string `json:"short"`
	Desc    string `json:"desc"`
	Purpose string `json:"purpose"`
}

// pickReproSteps 定稿最小集挑选与三层描述(P4-35 选择 + P4-62 A 兜底
// 废除):模型从 rec.Steps(去同型候选池)挑必要步骤并写短名/做什么/
// 作用,选中者按账本次序保留候选 Seq。挑选结果三态,总保证"未经模型
// 挑选的交换不进复现区":
//   - 挑中:rec.Steps = 挑中步骤(缺省字段退该步确定性兜底);
//   - 空选:模型判定池中无可证明漏洞的包,rec.Steps 清空、NoReproReason
//     = 模型理由(过闸门;不过退确定性原因句);
//   - 失败:调用/解析失败重试 1 次后仍失败,rec.Steps 清空、
//     NoReproReason = reproNoReproFailed。
//
// 候选池为空(零原料)或未配模型时零外呼,直接确定性原因句。P4-24:
// 挑中后接定稿校对+模式闸门(同会话,见 proofread.go)。
func (l *Loop) pickReproSteps(ctx context.Context, claimID string, rec *rings.TestClaimRecord, diag ReproPoolDiag) {
	if len(rec.Steps) == 0 {
		rec.Steps = nil
		rec.NoReproReason = noReproReason(diag, "")
		return
	}
	if l.Provider == nil {
		rec.Steps = nil
		rec.NoReproReason = noReproReason(diag, "")
		runlog.Warnf("定稿最小集挑选模型不可用: 任务 %s 判断 %s(复现区如实标注未生成)",
			l.TaskID, claimID)
		return
	}
	claimTitle := ""
	if n, err := l.Store.Node(ctx, l.TaskID, claimID); err == nil {
		claimTitle = n.Title
	}
	facts := reproFactClip(l.reproFacts(rec))
	gs := l.newGenSession(reproDescSystem)
	defer gs.close()
	head := reproAskHead(claimTitle, facts, len(rec.Steps))
	picks, skipReason, err := l.askReproDescs(ctx, gs, head)
	if err != nil {
		runlog.Warnf("定稿最小集挑选失败,重试一次: 任务 %s 判断 %s 原因 %s",
			l.TaskID, claimID, clipEval(err.Error()))
		picks, skipReason, err = l.askReproDescs(ctx, gs, head)
		if err != nil {
			rec.Steps = nil
			rec.NoReproReason = reproNoReproFailed
			runlog.Warnf("定稿最小集挑选重试仍失败,复现区标注未生成: 任务 %s 判断 %s 原因 %s",
				l.TaskID, claimID, clipEval(err.Error()))
			return
		}
	}
	if len(picks) == 0 {
		// 模型空选:池中无可证明漏洞的包(模型的判断成立,不再倒池)。
		rec.Steps = nil
		rec.NoReproReason = noReproReason(diag, skipReason)
		runlog.Infof("定稿最小集为空(模型判定池中无可证明漏洞的包): 任务 %s 判断 %s 原因 %s",
			l.TaskID, claimID, rec.NoReproReason)
		return
	}
	if !applyReproPick(rec, picks) {
		// 选择全部未命中候选池(契约违背):按挑选失败重试一次。
		runlog.Warnf("定稿最小集选择未命中复现池,重试一次: 任务 %s 判断 %s", l.TaskID, claimID)
		picks, skipReason, err = l.askReproDescs(ctx, gs, head)
		if err != nil || !applyReproPick(rec, picks) {
			rec.Steps = nil
			rec.NoReproReason = reproNoReproFailed
			runlog.Warnf("定稿最小集选择重试仍未命中,复现区标注未生成: 任务 %s 判断 %s", l.TaskID, claimID)
			return
		}
		_ = skipReason
	}
	l.polishReproDescs(ctx, claimID, gs, rec)
}

// noReproReason 无可用复现包的原因句:模型理由过文风闸门则用模型句,
// 否则(及池空/理由缺失)按入池诊断确定性拼装(8.5:陈述事实、数字
// 精确、能短则短)。
func noReproReason(diag ReproPoolDiag, modelReason string) string {
	if s := clipLine(strings.TrimSpace(modelReason), reproPurposeClip); s != "" && len(ScanMachineTalk(s)) == 0 {
		return reproNoReproPrefix + s
	}
	var parts []string
	if diag.ErrExchanges > 0 {
		parts = append(parts, fmt.Sprintf("%d 条交换未获响应", diag.ErrExchanges))
	}
	if diag.OffAnchor > 0 {
		parts = append(parts, fmt.Sprintf("%d 条交换的目标不是判断锚定的资产", diag.OffAnchor))
	}
	if diag.ToolSkipped > 0 {
		parts = append(parts, fmt.Sprintf("%d 条直连命令无法还原请求包", diag.ToolSkipped))
	}
	if len(parts) == 0 {
		return reproNoReproPrefix + "池内交换均不能证明该问题成立"
	}
	return reproNoReproPrefix + strings.Join(parts, "；") + "。"
}

// applyReproPick 应用模型的最小集选择(P4-35 ①):seq 须命中候选池
// (未命中的条目丢弃),选中步骤保留候选 Seq 与账本次序(不重编——校对
// 轮的 seq 契约"与你上一轮一致"靠它成立;呈现序号按列表位置,读侧
// reproStepHead 定);short/desc/purpose 缺省退该步确定性兜底;返回是
// 否存在有效选择(全未命中 = false,调用方按挑选失败处理)。
func applyReproPick(rec *rings.TestClaimRecord, picks []reproDescStep) bool {
	cand := make([]rings.ReproStep, len(rec.Steps))
	copy(cand, rec.Steps)
	chosen := make([]rings.ReproStep, 0, len(picks))
	for _, st := range cand {
		for _, p := range picks {
			if p.Seq != st.Seq {
				continue
			}
			if s := clipLine(strings.TrimSpace(p.Short), reproShortClip); s != "" {
				st.Short = s
			}
			if d := clipLine(strings.TrimSpace(p.Desc), reproDescClip); d != "" {
				st.Desc = d
			}
			if pu := clipLine(strings.TrimSpace(p.Purpose), reproPurposeClip); pu != "" {
				st.Purpose = pu
			}
			chosen = append(chosen, st)
			break
		}
	}
	if len(chosen) == 0 {
		return false
	}
	rec.Steps = chosen
	return true
}

// applyReproTexts 只改字段不动选择(校对/重写轮用):按 seq 覆盖
// short/desc/purpose(缺省保留上一稿),Steps 成员与序号不变。
func applyReproTexts(rec *rings.TestClaimRecord, steps []reproDescStep) {
	bySeq := map[int]reproDescStep{}
	for _, d := range steps {
		if d.Seq > 0 {
			bySeq[d.Seq] = d
		}
	}
	for i := range rec.Steps {
		d, ok := bySeq[rec.Steps[i].Seq]
		if !ok {
			continue
		}
		if s := clipLine(strings.TrimSpace(d.Short), reproShortClip); s != "" {
			rec.Steps[i].Short = s
		}
		if t := clipLine(strings.TrimSpace(d.Desc), reproDescClip); t != "" {
			rec.Steps[i].Desc = t
		}
		if pu := clipLine(strings.TrimSpace(d.Purpose), reproPurposeClip); pu != "" {
			rec.Steps[i].Purpose = pu
		}
	}
}

// reproFact 单步事实(喂模型的输入;凭据只报有无与形态,值不进)。
type reproFact struct {
	Seq        int    `json:"seq"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Cred       string `json:"cred"`
	ReqBody    string `json:"req_body,omitempty"`
	ReqBytes   int64  `json:"req_body_bytes,omitempty"`
	RespStatus int    `json:"resp_status"`
	RespBytes  int64  `json:"resp_bytes,omitempty"`
	RespType   string `json:"resp_type,omitempty"`
	RespHead   string `json:"resp_head,omitempty"`
	Location   string `json:"location,omitempty"`
	Note       string `json:"note,omitempty"`
}

// reproFacts 从 blob 交换原文提取每步事实(blob 读不到的步骤退账本
// 摘要字段,不阻塞其余步骤)。
func (l *Loop) reproFacts(rec *rings.TestClaimRecord) []reproFact {
	var blobs *swarm.BlobStore
	if l.Swarm != nil {
		blobs = l.Swarm.Blobs
	}
	facts := make([]reproFact, 0, len(rec.Steps))
	for _, st := range rec.Steps {
		f := reproFact{
			Seq: st.Seq, Method: st.Method, URL: st.URL,
			RespStatus: st.Status, Note: st.Note,
		}
		if blobs != nil && st.BlobRef != "" {
			if doc, err := blobs.Read(st.BlobRef); err == nil {
				var ex mirror.Exchange
				if json.Unmarshal(doc, &ex) == nil {
					f.Cred = credShape(ex.Request)
					if ex.Request != nil {
						f.ReqBytes = int64(len(ex.Request.Body))
						f.ReqBody = clipLine(string(ex.Request.Body), reproFactBodyClip)
					}
					if ex.Response != nil {
						f.RespBytes = int64(len(ex.Response.Body))
						f.RespType = headerValue(ex.Response.Headers, "Content-Type")
						f.Location = headerValue(ex.Response.Headers, "Location")
						f.RespHead = clipLine(string(ex.Response.Body), reproFactBodyClip)
					}
				}
			}
		}
		if f.Cred == "" {
			f.Cred = "未知(原文缺失)"
		}
		facts = append(facts, f)
	}
	return facts
}

// credShape 请求侧凭据形态(有无;值不进事实包):无 / Authorization
// (scheme)/ Cookie 会话 / 两者。
func credShape(req *mirror.Request) string {
	if req == nil {
		return ""
	}
	var auth, cookie bool
	var scheme string
	for _, h := range req.Headers {
		switch strings.ToLower(h.Name) {
		case "authorization":
			auth = true
			if v := strings.Fields(h.Value); len(v) > 0 {
				scheme = v[0]
			}
		case "cookie":
			cookie = true
		}
	}
	switch {
	case auth && cookie:
		return "Authorization(" + scheme + ")+Cookie"
	case auth:
		return "Authorization(" + scheme + ")"
	case cookie:
		return "Cookie 会话"
	default:
		return "无"
	}
}

func headerValue(headers []mirror.Header, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// reproAskHead 挑选提问的 user 段(候选步骤事实包;claimTitle 是声明的
// 存在性陈述,给模型"什么构成复现"的上下文;total 是候选总数,超过
// 喂入上限时如实告知)。
func reproAskHead(claimTitle string, facts []reproFact, total int) string {
	payload, err := json.Marshal(facts)
	if err != nil {
		payload = []byte("[]")
	}
	head := "判断: " + claimTitle + "\n复现步骤事实:\n" + string(payload)
	if total > len(facts) {
		head = fmt.Sprintf("步骤共 %d 条,此处列前 %d 条与最近 %d 条:\n", total, len(facts)/2, len(facts)-len(facts)/2) + head
	}
	return head
}

// askReproDescs 一次无工具单轮会话:候选步骤事实包 → 最小集选择与三层
// 描述 JSON(引擎链路与转录/用量记账同拆解会话)。空选合法返回(模型
// 判定池中无可证明漏洞的包),skip_reason 携带原因;错误 = 调用/解析
// 失败(调用方重试一次)。
func (l *Loop) askReproDescs(ctx context.Context, gs *genSession, head string) (picks []reproDescStep, skipReason string, err error) {
	text, err := gs.ask(ctx, head)
	if err != nil {
		return nil, "", err
	}
	return parseReproDescs(text)
}

// reproFactClip 候选事实包的喂入截取(账次序取首尾各半;链式流程的
// 关键交换在两端)。
func reproFactClip(facts []reproFact) []reproFact {
	if len(facts) <= reproFactAskCap {
		return facts
	}
	half := reproFactAskCap / 2
	out := make([]reproFact, 0, reproFactAskCap)
	out = append(out, facts[:half]...)
	out = append(out, facts[len(facts)-half:]...)
	return out
}

// parseReproDescs 宽容解析模型回复:剥围栏、取首尾大括号主体。空选择
// (steps 为空)是合法回答——须携带 skip_reason(模型判定池中无可证明
// 漏洞的包时如实说原因);既无选择又无原因按解析失败处理(调用方重试)。
func parseReproDescs(text string) ([]reproDescStep, string, error) {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	if body == "" {
		return nil, "", fmt.Errorf("回复为空")
	}
	var r struct {
		Steps      []reproDescStep `json:"steps"`
		SkipReason string          `json:"skip_reason"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, "", fmt.Errorf("不是合法 JSON: %w", err)
	}
	if len(r.Steps) == 0 {
		if strings.TrimSpace(r.SkipReason) == "" {
			return nil, "", fmt.Errorf("steps 为空且未说明原因")
		}
		return nil, r.SkipReason, nil
	}
	return r.Steps, "", nil
}

// reproDescSystem 最小集选择与三层描述的系统段(代码固定;稳定短段
// 利于前缀缓存)。文风即面板文风(8.5):陈述事实、数字精确、无 emoji、
// 无服务腔;提到目标用完整标识(P4-27 ③:裸端口/裸路径不上界面)。
// P4-62:空选是合法回答(池中无可证明漏洞的包时如实说原因,程序不再
// 倒池)。
const reproDescSystem = `为渗透测试发现挑选复现最小集并写步骤说明。输入是验证过程中的交换事实(已按方法+路径+状态去重;部分请求包由工具命令还原,响应可能未留存)。任务:

1. 挑出构成复现流程的必要步骤:一步就能复现的(如接口未授权访问)恰选 1 条;复现真要多步(如 拿会话凭据 → 凭据换票据 → 带票据请求最终接口)就选 N 条。对比交换(如带凭据的对照请求)、反证变体(换 UA / 防缓存参数 / 近似请求体)、枚举试错(大量同型尝试)、无关流量(与该问题无关的主页、资源请求)一律不选。该几步就几步,不凑数。
2. 全部交换都不能证明该问题成立时,steps 给空数组,并写 skip_reason 一句说明原因(如"池内交换均为无关流量,不能证明该问题"式,陈述事实)。
3. 对选中的每步写三段:
   - short:步骤短名,不超过 8 个字,动宾短语(如"拿会话凭据""带票据请求");
   - desc:一句可照做的中文指令,口径示例:向 10.0.0.5 发 GET /admin,无需凭据;返回 200 且响应含"管理后台"字样即复现;
   - purpose:这步在复现里的作用,一句(如"取得后续请求所需的会话凭据";只有一步时写"直接复现该问题"式)。

规则:
1. 只用给出的事实:方法、目标、路径(含查询串)、凭据有无及形态、请求体关键内容、响应状态码与关键特征。不编造未给出的细节,不加原理解释;响应未留存的步骤,desc 写清"返回未留存"。
2. 数字精确(字节数、状态码照抄);短句;无 emoji;提到目标用完整标识(域名或 IP 起头,如 jkysd.cn:9902)。
3. 请求体较长时只取复现必需的字段,可加"等 N 字节表单"式说明。

只输出 JSON:{"steps":[{"seq":1,"short":"…","desc":"…","purpose":"…"},…]} 或 {"steps":[],"skip_reason":"…"},seq 与输入一致,只含选中的步骤。`
