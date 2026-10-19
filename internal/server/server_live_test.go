package server

// P3-08 实时推送测试:最小 WS 测试客户端(真实握手 + 帧编解码)+
// 事件→主题→帧的全链路(账本 Append → OnCommit → 广播)。

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- 测试用最小 WS 客户端 ----

type wsClient struct {
	conn net.Conn
	br   *bufio.Reader
	t    *testing.T
}

// dialWS 对 httptest.Server 发起真实 WebSocket 握手,校验 Sec-WebSocket-Accept。
func dialWS(t *testing.T, base string) *wsClient {
	t.Helper()
	var keyRaw [16]byte
	if _, err := rand.Read(keyRaw[:]); err != nil {
		t.Fatalf("生成握手随机数: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(keyRaw[:])
	host := strings.TrimPrefix(base, "http://")
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Fatalf("拨号 %s: %v", host, err)
	}
	req := "GET /api/ws HTTP/1.1\r\nHost: " + host + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发握手请求: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读握手响应: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("握手状态行 = %q, want 101", statusLine)
	}
	var accept string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读握手头: %v", err)
		}
		if line == "\r\n" {
			break
		}
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ":"); ok &&
			strings.EqualFold(strings.TrimSpace(k), "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(v)
		}
	}
	want := base64.StdEncoding.EncodeToString(sha1Sum(key + wsMagic))
	if accept != want {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", accept, want)
	}
	return &wsClient{conn: conn, br: br, t: t}
}

// sha1Sum 测试侧握手校验(与实现同式:SHA-1(key + magic) 的 base64)。
func sha1Sum(s string) []byte {
	sum := sha1.Sum([]byte(s))
	return sum[:]
}

// nextMsg 读下一个文本帧载荷(跳过服务端 ping),超时即失败。
func (c *wsClient) nextMsg(timeout time.Duration) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			c.t.Fatalf("等待推送消息超时(%v)", timeout)
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(left))
		op, payload, err := wsReadFrame(c.br)
		if err != nil {
			c.t.Fatalf("读帧: %v", err)
		}
		if op != wsOpText {
			continue // ping/pong 等控制帧跳过
		}
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			c.t.Fatalf("推送消息不是 JSON 对象: %s", payload)
		}
		return m
	}
}

// expectMsg 读下一消息并断言字段子集相等。
func (c *wsClient) expectMsg(timeout time.Duration, want map[string]any) {
	c.t.Helper()
	got := c.nextMsg(timeout)
	for k, v := range want {
		if got[k] != v {
			c.t.Fatalf("推送消息字段 %s = %v, want %v(整条: %v)", k, got[k], v, got)
		}
	}
}

// drainUntil 持续读消息直至谓词命中(一次落账可触发多主题多帧,
// 主题顺序不保证),返回命中消息。
func (c *wsClient) drainUntil(timeout time.Duration, pred func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			c.t.Fatalf("等待目标推送超时(%v)", timeout)
		}
		m := c.nextMsg(left)
		if pred(m) {
			return m
		}
	}
}

// sendClientFrame 发一个掩码客户端帧(RFC 6455:客户端→服务器必须掩码)。
func (c *wsClient) sendClientFrame(op byte, payload []byte) {
	c.t.Helper()
	hdr := []byte{op | 0x80}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, byte(0x80|n))
	case n <= 0xFFFF:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(append(hdr, 0x80|127), ext[:]...)
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(append(hdr, masked...)); err != nil {
		c.t.Fatalf("发客户端帧: %v", err)
	}
}

func (c *wsClient) close() { _ = c.conn.Close() }

// ---- 测试装配 ----

func newLiveTestServer(t *testing.T) (*Server, *httptest.Server, *swarm.LedgerGate) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr, err := manager.New(manager.Config{Store: store, RootDir: dir})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	gate := swarm.NewLedgerGate(store)
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, gate
}

// commitEvents 真实路径入账(经 Store.Append → OnCommit → 广播)。
func commitEvents(t *testing.T, srv *Server, taskID string, events ...rings.Event) {
	t.Helper()
	if _, err := srv.Store.Append(t.Context(), taskID, events...); err != nil {
		t.Fatalf("入账: %v", err)
	}
}

// createLiveTask 建任务(投影要求任务先在;建任务本身即触发 tasks
// 推送,调用方在拨号前建,避免混入断言流)。
func createLiveTask(t *testing.T, srv *Server, taskID string) {
	t.Helper()
	if _, err := srv.Manager.CreateTask(t.Context(), taskID, "推送测试任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务 %s: %v", taskID, err)
	}
}

