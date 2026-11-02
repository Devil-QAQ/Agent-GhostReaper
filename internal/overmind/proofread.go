package overmind

// P4-24 ①(问题清单 #39):定稿校对——收割定稿冻结入账前,对将上面板
// 的模型文本(复现步骤描述 P4-08 ④ / 来龙去脉叙述 P4-13)在同一会话
// 里跑一次自我校对改写(人话标准 = 平实书面白话 + 锚例);失败保留
// 上一稿(生成稿 / 确定性兜底),不阻塞确认裁决。与 ② 模式闸门
// (machinegate.go)串联:校对 → 特征扫描 → 不合格退回重写一次 →
// 再不合格如实标注(Unpolished / NarrativeUnpolished)不阻塞。
// 成本有界:每冻结文本族校对恰一次、闸门重写至多一次;同会话续问,
// 转录与 token 记账同锚点连续(用量可见)。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// genSession 定稿生成的同会话运行件:生成与校对/重写共用一个引擎
// 会话(模型看得见自己刚写的原文),转录 JSONL 与 token 用量在同一
// 锚点上连续累计。零值不可用,经 Loop.newGenSession 创建。
type genSession struct {
	l     *Loop
	sess  *agentcore.Session
	usage *swarm.UsageTracker
	sid   string
}

// newGenSession 起一个无工具单轮形态的生成会话(引擎链路、适配器、
// 流式与用量记账全同拆解会话);system 为该文本族的系统段。
func (l *Loop) newGenSession(system string) *genSession {
	sid := l.nextSessionID("r")
	opts := agentcore.Options{
		Provider:               stripTokensProvider{l.Provider},
		SystemPrompt:           []string{system},
		Tools:                  []tool.CoreTool{}, // 显式空表:生成会话无工具(nil 会挂引擎默认工具集)
		PermissionMode:         permission.ModeDefault,
		MaxTurns:               2,
		SessionID:              sid,
		DisableBackgroundTasks: true,
	}
	if l.RootDir != "" {
		opts.Transcript = transcript.NewStore(filepath.Join(l.RootDir, l.TaskID, "transcripts"))
	}
	return &genSession{
		l:    l,
		sess: agentcore.NewSession(opts),
		usage: &swarm.UsageTracker{
			Store: l.Store, TaskID: l.TaskID, Agent: ActorOvermind,
			Model: l.Model, ModelOf: l.modelNow(), Transcript: sid, // MaxTurns=0:规划会话不落里程碑
		},
		sid: sid,
	}
}

// close 释放会话资源(幂等)。
func (s *genSession) close() { s.sess.Close() }

