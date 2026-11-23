package overmind

// 显示稿首次生成携带阅读标准，只有具体字段校验失败才修正一次。
// 原始证据和业务状态不因文风改写改变；生成与修正在同一会话留痕。

import (
	"context"
	"fmt"
	"ghostreaper/internal/failover"
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
// 锚点上连续记账;引擎用量只在一次 Prompt 内累计。零值不可用,
// 经 Loop.newGenSession 创建。
type genSession struct {
	l    *Loop
	sess *agentcore.Session
	sid  string
}

// Display traffic has a process-wide ceiling; planning and unit execution do
// not wait for these permits. Waiting here is cancellable and is not a call.
var displayRequests = make(chan struct{}, 2)

// newGenSession 起一个无工具单轮形态的生成会话(引擎链路、适配器、
// 流式与用量记账全同拆解会话);system 为该文本族的系统段。
func (l *Loop) newGenSession(system string) *genSession {
	sid := l.nextSessionID("r")
	opts := agentcore.Options{
		Provider:               failover.LibraryOutput(swarm.MeterProvider(l.Provider, l.Store, rings.TokenUsage{TaskID: l.TaskID, Agent: ActorOvermind, Model: l.Model, TranscriptID: sid}, l.setErr)),
		SystemPrompt:           []string{system, proofreadHumanStandard},
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
		sid:  sid,
	}
}

// close 释放会话资源(幂等)。
func (s *genSession) close() { s.sess.Close() }

// ask 同会话续问一轮:追加 user 消息并跑完引擎循环,返回本轮新增的
// assistant 文本(跨段拼接;Terminal 携带全会话消息,只取本轮尾段——
// 续问轮不能把前几轮的回复再拼进来);用量逐次入 token 独立表。
func (s *genSession) ask(ctx context.Context, user string) (string, error) {
	select {
	case displayRequests <- struct{}{}:
		defer func() { <-displayRequests }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	before := len(s.sess.Messages())
	var termErr error
	for ev, err := range s.sess.Prompt(ctx, user) {
		if err != nil {
			return "", fmt.Errorf("生成会话: %w", err)
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

// ---- 首次生成的阅读要求 ----

// proofreadHumanStandard 人话标准与锚例(P4-59 ① 起升级为报告级锚,
// 问题清单 #80①③;造物主定方向"靠硬规则不够、升级模型能力"——四段
// 正反样例泛化自造物主本人报告文风,不含任何报告原始信息)。P4-27 ③
// 目标完整标识约束保留。锚例自身须过模式闸门(模型照锚例写的句子不会
// 再被闸门打回;改文风标准时先跑 machinegate_test.go 的锚例用例)。
const proofreadHumanStandard = `阅读目标：让操作者先看懂结果与影响，再按需核对过程。用平实中文，说明说清问题，影响只写证据支持的后果，验证过程围绕关键动作和观察，修复建议给具体动作。内容组织按材料决定，不套“就是…从而…”或固定段落顺序。
概览不逐项抄写报文、参数、载荷与内部编号；精确请求、凭据和操作仍保留在正式复现与证据中。首次出现的对象要明确，已列资产的上下文中不必反复写完整地址。数字、前提和结论范围保持真实；未验证、证据不足与未发现的含义不同，不为表达简洁改变。没有材料时不补造，不用 emoji、套话或空泛评价。`

// ---- ② 模式闸门(扫描 + 退回重写一次 + 如实标注) ----

// talkRewriteReferralNote 闸门退回重写的指代条款(P4-59 活体实锤补:口令
// 等必须照抄的原文值本身可撞机器语特征——如"Adm!2026xQ"的"!2026"命中
// 句首裸端口、长口令串成压缩名词堆;事实一字不动与特征清零在此冲突,
// 重写时该处改用指代,事实不失)。
const talkRewriteReferralNote = `命中特征若出自必须照抄的原文值(如口令、密钥里的数字与符号),该处改用指代写法(如「备份包内的管理员口令」),不再贴原值;除此之外事实一字不动。
`

// polishReproDescs 步骤三层描述的校对+闸门链:① 同会话校对改写(失败
// 保留生成稿,如实 WARN);② 逐步特征扫描(P4-35 ② 起扫短名/做什么/
// 作用三段),命中则同会话退回重写一次并复扫,仍命中的步骤如实标注
// Unpolished(原文保留,不阻塞定稿)。
func (l *Loop) polishReproDescs(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord) {
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
		if hits := stepMachineHits(rec.Steps[i]); len(hits) > 0 {
			rec.Steps[i].Unpolished = true
			runlog.Warnf("定稿文字未过文风校验,按原文保留: 任务 %s 判断 %s 步骤 %d(%s)",
				l.TaskID, claimID, rec.Steps[i].Seq, strings.Join(talkHitLines(hits), ";"))
		}
	}
}

// stepMachineHits 单步三层文本(短名/做什么/作用)的机器语特征扫描,
// 任一段命中即记(短名本身是方法+路径的确定性兜底形态,方法词与路径
// 属事实不在特征表,如实不误伤)。
func stepMachineHits(st rings.ReproStep) []talkHit {
	var hits []talkHit
	for _, text := range []string{st.Short, st.Desc, st.Purpose} {
		if strings.TrimSpace(text) == "" {
			continue
		}
		hits = append(hits, scanDisplayText(text)...)
	}
	return hits
}

// reproDescHits 逐步扫描,返回有命中的步骤(seq → 命中清单)。
func reproDescHits(rec *rings.TestClaimRecord) map[int][]talkHit {
	out := map[int][]talkHit{}
	for _, st := range rec.Steps {
		if hits := stepMachineHits(st); len(hits) > 0 {
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
		fmt.Fprintf(&b, "步骤 %d(原文:%s / %s / %s):\n", st.Seq,
			clipLine(st.Short, 40), clipLine(st.Desc, 60), clipLine(st.Purpose, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + proofreadHumanStandard + "\n")
	b.WriteString(`只输出 JSON:{"steps":[{"seq":1,"short":"…","desc":"…","purpose":"…"},…]},只含上面点名的步骤。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	descs, _, err := parseReproDescs(text)
	if err != nil {
		return err
	}
	applyReproTexts(rec, descs)
	return nil
}

// polishNarrative 叙述的校对+闸门链(同 polishReproDescs 的单文本版)。
func (l *Loop) polishNarrative(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord) {
	hits := scanDisplayText(rec.Narrative)
	if len(hits) == 0 {
		return
	}
	if t, err := l.rewriteNarrative(ctx, gs, hits); err != nil {
		runlog.Warnf("定稿改写失败: 任务 %s 判断 %s 原因 %s(如实标注)",
			l.TaskID, claimID, clipEval(err.Error()))
	} else {
		rec.Narrative = strings.TrimSpace(t)
		hits = scanDisplayText(rec.Narrative)
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
	b.WriteString(talkRewriteReferralNote + proofreadHumanStandard + "\n")
	b.WriteString(`只输出 JSON:{"narrative":"…"}`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return "", err
	}
	return parseClaimNarrative(text)
}
