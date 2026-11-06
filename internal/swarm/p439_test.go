// p439_test.go P4-39「对话内大输出瘦身」确定性测试:① 30k 落盘守卫
// (超阈值落 blob+对话内截头引用、黑匣子 tool_spill 记账、阈值内原样、
// 错误结果同治、元数据透传)② read 按存档引用分段读回 ③ 宿主微压缩
// (保最近 5 条、submit_* 不清、幂等、门限、黑匣子 micro_compact、
// 窗口未配置不挂、超长判定委托)。
package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/rings"
)

// p439Env 建带库与 blob 的单位环境。
func p439Env(t *testing.T) (*unitEnv, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	e := &Executor{
		Store:   store,
		Blobs:   NewBlobStore(filepath.Join(dir, "blobs")),
		RootDir: dir,
	}
	env := &unitEnv{Executor: e, TaskID: "t-p439", StepID: "i1", UnitID: "u1", TranscriptID: "tr1"}
	return env, store
}

// p439StubTool 返回定长文本的哑工具。
func p439StubTool(name string, text string, isErr bool) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        name,
		Description: "stub",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			if isErr {
				return tool.Errorf(text), nil
			}
			return tool.Text(text), nil
		},
	})
}

// p439Blackbox 拉全部黑匣子记录。
func p439Blackbox(t *testing.T, store *rings.Store, task string) []rings.BlackboxRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.BlackboxRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var r rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 blackbox.record: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// ---- ① 落盘守卫 ----

func TestP439SpillGuardOversized(t *testing.T) {
	env, store := p439Env(t)
	orig := strings.Repeat("整段大输出行。\n", 6000) // ~108k 字符
	wrapped := wrapSpillGuard(env, []tool.CoreTool{p439StubTool("list_claims", orig, false)})[0]

	res, err := wrapped.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	got := res.Flatten()
	if len(got) >= len(orig) {
		t.Fatalf("对话内结果应显著缩短: %d vs 原文 %d", len(got), len(orig))
	}
	if !strings.HasPrefix(got, orig[:blobThreshold]) {
		t.Fatalf("对话内应保留原文截头")
	}
	ref := extractBlobRef(got)
	if ref == "" || !strings.Contains(got, "[输出超长,全文已存档: "+ref+"]") {
		t.Fatalf("对话内应有存档引用占位: %q", clipLine(got, 120))
	}
	// 按指针读回:字节一致。
	back, err := env.Executor.Blobs.Read(ref)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if string(back) != orig {
		t.Fatalf("落盘原文与字节不一致: %d vs %d", len(back), len(orig))
	}
	// 黑匣子 tool_spill:引用与字节数对账。
	recs := p439Blackbox(t, store, env.TaskID)
	var spill *rings.BlackboxRecord
	for i := range recs {
		if recs[i].Type == rings.BlackboxToolSpill {
			spill = &recs[i]
		}
	}
	if spill == nil {
		t.Fatalf("黑匣子缺 tool_spill")
	}
	if spill.Detail["ref"] != ref || spill.Detail["tool"] != "list_claims" {
		t.Fatalf("tool_spill 明细不符: %v", spill.Detail)
	}
	if b, _ := spill.Detail["bytes"].(float64); int(b) != len(orig) {
		t.Fatalf("tool_spill 字节数不符: %v vs %d", spill.Detail["bytes"], len(orig))
	}
}

func TestP439SpillGuardUnderThresholdPassthrough(t *testing.T) {
	env, store := p439Env(t)
	orig := strings.Repeat("短", convSpillThreshold/3) // ~10k 字符,阈值内
	wrapped := wrapSpillGuard(env, []tool.CoreTool{p439StubTool("http_probe", orig, false)})[0]

	res, err := wrapped.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || res.Flatten() != orig {
		t.Fatalf("阈值内应原样返回: err=%v", err)
	}
	for _, r := range p439Blackbox(t, store, env.TaskID) {
		if r.Type == rings.BlackboxToolSpill {
			t.Fatalf("阈值内不应记 tool_spill")
		}
	}
}

