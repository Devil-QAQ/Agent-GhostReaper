package shepherd

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/swarm"
)

// ---- P4-67 ⑥(问题清单 #86 E):牧者信息源通道 ----
// "xxx 是查资料的不是目标"→标记信息源;"xxx 别当信息源了"→撤销;
// 与面板/卡三通道同源同账本(同一 roe.updated 快照动作)。

// TestP467ShepherdInfoSourceMarkAndRevoke 完成标准⑤:牧者标记与面板
// 等效(同一账本动作,同源对账)——标记入清单(带依据)、撤销恢复
// 拦截(tg- 缓存作废)、重复撤销如实报错、回执人话。
func TestP467ShepherdInfoSourceMarkAndRevoke(t *testing.T) {
	s, store, task := p454Env(t)
	ctx := context.Background()

	// 标记:"gitee.com 是查资料的不是目标"。
	out, isErr := runP454Tool(t, s, task, "set_info_source",
		`{"action":"mark","hosts":["gitee.com"],"basis":"公开代码托管服务"}`)
	if isErr || !strings.Contains(out, "gitee.com") {
		t.Fatalf("标记回执不符: %q err=%v", out, isErr)
	}
	st, _ := store.CurrentRoE(ctx, task)
	if len(st.InfoSources) != 1 || st.InfoSources[0].Host != "gitee.com" ||
		!strings.Contains(st.InfoSources[0].Basis, "代码托管") {
		t.Fatalf("牧者标记应与面板同账本入清单: %+v", st.InfoSources)
	}
	// 与面板等效:同一 GrantInfoSource 动作(幂等,清单不重复)。
	if err := swarm.GrantInfoSource(ctx, store, task, "gitee.com", "面板同款动作", "creator"); err != nil {
		t.Fatal(err)
	}
	st, _ = store.CurrentRoE(ctx, task)
	if len(st.InfoSources) != 1 {
		t.Fatalf("同源动作应幂等(清单不重复),得 %d 条", len(st.InfoSources))
	}
	// 命中:RoE 约束块渲染信息源行(提示词纪律同步)。
	block := swarm.RenderRoEConstraint("lead", nil, nil, "", nil, nil, "", st.InfoSources)
	if !strings.Contains(block, "信息源") || !strings.Contains(block, "gitee.com") {
		t.Fatalf("约束块应渲染信息源行: %q", block)
	}
	// 撤销:"gitee.com 别当信息源了"(带端口写法也归一)。
	out, isErr = runP454Tool(t, s, task, "set_info_source",
		`{"action":"revoke","hosts":["gitee.com:443"]}`)
	if isErr || !strings.Contains(out, "撤销") {
		t.Fatalf("撤销回执不符: %q err=%v", out, isErr)
	}
	st, _ = store.CurrentRoE(ctx, task)
	if len(st.InfoSources) != 0 {
		t.Fatalf("撤销后清单应空: %+v", st.InfoSources)
	}
	if v, _ := swarm.TriageVerdictOf(ctx, store, task, "gitee.com"); v != swarm.TriageRevoked {
		t.Fatalf("撤销应落粘性标记,得 %q", v)
	}
	// 重复撤销:回执如实说明不在清单(工具逐址收集,不整单失败)。
	out2, _ := runP454Tool(t, s, task, "set_info_source",
		`{"action":"revoke","hosts":["gitee.com"]}`)
	if !strings.Contains(out2, "不在信息源清单") {
		t.Fatalf("撤销不在册地址回执应如实说明: %q", out2)
	}
	// 坏地址:回执如实说明跳过(不整单失败)。
	out3, isErr3 := runP454Tool(t, s, task, "set_info_source",
		`{"action":"mark","hosts":["不是地址"],"basis":"x"}`)
	if isErr3 && !strings.Contains(out3, "不是可识别的主机地址") {
		t.Fatalf("不可识别地址回执应说明跳过: %q", out3)
	}
	if !isErr3 && !strings.Contains(out3, "跳过") {
		t.Fatalf("不可识别地址回执应说明跳过: %q", out3)
	}
}
