// P3-08 WebSocket 实时推送:状态变化主动推面板,替换轮询。
//
// 推送源是账本单点——一切面板关心的状态变化(任务状态 / 审批待决 /
// 发现 / 受阻)都经 rings.Store.Append 落库,store.OnCommit 在提交后
// 通知本枢纽,枢纽按事件 kind 映射推送主题广播给在线连接(R7 无旁路:
// 推送只携带"什么变了"的信号与审批待决载荷,数据装配仍归各读端点,
// 展示与操作同源)。
//
// 推送范围分级(P3-08):
//   - 高频轻事件:dirty 信号(tasks / claims:<任务>)——运行期步骤迁移
//     每次落账都发,零载荷,订阅方自行拉取对应端点;
//   - 低频重事件:approval:<任务> 携带该任务当前全部待决审批载荷
//     (与审批端点同一读法),审批到达即时可弹;ready 在连接建立/
//     重连成功时发送,订阅方据此全量重同步。
//
// 协议是 WebSocket 最小服务端子集(RFC 6455,零新依赖):握手 +
// 服务端文本帧 + ping/pong/close 控制帧处理;浏览器客户端不发数据帧。
// 断线重连与降级轮询归面板侧(web/lib/live.js + usePolling)。
package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- 推送主题 ----

const (
	liveTopicTasks    = "tasks"     // 任务列表态(状态/受阻/目标进度/运行单位派生)
	liveTopicApproval = "approval:" // + 任务 ID(低频重事件,携带待决载荷)
	liveTopicClaims   = "claims:"   // + 任务 ID(发现面)
)

const (
	wsMagic          = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA

	wsMaxClientFrame  = 1 << 20 // 客户端帧上限(客户端不发数据,超限即断)
	wsPingEvery       = 20 * time.Second
	wsReadIdleTimeout = 45 * time.Second // 须 > wsPingEvery(浏览器自动回 pong 保活)
	liveSendBuffer    = 128              // 每连接发送缓冲(打满视为慢连接,断开由重连重同步)
)

// liveHub 推送枢纽:在线连接表 + 主题标脏集合 + 单广播循环。
// Mark 由账本 OnCommit(单写者循环内)调用——严格非阻塞、绝不回调
// Store;广播循环异步组装载荷并扇出。
type liveHub struct {
	gate swarm.ApprovalGate // 审批待决载荷读源(与审批端点同一实例)

	mu    sync.Mutex
	conns map[*liveConn]struct{}
	dirty map[string]bool
	poke  chan struct{}

	// nodeTypes 节点类型缓存(node.added 喂入,node.state_changed 查表
	// 判是否发现迁移)。仅在 onCommit(账本单写者 goroutine)触达,
	// 无锁。
	nodeTypes map[string]map[string]rings.NodeType
}

func newLiveHub(gate swarm.ApprovalGate) *liveHub {
	h := &liveHub{
		gate:      gate,
		conns:     map[*liveConn]struct{}{},
		dirty:     map[string]bool{},
		poke:      make(chan struct{}, 1),
		nodeTypes: map[string]map[string]rings.NodeType{},
	}
	go h.run()
	return h
}