func TestP439SpillGuardErrorResultAlsoSpilled(t *testing.T) {
	env, _ := p439Env(t)
	orig := strings.Repeat("报错正文很长。\n", 4000) // ~60k
	wrapped := wrapSpillGuard(env, []tool.CoreTool{p439StubTool("bash", orig, true)})[0]

	res, err := wrapped.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if !res.IsError {
		t.Fatalf("错误标记应保留")
	}
	got := res.Flatten()
	ref := extractBlobRef(got)
	if ref == "" {
		t.Fatalf("错误大输出也应落盘占位")
	}
	if back, _ := env.Executor.Blobs.Read(ref); string(back) != orig {
		t.Fatalf("错误原文读回应字节一致")
	}
}

func TestP439SpillGuardMetadataPassthrough(t *testing.T) {
	env, _ := p439Env(t)
	inner := p439StubTool("read", "x", false)
	wrapped := wrapSpillGuard(env, []tool.CoreTool{inner})[0]
	if wrapped.Name() != "read" || wrapped.Description() != "stub" {
		t.Fatalf("元数据应透传: %q", wrapped.Name())
	}
	if !wrapped.IsReadOnly(json.RawMessage(`{}`)) {
		t.Fatalf("只读判定应透传")
	}
	if d := wrapped.CheckPermissions(context.Background(), json.RawMessage(`{}`), permission.Context{}); d.Behavior != permission.Allow {
		t.Fatalf("权限判定应透传: %+v", d)
	}
	// 内层错误原样上抛。
	boom := tool.Build(tool.Spec{Name: "boom", Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		return tool.Result{}, errors.New("炸了")
	}})
	w2 := wrapSpillGuard(env, []tool.CoreTool{boom})[0]
	if _, err := w2.Call(context.Background(), json.RawMessage(`{}`), nil); err == nil || err.Error() != "炸了" {
		t.Fatalf("内层 error 应原样上抛: %v", err)
	}
}

// ---- ① read 按存档引用回读 ----

func TestP439ReadBlobRef(t *testing.T) {
	env, store := p439Env(t)
	lines := make([]string, 0, 300)
	for i := 1; i <= 300; i++ {
		lines = append(lines, fmt.Sprintf("第 %d 行内容", i))
	}
	full := strings.Join(lines, "\n")
	ref, err := env.Executor.Blobs.Write([]byte(full))
	if err != nil {
		t.Fatalf("写入 blob 失败: %v", err)
	}
	rd := env.readTool()
	call := func(args string) tool.Result {
		t.Helper()
		res, err := rd.Call(context.Background(), json.RawMessage(args), nil)
		if err != nil {
			t.Fatalf("read 调用失败: %v", err)
		}
		return res
	}
	// 默认段:前 400 行(全文 300 行,一次读齐)。
	res := call(fmt.Sprintf(`{"path":%q}`, ref))
	text := res.Flatten()
	if !strings.Contains(text, "第 1 行内容") || !strings.Contains(text, "第 300 行内容") {
		t.Fatalf("引用读回应覆盖首尾行: %q", clipLine(text, 120))
	}
	// 分段读回:offset=101, limit=50 → 第 101~150 行。
	res = call(fmt.Sprintf(`{"path":%q,"offset":101,"limit":50}`, ref))
	text = res.Flatten()
	if !strings.Contains(text, "第 101 行内容") || !strings.Contains(text, "第 150 行内容") {
		t.Fatalf("分段读回不符: %q", clipLine(text, 160))
	}
	if strings.Contains(text, "第 100 行内容") || strings.Contains(text, "第 151 行内容") {
		t.Fatalf("分段边界越界: %q", clipLine(text, 160))
	}
	// 逐段对账:两段拼齐即全文。
	resA := call(fmt.Sprintf(`{"path":%q,"limit":150}`, ref))
	resB := call(fmt.Sprintf(`{"path":%q,"offset":151}`, ref))
	if !strings.Contains(resA.Flatten(), "第 150 行内容") || !strings.Contains(resB.Flatten(), "第 151 行内容") {
		t.Fatalf("两段拼齐不符")
	}
	// 坏引用:格式非法/原文缺失的人话报错。
	bad := call(`{"path":"sha256:` + strings.Repeat("0", 64) + `"}`)
	if !bad.IsError || !strings.Contains(bad.Flatten(), "存档原文不存在") {
		t.Fatalf("缺失引用应人话报错: %q", clipLine(bad.Flatten(), 120))
	}
	junk := call(`{"path":"参见 sha256:` + strings.Repeat("a", 64) + `"}`)
	if !junk.IsError || !strings.Contains(junk.Flatten(), "原样填入") {
		t.Fatalf("夹带引用应指路原样填入: %q", clipLine(junk.Flatten(), 120))
	}
	// 超 30,000 的 read 段同样落盘占位(30k 定值口径),且该段引用可再
	// 回读(套娃逃逸:分段 limit 收小即全文可见);落盘动作记黑匣子。
	bigLines := make([]string, 0, 2000)
	for i := 1; i <= 2000; i++ {
		bigLines = append(bigLines, strings.Repeat(fmt.Sprintf("长行 %d ", i), 10))
	}
	bigRef, _ := env.Executor.Blobs.Write([]byte(strings.Join(bigLines, "\n")))
	before := len(p439Blackbox(t, store, env.TaskID))
	res = call(fmt.Sprintf(`{"path":%q}`, bigRef))
	text = res.Flatten()
	segRef := extractBlobRef(text[strings.LastIndex(text, "全文已存档: "):])
	if segRef == "" || len(text) >= len(strings.Join(bigLines, "\n")) {
		t.Fatalf("超 30k 的 read 段应落盘占位: len=%d", len(text))
	}
	seg, err := env.Executor.Blobs.Read(segRef)
	if err != nil || !strings.Contains(string(seg), "长行 1 ") {
		t.Fatalf("read 段引用应可回读: err=%v", err)
	}
	var readSpill *rings.BlackboxRecord
	for i, r := range p439Blackbox(t, store, env.TaskID) {
		if r.Type == rings.BlackboxToolSpill && r.Detail["tool"] == "read" {
			readSpill = &p439Blackbox(t, store, env.TaskID)[i]
		}
	}
	if readSpill == nil {
		t.Fatalf("read 落盘应记黑匣子 tool_spill")
	}
	if readSpill.Detail["ref"] != segRef {
		t.Fatalf("read 落盘黑匣子引用不符: %v", readSpill.Detail)
	}
	if n := len(p439Blackbox(t, store, env.TaskID)); n != before+1 {
		t.Fatalf("应恰新增一条黑匣子记录: %d → %d", before, n)
	}
}

