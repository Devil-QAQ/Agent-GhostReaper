// Package interject 的快照构建(P2-08f,第四章 §7 修订:插话"问"轻量化
// 为快照直问)。回答依据 = 目标最近一次请求的不可变上下文快照:
//
//	目标身份与状态(单位/主宰、执行中/已结束)
//	+ 目标的系统提示(经同一组装路径重建:RoE 约束块 + 三段式基线)
//	+ 目标的工具定义(只读目录,模型看得懂目标在引用什么)
//	+ 会话消息重放(transcript JSONL → 消息流,尾部字符预算截尾)
//
// 只读目标历史、不建会话、不进目标 transcript——"问不改执行"由结构
// 保证。重建的系统提示取当前配置值(RoE/可编辑正文有改动时与目标
// 历史请求略有出入,快照头如实声明口径)。
package interject

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 快照重放的规模上限:单条消息截头 + 总字符预算(尾部保留——最近的
// 轮次最能回答"现在在干嘛"),超预算的旧消息丢弃并注明条数。
const (
	snapshotMsgClip   = 1500
	snapshotBudget    = 24000
	snapshotPriorQA   = 6   // 追问链回放的早前问答条数上限
	snapshotOvermindN = 3   // 主宰身份头的近轮研判条数
	AskSecondsDefault = 120 // 单次直问的墙钟上限(设计定稿约 120 秒)
)

// buildSnapshot 构造目标的快照正文(问的回答依据)。
func (s *Service) buildSnapshot(ctx context.Context, taskID, target, sessionID string) (string, error) {
	if target == TargetOvermind {
		return s.overmindSnapshot(ctx, taskID)
	}
	return s.unitSnapshot(ctx, taskID, sessionID)
}

// unitIdentity 单位身份与状态(账本扫描:步骤/单位/终因)。
func (s *Service) unitIdentity(ctx context.Context, taskID, sessionID string) (stepID, unitID, stepTitle, status, finishNote string, err error) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return "", "", "", "", "", err
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || rec.TranscriptID != sessionID {
			continue
		}
		switch rec.Type {
		case rings.SwarmRecSessionStarted:
			stepID, unitID = rec.StepID, rec.UnitID
		case rings.SwarmRecSessionEnd:
			stepID, unitID = rec.StepID, rec.UnitID
			finishNote = rec.Note
		}
	}
	if stepID == "" {
		return "", "", "", "", "", fmt.Errorf("会话 %s 不在账本中(单位不存在)", sessionID)
	}
	if n, nerr := s.Store.Node(ctx, taskID, stepID); nerr == nil {
		stepTitle, status = n.Title, n.Status
	}
	return stepID, unitID, stepTitle, status, finishNote, nil
}

// unitSnapshot 单位快照:身份 + 系统提示重建 + 工具目录 + 转录重放。
func (s *Service) unitSnapshot(ctx context.Context, taskID, sessionID string) (string, error) {
	stepID, unitID, stepTitle, status, finishNote, err := s.unitIdentity(ctx, taskID, sessionID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("【目标身份】虫群单位 " + unitID + ",负责步骤 " + stepID)
	if stepTitle != "" {
		b.WriteString("(" + clip(stepTitle, 120) + ")")
	}
	fmt.Fprintf(&b, ",状态 %s,会话 %s。\n", status, sessionID)
	switch {
	case finishNote != "":
		fmt.Fprintf(&b, "该单位已结束(%s)。以下回答基于其历史记录,不是实时对话。\n", clip(finishNote, 200))
	case status == "running":
		b.WriteString("该单位正在执行中;快照截至其最近一次请求,之后的变化不可见。\n")
	default:
		b.WriteString("快照截至其最近一次请求。\n")
	}

	// 系统提示重建:与执行器同一组装路径(RoE 约束块置顶 + 三段式)。
	roe, rerr := s.Store.CurrentRoE(ctx, taskID)
	if rerr != nil {
		roe = rings.RoeState{} // 读失败退零值如实展示(与执行器一致)
	}
	b.WriteString("\n【目标的系统提示(按当前配置重建)】\n")
	b.WriteString(swarm.RenderRoEConstraint("你的探索手段受此框定。",
		roe.AllowedTools, roe.DeniedTools, roe.ApprovalThreshold, nil, nil, roe.CreatorNotes, roe.InfoSources))
	b.WriteString("\n")
	b.WriteString(strings.Join(swarm.WorkerPromptSpec.Assemble(ctx, s.Store), "\n\n"))

	// 工具目录(只读呈现;应答自身无工具)。
	b.WriteString("\n\n【目标的可用工具(只读目录)】\n")
	for _, t := range swarm.ToolCatalog() {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Desc)
	}

	// 会话消息重放。
	if s.RootDir == "" {
		b.WriteString("\n【会话消息重放】转录目录未配置,仅账本侧信息。\n")
		return b.String(), nil
	}
	tstore := transcript.NewStore(filepath.Join(s.RootDir, taskID, "transcripts"))
	replay, rerr := replayTranscript(tstore, sessionID)
	if rerr != nil {
		b.WriteString("\n【会话消息重放】转录读取失败: " + rerr.Error() + "\n")
		return b.String(), nil
	}
	b.WriteString("\n【会话消息重放(截至最近一次请求)】\n")
	b.WriteString(replay)
	return b.String(), nil
}

