package shepherd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// skillBodyMarkers 完整版正文的抽样标记(解析完整性的断言面)。
var skillBodyMarkers = []string{
	"# humanizer:去掉中文里的模型腔",
	"## A. 铺垫代替陈述",
	"## B. 节奏靠规则",
	"## C. 夸大与借来的权威",
	"## D. 格式靠规则",
	"## E. 聊天与草稿的残留",
	"## F. 写给错误的读者",
	"## 怎么干活",
	"## 什么时候不动手",
	"### 26. 给已知背景的读者复述",
}

// TestLoadSkillsFullPackage 内嵌 skill 包经 Norma 原生 LoadDir 解析:
// 名字/描述在位,26 条模式全量(编号齐、六组齐、关键小节齐),正文
// 足量(防截断退化),盘上物化与内嵌一致,重复加载幂等。
func TestLoadSkillsFullPackage(t *testing.T) {
	dir := t.TempDir()
	reg, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("加载 skill 包失败: %v", err)
	}
	s, ok := reg.Get("humanizer")
	if !ok {
		t.Fatalf("注册表里没有 humanizer: %v", reg.List())
	}
	if s.Description == "" || !strings.Contains(s.Description, "改像人话") {
		t.Fatalf("描述应含触发条件: %q", s.Description)
	}
	for _, m := range skillBodyMarkers {
		if !strings.Contains(s.Instructions, m) {
			t.Fatalf("正文缺标记 %q(疑似截断或缺节)", m)
		}
	}
	// 26 条编号全量:### 1. 到 ### 26. 每条都在。
	for i := 1; i <= 26; i++ {
		want := "### " + strconv.Itoa(i) + ". "
		if !strings.Contains(s.Instructions, want) {
			t.Fatalf("模式 §%d 缺失(清单应 26 条全量)", i)
		}
	}
	if len(s.Instructions) < 4000 {
		t.Fatalf("正文过短(%d 字节),疑精简版混入", len(s.Instructions))
	}
	// 盘上物化与内嵌一致;重复加载不报错(幂等)。
	cur, err := os.ReadFile(filepath.Join(dir, "humanizer", "SKILL.md"))
	if err != nil {
		t.Fatalf("盘上未物化 SKILL.md: %v", err)
	}
	embedded, err := skillFS.ReadFile("skills/humanizer/SKILL.md")
	if err != nil || string(cur) != string(embedded) {
		t.Fatalf("盘上文件与内嵌不一致: err=%v", err)
	}
	if _, err := LoadSkills(dir); err != nil {
		t.Fatalf("重复加载失败: %v", err)
	}
}

// TestSkillListingDefersBody deferred 机制:Skill 工具的广告面(描述 +
// 可用清单)只带名字与触发条件,完整正文不进上下文——平时零占用。
func TestSkillListingDefersBody(t *testing.T) {
	reg, err := LoadSkills(t.TempDir())
	if err != nil {
		t.Fatalf("加载 skill 包失败: %v", err)
	}
	st := reg.Tool()
	if st.Name() != "Skill" {
		t.Fatalf("工具名应为 Skill,实为 %s", st.Name())
	}
	prompt := st.Prompt()
	if !strings.Contains(prompt, "humanizer") || !strings.Contains(prompt, "去掉 AI 味") {
		t.Fatalf("清单应含名字与触发条件: %q", prompt)
	}
	for _, m := range skillBodyMarkers {
		if strings.Contains(prompt, m) {
			t.Fatalf("deferred 失守:广告面混入正文标记 %q", m)
		}
	}
}

// TestSkillInvokeInjectsInstructions 调用即注入:Skill(humanizer) 的
// 返回里,完整指令作为独立指令消息(Extra)进入会话,tool_result 本身
// 只是一句确认。
func TestSkillInvokeInjectsInstructions(t *testing.T) {
	reg, err := LoadSkills(t.TempDir())
	if err != nil {
		t.Fatalf("加载 skill 包失败: %v", err)
	}
	st := reg.Tool()
	res, err := st.Call(context.Background(), []byte(`{"name":"humanizer"}`), nil)
	if err != nil {
		t.Fatalf("调用 Skill 工具失败: %v", err)
	}
	if res.IsError {
		t.Fatalf("调用不应出错")
	}
	if len(res.Extra) != 1 {
		t.Fatalf("应注入恰好一条指令消息,实为 %d", len(res.Extra))
	}
	inj := res.Extra[0]
	if inj.Role != llm.RoleUser {
		t.Fatalf("指令消息应为 user 角色,实为 %s", inj.Role)
	}
	body := ""
	for _, blk := range inj.Content {
		if blk.Type == llm.BlockText {
			body += blk.Text
		}
	}
	for _, m := range []string{"<invoked-skill name=\"humanizer\"", "# humanizer:去掉中文里的模型腔", "### 1. 不是 X,而是 Y"} {
		if !strings.Contains(body, m) {
			t.Fatalf("注入的指令缺 %q", m)
		}
	}
}

