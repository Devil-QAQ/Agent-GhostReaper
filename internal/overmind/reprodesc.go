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
// 生成失败(模型错误/解析失败/无效选择)不阻塞确认裁决:候选池原样
// 落账(短名/描述为组装时已落的确定性兜底,作用为空),如实留 WARN
// ——确认是机器裁决(R2),描述是包装。

// reproDescClip 单步描述(做什么)的落账上限(超长截断;面板与报告同文)。
const reproDescClip = 400

// reproShortClip / reproPurposeClip 短名与作用的落账上限(P4-35 ②:
// 短名是标题段 ≤8 字、作用是一句,超长截断)。
const reproShortClip = 24

const reproPurposeClip = 160

// reproFactAskCap 喂给模型的候选交换上限(账次序取首尾各半——枚举型
// 反证候选可达数百条,链式流程的关键交换在两端;超出的候选模型选不
// 到,生成失败整体回退候选池,如实不静默)。
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

// genReproDescs 定稿最小集选择与三层描述(P4-35):模型从 rec.Steps
// (去同型候选池)挑必要步骤并写短名/做什么/作用,选中者按账本次序
// 重编 1..n。零步骤/无 Provider 直接返回(单测与未配模型路径,零外呼;
// 候选池原样落账)。P4-24:生成后接定稿校对+模式闸门(同会话,见
// proofread.go)。
func (l *Loop) genReproDescs(ctx context.Context, claimID string, rec *rings.TestClaimRecord) {
	if l.Provider == nil || len(rec.Steps) == 0 {
		return
	}
	claimTitle := ""
	if n, err := l.Store.Node(ctx, l.TaskID, claimID); err == nil {
		claimTitle = n.Title
	}
	facts := reproFactClip(l.reproFacts(rec))
	gs := l.newGenSession(reproDescSystem)
	defer gs.close()
	picks, err := l.askReproDescs(ctx, gs, claimTitle, facts, len(rec.Steps))
	if err != nil {
		runlog.Warnf("定稿最小集生成失败: 任务 %s 判断 %s 原因 %s(回退确定性兜底)",
			l.TaskID, claimID, clipEval(err.Error()))
		return
	}
	if !applyReproPick(rec, picks) {
		runlog.Warnf("定稿最小集无效(无命中步骤),回退确定性兜底: 任务 %s 判断 %s",
			l.TaskID, claimID)
	}
	l.polishReproDescs(ctx, claimID, gs, rec)
}

// applyReproPick 应用模型的最小集选择(P4-35 ①):seq 须命中候选池
// (未命中的条目丢弃),选中步骤保留候选 Seq 与账本次序(不重编——校对
// 轮的 seq 契约"与你上一轮一致"靠它成立;呈现序号按列表位置,读侧
// reproStepHead 定);short/desc/purpose 缺省退该步确定性兜底;返回是
// 否存在有效选择(全未命中 = false,调用方回退候选池)。
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

// askReproDescs 一次无工具单轮会话:候选步骤事实包 → 最小集选择与
// 三层描述 JSON(引擎链路与转录/用量记账同拆解会话)。claimTitle 是
// 声明的存在性陈述,给模型"什么构成复现"的上下文(只进提示词,不复述
// 进步骤描述);total 是候选总数(超过喂入上限时如实告知)。
func (l *Loop) askReproDescs(ctx context.Context, gs *genSession, claimTitle string, facts []reproFact, total int) ([]reproDescStep, error) {
	payload, err := json.Marshal(facts)
	if err != nil {
		return nil, err
	}
	head := "判断: " + claimTitle + "\n复现步骤事实:\n" + string(payload)
	if total > len(facts) {
		head = fmt.Sprintf("步骤共 %d 条,此处列前 %d 条与最近 %d 条:\n", total, len(facts)/2, len(facts)-len(facts)/2) + head
	}
	text, err := gs.ask(ctx, head)
	if err != nil {
		return nil, err
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

// parseReproDescs 宽容解析模型回复:剥围栏、取首尾大括号主体;空选择
// 与漏步骤由调用方回退(不整体作废——已生成的选择照常冻结)。
func parseReproDescs(text string) ([]reproDescStep, error) {
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
		return nil, fmt.Errorf("回复为空")
	}
	var r struct {
		Steps []reproDescStep `json:"steps"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, fmt.Errorf("不是合法 JSON: %w", err)
	}
	if len(r.Steps) == 0 {
		return nil, fmt.Errorf("steps 为空")
	}
	return r.Steps, nil
}

// reproDescSystem 最小集选择与三层描述的系统段(代码固定;稳定短段
// 利于前缀缓存)。文风即面板文风(8.5):陈述事实、数字精确、无 emoji、
// 无服务腔;提到目标用完整标识(P4-27 ③:裸端口/裸路径不上界面)。
const reproDescSystem = `为渗透测试发现挑选复现最小集并写步骤说明。输入是验证过程中的交换事实(已按方法+路径+状态去重)。任务:

1. 挑出构成复现流程的必要步骤:一步就能复现的(如接口未授权访问)恰选 1 条;复现真要多步(如 拿会话凭据 → 凭据换票据 → 带票据请求最终接口)就选 N 条。对比交换(如带凭据的对照请求)、反证变体(换 UA / 防缓存参数 / 近似请求体)、枚举试错(大量同型尝试)一律不选。该几步就几步,不凑数。
2. 对选中的每步写三段:
   - short:步骤短名,不超过 8 个字,动宾短语(如"拿会话凭据""带票据请求");
   - desc:一句可照做的中文指令,口径示例:向 10.0.0.5 发 GET /admin,无需凭据;返回 200 且响应含"管理后台"字样即复现;
   - purpose:这步在复现里的作用,一句(如"取得后续请求所需的会话凭据";只有一步时写"直接复现该问题"式)。

规则:
1. 只用给出的事实:方法、目标、路径(含查询串)、凭据有无及形态、请求体关键内容、响应状态码与关键特征。不编造未给出的细节,不加原理解释。
2. 数字精确(字节数、状态码照抄);短句;无 emoji;提到目标用完整标识(域名或 IP 起头,如 jkysd.cn:9902)。
3. 请求体较长时只取复现必需的字段,可加"等 N 字节表单"式说明。

只输出 JSON:{"steps":[{"seq":1,"short":"…","desc":"…","purpose":"…"},…]},seq 与输入一致,只含选中的步骤。`
