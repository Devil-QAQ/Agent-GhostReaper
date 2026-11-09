package rings

import (
	"context"
	"strings"
	"testing"
)

// ---- P4-44 资产范围归属与任务类型(问题清单 #61 ①②) ----
// 词表/闭包/归属判定/呈现态为纯函数;投影与读侧(store)覆盖:
// task_type 随 created 定格进 task_extras、scope_approved 随 RoE 快照
// 整体覆盖、PendingScopeAssets 的待批口径(渗透才计,放行/摘除剔除)。

func TestP444ScopeAnchorHost(t *testing.T) {
	cases := []struct {
		title string
		host  string
		isIP  bool
		ok    bool
	}{
		{"https://WWW.Example.com:8443/app", "www.example.com", false, true},
		{"http://10.0.0.5:8080", "10.0.0.5", true, true},
		{"example.com", "example.com", false, true},
		{"[::1]:9000", "::1", true, true},
		{"192.168.1.10", "192.168.1.10", true, true},
		{"某管理员的账号", "", false, false},
		{"张三(zhangsan@example.com)", "", false, false},
		// URL 路径含空格:url.Parse 宽容接受,host 部分仍是合法锚点。
		{"http://ex.com/a b", "ex.com", false, true},
	}
	for _, c := range cases {
		a, ok := ScopeAnchorOf(c.title)
		if ok != c.ok || (ok && (a.Host != c.host || a.IsIP != c.isIP)) {
			t.Errorf("ScopeAnchorOf(%q) = %+v,%v; want host %q,isIP %v,%v",
				c.title, a, ok, c.host, c.isIP, c.ok)
		}
	}
	// P4-50 ③写法粒度锚点:端口限定与 *.域 通配;通配不与端口组合。
	if a, ok := ScopeAnchorOf("api.example.com:8443"); !ok || a.Host != "api.example.com" || a.Port != "8443" {
		t.Errorf("host:port 锚点应带端口: %+v,%v", a, ok)
	}
	if a, ok := ScopeAnchorOf("*.Example.COM"); !ok || a.Host != "example.com" || !a.Wildcard {
		t.Errorf("*.域 锚点应为通配基域: %+v,%v", a, ok)
	}
	if a, ok := ScopeAnchorOf("10.0.0.5:8080"); !ok || a.Port != "8080" {
		t.Errorf("IP:端口 锚点应带端口: %+v,%v", a, ok)
	}
	if _, ok := ScopeAnchorOf("*.example.com:8080"); ok {
		t.Error("通配+端口不支持,应无锚点")
	}
	if _, ok := ScopeAnchorOf("a*b.example.com"); ok {
		t.Error("段中 * 非通配写法,应无锚点")
	}
}

