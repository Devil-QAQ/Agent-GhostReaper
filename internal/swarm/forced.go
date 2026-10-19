package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// 研判后放行(第四章 §3 v2 拒绝线,P2-06b):拒绝线动作直接拒绝且留痕
// 后,造物主可在面板逐次放行——确认后系统按记录在案的参数执行那一次
// 调用,强制放行全记账。执行不复活被熔断的单位:放行执行的是被拒
// 调用本身,在合成的独立单位环境里跑(镜归因到原 Step,流量照章留痕)。

// forcedReleaseUnitPrefix 研判后放行执行的单位号前缀(与虫群单位 uN
// 区分;审计上"这次执行是造物主放行的"一目了然)。
const forcedReleaseUnitPrefix = "fr-"

// ExecuteForcedRelease 执行一次研判后放行:按被拒记录的 ToolName/
// ToolArgs 原样调用工具一次(单发路径:bash 限时与工具一致、超时即杀
// 不转后台——没有后续会话去读后台输出),执行记录入账
// (swarm.record tool_call,actor=swarm:fr-*,Note 回指审批请求)。
// 镜已装配时起独立会话(归因 Step/放行单位)。裁决记录
// (approval.record forced)由调用方先落——账本先于副作用(R7)。
func (e *Executor) ExecuteForcedRelease(ctx context.Context, taskID string, rec rings.ApprovalRecord) (string, error) {
	env := &unitEnv{
		Executor: e, TaskID: taskID, StepID: rec.StepID,
		UnitID:  shortID(forcedReleaseUnitPrefix),
		oneShot: true,
	}
	if e.Mirror != nil {
		sess, err := e.Mirror.Session(mirror.Meta{
			TaskID: taskID, StepID: rec.StepID,
			UnitID: env.UnitID, TranscriptID: "forced-" + env.UnitID,
		})
		if err != nil {
			return "", fmt.Errorf("swarm: 研判后放行起镜会话: %w", err)
		}
		defer sess.Close()
		env.mirror = sess
	}

	// 外层兜底限期 = 单工具上限 + 30 秒余量:工具自身的 timeout_ms
	// (≤600 秒)为准,兜底只防失控,不抢跑。
	cctx, cancel := context.WithTimeout(ctx, bashMaxTimeout+30*time.Second)
	defer cancel()

	var target tool.CoreTool
	for _, t := range env.toolset() {
		if t.Name() == rec.ToolName {
			target = t
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("swarm: 研判后放行:工具 %s 不在工具集内", rec.ToolName)
	}
	res, err := target.Call(cctx, json.RawMessage(rec.ToolArgs), nil)
	if err != nil {
		return "", fmt.Errorf("swarm: 研判后放行执行 %s: %w", rec.ToolName, err)
	}
	var b strings.Builder
	for _, blk := range res.Content {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	resultText := b.String()

	// 执行记录入账(与虫群 tool_call 同构;R18 大输出走 blob)。
	srec := rings.SwarmRecord{
		StepID: rec.StepID, UnitID: env.UnitID,
		Type: rings.SwarmRecToolCall, ToolName: rec.ToolName,
		ToolArgs: rec.ToolArgs, IsError: res.IsError,
		Note: "研判后放行执行(审批请求 " + rec.RequestID + ",造物主强制放行)",
	}
	if ref := extractBlobRef(resultText); ref != "" && e.Blobs.Exists(ref) {
		srec.ResultRef, srec.ResultHead = ref, clipLine(resultText, 512)
	} else if ref, head := e.Blobs.spill(resultText); ref != "" {
		srec.ResultRef, srec.ResultHead = ref, clipLine(head, 512)
	} else {
		srec.ResultHead = clipLine(resultText, 2048)
	}
	if _, err := e.Store.Append(ledgerCtx, taskID, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID, Payload: srec,
	}); err != nil {
		return resultText, fmt.Errorf("swarm: 研判后放行执行入账: %w", err)
	}
	return resultText, nil
}
