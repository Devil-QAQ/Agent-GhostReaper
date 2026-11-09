package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-53(问题清单 #73⑤⑥):建任务范围说明 + 提取分流(面板侧) ----
// ① 资产栏范围说明行与类型说明行为前端呈现(活体验收),服务端侧
//    无逻辑;
// ② 初始摘除落库:excluded_seeds(标识形态)→ 排除资产入图(来源
//    发现、范围外)与 RoE 排除清单同批落账——与运行中摘除同一机制;
// ③ 校验:无锚点条目人话拒建、与种子锚点冲突(同一目标既授权又
//    禁止)人话拒建、锚点级去重;
// ④ 提取分流解析:excluded_assets 与资产冲突以资产为准、锚点去重、
//    词表校验(语义辨认本身是【模型】活体验收项,解析为确定性)。

// TestP453CreateInitialExcludes 初始摘除落库:排除资产入图(来源发现、
// 范围外、类按写法推断)+ RoE 排除清单(次序与运行中摘除同构:先入图
// 后落排除)+ 渗透任务不进待批清单(excluded 优先)+ 通配排除按写法
// 粒度覆盖子域目标。
func TestP453CreateInitialExcludes(t *testing.T) {
	h, store := p449Server(t)
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"pentest","goals":["拿下管理权限"],"assets":[{"class":"subdomain","title":"aaa.x.com"}],"excluded_seeds":["*.x.com","bbb.x.com"]}`, http.StatusOK, &created)
	task := created["id"]

	assets, err := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if err != nil {
		t.Fatalf("查资产失败: %v", err)
	}
	byTitle := map[string]rings.NodeFull{}
	for _, a := range assets {
		byTitle[a.Title] = a
	}
	seed := byTitle["aaa.x.com"]
	if seed.ID == "" || rings.AssetSourceOf(seed.Detail) != rings.AssetSourceSeed {
		t.Fatalf("种子资产应照旧入图(来源种子): %+v", seed)
	}
	for title, wantClass := range map[string]rings.AssetClass{
		"*.x.com":   rings.AssetSubdomain,
		"bbb.x.com": rings.AssetDomain,
	} {
		a := byTitle[title]
		if a.ID == "" {
			t.Fatalf("排除资产 %s 应入图", title)
		}
		if rings.AssetSourceOf(a.Detail) != rings.AssetSourceDiscovered {
			t.Fatalf("排除资产 %s 来源应为发现: %v", title, rings.AssetSourceOf(a.Detail))
		}
		if rings.AssetScopeOf(a.Detail) != rings.AssetScopeOut {
			t.Fatalf("排除资产 %s 应为范围外: %v", title, rings.AssetScopeOf(a.Detail))
		}
		if a.AssetClass != wantClass {
			t.Fatalf("排除资产 %s 类应为 %s, got %s", title, wantClass, a.AssetClass)
		}
	}

	// RoE 排除清单含两条排除资产 ID(与运行中摘除同一投影)。
	st, err := store.CurrentRoE(t.Context(), task)
	if err != nil {
		t.Fatalf("查 RoE 失败: %v", err)
	}
	if len(st.ExcludedAssets) != 2 {
		t.Fatalf("排除清单应含 2 条, got %v", st.ExcludedAssets)
	}
	exSet := st.ExcludedAssetSet()
	for title := range map[string]bool{"*.x.com": true, "bbb.x.com": true} {
		if !exSet[byTitle[title].ID] {
			t.Fatalf("排除清单应含 %s 的资产 ID", title)
		}
	}

	// 事件次序:排除资产 node.added 先于排除清单 roe.updated(与运行中
	// 摘除同构);排除清单事件的 Note 带初始摘除说明。
	evs, err := store.Events(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件失败: %v", err)
	}
	nodeRev, roeRev := int64(-1), int64(-1)
	for _, ev := range evs {
		if ev.Kind != rings.EventRoeUpdated {
			continue
		}
		var p rings.RoeUpdated
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 roe.updated 失败: %v", err)
		}
		if len(p.ExcludedAssets) > 0 {
			if roeRev >= 0 {
				t.Fatalf("排除清单 roe.updated 应只落一笔")
			}
			roeRev = ev.Revision
			if !strings.Contains(p.Note, "初始摘除") {
				t.Fatalf("排除清单事件应带初始摘除说明: %q", p.Note)
			}
		}
	}
	if roeRev < 0 {
		t.Fatalf("未找到排除清单 roe.updated")
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeAdded || ev.Revision >= roeRev {
			continue
		}
		var p rings.NodeAdded
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Title == "*.x.com" {
			nodeRev = ev.Revision
		}
	}
	if nodeRev < 0 || nodeRev >= roeRev {
		t.Fatalf("排除资产入图应先于排除清单落账: node rev %d, roe rev %d", nodeRev, roeRev)
	}

	// 渗透任务待批清单不含排除资产(excluded 优先于待批呈现)。
	pending, err := store.PendingScopeAssets(t.Context(), task)
	if err != nil {
		t.Fatalf("查待批失败: %v", err)
	}
	for _, p := range pending {
		if p.Title == "*.x.com" || p.Title == "bbb.x.com" {
			t.Fatalf("排除资产不应出现在待批清单: %+v", p)
		}
	}

	// 通配排除按写法粒度覆盖子域目标(P4-51 ⑦ 防骚扰判定面直测);
	// 同时验证排除资产不贡献种子闭包(discovered 未放行)——种子目标
	// 由闭包放行在先(denyScope 判定链闭包命中即过,不受摘除影响),
	// 闭包外子域目标才落到排除判定。
	closure := rings.BuildScopeClosure(rings.ScopeSeedsOf(assets), nil, nil)
	if !closure.CoversTarget("aaa.x.com", "", nil) {
		t.Fatalf("种子 aaa.x.com 应在闭包内(围栏放行,摘除不误伤)")
	}
	if closure.CoversTarget("ccc.x.com", "", nil) {
		t.Fatalf("排除资产 *.x.com 不应贡献闭包(ccc.x.com 须在闭包外才落到排除判定)")
	}
	if !rings.ExcludedCoversTarget(assets, exSet, "ccc.x.com", "") {
		t.Fatalf("通配排除 *.x.com 应覆盖 ccc.x.com")
	}
	if !rings.ExcludedCoversTarget(assets, exSet, "bbb.x.com", "") {
		t.Fatalf("排除 bbb.x.com 应覆盖同 host 目标")
	}
	if rings.ExcludedCoversTarget(assets, exSet, "other.y.com", "") {
		t.Fatalf("排除不应覆盖域外目标 other.y.com")
	}
}

// TestP453CreateExcludesRejects 拒建路径:与种子锚点冲突(字面相同与
// URL 等同两形态)、无主机锚点条目,均人话拒建;锚点级去重(大小写
// 归一)只入一条。
func TestP453CreateExcludesRejects(t *testing.T) {
	h, store := p449Server(t)
	cases := []struct {
		name, body, wantErr string
	}{
		{"字面相同", `{"task_type":"pentest","goals":["g"],"assets":[{"class":"subdomain","title":"aaa.x.com"}],"excluded_seeds":["aaa.x.com"]}`, "既在资产栏又在不要测的资产"},
		{"URL 等同裸域名", `{"task_type":"pentest","goals":["g"],"assets":[{"class":"service","title":"https://aaa.x.com/login"}],"excluded_seeds":["aaa.x.com"]}`, "既在资产栏又在不要测的资产"},
		{"无主机锚点", `{"task_type":"pentest","goals":["g"],"assets":[{"class":"subdomain","title":"aaa.x.com"}],"excluded_seeds":["其他子域"]}`, "不是域名 / IP 写法"},
	}
	for _, c := range cases {
		var errBody map[string]any
		post(t, h, http.MethodPost, "/api/tasks", c.body, http.StatusBadRequest, &errBody)
		if msg, _ := errBody["error"].(string); !strings.Contains(msg, c.wantErr) {
			t.Fatalf("%s: 拒建理由应含 %q, got %q", c.name, c.wantErr, msg)
		}
	}

	// 锚点级去重:同一通配写法大小写不同只入一条。
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"pentest","goals":["g"],"assets":[{"class":"subdomain","title":"aaa.x.com"}],"excluded_seeds":["*.x.com","*.X.COM"]}`, http.StatusOK, &created)
	st, err := store.CurrentRoE(t.Context(), created["id"])
	if err != nil {
		t.Fatalf("查 RoE 失败: %v", err)
	}
	if len(st.ExcludedAssets) != 1 {
		t.Fatalf("锚点级去重应只入一条, got %v", st.ExcludedAssets)
	}
}