// ---- ② 宿主微压缩 ----

// p439Turns 造 n 对 tool_use/tool_result 轮(过程工具与受控入口穿插)。
// 结果体按字节数凑长(ASCII 填充,规避多字节膨胀)。
func p439Turns(names []string, bodyBytes int) []llm.Message {
	var msgs []llm.Message
	for i, name := range names {
		id := fmt.Sprintf("tu-%d", i)
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(`{}`)},
		}})
		header := fmt.Sprintf("%s result %d: ", name, i)
		body := header + strings.Repeat("x", bodyBytes-len(header))
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: id, Content: []llm.ContentBlock{llm.TextBlock(body)}},
		}})
	}
	return msgs
}

func TestP439MicroCompactKeepsRecentFive(t *testing.T) {
	names := []string{
		"bash", "read", "http_probe", "port_probe", "web_search",
		"web_fetch", "list_claims", "bash",
		"submit_test_claim", "submit_candidate_fact", // 受控入口:不清
		"read", "bash",
	}
	msgs := p439Turns(names, 500)
	out, cleared, freed := microCompactGR(msgs)
	// 可压缩 10 条,保最近 5 条 → 清 5 条(最旧的 bash/read/http_probe/port_probe/web_search)。
	if cleared != 5 {
		t.Fatalf("应清出 5 条,实际 %d", cleared)
	}
	if freed <= 0 {
		t.Fatalf("清出字符数应为正")
	}
	if len(out) != len(msgs) {
		t.Fatalf("消息条数不应变")
	}
	clearedIDs := map[string]bool{"tu-0": true, "tu-1": true, "tu-2": true, "tu-3": true, "tu-4": true}
	for _, m := range out {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			single := len(b.Content) == 1 && b.Content[0].Type == llm.BlockText
			if clearedIDs[b.ToolUseID] {
				if !single || b.Content[0].Text != microClearedText {
					t.Fatalf("旧轮结果应清为占位: %s", b.ToolUseID)
				}
			} else if single && b.Content[0].Text == microClearedText {
				t.Fatalf("近 5 条与受控入口不应被清: %s", b.ToolUseID)
			}
		}
	}
	// 原数组不动(非破坏:调用方拿返回值)。
	if msgs[1].Content[0].Content[0].Text == microClearedText {
		t.Fatalf("原消息数组不应被就地改写")
	}
	// 幂等:再跑一遍零清出。
	if _, again, _ := microCompactGR(out); again != 0 {
		t.Fatalf("重复清理应幂等,实际清 %d", again)
	}
	// 可压缩条数 ≤ 5:零清出。
	small := p439Turns([]string{"bash", "bash", "bash", "submit_test_claim"}, 100)
	if _, n, _ := microCompactGR(small); n != 0 {
		t.Fatalf("≤5 条可压缩不应清,实际 %d", n)
	}
}