func TestP444ScopeClosureCovers(t *testing.T) {
	// 给域名:该域名自身可测(任意端口);兄弟/父子子域名与同 IP 其他
	// 端口(以 IP 形态出现)不在内。
	domainSeed := BuildScopeClosure([]ScopeSeed{
		{ID: "a-1", Title: "www.example.com", Source: AssetSourceSeed},
		{ID: "a-2", Title: "https://app.example.com:443", Source: AssetSourceManual},
	}, nil, nil)
	if !domainSeed.CoversTarget("www.example.com", "", nil) {
		t.Fatal("种子域名自身应在闭包内")
	}
	if !domainSeed.CoversTarget("WWW.EXAMPLE.COM:8080", "", nil) {
		t.Fatal("同域名各端口应在闭包内(裸域名=全端口)")
	}
	if domainSeed.CoversTarget("api.example.com", "", nil) {
		t.Fatal("兄弟子域名不在闭包内(给域名=该域名自身)")
	}
	if domainSeed.CoversTarget("10.0.0.5", "", nil) {
		t.Fatal("其他 IP 不在闭包内")
	}
	// discovered 资产默认不入闭包;放行后进入(闭包=种子∪手动∪已放行)。
	mixed := BuildScopeClosure([]ScopeSeed{
		{ID: "a-1", Title: "www.example.com", Source: AssetSourceSeed},
		{ID: "a-9", Title: "ok.example.net", Source: AssetSourceDiscovered},
	}, map[string]bool{"a-9": true}, nil)
	if !mixed.CoversTarget("ok.example.net", "", nil) {
		t.Fatal("已放行的发现资产应入闭包")
	}
	notApproved := BuildScopeClosure([]ScopeSeed{
		{ID: "a-9", Title: "ok.example.net", Source: AssetSourceDiscovered},
	}, nil, nil)
	if !notApproved.Empty() {
		t.Fatal("未放行的发现资产不入闭包(闭包为空)")
	}

	// 给 IP:该服务器全部可测——任意端口直配,域名经解析命中视为同机。
	ipSeed := BuildScopeClosure([]ScopeSeed{
		{ID: "a-1", Title: "10.0.0.5", Source: AssetSourceSeed},
		{ID: "a-2", Title: "http://10.0.0.5:8080", Source: AssetSourceSeed},
	}, nil, nil)
	if !ipSeed.CoversTarget("10.0.0.5", "", nil) {
		t.Fatal("种子 IP 任意端口应在闭包内")
	}
	if !ipSeed.CoversTarget("10.0.0.5:9999", "", nil) {
		t.Fatal("同 IP 其他端口应在闭包内")
	}
	if ipSeed.CoversTarget("10.0.0.6", "", nil) {
		t.Fatal("其他 IP 不在闭包内")
	}
	resolve := func(host string) []string {
		if host == "vhost.example.com" {
			return []string{"10.0.0.5"}
		}
		return nil
	}
	if !ipSeed.CoversTarget("vhost.example.com", "", resolve) {
		t.Fatal("解析命中种子 IP 的域名应视为同机在闭包内")
	}
	if ipSeed.CoversTarget("unknown.example.com", "", resolve) {
		t.Fatal("解析不到种子 IP 的域名不在闭包内")
	}
	if ipSeed.CoversTarget("unknown.example.com", "", nil) {
		t.Fatal("无解析器时域名按保守(不在闭包)")
	}
	// 混合给取并集:域名种子 + IP 种子同时成立。
	union := BuildScopeClosure([]ScopeSeed{
		{ID: "a-1", Title: "www.example.com", Source: AssetSourceSeed},
		{ID: "a-2", Title: "10.0.0.5", Source: AssetSourceSeed},
	}, nil, nil)
	if !union.CoversTarget("www.example.com", "", nil) || !union.CoversTarget("10.0.0.5:7070", "", nil) {
		t.Fatal("混合种子应取并集")
	}
}

func TestP444JudgeAssetScope(t *testing.T) {
	c := BuildScopeClosure([]ScopeSeed{{ID: "a-1", Title: "www.example.com", Source: AssetSourceSeed}}, nil, nil)
	if got := JudgeAssetScope(c, "www.example.com:8080", nil); got != AssetScopeIn {
		t.Errorf("种子域名同域任意端口应 in,得 %q", got)
	}
	if got := JudgeAssetScope(c, "api.example.com", nil); got != AssetScopeOut {
		t.Errorf("兄弟子域名应 out,得 %q", got)
	}
	// 无主机锚点的资产(人员/组织/泄露信息)按范围内:无从"发起测试"。
	if got := JudgeAssetScope(c, "张三(zhangsan@example.com)", nil); got != AssetScopeIn {
		t.Errorf("无锚点资产应 in,得 %q", got)
	}
}

