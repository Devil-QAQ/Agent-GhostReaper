package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// memBlobs 内存内容寻址存储(测试替身,与 swarm.BlobStore 同构:
// sha256 命名、同内容同引用)。
type memBlobs struct {
	mu   sync.Mutex
	blob map[string][]byte
}

func newMemBlobs() *memBlobs { return &memBlobs{blob: map[string][]byte{}} }

func (b *memBlobs) Write(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	ref := "sha256:" + hex.EncodeToString(sum[:])
	b.mu.Lock()
	b.blob[ref] = data
	b.mu.Unlock()
	return ref, nil
}

func (b *memBlobs) Read(ref string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.blob[ref]
	if !ok {
		return nil, fmt.Errorf("no blob %s", ref)
	}
	return d, nil
}

func (b *memBlobs) get(ref string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.blob[ref]
}

func (b *memBlobs) has(ref string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.blob[ref]
	return ok
}

// newTestMirror 起一套 镜核+会话(归因固定 t1/s1/u1),并铺垫归因端点。
func newTestMirror(t *testing.T) (*Mirror, *Session, *rings.Store, *memBlobs) {
	t.Helper()
	tmp := t.TempDir()
	store, err := rings.Open(filepath.Join(tmp, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobs := newMemBlobs()
	m := New(store, blobs, tmp)
	if _, err := store.Append(context.Background(), "t1",
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test",
			Payload: rings.NodeAdded{NodeID: "web1", NodeType: rings.NodeAsset, Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Status: "dispatched"}},
	); err != nil {
		t.Fatalf("铺垫失败: %v", err)
	}
	sess, err := m.Session(Meta{TaskID: "t1", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return m, sess, store, blobs
}

// proxiedClient 模拟"工具":标准 http.Client,传输层 Proxy 恒指会话
// (工具代码不含任何镜知识——透明性在结构上成立)。
func proxiedClient(sess *Session) *http.Client {
	return sess.HTTPClient(10 * time.Second)
}

func mirrorRecords(t *testing.T, store *rings.Store) []rings.MirrorRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), "t1", 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.MirrorRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var rec rings.MirrorRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

func lastMirrorPayload(t *testing.T, store *rings.Store) []byte {
	t.Helper()
	evs, err := store.Events(context.Background(), "t1", 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == rings.EventMirrorRecord {
			return evs[i].Payload
		}
	}
	return nil
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	return u
}

func hasHeader(hs []Header, name, value string) bool {
	for _, h := range hs {
		if h.Name == name && h.Value == value {
			return true
		}
	}
	return false
}

func TestHTTPExchangeFullCaptureAndReplay(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)

	const body = `{"service":"gr-mock-main","version":"2.4.1"}`
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/api/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer origin.Close()

	// 工具视角:普通客户端普通请求,响应正常——接管透明。
	client := proxiedClient(sess)
	resp, err := client.Get(origin.URL + "/api/version")
	if err != nil {
		t.Fatalf("经镜请求失败: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != body {
		t.Fatalf("工具侧响应不符: %d %q", resp.StatusCode, got)
	}
	if hits.Load() != 1 {
		t.Fatalf("目标应恰被命中 1 次,得到 %d", hits.Load())
	}

	// 留痕:恰 1 条 http 记录,归因/摘要齐全。
	recs := mirrorRecords(t, store)
	if len(recs) != 1 {
		t.Fatalf("应恰 1 条 mirror.record,得到 %d", len(recs))
	}
	rec := recs[0]
	u := mustURL(t, origin.URL)
	if rec.Type != rings.MirrorRecHTTP || rec.StepID != "s1" || rec.UnitID != "u1" ||
		rec.TranscriptID != "swarm-u1" || rec.Method != "GET" ||
		rec.URL != origin.URL+"/api/version" || rec.Status != 200 || rec.BlobRef == "" {
		t.Fatalf("记录摘要不符: %+v", rec)
	}
	if rec.RespBytes != int64(len(body)) {
		t.Fatalf("响应字节计数不符: %d", rec.RespBytes)
	}

	// 回放:blob 文档与真实交换逐字段一致(请求方法/URL/头、响应状态/体)。
	var ex Exchange
	if err := json.Unmarshal(blobs.get(rec.BlobRef), &ex); err != nil {
		t.Fatalf("解码交换文档失败: %v", err)
	}
	if ex.ID != rec.ExchangeID || ex.Request.Method != "GET" ||
		ex.Request.URL != origin.URL+"/api/version" || string(ex.Response.Body) != body ||
		ex.Response.Status != 200 {
		t.Fatalf("回放交换不符: %+v", ex)
	}
	if ex.Request.Host != u.Host {
		t.Fatalf("请求留痕应含 Host: %q", ex.Request.Host)
	}
	if !hasHeader(ex.Response.Headers, "Content-Type", "application/json") {
		t.Fatalf("响应留痕应含 Content-Type: %+v", ex.Response.Headers)
	}

	// R18:账本负载只存引用与摘要,不含响应体原文。
	if strings.Contains(string(lastMirrorPayload(t, store)), body) {
		t.Fatalf("账本负载不得内嵌流量体原文(R18)")
	}
}

func TestAllTrafficCaptured(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)

	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/loop" {
			w.Header().Set("Location", "/landed")
			w.WriteHeader(302)
			return
		}
		w.WriteHeader(200)
	}))
	defer origin.Close()

	client := proxiedClient(sess)
	paths := []string{"/a", "/b", "/missing", "/a"} // 含 404 与重复
	for _, p := range paths {
		resp, err := client.Get(origin.URL + p)
		if err != nil {
			t.Fatalf("请求 %s 失败: %v", p, err)
		}
		_ = resp.Body.Close()
	}
	// 重定向逐跳留痕:客户端追 302 → 第二跳也经镜。
	resp, err := client.Get(origin.URL + "/loop")
	if err != nil {
		t.Fatalf("重定向请求失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Request.URL.Path != "/landed" {
		t.Fatalf("工具应正常落到 /landed: %d", resp.StatusCode)
	}

	total := len(paths) + 2
	if hits.Load() != int64(total) {
		t.Fatalf("目标命中 %d 次,期望 %d", hits.Load(), total)
	}
	recs := mirrorRecords(t, store)
	if len(recs) != total {
		t.Fatalf("留痕 %d 条 ≠ 目标命中 %d 次(非 100%% 经镜)", len(recs), total)
	}
	// 逐路径对账:每条命中都有同 URL 记录,零漏。
	byURL := map[string]int{}
	for _, rec := range recs {
		if rec.BlobRef == "" || !blobs.has(rec.BlobRef) {
			t.Fatalf("记录 %s 缺 blob 文档", rec.ExchangeID)
		}
		byURL[rec.URL]++
	}
	for _, p := range []string{"/a", "/b", "/missing"} {
		want := 1
		if p == "/a" {
			want = 2
		}
		if byURL[origin.URL+p] != want {
			t.Fatalf("路径 %s 留痕 %d 条 ≠ 命中 %d 次", p, byURL[origin.URL+p], want)
		}
	}
	if byURL[origin.URL+"/loop"] != 1 || byURL[origin.URL+"/landed"] != 1 {
		t.Fatalf("重定向两跳应各留一痕: %+v", byURL)
	}
}

