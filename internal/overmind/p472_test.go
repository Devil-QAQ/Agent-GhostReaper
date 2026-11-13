// ---- P4-72(问题清单 #88 B 方案):发现路的语法守门挂点与合一适配 ----
// 语法守门同源单函数挂资产入图处:非地址形态主机(无点内网名、垃圾
// token)不进分诊,照走现行入图——无点主机名本就匹配不上闭包写法,
// 是图上资产但不是可判定越界的地址目标。
package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestP472DiscoveryNonAddressSkipsTriage 非地址主机跳过分诊(语法守门
// 第三挂点):无点内网名/裸数字资产标识零分诊调用、现行入图。
func TestP472DiscoveryNonAddressSkipsTriage(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p472-disc-gate"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, task, "f-portal", "internal-portal")
	p444FactWithAsset(t, store, task, "f-num", "400")
	called := 0
	triage := func(_ context.Context, in swarm.TriageInput) (swarm.TriageReply, error) {
		called++
		return swarm.TriageReply{}, context.Canceled
	}
	if _, err := Harvest(ctx, store, task, triage); err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	if called != 0 {
		t.Fatalf("非地址主机应零分诊调用,得 %d 次", called)
	}
	// 现行入图:两个非地址资产照常入图(现行机制不因守门收窄)。
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets) != 3 { // 种子 + 两个非地址新资产
		t.Fatalf("非地址资产应现行入图(种子+2),得 %d 个", len(assets))
	}
}

// TestP472DiscoveryUnifiedTriageStub 合一契约发现路适配:信息源判定不
// 入图、候选入图(打桩回清单形态,锚点 host 具名)。
func TestP472DiscoveryUnifiedTriageStub(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p472-disc-unified"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, task, "f-info", "gitee.com")
	p444FactWithAsset(t, store, task, "f-cand", "mail.example.com")
	triage := func(_ context.Context, in swarm.TriageInput) (swarm.TriageReply, error) {
		v := swarm.TriageCandidate
		if strings.HasPrefix(in.Host, "gitee.com") {
			v = swarm.TriageInfoAccess
		}
		return swarm.TriageReply{Targets: []swarm.TriageTarget{
			{Host: in.Host, Verdict: v, Basis: "打桩依据"},
		}}, nil
	}
	if _, err := Harvest(ctx, store, task, triage); err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 1 || roe.InfoSources[0].Host != "gitee.com" {
		t.Fatalf("gitee.com 应入信息源清单,得 %+v", roe.InfoSources)
	}
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets) != 2 { // 种子 + 候选
		t.Fatalf("信息源零入图、候选入图(种子+1),得 %d 个", len(assets))
	}
}
