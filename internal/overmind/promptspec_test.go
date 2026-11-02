package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- §11 语义细则入各角色基线:逐条对照核对 ----

// TestOvermindPromptSpecClauses 主宰基线固定段的语义细则逐条核对
// (完成标准④"各角色基线逐条对照 §11"):0 意图正当理由(硬底线 +
// 两种正当理由)、多样性条款、复核上限、重派判定树(done/exhausted/
// blocked)、收尾词;输出契约(JSON)为最后一段——机器解析接口不随
// 正文编辑漂移。
func TestOvermindPromptSpecClauses(t *testing.T) {
	fixed := OvermindPromptSpec.Fixed
	if len(fixed) != 3 {
		t.Fatalf("主宰基线应为三段固定段(纪律/输出契约/常驻文风),实际 %d", len(fixed))
	}
	d := fixed[0]
	for _, want := range []string{
		"意图数量硬底线",                         // 0 意图正当理由的硬底线面
		"必须派发至少 1 条意图",                    // 防停摆
		"在等在跑单位产出",                        // 正当理由①
		"全部目标已达成",                         // 正当理由②
		"路线多样性",                           // 多样性条款
		"2~3 条机理不同",                       // 理想并存路线数
		"多样性永远服从 RoE",                     // 多样性服从 RoE
		"复核上限",                            // 复核上限
		"同一否定方向至多复核一次",                    // 至多一次
		"重派判定树",                           // 重派判定树
		"done:", "exhausted:", "blocked(", // 三种停法三种对策
		"轨迹真实进度,不是状态措辞", // 依据是 trace 不是措辞
		"0 条意图是完全正常的结果", // 收尾词
		// P3-14a B:拆解硬底线——必须干新活,不碰工具与打法。
		"必须产出至少一个新方向(新模块/新角度/更深一层)",
		"禁止重复已确认事实与已完成步骤",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("主宰纪律段缺 §11 条款: %q", want)
		}
	}
	if tail := strings.TrimSpace(fixed[1]); !strings.HasSuffix(tail, "不要输出 JSON 以外的任何内容。") ||
		!strings.Contains(tail, `"new_intents"`) {
		t.Fatalf("输出契约段(JSON)必须完整在位")
	}
	// P3-11a:常驻文风段殿后(研判叙述按人话写,JSON 契约原样——
	// 作用域行在段内)。
	if fixed[2] != swarm.ResidentStyleRules {
		t.Fatalf("主宰基线第 3 固定段应为常驻文风条款")
	}
	// P3-11a:假设评估会话(单发系统提示词)同样携带——裁决理由与
	// 验证意图是上面板的研判叙述。
	if !strings.Contains(hypoEvalSystemPrompt, swarm.ResidentStyleRules) {
		t.Fatalf("假设评估提示词须含常驻文风条款")
	}
	// 虫群侧写回粒度条款(§11)在虫群基线——跨包断言见 swarm 包
	// promptspec_test;此处核对主宰侧不重复携带虫群条款。
	if strings.Contains(d, "写回粒度") {
		t.Fatalf("写回粒度属虫群基线,主宰纪律段不应携带")
	}
}

// TestOvermindIntentStyleClause P4-11 ①:拆解契约带 intent 文风收口与
// 正反样例,声明一句话摘要(combined_claims 的 statement/impact)同款
// 收口——共用句与虫群声明提交同一份(swarm.StructuredFieldStyle,防两
// 处漂移);只管新生成不改写已有数据在文;文风块不碰 assessment 叙述
// 字段(研判叙述自由度不收窄)。
func TestOvermindIntentStyleClause(t *testing.T) {
	for _, want := range []string{
		"字段文风",
		"只管新写,不改写图上已有步骤",
		"资产+手法+目标的点式短句",
		swarm.StructuredFieldStyle,
		"写不下就收窄定位——点一个代表路径加\"等 N 个\",不铺枚举清单",
		"正例:对 10.0.0.5:8080 做目录枚举,找未授权入口",
		"正例(长定位收窄):对 4330 根目录直取 20 个常见文件名(README.md 等),确认根外文件是否暴露",
		"反例:",
		"反例(铺清单):",
		"无定位、无数字、长而空",
		"statement / impact",
	} {
		if !strings.Contains(overmindOutputContract, want) {
			t.Fatalf("拆解契约缺 P4-11 ① 文风条款: %q", want)
		}
	}
	// 文风块只收口 intent 与声明字段:不带 assessment 五问的措辞约束
	// (研判叙述仍由模型自由写,ResidentStyleRules 管人话不管长度)。
	style := overmindOutputContract[strings.Index(overmindOutputContract, "字段文风"):]
	for _, banned := range []string{"q_access", "assessment 叙述", "note 文风"} {
		if strings.Contains(style, banned) {
			t.Fatalf("文风块不应约束 assessment 叙述字段: %q", banned)
		}
	}
}