// overmindSnapshot 主宰快照:身份(近轮研判概览)+ 系统提示重建 +
// (研判会话无工具)+ 最近一次研判会话的转录重放。
func (s *Service) overmindSnapshot(ctx context.Context, taskID string) (string, error) {
	var b strings.Builder
	b.WriteString("【目标身份】主宰(计划权威)——态势研判/拆解/派发/收割的大脑。以下是其近期研判概览与最近一次研判请求的快照。\n")
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return "", err
	}
	var assess []rings.OvermindRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindRecord {
			continue
		}
		var rec rings.OvermindRecord
		if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.OvermindRecAssessment {
			assess = append(assess, rec)
		}
	}
	start := 0
	if len(assess) > snapshotOvermindN {
		start = len(assess) - snapshotOvermindN
	}
	if len(assess) == 0 {
		b.WriteString("(账本中尚无研判记录——主宰循环未跑过)\n")
	} else {
		for i, a := range assess[start:] {
			fmt.Fprintf(&b, "研判 #%d(派发 %d 条,组合 %d 条):%s | %s | %s | %s | %s",
				len(assess)-len(assess[start:])+i+1, a.Dispatched, len(a.Combined),
				nz(a.QAccess), nz(a.QPrivilege), nz(a.QCombination), nz(a.QUnlock), nz(a.QOverturn))
			if a.Note != "" {
				fmt.Fprintf(&b, " | %s", clip(a.Note, 200))
			}
			b.WriteString("\n")
		}
	}

	// 系统提示重建:与主宰循环同一组装路径(RoE 约束块 + 三段式;摘除
	// 清单与 loop.overmindSystemSegments 同源)。
	roe, rerr := s.Store.CurrentRoE(ctx, taskID)
	if rerr != nil {
		roe = rings.RoeState{}
	}
	var excluded []rings.ExcludedAssetView
	if excluded, rerr = s.Store.ExcludedAssets(ctx, taskID); rerr != nil {
		excluded = nil
	}
	b.WriteString("\n【目标的系统提示(按当前配置重建)】\n")
	pending, perr := s.Store.PendingScopeAssets(ctx, taskID)
	if perr != nil {
		pending = nil
	}
	b.WriteString(swarm.RenderRoEConstraint("派发的意图与采用的手段不得越出此边界。",
		roe.AllowedTools, roe.DeniedTools, roe.ApprovalThreshold, excluded, pending, roe.CreatorNotes, roe.InfoSources))
	b.WriteString("\n")
	b.WriteString(strings.Join(overmind.OvermindPromptSpec.Assemble(ctx, s.Store), "\n\n"))
	b.WriteString("\n\n【目标的可用工具(只读目录)】研判会话无工具(态势快照经 user turn 注入,重放里可见)。\n")

	// 最近一次研判会话的重放(按文件修改时刻取最新 overmind-*)。
	if s.RootDir == "" {
		return b.String(), nil
	}
	sid, ok := latestOvermindSession(filepath.Join(s.RootDir, taskID, "transcripts"))
	if !ok {
		b.WriteString("\n【会话消息重放】尚无研判会话转录。\n")
		return b.String(), nil
	}
	tstore := transcript.NewStore(filepath.Join(s.RootDir, taskID, "transcripts"))
	replay, rerr := replayTranscript(tstore, sid)
	if rerr != nil {
		b.WriteString("\n【会话消息重放】转录读取失败: " + rerr.Error() + "\n")
		return b.String(), nil
	}
	b.WriteString("\n【会话消息重放(最近一次研判请求,会话 " + sid + ")】\n")
	b.WriteString(replay)
	return b.String(), nil
}

