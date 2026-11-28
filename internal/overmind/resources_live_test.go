package overmind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/testmodel"
)

func TestResourcePlannerLiveExploration(t *testing.T) {
	p := testmodel.Provider(t)
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-conditions", true: "new-evidence"}[fresh], func(t *testing.T) {
			if !fresh {
				l, _ := workReviewFixture(t, "repeat")
				l.Provider = p
				if evidence := os.Getenv("GHOSTREAPER_PLANNER_EVIDENCE"); evidence != "" {
					l.RootDir = filepath.Join(evidence, "repeat")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
				defer cancel()
				r, _, e := l.decompose(ctx)
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, control := range r.WorkControls {
					if control.StepID == "s1" && (control.Action == "steer" || control.Action == "stop") {
						found = true
					}
				}
				if !found {
					t.Fatal("同条件无新增重复未纠偏或止损")
				}
				b, _ := json.Marshal(r.WorkControls)
				t.Log(string(b))
				return
			}

			s := openStore(t)
			mkTask(t, s, "resource-plan")
			events := []rings.Event{
				{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{CreatorNotes: new("本任务只核查 https://fixture.example.test/check 的报告导出访问控制，不涉及其他功能或目标。相同条件下的无新增重试没有意义，按实际观察决定下一步。")}},
				{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "g1", NodeType: rings.NodeGoal, Status: GoalStatusActive, Title: "核查指定报告导出的访问控制"}},
				{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, Status: "active", AssetClass: rings.AssetService, Title: "https://fixture.example.test/check"}},
				{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Status: "done", Title: "测试未登录时直接导出报告", Detail: map[string]any{"action_level": "主动测试", "goal_id": "g1", "result": "两个 PoC 在未登录条件下直接请求均为403；相同条件已经额外复核一次，仍为403。GET、POST、方法覆盖头和全部参数变体均已检查；导出页面及前端脚本已经读取，未找到其他入口或新条件。没有登录态，没有其他实际线索。"}}},
				{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
				{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Status: "confirmed", Title: "未登录直接导出均为403，相同条件复核后仍为403；GET、POST、方法覆盖头及全部参数变体均已测，页面和前端脚本也已查过，当前没有新的入口或条件。这仅说明已测试方法在该条件下失败，不说明整个访问控制方向不可行。"}},
			}
			if fresh {
				events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "f2", NodeType: rings.NodeFact, Status: "confirmed", Title: "新证据：操作者提供两个获授权测试账号的有效登录态和各自报告编号；账号A正常导出自己的报告返回200，报告编号是可控参数。尚未测A访问B报告。这与此前未登录直接请求的条件实质不同。"}})
			}
			if _, e := s.Append(t.Context(), "resource-plan", events...); e != nil {
				t.Fatal(e)
			}
			root := t.TempDir()
			if evidence := os.Getenv("GHOSTREAPER_PLANNER_EVIDENCE"); evidence != "" {
				root = filepath.Join(evidence, map[bool]string{false: "same", true: "new"}[fresh])
			}
			l := &Loop{Store: s, TaskID: "resource-plan", Provider: p, RootDir: root, Swarm: swarm.NewExecutor(s, p, root, swarm.Budget{})}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			r, _, e := l.decompose(ctx)
			if e != nil {
				t.Fatal("正式研判未完成", e)
			}
			b, _ := json.Marshal(r)
			t.Log(string(b))
			if fresh && len(r.NewIntents) == 0 {
				t.Fatal("新证据未形成正常探索")
			}

		})
	}
}