func TestP444ScopeStateOf(t *testing.T) {
	cases := []struct {
		name                     string
		taskType, source, frozen string
		approved, excluded       bool
		want                     string
	}{
		{"渗透发现范围外", TaskTypePentest, AssetSourceDiscovered, AssetScopeOut, false, false, ScopeStatePending},
		{"渗透发现范围内", TaskTypePentest, AssetSourceDiscovered, AssetScopeIn, false, false, ""},
		{"渗透种子恒范围外标记也不锁", TaskTypePentest, AssetSourceSeed, AssetScopeOut, false, false, ""},
		{"放行转范围内", TaskTypePentest, AssetSourceDiscovered, AssetScopeOut, true, false, ""},
		{"摘除走既有徽标", TaskTypePentest, AssetSourceDiscovered, AssetScopeOut, false, true, ""},
		{"红队范围外可测", TaskTypeRedteam, AssetSourceDiscovered, AssetScopeOut, false, false, ScopeStateOutOpen},
		{"未标范围外可测", "", AssetSourceDiscovered, AssetScopeOut, false, false, ScopeStateOutOpen},
		{"红队范围内", TaskTypeRedteam, AssetSourceDiscovered, AssetScopeIn, false, false, ""},
	}
	for _, c := range cases {
		if got := ScopeStateOf(c.taskType, c.source, c.frozen, c.approved, c.excluded); got != c.want {
			t.Errorf("%s: ScopeStateOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestP444TaskTypeVocabAndProjection(t *testing.T) {
	if !IsValidTaskType(TaskTypePentest) || !IsValidTaskType(TaskTypeRedteam) {
		t.Fatal("词表应含 pentest/redteam")
	}
	if IsValidTaskType("") || IsValidTaskType("purple") {
		t.Fatal("空串与词表外值不合法")
	}
	if TaskTypeOf(nil) != "" || TaskTypeOf(map[string]any{"task_type": "pentest"}) != TaskTypePentest {
		t.Fatal("TaskTypeOf 归一:缺省空串=存量未标")
	}
	s := openTestStore(t)
	mustAppend(t, s, "t-1",
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": TaskTypePentest}}},
	)
	extras, ok, err := s.TaskExtras(context.Background(), "t-1")
	if err != nil || !ok || extras.TaskType != TaskTypePentest {
		t.Fatalf("task_type 应随 created 定格进 task_extras: %+v %v %v", extras, ok, err)
	}
	// 词表外类型:投影入口拒绝(受控校验)。
	if _, err := s.Append(context.Background(), "t-2",
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": "purple"}}}); err == nil {
		t.Fatal("词表外任务类型应被投影入口拒绝")
	} else if !strings.Contains(err.Error(), "不在词表内") {
		t.Fatalf("拒绝信息应人话指明词表: %v", err)
	}
}

func TestP444ScopeApprovedProjection(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, "t-1", Event{Kind: EventTaskLifecycle, Actor: "test",
		Payload: TaskLifecycle{Action: "created"}})
	mustAppend(t, s, "t-1", Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ScopeApproved: []string{"a-1", "a-2"}}})
	roe, err := s.CurrentRoE(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(roe.ScopeApproved) != 2 || !roe.ScopeApprovedSet()["a-2"] {
		t.Fatalf("放行清单应随快照整体覆盖: %+v", roe.ScopeApproved)
	}
	// 空快照(显式空数组)= 清空;nil = 保持事件原样(语义由 server 写
	// 入侧保证,投影侧整体覆盖)。
	mustAppend(t, s, "t-1", Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ScopeApproved: []string{}}})
	roe, _ = s.CurrentRoE(context.Background(), "t-1")
	if len(roe.ScopeApproved) != 0 {
		t.Fatalf("显式空清单应清空: %+v", roe.ScopeApproved)
	}
	// 重复条目拒收。
	if _, err := s.Append(context.Background(), "t-1", Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ScopeApproved: []string{"a-1", "a-1"}}}); err == nil {
		t.Fatal("放行清单重复资产应拒收")
	}
}