// onCommit 账本提交后回调(rings.OnCommit):事件批 → 推送主题。
// 在账本单写者循环内同步执行,只做映射与标脏,零阻塞零回调。
func (h *liveHub) onCommit(taskID string, events []rings.Event) {
	var topics []string
	claim := liveTopicClaims + taskID
	for _, ev := range events {
		switch ev.Kind {
		case rings.EventTaskLifecycle, rings.EventTaskMeta:
			topics = append(topics, liveTopicTasks)
		case rings.EventClosureRecord:
			// 收口信号(P4-18):提醒到达与两键裁决都改任务列表的待决
			// 投影,订阅方重拉即弹/即收。
			topics = append(topics, liveTopicTasks)
		case rings.EventRoeUpdated:
			// RoE 变更改写图载荷的资产范围口径(P4-05 摘除 / P4-09
			// 覆盖条分母)——tasks 主题订阅方(工作台图 / 任务列表)重拉。
			topics = append(topics, liveTopicTasks)
		case rings.EventApprovalRecord:
			// 审批请求/裁决改动待决计数——除本任务的审批重事件外,
			// 任务列表的 pending_approvals 投影(P4-23)随之标脏。
			topics = append(topics, liveTopicApproval+taskID, liveTopicTasks)
		case rings.EventTestClaimRecord:
			topics = append(topics, claim)
		case rings.EventNodeAdded:
			nt, ok := payloadAs[rings.NodeAdded](ev.Payload)
			if ok {
				types := h.nodeTypes[taskID]
				if types == nil {
					types = map[string]rings.NodeType{}
					h.nodeTypes[taskID] = types
				}
				types[nt.NodeID] = nt.NodeType
				if nt.NodeType == rings.NodeTestClaim {
					topics = append(topics, claim)
				}
			}
			topics = append(topics, liveTopicTasks)
		case rings.EventNodeStateChanged:
			if p, ok := payloadAs[rings.NodeStateChanged](ev.Payload); ok {
				if h.nodeTypes[taskID][p.NodeID] == rings.NodeTestClaim {
					topics = append(topics, claim)
				}
			}
			topics = append(topics, liveTopicTasks)
		}
	}
	if len(topics) == 0 {
		return
	}
	h.mu.Lock()
	for _, t := range topics {
		h.dirty[t] = true
	}
	h.mu.Unlock()
	select {
	case h.poke <- struct{}{}:
	default:
	}
}

// payloadAs 事件负载的宽容取值:装配路径传类型化结构体,测试/历史路径
// 可能传 JSON 字节——两种都认,认不出返回 false(按未知类型处理)。
func payloadAs[T any](p any) (T, bool) {
	switch v := p.(type) {
	case T:
		return v, true
	case []byte:
		var out T
		if json.Unmarshal(v, &out) == nil {
			return out, true
		}
	case json.RawMessage:
		var out T
		if json.Unmarshal(v, &out) == nil {
			return out, true
		}
	}
	var zero T
	return zero, false
}

// run 广播循环:标脏集合按主题合并(突发批只发一轮),轻事件零载荷、
// 审批主题现读审批门组装待决载荷。慢连接(发送缓冲打满)直接断开——
// 面板重连后经 ready 全量重同步,不追求对慢消费者的补发。
func (h *liveHub) run() {
	for range h.poke {
		h.mu.Lock()
		dirty := h.dirty
		h.dirty = map[string]bool{}
		h.mu.Unlock()
		for topic := range dirty {
			msg, err := h.frameOf(topic)
			if err != nil {
				continue
			}
			h.mu.Lock()
			for c := range h.conns {
				select {
				case c.send <- msg:
				default:
					c.shutdown() // 慢连接:断开,重连重同步
				}
			}
			h.mu.Unlock()
		}
	}
}

// frameOf 主题 → 推送消息(组装不在锁内:审批载荷要读审批门)。
func (h *liveHub) frameOf(topic string) ([]byte, error) {
	if taskID, ok := strings.CutPrefix(topic, liveTopicApproval); ok {
		pending := h.gate.Pending(taskID)
		out := make([]approvalView, 0, len(pending))
		for _, p := range pending {
			out = append(out, approvalView{
				ID: p.ID, StepID: p.StepID, UnitID: p.UnitID, Intent: p.Intent,
				ActionLevel: string(p.ActionLevel), ToolName: p.ToolName,
				ToolArgs: p.ToolArgs, Note: p.Note,
				CreatedAt: p.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			})
		}
		return json.Marshal(map[string]any{"t": "approval", "task": taskID, "pending": out})
	}
	return json.Marshal(map[string]string{"t": "dirty", "k": topic})
}

// ---- 连接 ----

