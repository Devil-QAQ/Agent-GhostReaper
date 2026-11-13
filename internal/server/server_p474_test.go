// ---- P4-74(问题清单 #90):审批卡面与记录可读性 · 面板 API 侧 ----
// ① 卡面以命令为准:待决弹门票视图带命令分级(cmd_level,徽标)与步骤
// 分级背景行(step_ground),危害说明按命令实际内容拼装;② 空参数
// TicketToolArgs 原样上卡(前端如实标注"参数未记录"的判定在 web 侧)。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p474EnterGate 落一张在途弹门票(直接经 Gate.Enter,零模型)。
func p474EnterGate(t *testing.T, srv *Server, task, unit, intent string, level rings.ActionLevel, args string) {
	t.Helper()
	if _, err := srv.Gate.Enter(swarm.ApprovalRequest{
		TaskID: task, StepID: "s1", UnitID: unit, Intent: intent,
		ActionLevel: level, ToolName: "bash", ToolArgs: args,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestP474GateCardFaceByCommand 完成标准①(卡面装配):只读命令在
// 修改与持久化步骤中出卡——harm_note 为查看类锚例(命令实际危害)、
// cmd_level=只读探测(徽标按命令)、step_ground 背景行逐字;写入命令
// 卡面为写入、零背景行;认不出的命令回退步骤分级;action_level 审计
// 字段原样(账本与记录不受呈现层影响)。
func TestP474GateCardFaceByCommand(t *testing.T) {
	srv, store, h := p469ServerEnv(t)
	const task = "p474-card-face"
	p471SeedPentest(t, store, task)

	// #90 实锤形态:pwd; ls -la 顶着"修改与持久化"分级弹门。
	p474EnterGate(t, srv, task, "u1", "在目标站落地一个持久化凭据", rings.ActionModifyPersist,
		`{"command":"pwd; ls -la"}`)
	// 对照:写入命令(注册提交)——卡面写入、无背景行。
	p474EnterGate(t, srv, task, "u2", "创建可登录账号", rings.ActionModifyPersist,
		`{"command":"curl -X POST -d 'u=a&p=b' http://jkysd.cn:9902/register"}`)
	// 对照:认不出的命令——命令分级空、无背景行(前端回退步骤分级)。
	p474EnterGate(t, srv, task, "u3", "自定义工具验证", rings.ActionModifyPersist,
		`{"command":"mycustomtool --x 1"}`)

	var body struct {
		Pending []struct {
			ID          string `json:"id"`
			UnitID      string `json:"unit_id"`
			ActionLevel string `json:"action_level"`
			HarmNote    string `json:"harm_note"`
			CmdLevel    string `json:"cmd_level"`
			StepGround  string `json:"step_ground"`
			ToolArgs    string `json:"tool_args"`
		} `json:"pending"`
	}
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)

	byUnit := map[string]int{}
	for i, p := range body.Pending {
		byUnit[p.UnitID] = i
	}
	// 只读命令:卡面=命令实际危害(锚例)+命令分级徽标+步骤分级背景行。
	ro := body.Pending[byUnit["u1"]]
	if ro.HarmNote != "将执行 pwd 与 ls 查看目录和文件列表。目标不会发生任何变化。" {
		t.Fatalf("只读命令危害说明应为查看类锚例: %q", ro.HarmNote)
	}
	if ro.CmdLevel != "只读探测" {
		t.Fatalf("命令分级应为只读探测: %q", ro.CmdLevel)
	}
	if ro.StepGround != "所属步骤含修改类动作，按步骤门槛过门" {
		t.Fatalf("步骤分级背景行不符: %q", ro.StepGround)
	}
	if ro.ActionLevel != "修改与持久化" {
		t.Fatalf("步骤分级审计字段应原样: %q", ro.ActionLevel)
	}
	// 写入命令:注册锚例、无背景行。
	w := body.Pending[byUnit["u2"]]
	if w.CmdLevel != "修改与持久化" || w.StepGround != "" {
		t.Fatalf("写入命令应无背景行: cmd=%q ground=%q", w.CmdLevel, w.StepGround)
	}
	if want := "将向 jkysd.cn:9902 提交注册表单创建账号。目标会新增一个可登录账号。账号会留存于目标系统，管理员可能察觉异常注册。"; w.HarmNote != want {
		t.Fatalf("写入命令危害说明应为注册锚例: %q", w.HarmNote)
	}
	// 认不出的命令:cmd_level 空、无背景行。
	unk := body.Pending[byUnit["u3"]]
	if unk.CmdLevel != "" || unk.StepGround != "" {
		t.Fatalf("认不出的命令应零命令分级零背景行: cmd=%q ground=%q", unk.CmdLevel, unk.StepGround)
	}
}

// TestP474EmptyArgsAndScopeCardFace 完成标准②(空参数原样上卡)与范围卡
// 危害说明按命令拼装:无参数调用("{}")ToolArgs 原样透传(前端如实标注
// "参数未记录");带 GET 命令的范围拦截卡 harm_note 为只读卡面。
func TestP474EmptyArgsAndScopeCardFace(t *testing.T) {
	srv, store, h := p469ServerEnv(t)
	const task = "p474-empty-args"
	p471SeedPentest(t, store, task)
	// 无参数 bash 调用(模型发起的空参数形态):ToolArgs 原样 "{}"。
	p474EnterGate(t, srv, task, "u1", "空参数调用", rings.ActionModifyPersist, `{}`)
	// 带命令的范围拦截卡(账本 scope_wait):危害说明按命令实际内容。
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u2", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("gitee.com", "443"), StepID: "s-cover", UnitID: "u2",
			Type: rings.ApprovalRecScopeWait, ActionLevel: rings.ActionModifyPersist,
			Intent: "收集代码情报", ToolName: "bash",
			ToolArgs: json.RawMessage(`{"command":"curl https://gitee.com:443/x"}`),
		}}); err != nil {
		t.Fatal(err)
	}

	var body struct {
		Pending []struct {
			UnitID   string `json:"unit_id"`
			Kind     string `json:"kind"`
			ToolArgs string `json:"tool_args"`
			HarmNote string `json:"harm_note"`
			CmdLevel string `json:"cmd_level"`
		} `json:"pending"`
	}
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)

	for _, p := range body.Pending {
		switch p.UnitID {
		case "u1":
			if p.ToolArgs != "{}" {
				t.Fatalf("空参数应原样上卡(前端标注参数未记录): %q", p.ToolArgs)
			}
		case "u2":
			if p.Kind != "scope" {
				t.Fatalf("u2 应为范围卡: %q", p.Kind)
			}
			// GET 拉取:目标不会变化(按命令说,不按步骤分级说)。
			if !containsAll(p.HarmNote, "curl", "目标不会发生任何变化") {
				t.Fatalf("范围卡危害说明应按命令实际内容: %q", p.HarmNote)
			}
		}
	}
}

// containsAll 全部子串命中。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