// ask 同会话续问一轮:追加 user 消息并跑完引擎循环,返回本轮新增的
// assistant 文本(跨段拼接;Terminal 携带全会话消息,只取本轮尾段——
// 续问轮不能把前几轮的回复再拼进来);用量逐次入 token 独立表。
func (s *genSession) ask(ctx context.Context, user string) (string, error) {
	before := len(s.sess.Messages())
	var termErr error
	for ev, err := range s.sess.Prompt(ctx, user) {
		if err != nil {
			return "", fmt.Errorf("生成会话: %w", err)
		}
		if ev.Kind == harness.KindUsage && ev.Usage != nil {
			if uerr := s.usage.OnUsage(*ev.Usage); uerr != nil {
				s.l.setErr(uerr)
			}
		}
		if ev.Kind == harness.KindResult && ev.Terminal != nil {
			if ev.Terminal.Err != nil {
				termErr = fmt.Errorf("生成终止: %w", ev.Terminal.Err)
			}
		}
	}
	if termErr != nil {
		return "", termErr
	}
	var texts []string
	for _, m := range s.sess.Messages()[before:] {
		if m.Role == llm.RoleAssistant {
			if t := strings.TrimSpace(m.Text()); t != "" {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, ""), nil
}

// ---- ① 定稿校对(同会话自我校对改写) ----

// proofreadHumanStandard 人话标准与锚例(两族文本共用;锚例本身须过
// 模式闸门——模型照锚例写的句子不会再被闸门打回)。P4-27 ③ 追加
// 目标完整标识约束(裸端口/裸路径不上界面,问题清单 #44)。
const proofreadHumanStandard = `人话标准:平实书面白话,像写给同事看的工作说明;短句,数字精确;无 emoji;不写原理与实现细节。
提到目标一律用完整标识:域名或 IP 起头、host:port 形态(如 jkysd.cn:9902、10.0.0.5:8080);不用裸端口、裸路径指代目标(读者不知道说的是哪个目标)。
锚例:向 10.0.0.5 发 GET /admin,无需凭据;返回 200 且响应含"管理后台"字样即复现
禁这些机器语:内部编号、解释编号、裸版本号数字、程序报错原文、英文状态词、无动词的压缩名词堆。`

// reproProofreadAsk 步骤描述的校对续问(同会话,模型可见自己刚写的
// 全部描述)。
const reproProofreadAsk = `校对改写你刚写的全部步骤描述:只改文风,事实一字不动(方法、目标、路径、凭据、数字、状态码、特征全照抄);一步一句可照做的指令。
` + proofreadHumanStandard + `
只输出 JSON:{"steps":[{"seq":1,"desc":"…"},…]},seq 与你上一轮一致,覆盖全部步骤。`

// narrativeProofreadAsk 叙述的校对续问。
const narrativeProofreadAsk = `校对改写你刚写的叙述:只改文风,事实一字不动(单位、轮次、地址、数字全照抄);仍写清四件事——在哪发现、谁发现、怎么验证、怎么确认;一段不超过 150 字。
` + proofreadHumanStandard + `
只输出 JSON:{"narrative":"…"}`

// ---- ② 模式闸门(扫描 + 退回重写一次 + 如实标注) ----

// polishReproDescs 步骤描述的校对+闸门链:① 同会话校对改写(失败保留
// 生成稿,如实 WARN);② 逐步特征扫描,命中则同会话退回重写一次并
// 复扫,仍命中的步骤如实标注 Unpolished(原文保留,不阻塞定稿)。
func (l *Loop) polishReproDescs(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord) {
	if text, err := gs.ask(ctx, reproProofreadAsk); err != nil {
		runlog.Warnf("定稿校对失败: 任务 %s 判断 %s 原因 %s(保留生成稿)",
			l.TaskID, claimID, clipEval(err.Error()))
	} else if descs, perr := parseReproDescs(text); perr != nil {
		runlog.Warnf("定稿校对解析失败: 任务 %s 判断 %s 原因 %s(保留生成稿)",
			l.TaskID, claimID, clipEval(perr.Error()))
	} else {
		applyReproDescs(rec, descs)
	}
	// 闸门:逐步扫;有命中 → 同会话重写一次 → 复扫;仍命中如实标注。
	dirty := reproDescHits(rec)
	if len(dirty) == 0 {
		return
	}
	if err := l.rewriteReproDescs(ctx, claimID, gs, rec, dirty); err != nil {
		runlog.Warnf("定稿改写失败: 任务 %s 判断 %s 原因 %s(如实标注)",
			l.TaskID, claimID, clipEval(err.Error()))
	}
	for i := range rec.Steps {
		if hits := ScanMachineTalk(rec.Steps[i].Desc); len(hits) > 0 {
			rec.Steps[i].Unpolished = true
			runlog.Warnf("定稿文字未过文风校验,按原文保留: 任务 %s 判断 %s 步骤 %d(%s)",
				l.TaskID, claimID, rec.Steps[i].Seq, strings.Join(talkHitLines(hits), ";"))
		}
	}
}

// reproDescHits 逐步扫描,返回有命中的步骤(seq → 命中清单)。
func reproDescHits(rec *rings.TestClaimRecord) map[int][]talkHit {
	out := map[int][]talkHit{}
	for _, st := range rec.Steps {
		if hits := ScanMachineTalk(st.Desc); len(hits) > 0 {
			out[st.Seq] = hits
		}
	}
	return out
}

// rewriteReproDescs 闸门退回重写:命中步骤与特征喂回同会话,重写后再
// 复扫(仍命中的步骤由调用方如实标注)。
func (l *Loop) rewriteReproDescs(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord, dirty map[int][]talkHit) error {
	var b strings.Builder
	b.WriteString("你写的步骤描述里有机器语,逐处改写掉,事实一字不动:\n")
	for _, st := range rec.Steps {
		hits, ok := dirty[st.Seq]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "步骤 %d(原文:%s):\n", st.Seq, clipLine(st.Desc, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(proofreadHumanStandard + "\n")
	b.WriteString(`只输出 JSON:{"steps":[{"seq":1,"desc":"…"},…]},只含上面点名的步骤。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	descs, err := parseReproDescs(text)
	if err != nil {
		return err
	}
	applyReproDescs(rec, descs)
	return nil
}

// polishNarrative 叙述的校对+闸门链(同 polishReproDescs 的单文本版)。
func (l *Loop) polishNarrative(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord) {
	if text, err := gs.ask(ctx, narrativeProofreadAsk); err != nil {
		runlog.Warnf("定稿校对失败: 任务 %s 判断 %s 原因 %s(保留生成稿)",
			l.TaskID, claimID, clipEval(err.Error()))
	} else if t, perr := parseClaimNarrative(text); perr != nil {
		runlog.Warnf("定稿校对解析失败: 任务 %s 判断 %s 原因 %s(保留生成稿)",
			l.TaskID, claimID, clipEval(perr.Error()))
	} else {
		rec.Narrative = clipLine(t, narrativeClip)
	}
	hits := ScanMachineTalk(rec.Narrative)
	if len(hits) == 0 {
		return
	}
	if t, err := l.rewriteNarrative(ctx, gs, hits); err != nil {
		runlog.Warnf("定稿改写失败: 任务 %s 判断 %s 原因 %s(如实标注)",
			l.TaskID, claimID, clipEval(err.Error()))
	} else {
		rec.Narrative = clipLine(t, narrativeClip)
		hits = ScanMachineTalk(rec.Narrative)
	}
	if len(hits) > 0 {
		rec.NarrativeUnpolished = true
		runlog.Warnf("定稿文字未过文风校验,按原文保留: 任务 %s 判断 %s(%s)",
			l.TaskID, claimID, strings.Join(talkHitLines(hits), ";"))
	}
}

// rewriteNarrative 闸门退回重写一次(叙述)。
func (l *Loop) rewriteNarrative(ctx context.Context, gs *genSession, hits []talkHit) (string, error) {
	var b strings.Builder
	b.WriteString("你写的叙述里有机器语,逐处改写掉,事实一字不动:\n")
	for _, line := range talkHitLines(hits) {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString(proofreadHumanStandard + "\n")
	b.WriteString(`只输出 JSON:{"narrative":"…"}`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return "", err
	}
	return parseClaimNarrative(text)
}