// liveConn 一条推送连接:reader 处理客户端控制帧(ping→pong、close),
// writer 独占数据帧发送;两侧任一退出即收连接。
type liveConn struct {
	conn    net.Conn
	rw      *bufio.ReadWriter
	hub     *liveHub
	send    chan []byte
	done    chan struct{}
	once    sync.Once
	writeMu sync.Mutex // reader 回 pong 与 writer 数据帧的写互斥
}

func (h *liveHub) add(c *liveConn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *liveHub) remove(c *liveConn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// shutdown 幂等收连接:关闭 done 与底层连接,两个循环随即退出。
func (c *liveConn) shutdown() {
	c.once.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

func (c *liveConn) writeLoop() {
	defer c.hub.remove(c)
	defer c.shutdown()
	ticker := time.NewTicker(wsPingEvery)
	defer ticker.Stop()
	// 首帧 ready:连接建立即确认;重连成功同义(订阅方全量重同步)。
	if err := c.writeFrame(wsOpText, []byte(`{"t":"ready"}`)); err != nil {
		return
	}
	for {
		select {
		case msg := <-c.send:
			if err := c.writeFrame(wsOpText, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.writeFrame(wsOpPing, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *liveConn) readLoop() {
	defer c.hub.remove(c)
	defer c.shutdown()
	br := c.rw.Reader
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsReadIdleTimeout))
		op, payload, err := wsReadFrame(br)
		if err != nil {
			return
		}
		switch op {
		case wsOpPing:
			if len(payload) > 125 { // 控制帧载荷上限(RFC 6455 §5.5)
				return
			}
			if err := c.writeFrame(wsOpPong, payload); err != nil {
				return
			}
		case wsOpClose:
			// 回 close 帧后收连接(握手礼节,尽力而为)。
			_ = c.writeFrame(wsOpClose, nil)
			return
		case wsOpText, wsOpBinary, wsOpContinuation, wsOpPong:
			// 面板客户端不发数据帧;pong 是保活回执,丢弃即可。
		}
	}
}

// writeFrame 单帧发送(服务端帧不掩码)。调用方:writer 循环与 reader
// 的 pong 回执,经 writeMu 互斥。
func (c *liveConn) writeFrame(op byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsWriteFrame(c.rw.Writer, op, payload)
}

// ---- WebSocket 帧编解码(RFC 6455 最小子集) ----

// wsWriteFrame 写一个服务端→客户端帧(FIN=1、不掩码),写后立即冲刷。
func wsWriteFrame(bw *bufio.Writer, op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(append(hdr, 127), ext[:]...)
	}
	if _, err := bw.Write(hdr); err != nil {
		return err
	}
	if _, err := bw.Write(payload); err != nil {
		return err
	}
	return bw.Flush()
}

// wsReadFrame 读一个客户端→服务器帧(按协议必须掩码;宽容接受不掩码)。
// 数据帧的分组转发不需要——客户端不发数据,读到即弃;分片(continuation)
// 同样逐帧读弃,不影响控制帧的穿插处理。
func wsReadFrame(br *bufio.Reader) (op byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return
	}
	op = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	n := uint64(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > wsMaxClientFrame {
		return 0, nil, fmt.Errorf("客户端帧超限(%d 字节)", n)
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

// ---- HTTP 升级端点 ----

// handleWS GET /api/ws:握手升级并接管连接,注册进推送枢纽。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("实时推送未装配"))
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		writeErr(w, http.StatusBadRequest, errors.New("缺少 WebSocket 升级头"))
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		writeErr(w, http.StatusBadRequest, errors.New("缺少 Sec-WebSocket-Key"))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("连接无法接管"))
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("接管连接: %w", err))
		return
	}
	sum := sha1.Sum([]byte(key + wsMagic))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.Write([]byte(resp)); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	c := &liveConn{
		conn: conn, rw: rw, hub: s.live,
		send: make(chan []byte, liveSendBuffer), done: make(chan struct{}),
	}
	s.live.add(c)
	go c.writeLoop()
	go c.readLoop()
}