// TestP453CreateWithoutExcludes 回归:不带 excluded_seeds 的建任务零
// 额外 roe.updated(排除机制不启用时事件流无痕)。
func TestP453CreateWithoutExcludes(t *testing.T) {
	h, store := p449Server(t)
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["g"],"assets":[{"class":"subdomain","title":"aaa.x.com"}]}`, http.StatusOK, &created)
	evs, err := store.Events(t.Context(), created["id"], 0, 0)
	if err != nil {
		t.Fatalf("查事件失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind == rings.EventRoeUpdated {
			t.Fatalf("无摘除无约束的建任务不应落 roe.updated: revision %d", ev.Revision)
		}
	}
	st, err := store.CurrentRoE(t.Context(), created["id"])
	if err != nil {
		t.Fatalf("查 RoE 失败: %v", err)
	}
	if len(st.ExcludedAssets) != 0 {
		t.Fatalf("排除清单应为空: %v", st.ExcludedAssets)
	}
}

// TestP453ExtractParsesExcludes 提取分流解析:excluded_assets 候选
// 修剪保留、与 assets 同目标以资产为准(丢弃入 issues)、锚点级去重、
// 类词表校验。
func TestP453ExtractParsesExcludes(t *testing.T) {
	reply, issues := parseTaskExtract(`{"assets":[{"class":"subdomain","title":"aaa.x.com"}],"excluded_assets":[{"class":"subdomain","title":"*.x.com"},{"class":"host","title":"10.0.0.8"}]}`)
	if len(issues) != 0 {
		t.Fatalf("无 issues 预期: %v", issues)
	}
	if len(reply.ExcludedAssets) != 2 || reply.ExcludedAssets[0].Title != "*.x.com" || reply.ExcludedAssets[1].Title != "10.0.0.8" {
		t.Fatalf("摘除候选解析不符: %+v", reply.ExcludedAssets)
	}

	// 与 assets 同一目标(URL 等同裸域名,锚点级对比):以资产为准。
	conflict, issues := parseTaskExtract(`{"assets":[{"class":"service","title":"https://bbb.x.com/"},{"class":"subdomain","title":"aaa.x.com"}],"excluded_assets":[{"class":"subdomain","title":"bbb.x.com"}]}`)
	if len(conflict.ExcludedAssets) != 0 {
		t.Fatalf("同目标摘除候选应丢弃: %+v", conflict.ExcludedAssets)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "以资产栏为准") {
		t.Fatalf("同目标丢弃应入 issues: %v", issues)
	}
	if len(conflict.Assets) != 2 {
		t.Fatalf("资产候选不应受影响: %+v", conflict.Assets)
	}

	// 锚点级去重(大小写归一)与类词表校验。
	dup, issues := parseTaskExtract(`{"excluded_assets":[{"class":"subdomain","title":"*.x.com"},{"class":"subdomain","title":"*.X.COM"},{"class":"bad","title":"ccc.x.com"}]}`)
	if len(dup.ExcludedAssets) != 1 || dup.ExcludedAssets[0].Title != "*.x.com" {
		t.Fatalf("锚点级去重应只留一条: %+v", dup.ExcludedAssets)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "已丢弃") {
		t.Fatalf("类词表外应丢弃入 issues: %v", issues)
	}
}