// TestSessionOffersSkillTool 会话接线:Skills 非 nil 时模型请求的工具表
// 含 Skill(与看/说/答并列),nil 时不出现;system 三段式不受影响
// (常驻 C 段原样,与 skill 按需层并存不冲突)。模型调用 Skill 后,
// 下一轮请求里出现完整指令(端到端 mock 链路)。
func TestSessionOffersSkillTool(t *testing.T) {
	store := openStore(t)
	root := t.TempDir()
	const task = "sh-skill"
	seedTask(t, store, task)

	reg, err := LoadSkills(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatalf("加载 skill 包失败: %v", err)
	}
	m := newScriptedModel(func(rr *recordedReq) scriptedRep {
		if rr.Index == 0 {
			return scriptedRep{ToolCalls: []scriptedToolCall{
				{ID: "sk1", Name: "Skill", Args: `{"name":"humanizer"}`},
			}}
		}
		return scriptedRep{Text: "改好了:这段话去掉了三连与收尾。"}
	})
	defer m.Close()
	prov, err := m.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}
	s := &Service{Store: store, Provider: prov, RootDir: root,
		Model: "scripted-shepherd-model", Skills: reg}

	if _, err := s.Enqueue(context.Background(), task, "把这段改像人话:……"); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitView(t, s, task, func(v *View) bool {
		return len(v.Messages) == 1 && v.Messages[0].Reply != ""
	})

	reqs := m.requests()
	if len(reqs) < 2 {
		t.Fatalf("应有至少两次模型请求,实为 %d", len(reqs))
	}
	offered := map[string]bool{}
	for _, tn := range reqs[0].ToolsOffered {
		offered[tn] = true
	}
	for _, name := range []string{"read_progress", "read_findings", "read_units", "advise_overmind", "urgent_intent", "add_goal", "add_constraint", "Skill"} {
		if !offered[name] {
			t.Fatalf("工具 %s 未广告: %v", name, reqs[0].ToolsOffered)
		}
	}
	// system 三段式原样(常驻文风条款仍在,skill 不动它)。
	if !strings.Contains(reqs[0].System, "输出文风(代码固定") {
		t.Fatalf("常驻 C 段应保留: %q", reqs[0].System)
	}
	// 调用后下一轮请求里出现注入的完整指令(deferred → 按需注入)。
	joined := strings.Join(func() []string {
		out := []string{}
		for _, msg := range reqs[1].Messages {
			out = append(out, msg.Content)
		}
		return out
	}(), "\n")
	if !strings.Contains(joined, "<invoked-skill name=\"humanizer\"") ||
		!strings.Contains(joined, "### 1. 不是 X,而是 Y") {
		t.Fatalf("第二轮请求应含注入的完整指令")
	}
	// 工具调用留痕入账(Skill 调用在对话流可见)。
	found := false
	for _, rec := range shepherdEvents(t, store, task) {
		if rec.Type == "tool_call" && rec.ToolName == "Skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Skill 调用应留痕入账")
	}

	// 对照:Skills 为 nil 时不广告 Skill 工具。
	store2 := openStore(t)
	root2 := t.TempDir()
	const task2 = "sh-noskill"
	seedTask(t, store2, task2)
	m2 := newScriptedModel(func(rr *recordedReq) scriptedRep {
		return scriptedRep{Text: "进度正常。"}
	})
	defer m2.Close()
	prov2, err := m2.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}
	s2 := &Service{Store: store2, Provider: prov2, RootDir: root2, Model: "scripted-shepherd-model"}
	if _, err := s2.Enqueue(context.Background(), task2, "进度?"); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitView(t, s2, task2, func(v *View) bool {
		return len(v.Messages) == 1 && v.Messages[0].Reply != ""
	})
	for _, tn := range m2.requests()[0].ToolsOffered {
		if tn == "Skill" {
			t.Fatalf("Skills 为 nil 时不应广告 Skill 工具")
		}
	}
}