// ---- 握手与协议 ----

func TestLiveReadyOnConnect(t *testing.T) {
	_, ts, _ := newLiveTestServer(t)
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})
}

func TestLiveHandshakeRejectsPlainGET(t *testing.T) {
	_, ts, _ := newLiveTestServer(t)
	r, err := http.Get(ts.URL + "/api/ws")
	if err != nil {
		t.Fatalf("GET /api/ws: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("普通 GET 状态 = %d, want 400", r.StatusCode)
	}
}

// ---- 事件 → 主题 → 帧 ----

func TestLiveTaskDirty(t *testing.T) {
	srv, ts, _ := newLiveTestServer(t)
	createLiveTask(t, srv, "t-live")
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})

	commitEvents(t, srv, "t-live", rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "started"},
	})
	c.expectMsg(2*time.Second, map[string]any{"t": "dirty", "k": "tasks"})
}

func TestLiveApprovalPayload(t *testing.T) {
	srv, ts, gate := newLiveTestServer(t)
	createLiveTask(t, srv, "t-ap")
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})

	ticket, err := gate.Enter(swarm.ApprovalRequest{
		TaskID: "t-ap", StepID: "s-1", UnitID: "u-1", Intent: "读取配置",
		ActionLevel: rings.ActionLevel("只读探测"), ToolName: "bash", ToolArgs: `{"cmd":"cat app.conf"}`,
	})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	m := c.nextMsg(2 * time.Second)
	if m["t"] != "approval" || m["task"] != "t-ap" {
		t.Fatalf("推送 = %v, want approval/t-ap", m)
	}
	pending, ok := m["pending"].([]any)
	if !ok || len(pending) != 1 {
		t.Fatalf("pending = %v, want 1 条", m["pending"])
	}
	first := pending[0].(map[string]any)
	for k, want := range map[string]string{
		"step_id": "s-1", "unit_id": "u-1", "intent": "读取配置",
		"tool_name": "bash", "action_level": "只读探测",
	} {
		if first[k] != want {
			t.Fatalf("待决载荷字段 %s = %v, want %q", k, first[k], want)
		}
	}

	// 裁决后:同主题再推一轮,载荷转空(面板即时撤单)。
	if err := gate.Decide(t.Context(), "t-ap", ticket.Req.ID, swarm.GateDecision{Approved: true}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	m = c.nextMsg(2 * time.Second)
	if m["t"] != "approval" || m["task"] != "t-ap" {
		t.Fatalf("推送 = %v, want approval/t-ap", m)
	}
	if pend, ok := m["pending"].([]any); !ok || len(pend) != 0 {
		t.Fatalf("裁决后 pending = %v, want 空", m["pending"])
	}
}

func TestLiveClaimsTopic(t *testing.T) {
	srv, ts, _ := newLiveTestServer(t)
	createLiveTask(t, srv, "t-cl")
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})

	// 非发现节点:只标 tasks。
	commitEvents(t, srv, "t-cl", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "n-goal", NodeType: rings.NodeGoal, Title: "摸清边界"},
	})
	c.drainUntil(2*time.Second, func(m map[string]any) bool { return m["k"] == "tasks" })

	// 发现节点:claims + tasks 两主题都到(顺序不保证,收齐为准)。
	commitEvents(t, srv, "t-cl", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "u-1",
		Payload: rings.NodeAdded{NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "存在未鉴权接口", Status: "candidate"},
	})
	got := map[string]bool{}
	for len(got) < 2 {
		c.drainUntil(2*time.Second, func(m map[string]any) bool {
			k, _ := m["k"].(string)
			if k == "" || got[k] {
				return false // 非目标或同主题重复不计
			}
			got[k] = true
			return true
		})
	}
	if !got["claims:t-cl"] || !got["tasks"] {
		t.Fatalf("发现新增推送主题 = %v, want claims:t-cl + tasks", got)
	}

	// 发现状态迁移(类型缓存命中):claims 再推(tasks 同批照发,收齐为准)。
	commitEvents(t, srv, "t-cl", rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: "tc-1", Status: "confirmed"},
	})
	c.drainUntil(2*time.Second, func(m map[string]any) bool { return m["k"] == "claims:t-cl" })

	// testclaim.record(裁决/复测/重放/绑定)同走 claims。
	commitEvents(t, srv, "t-cl", rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "harvester",
		Payload: rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high"},
	})
	c.drainUntil(2*time.Second, func(m map[string]any) bool { return m["k"] == "claims:t-cl" })
}

