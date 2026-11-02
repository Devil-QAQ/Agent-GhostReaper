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
// 生成失败(模型错误/解析失败/漏步骤)不阻塞确认裁决:该步回退组装时
// 已落的确定性描述,如实留 WARN——确认是机器裁决(R2),描述是包装。

// reproDescClip 单步描述的落账上限(超长截断;面板与报告同文)。
const reproDescClip = 400

// reproFactBodyClip 喂给模型的请求/响应正文头截取(指令只需关键内容,
// 全文在 blob)。
const reproFactBodyClip = 256

// reproDescStep 模型回复契约里的单步描述。
type reproDescStep struct {
	Seq  int    `json:"seq"`
	Desc string `json:"desc"`
}

// genReproDescs 定稿描述生成:改写 rec.Steps[i].Desc 为指令式人话。
// 零步骤/无 Provider 直接返回(单测与未配模型路径,零外呼)。
// P4-24:生成后接定稿校对+模式闸门(同会话,见 proofread.go)。
func (l *Loop) genReproDescs(ctx context.Context, claimID string, rec *rings.TestClaimRecord) {
	if l.Provider == nil || len(rec.Steps) == 0 {
		return
	}
	claimTitle := ""
	if n, err := l.Store.Node(ctx, l.TaskID, claimID); err == nil {
		claimTitle = n.Title
	}
	facts := l.reproFacts(rec)
	gs := l.newGenSession(reproDescSystem)
	defer gs.close()
	descs, err := l.askReproDescs(ctx, gs, claimTitle, facts)
	if err != nil {
		runlog.Warnf("定稿描述生成失败: 任务 %s 判断 %s 原因 %s(回退确定性描述)",
			l.TaskID, claimID, clipEval(err.Error()))
		return
	}
	applyReproDescs(rec, descs)
	l.polishReproDescs(ctx, claimID, gs, rec)
}

// applyReproDescs 按 seq 应用模型描述(截断到落账上限;漏 seq 的步骤
// 保留原描述——确定性兜底或上一轮改写稿)。
func applyReproDescs(rec *rings.TestClaimRecord, descs []reproDescStep) {
	bySeq := map[int]string{}
	for _, d := range descs {
		if d.Seq > 0 && strings.TrimSpace(d.Desc) != "" {
			bySeq[d.Seq] = clipLine(strings.TrimSpace(d.Desc), reproDescClip)
		}
	}
	for i := range rec.Steps {
		if d, ok := bySeq[rec.Steps[i].Seq]; ok {
			rec.Steps[i].Desc = d
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

// askReproDescs 一次无工具单轮会话:事实包 → 指令式描述 JSON(引擎
// 链路与转录/用量记账同拆解会话)。claimTitle 是声明的存在性陈述,
// 给模型"什么构成复现"的上下文(只进提示词,不复述进步骤描述)。
func (l *Loop) askReproDescs(ctx context.Context, gs *genSession, claimTitle string, facts []reproFact) ([]reproDescStep, error) {
	payload, err := json.Marshal(facts)
	if err != nil {
		return nil, err
	}
	text, err := gs.ask(ctx, "判断: "+claimTitle+"\n复现步骤事实:\n"+string(payload))
	if err != nil {
		return nil, err
	}
	return parseReproDescs(text)
}

// parseReproDescs 宽容解析模型回复:剥围栏、取首尾大括号主体、漏步骤
// 由调用方回退(不整体作废——已生成的步骤照常冻结)。
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

// reproDescSystem 描述生成的系统段(代码固定;稳定短段利于前缀缓存)。
// 文风即面板文风(8.5):陈述事实、数字精确、无 emoji、无服务腔。
const reproDescSystem = `为渗透测试发现的复现步骤写描述。对每一步,融合请求与响应事实,写成一句可照做的中文指令,口径示例:

- 向 10.0.0.5 发 GET /admin,无需凭据;返回 200 且响应含"管理后台"字样即复现
- 向 10.0.0.5 发 POST /login,请求体 user=admin&pass=admin,无需凭据;返回 302 跳转 /home 即复现

规则:
1. 指令口径(操作者照着做一遍),不写过去时流水账;一步一句。
2. 只用给出的事实:方法、目标、路径(含查询串)、凭据有无及形态、请求体关键内容、响应状态码与关键特征(正文特征/跳转目标)。不编造未给出的细节,不加原理解释。
3. 数字精确(字节数、状态码照抄);无 emoji;短句。
4. 请求体较长时只取复现必需的字段,可加"等 N 字节表单"式说明。

只输出 JSON:{"steps":[{"seq":1,"desc":"…"},…]},seq 与输入一致,覆盖全部步骤。`