// p444SeedTask 建任务(created 带 task_type)+ 种子资产。
func p444SeedTask(t *testing.T, s *Store, taskID, taskType string, seeds ...string) {
	t.Helper()
	detail := map[string]any{}
	if taskType != "" {
		detail["task_type"] = taskType
	}
	mustAppend(t, s, taskID, Event{Kind: EventTaskLifecycle, Actor: "test",
		Payload: TaskLifecycle{Action: "created", Detail: detail}})
	for i, seed := range seeds {
		mustAppend(t, s, taskID, na(NodeAsset, "a-seed"+string(rune('a'+i)), seed, "active",
			AssetHost, map[string]any{"source": AssetSourceSeed}))
	}
}

// p444Discover 模拟收割入图:discovered 资产带冻结 scope 标记。
func p444Discover(t *testing.T, s *Store, taskID, id, title, frozen string) {
	t.Helper()
	detail := map[string]any{"source": AssetSourceDiscovered}
	if frozen != "" {
		detail["scope"] = frozen
	}
	mustAppend(t, s, taskID, na(NodeAsset, id, title, "active", AssetHost, detail))
}

func TestP444PendingScopeAssets(t *testing.T) {
	s := openTestStore(t)
	p444SeedTask(t, s, "t-pen", TaskTypePentest, "www.example.com")
	p444Discover(t, s, "t-pen", "a-o1", "api.example.com", AssetScopeOut)
	p444Discover(t, s, "t-pen", "a-o2", "10.0.0.9:9999", AssetScopeOut)
	p444Discover(t, s, "t-pen", "a-in", "www.example.com:8080", AssetScopeIn)

	pending, err := s.PendingScopeAssets(context.Background(), "t-pen")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("渗透任务应计 2 项待批(范围内发现不计): %+v", pending)
	}
	// 放行一项 → 剩 1;忽略一项(摘除)→ 清零。
	mustAppend(t, s, "t-pen", Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ScopeApproved: []string{"a-o1"}}})
	pending, _ = s.PendingScopeAssets(context.Background(), "t-pen")
	if len(pending) != 1 || pending[0].ID != "a-o2" {
		t.Fatalf("放行后应剩 1 项待批: %+v", pending)
	}
	mustAppend(t, s, "t-pen", Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ScopeApproved: []string{"a-o1"}, ExcludedAssets: []string{"a-o2"}}})
	pending, _ = s.PendingScopeAssets(context.Background(), "t-pen")
	if len(pending) != 0 {
		t.Fatalf("忽略(摘除)后待批应清零: %+v", pending)
	}

	// 红队任务:同样的范围外发现恒不构成待批。
	p444SeedTask(t, s, "t-red", TaskTypeRedteam, "www.example.com")
	p444Discover(t, s, "t-red", "a-r1", "api.example.com", AssetScopeOut)
	pending, _ = s.PendingScopeAssets(context.Background(), "t-red")
	if len(pending) != 0 {
		t.Fatalf("红队任务无待批: %+v", pending)
	}
	// 存量未标任务:行为等同红队。
	p444SeedTask(t, s, "t-old", "", "www.example.com")
	p444Discover(t, s, "t-old", "a-x1", "api.example.com", AssetScopeOut)
	pending, _ = s.PendingScopeAssets(context.Background(), "t-old")
	if len(pending) != 0 {
		t.Fatalf("存量任务无待批(不锁): %+v", pending)
	}
}

func TestP444AssetGraphScopeOut(t *testing.T) {
	s := openTestStore(t)
	p444SeedTask(t, s, "t-1", TaskTypePentest, "www.example.com")
	p444Discover(t, s, "t-1", "a-o1", "api.example.com", AssetScopeOut)
	ag, err := s.AssetGraph(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range ag.Assets {
		if a.ID == "a-o1" {
			found = true
			if !a.ScopeOut || a.Source != AssetSourceDiscovered {
				t.Fatalf("范围外标记应随资产图读出: %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("资产图应含发现资产")
	}
}