// ---- 重连与降级 ----

func TestLiveReconnectResync(t *testing.T) {
	srv, ts, _ := newLiveTestServer(t)
	createLiveTask(t, srv, "t-rc")
	c1 := dialWS(t, ts.URL)
	c1.expectMsg(2*time.Second, map[string]any{"t": "ready"})
	c1.close()

	// 断线期间落账(老连接收不到,由重连后 ready 重同步兜住)。
	commitEvents(t, srv, "t-rc", rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "stopped"},
	})

	c2 := dialWS(t, ts.URL)
	defer c2.close()
	c2.expectMsg(2*time.Second, map[string]any{"t": "ready"})

	// 重连后新事件照常到达。
	pin := true
	commitEvents(t, srv, "t-rc", rings.Event{
		Kind: rings.EventTaskMeta, Actor: "creator",
		Payload: rings.TaskMeta{Pinned: &pin, Reason: "重连后变更"},
	})
	c2.expectMsg(2*time.Second, map[string]any{"t": "dirty", "k": "tasks"})
}

func TestLivePingPong(t *testing.T) {
	_, ts, _ := newLiveTestServer(t)
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})

	// 客户端 ping → 服务端回 pong(同载荷)。
	c.sendClientFrame(wsOpPing, []byte("keepalive"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("等待 pong 超时")
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(left))
		op, payload, err := wsReadFrame(c.br)
		if err != nil {
			t.Fatalf("读帧: %v", err)
		}
		if op == wsOpPong {
			if string(payload) != "keepalive" {
				t.Fatalf("pong 载荷 = %q, want keepalive", payload)
			}
			return
		}
	}
}

// ---- 慢连接:发送缓冲打满即断(不补发,靠重连重同步) ----

func TestLiveSlowClientKicked(t *testing.T) {
	hub := newLiveHub(swarm.NewLedgerGate(nil))
	c := &liveConn{
		conn: nil, rw: nil, hub: hub,
		send: make(chan []byte, 1), done: make(chan struct{}),
	}
	hub.add(c)
	// 灌满缓冲(1 条)再触发广播:慢连接被断开(done 关闭)。
	c.send <- []byte(`{"t":"dirty","k":"tasks"}`)
	hub.mu.Lock()
	hub.dirty["tasks"] = true
	hub.mu.Unlock()
	hub.poke <- struct{}{}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("慢连接未被断开")
	}
}

// ---- 帧编解码单元 ----

func TestWSWriteFrameLengths(t *testing.T) {
	sizes := []int{0, 5, 125, 126, 65535, 65536, 200000}
	for _, n := range sizes {
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = byte(i % 251)
		}
		pr, pw := net.Pipe()
		go func() {
			bw := bufio.NewWriter(pw)
			if err := wsWriteFrame(bw, wsOpText, payload); err != nil {
				t.Errorf("写 %d 字节帧: %v", n, err)
			}
			_ = pw.Close()
		}()
		br := bufio.NewReader(pr)
		op, got, err := wsReadFrame(br)
		if err != nil {
			t.Fatalf("读 %d 字节帧: %v", n, err)
		}
		if op != wsOpText || len(got) != n {
			t.Fatalf("帧 op=%d len=%d, want text/%d", op, len(got), n)
		}
		for i := range got {
			if got[i] != payload[i] {
				t.Fatalf("%d 字节帧第 %d 字节不符", n, i)
			}
		}
		_ = pr.Close()
	}
}

func TestWSReadFrameUnmasks(t *testing.T) {
	// 客户端掩码帧经 wsReadFrame 解掩码还原。
	payload := []byte(`{"t":"ready"}`)
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	frame := []byte{wsOpText | 0x80, 0x80 | byte(len(payload))}
	frame = append(frame, mask[:]...)
	frame = append(frame, masked...)
	pr, pw := net.Pipe()
	go func() { _, _ = pw.Write(frame); _ = pw.Close() }()
	op, got, err := wsReadFrame(bufio.NewReader(pr))
	if err != nil {
		t.Fatalf("读掩码帧: %v", err)
	}
	if op != wsOpText || string(got) != string(payload) {
		t.Fatalf("解掩码结果 = %s", got)
	}
	_ = pr.Close()
}
