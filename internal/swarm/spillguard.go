// spillguard.go P4-39 ①「对话内大输出瘦身」的入对话前落盘守卫:任何
// 单位工具的结果文本超过 30,000 字符(定值,不做配置项)即整段落内容
// 寻址仓(与 P4-35 证据仓库同构的 BlobStore),对话内只留截头 + 占位
// 引用——模型经 read 工具按引用分段读回全文(tools_web.go 的引用解析),
// 不新增工具。既有 per-工具 R18 spill(bash/read/web_fetch,4096 阈值)
// 比本守卫更严,照旧先行;本守卫兜住无自有 spill 的工具(如 list_claims)
// 与未来新增工具,结构上保证"单次工具输出入对话必有界"。
// 记账(P4-39 ④):落盘动作记黑匣子 tool_spill(引用+字节数),账本
// SwarmRecord 侧的 ResultRef 对账路径不变(extractBlobRef 提取引用)。
package swarm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// convSpillThreshold 单次工具输出入对话的落盘阈值(P4-39 ①:30,000 字符
// 定值,不做配置项)。超过即整段落 blob,对话内只留截头。
const convSpillThreshold = 30000

// spillGuardTool 守卫包装:元数据(名称/Schema/权限/只读判定)全透传
// 内层,只在 Call 出口做超长落盘(与 guard.go 的 wrappedTool 同款形态)。
type spillGuardTool struct {
	inner tool.CoreTool
	env   *unitEnv
}

// wrapSpillGuard 给工具全集套落盘守卫(在 toolGuard 包装之外再包一层:
// 守卫看到的是门卫处理后的最终结果——暂缓等合成短文本不受影响)。
func wrapSpillGuard(env *unitEnv, tools []tool.CoreTool) []tool.CoreTool {
	out := make([]tool.CoreTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, &spillGuardTool{inner: t, env: env})
	}
	return out
}

func (g *spillGuardTool) Name() string                { return g.inner.Name() }
func (g *spillGuardTool) Description() string         { return g.inner.Description() }
func (g *spillGuardTool) Prompt() string              { return g.inner.Prompt() }
func (g *spillGuardTool) InputSchema() map[string]any { return g.inner.InputSchema() }
func (g *spillGuardTool) IsReadOnly(in json.RawMessage) bool {
	return g.inner.IsReadOnly(in)
}
func (g *spillGuardTool) IsConcurrencySafe(in json.RawMessage) bool {
	return g.inner.IsConcurrencySafe(in)
}
func (g *spillGuardTool) CheckPermissions(ctx context.Context, in json.RawMessage, pc permission.Context) permission.Decision {
	return g.inner.CheckPermissions(ctx, in, pc)
}

func (g *spillGuardTool) Call(ctx context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
	res, err := g.inner.Call(ctx, in, tc)
	if err != nil {
		return res, err
	}
	// 只处理纯文本结果块(本项目工具的返回形态);零文本或多块结构不动。
	text := res.Flatten()
	if len(text) <= convSpillThreshold {
		return res, nil
	}
	e := g.env.Executor
	ref, werr := e.Blobs.Write([]byte(text))
	if werr != nil {
		// 落盘失败不拦工具结果:如实原样返回(对话变大一次,好过结果丢失;
		// 故障进运行日志可查)。
		runlog.Errorf("工具输出落盘失败: 任务 %s 单位 %s 工具 %s: %v",
			g.env.TaskID, g.env.UnitID, g.inner.Name(), werr)
		return res, nil
	}
	head := text[:blobThreshold]
	out := fmt.Sprintf("%s\n[输出超长,全文已存档: %s]", head, ref)
	detail := map[string]any{
		"tool": g.inner.Name(), "ref": ref, "bytes": len(text),
	}
	if berr := blackbox.Record(context.Background(), e.Store, g.env.TaskID, "swarm:"+g.env.UnitID,
		rings.BlackboxRecord{Type: rings.BlackboxToolSpill, StepID: g.env.StepID, UnitID: g.env.UnitID, Detail: detail}); berr != nil {
		runlog.Errorf("黑匣子写入失败: 工具输出落盘 %s/%s: %v", g.env.TaskID, g.env.UnitID, berr)
	}
	res.Content = []llm.ContentBlock{llm.TextBlock(out)}
	return res, nil
}
