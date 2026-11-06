package server

// P3-07 重放端点与存档断点端点测试:重放发起守卫(404/400/503)、
// 异步驱动入账与列表/详情视图字段;存档断点端点的状态流转与守卫。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestServerClaimReplay 重放校验端点(P3-07):无证据 400、无镜 503、
// 发起 200、结论入账后列表/详情字段齐备。
func TestServerClaimReplay(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"重放端点验证"}`, http.StatusOK, &created)
	task := created["id"]

	// 铺垫:已确认声明(先无绑定证据)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active",
			AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-rp1", NodeType: rings.NodeTestClaim, Title: "接口未授权可读",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "impact": "可读全部用户",
				"vulnclass": "信息暴露", "evidence": "GET 200"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-rp1", ToNode: "web1"}},
	)

	// 404:任务/声明不存在。
	post(t, h, http.MethodPost, "/api/tasks/no-such/claims/tc-rp1/replay", `{}`, http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-nope/replay", `{}`, http.StatusNotFound, nil)
	// 503:执行器未装配。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-rp1/replay", `{}`, http.StatusServiceUnavailable, nil)

	// 装配执行器(重放不触模型,持连 provider 即可)。
	hang := newHoldConns(t)
	prov, err := llm.NewProvider(llm.Config{
		Format: llm.FormatOpenAI, BaseURL: "http://" + hang.addr(), APIKey: "k", Model: "m",
		MaxRetries: -1,
	})
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	exec := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	exec.Mirror = mirror.New(store, exec.Blobs, t.TempDir())
	srv.AttachRuntime(exec)

	// 400:无绑定证据。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-rp1/replay", `{}`, http.StatusBadRequest, nil)

	// 铺绑定证据:镜侧直发一条(经真实镜留痕)再绑定。
	var body atomic.Value
	body.Store("vulnerable-body-v1")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer target.Close()
	sess, err := exec.Mirror.Session(mirror.Meta{TaskID: task, StepID: "s1", UnitID: "u1"})
	if err != nil {
		t.Fatalf("起镜会话: %v", err)
	}
	c := sess.HTTPClient(5 * time.Second)
	resp, err := c.Get(target.URL + "/api/user")
	if err != nil {
		t.Fatalf("造证据: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	_ = sess.Close()
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "creator",
			Payload: rings.TestClaimRecord{ClaimID: "tc-rp1", Type: rings.TestClaimRecEvidence,
				Exchanges: []string{exIDOf(t, store, task, "s1")}, By: "creator"}},
	)

	// 发起:200 + 轮次/锚;结论入账(异步驱动,轮询)。
	var started map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-rp1/replay", `{}`, http.StatusOK, &started)
	if started["round"].(float64) != 1 || started["anchor"] != "replay-tc-rp1-r1" {
		t.Fatalf("发起响应不符: %v", started)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var detail struct {
			Claim   map[string]any   `json:"claim"`
			Replays []map[string]any `json:"replays"`
		}
		post(t, h, http.MethodGet, "/api/tasks/"+task+"/claims/tc-rp1", "", http.StatusOK, &detail)
		if len(detail.Replays) == 1 {
			if detail.Claim["replay_verdict"] != "reproduced" ||
				detail.Claim["replay_round"].(float64) != 1 || detail.Claim["replayable"] != true {
				t.Fatalf("列表/详情重放字段不符: %v", detail.Claim)
			}
			items, _ := json.Marshal(detail.Replays[0]["items"])
			if len(items) < 10 {
				t.Fatalf("详情重放历史缺逐条事实: %v", detail.Replays[0])
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("重放结论未在限期内入账")
}

// exIDOf 取步骤下最新带 blob 的镜交换 ID。
func exIDOf(t *testing.T, store *rings.Store, task, stepID string) string {
	t.Helper()
	evs, err := store.Events(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}
	id := ""
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if json.Unmarshal(ev.Payload, &m) == nil && m.StepID == stepID && m.BlobRef != "" {
			id = m.ExchangeID
		}
	}
	if id == "" {
		t.Fatalf("未找到 %s 的镜交换", stepID)
	}
	return id
}

// TestServerTaskCheckpoint 存档断点端点(P3-07):守卫(404/400)与
// 状态流转(checkpointed → frozen)。
func TestServerTaskCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	ckptGate := ckpt.New()
	started := make(chan string, 4)
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		Checkpoint: ckptGate,
		OnTaskRun: func(ctx context.Context, taskID string, wake <-chan struct{}) {
			select {
			case started <- taskID:
			default:
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-wake:
				}
				if ckptGate.Armed(taskID) {
					return
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"断点端点验证"}`, http.StatusOK, &created)
	task := created["id"]
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("任务未拉起")
	}

	// 404:任务不存在。
	post(t, h, http.MethodPost, "/api/tasks/no-such/checkpoint", `{}`, http.StatusNotFound, nil)
	// 200:存档(无运行件干扰,冻结即时)。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/checkpoint", `{}`, http.StatusOK, &out)
	if out["status"] != "checkpointed" || out["frozen"] != true {
		t.Fatalf("存档响应不符: %v", out)
	}
	// 400:重复存档(已存档态)。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/checkpoint", `{}`, http.StatusBadRequest, nil)
}