func TestBigBodyGoesBlobNotLedger(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)

	big := strings.Repeat("x", 512<<10) // 512KB,远超任何内联阈值
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer origin.Close()

	client := proxiedClient(sess)
	resp, err := client.Post(origin.URL+"/upload", "text/plain", strings.NewReader("payload-上行留痕"))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	recs := mirrorRecords(t, store)
	if len(recs) != 1 {
		t.Fatalf("应恰 1 条记录,得到 %d", len(recs))
	}
	if recs[0].RespBytes != int64(len(big)) {
		t.Fatalf("响应字节不符: %d", recs[0].RespBytes)
	}
	pl := lastMirrorPayload(t, store)
	if len(pl) >= len(big) {
		t.Fatalf("账本负载 %d 字节,不得内嵌大流量体(R18)", len(pl))
	}
	var ex Exchange
	_ = json.Unmarshal(blobs.get(recs[0].BlobRef), &ex)
	if len(ex.Response.Body) != len(big) || string(ex.Request.Body) != "payload-上行留痕" {
		t.Fatalf("blob 文档应保真双向原文: resp=%d req=%q", len(ex.Response.Body), ex.Request.Body)
	}
}

func TestForwardFailureRecorded(t *testing.T) {
	_, sess, store, _ := newTestMirror(t)

	client := proxiedClient(sess)
	// 保留端口 1(无监听):拨号必败——代理惯例回 502(通用文案,不暴露
	//镜的存在),详细原因落账本留痕 Error 字段。
	resp, err := client.Get("http://127.0.0.1:1/nowhere")
	if err != nil {
		t.Fatalf("代理应回 502 响应而非传输错误: %v", err)
	}
	code := resp.StatusCode
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if code != http.StatusBadGateway {
		t.Fatalf("工具侧应见 502,得到 %d", code)
	}
	recs := mirrorRecords(t, store)
	if len(recs) != 1 || recs[0].Error == "" || recs[0].Status != 0 {
		t.Fatalf("转发失败应如实留痕: %+v", recs)
	}
}

func TestConcurrentAndAttribution(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))
	defer origin.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := proxiedClient(sess)
			for j := 0; j < 5; j++ {
				resp, err := c.Get(fmt.Sprintf("%s/c%d-%d", origin.URL, i, j))
				if err != nil {
					t.Errorf("并发请求失败: %v", err)
					return
				}
				_ = resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()

	recs := mirrorRecords(t, store)
	if len(recs) != 40 {
		t.Fatalf("40 次并发请求应留 40 痕,得到 %d", len(recs))
	}
	ids := map[string]bool{}
	for _, rec := range recs {
		if rec.StepID != "s1" || rec.UnitID != "u1" {
			t.Fatalf("归因不符: %+v", rec)
		}
		if ids[rec.ExchangeID] {
			t.Fatalf("交换 ID 重复: %s", rec.ExchangeID)
		}
		ids[rec.ExchangeID] = true
		if !blobs.has(rec.BlobRef) {
			t.Fatalf("记录 %s 缺 blob", rec.ExchangeID)
		}
	}
	if sess.Lost() != 0 {
		t.Fatalf("留痕失败计数应恒 0,得到 %d", sess.Lost())
	}

	// 回放全集:40 条全可还原、内容保真。
	exs, err := Exchanges(context.Background(), store, blobs, "t1")
	if err != nil {
		t.Fatalf("回放失败: %v", err)
	}
	if len(exs) != 40 {
		t.Fatalf("回放 40 条,得到 %d", len(exs))
	}
	for _, ex := range exs {
		if string(ex.Response.Body) != "ok:"+mustURL(t, ex.Request.URL).Path {
			t.Fatalf("回放内容失真: %q", ex.Response.Body)
		}
	}
}

func TestSessionCloseStopsListener(t *testing.T) {
	_, sess, _, _ := newTestMirror(t)
	if err := sess.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	// 关闭后经该会话的请求必失败(监听已撤,强制经镜=无直连旁路)。
	c := sess.HTTPClient(time.Second)
	if resp, err := c.Get("http://127.0.0.1:9/x"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("会话关闭后不应仍可通行")
	}
	// 二次 Close 幂等。
	_ = sess.Close()
}