// TestOvermindSystemSegmentsRoETop 主宰拆解 system 段位置规范:RoE
// 约束块置顶(凌驾声明)→ A 可编辑正文 → B 纪律 → C 输出契约;DB
// 覆盖 A 段后固定段逐字仍在(删不掉)。
func TestOvermindSystemSegmentsRoETop(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "om-prompt-task"
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{DeniedTools: []string{"bash"}},
	}); err != nil {
		t.Fatalf("种 RoE 失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}

	segs := l.overmindSystemSegments(ctx)
	if len(segs) != 5 {
		t.Fatalf("主宰 system 应为 5 段(RoE/A/B/C/文风),实际 %d", len(segs))
	}
	if !strings.HasPrefix(segs[0], "【授权约束 · RoE】") || !strings.Contains(segs[0], "凌驾于下方一切探索启发式") ||
		!strings.Contains(segs[0], "禁止 [bash]") {
		t.Fatalf("RoE 约束块须置顶且含账本当前边界: %q", clipEval(segs[0]))
	}
	if segs[1] != overmindEditableDefault || segs[2] != overmindDisciplineRules || segs[3] != overmindOutputContract ||
		segs[4] != swarm.ResidentStyleRules {
		t.Fatalf("默认组装段序不符:A 默认正文/B 纪律/C 输出契约/D 常驻文风")
	}

	// DB 覆盖 A 段:固定段仍在(三段式结构保证)。
	if err := store.SetSetting(ctx, SettingKeyPromptOvermind, "定制主宰研判正文", "creator"); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	segs = l.overmindSystemSegments(ctx)
	if segs[1] != "定制主宰研判正文" {
		t.Fatalf("A 段应为 DB 覆盖正文: %q", segs[1])
	}
	if segs[2] != overmindDisciplineRules || segs[3] != overmindOutputContract ||
		segs[4] != swarm.ResidentStyleRules {
		t.Fatalf("覆盖后固定段必须逐字在位")
	}
}

// TestDeltaLinesCarryDirectives §11 触发事件带预写行动指令:快照变化集
// 每行附"该做什么"——设计原例"人新增了目标:请据此补充探索方向"逐字
// 在位;步骤停法带重派判定树路由;RoE 更新带越界复核提示。
func TestDeltaLinesCarryDirectives(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "delta-directive-task"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "摸清暴露面", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a1", NodeType: rings.NodeAsset, Title: "演示站点", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测站点", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f-1", NodeType: rings.NodeFact, Title: "路径存在", Status: "candidate"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "f-1", Status: "confirmed"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ApprovalThreshold: rings.ActionLevel("主动测试")}},
	); err != nil {
		t.Fatalf("种事件失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}
	joined := strings.Join(l.deltaSinceLastAssessment(ctx), "\n")
	for _, want := range []string{
		"人新增了目标 g1 [active] 摸清暴露面 → 据此补充探索方向",
		"新资产 a1 [service] 演示站点 → 评估覆盖缺口",
		"步骤 s1 → done → 检查归纳产出后按产出规划",
		"f-1 → confirmed(路径存在) → 新确认事实",
		"RoE 授权边界已更新 → 复核后续派发是否越界",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("变化集行缺行动指令: %q\n实际:\n%s", want, joined)
		}
	}
}