func TestP439UnitCompactorPreGateAndLedger(t *testing.T) {
	env, store := p439Env(t)
	// 窗口 50000、摘要预留 4096 → 微压缩线 (50000-4096)*6/10 = 27442。
	c := newUnitCompactor(store, env.TaskID, env.StepID, env.UnitID, 50000, 4096, nil)
	if c == nil {
		t.Fatalf("窗口已配置应挂压缩器")
	}
	if got := c.microThreshold(); got != (50000-4096)*6/10 {
		t.Fatalf("微压缩线算式不符: %d", got)
	}
	msgs := p439Turns([]string{"bash", "read", "http_probe", "port_probe", "web_search", "web_fetch", "bash", "read"}, 4000)

	// 门限下(lastInputTokens 远低于线):不动、无黑匣子。
	out := c.Pre(context.Background(), msgs, 1000)
	same := true
	for i := range msgs {
		if len(out[i].Content) != len(msgs[i].Content) {
			same = false
		}
	}
	if !same {
		t.Fatalf("门限下不应清理")
	}
	for _, r := range p439Blackbox(t, store, env.TaskID) {
		if r.Type == rings.BlackboxMicroCompact {
			t.Fatalf("门限下不应记 micro_compact")
		}
	}

	// 过线(lastInputTokens 超线):清出 3 条(8-5),黑匣子明细对账。
	out = c.Pre(context.Background(), msgs, c.microThreshold()+1000)
	if out[1].Content[0].Content[0].Text != microClearedText {
		t.Fatalf("过线应清最旧结果体")
	}
	if out[len(out)-1].Content[0].Content[0].Text == microClearedText {
		t.Fatalf("最近 5 条应保留")
	}
	recs := p439Blackbox(t, store, env.TaskID)
	var mc *rings.BlackboxRecord
	for i := range recs {
		if recs[i].Type == rings.BlackboxMicroCompact {
			mc = &recs[i]
		}
	}
	if mc == nil {
		t.Fatalf("黑匣子缺 micro_compact")
	}
	if mc.UnitID != env.UnitID || mc.StepID != env.StepID {
		t.Fatalf("micro_compact 归属不符: %v/%v", mc.UnitID, mc.StepID)
	}
	if n, _ := mc.Detail["cleared"].(float64); int(n) != 3 {
		t.Fatalf("micro_compact 清出条数不符: %v", mc.Detail)
	}
	if w, _ := mc.Detail["window"].(float64); int(w) != 50000 {
		t.Fatalf("micro_compact 窗口明细不符: %v", mc.Detail)
	}

	// 幂等:对已清理结果再 Pre,不再记新事件。
	before := len(p439Blackbox(t, store, env.TaskID))
	c.Pre(context.Background(), out, c.microThreshold()+1000)
	if after := len(p439Blackbox(t, store, env.TaskID)); after != before {
		t.Fatalf("重复 Pre 不应再记黑匣子: %d → %d", before, after)
	}
}

func TestP439UnitCompactorNilWithoutWindow(t *testing.T) {
	if c := newUnitCompactor(nil, "t", "i1", "u1", 0, 4096, nil); c != nil {
		t.Fatalf("窗口未配置应返回 nil")
	}
}

func TestP439UnitCompactorIsOverflowDelegates(t *testing.T) {
	env, store := p439Env(t)
	c := newUnitCompactor(store, env.TaskID, env.StepID, env.UnitID, 50000, 4096, nil)
	if !c.IsOverflow(errors.New("request too long for model context length")) {
		t.Fatalf("超长错误判定应委托引擎")
	}
	if c.IsOverflow(errors.New("别的错误")) {
		t.Fatalf("非超长不应误判")
	}
}