// latestOvermindSession 转录目录里最近修改的 overmind-* 会话 ID。
func latestOvermindSession(dir string) (string, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	type cand struct {
		id string
		mt time.Time
	}
	var best cand
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "overmind-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().After(best.mt) {
			best = cand{id: strings.TrimSuffix(name, ".jsonl"), mt: info.ModTime()}
		}
	}
	return best.id, best.id != ""
}

// replayTranscript 转录 JSONL → 逐条消息行(尾部预算:最近的轮次保留,
// 超预算的旧消息丢弃并注明条数;单条截头防单条巨型结果吃穿预算)。
func replayTranscript(tstore *transcript.Store, sessionID string) (string, error) {
	path := tstore.MainPath(sessionID)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "(转录文件缺失,仅有账本侧信息)\n", nil
		}
		return "", err
	}
	recs, err := tstore.Load(path)
	if err != nil {
		return "", err
	}
	msgs, _ := transcript.Messages(recs)
	if len(msgs) == 0 {
		return "(会话尚无消息)\n", nil
	}
	lines := make([]string, 0, len(msgs))
	for i, m := range msgs {
		line := messageLine(m)
		if line == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, clip(line, snapshotMsgClip)))
	}
	// 尾部预算:从尾部累计,超预算即截断头部。
	var kept []string
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if total+len(lines[i]) > snapshotBudget && len(kept) > 0 {
			kept = append([]string{fmt.Sprintf("(更早的 %d 条消息略)", i+1)}, kept...)
			break
		}
		kept = append([]string{lines[i]}, kept...)
		total += len(lines[i])
	}
	return strings.Join(kept, "\n") + "\n", nil
}

// priorQA 追问链:该(目标)子会话此前的问与答(按账本时序,尾部
// snapshotPriorQA 条)。每问都是独立快照直问,追问靠回放早前问答
// 进同一次请求——不建会话,链在提示里。
func (s *Service) priorQA(ctx context.Context, taskID, target, sessionID, excludeID string) string {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return ""
	}
	type qa struct{ q, a string }
	var list []qa
	byID := map[string]int{}
	for _, ev := range evs {
		if ev.Kind != rings.EventInterjectRecord {
			continue
		}
		var rec rings.InterjectRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || !matchTarget(rec, target, sessionID) {
			continue
		}
		switch rec.Type {
		case rings.InterjectRecUserMessage:
			if rec.Mode != "ask" || rec.MessageID == excludeID {
				continue
			}
			byID[rec.MessageID] = len(list)
			list = append(list, qa{q: rec.Text})
		case rings.InterjectRecReply:
			if i, ok := byID[rec.MessageID]; ok {
				list[i].a = rec.Text
			}
		}
	}
	var withAns []qa
	for _, x := range list {
		if x.a != "" {
			withAns = append(withAns, x)
		}
	}
	if len(withAns) == 0 {
		return ""
	}
	start := 0
	if len(withAns) > snapshotPriorQA {
		start = len(withAns) - snapshotPriorQA
	}
	var b strings.Builder
	b.WriteString("【此前的插话问答(供追问衔接)】\n")
	for _, x := range withAns[start:] {
		fmt.Fprintf(&b, "问: %s\n答: %s\n", clip(x.q, 600), clip(x.a, 1200))
	}
	return b.String()
}

func nz(s string) string {
	if strings.TrimSpace(s) == "" {
		return "无"
	}
	return clip(s, 120)
}

// messageLine 一条消息的单行摘要(assistant/user 文本、工具调用与结果)。
func messageLine(m llm.Message) string {
	var parts []string
	for _, blk := range m.Content {
		switch blk.Type {
		case llm.BlockText:
			if t := strings.TrimSpace(blk.Text); t != "" {
				parts = append(parts, t)
			}
		case llm.BlockToolUse:
			parts = append(parts, fmt.Sprintf("[调用工具 %s %s]", blk.Name, string(blk.Input)))
		case llm.BlockToolResult:
			parts = append(parts, fmt.Sprintf("[工具结果 %s]", flattenBlocks(blk.Content)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return string(m.Role) + ": " + strings.Join(parts, " | ")
}

// flattenBlocks 工具结果块文本拼合。
func flattenBlocks(bs []llm.ContentBlock) string {
	var b strings.Builder
	for _, blk := range bs {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}
