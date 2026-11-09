package report

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 格式矩阵与漏洞级导出(P2-13,第五章 §2/§3):格式 md 单文件 / md 分
// 文件 zip / csv / json × 范围(当前筛选 / 全部 / 勾选项,调用方把筛选
// 解析成声明 ID 清单传入);漏洞级导出单份附关联流量证据包(镜留痕
// blob 全文,审计闭环)。全部确定性组装、零 LLM(R17)——相同账本
// 状态(含 zip 条目时间戳,取账本最后事件时刻)重复导出字节级一致。

// 导出格式白名单。
const (
	FormatMD   = "md"
	FormatZip  = "zip"
	FormatCSV  = "csv"
	FormatJSON = "json"
)

// ExportFormat 支持的格式表(白名单校验)。
func ExportFormat(f string) bool {
	switch f {
	case FormatMD, FormatZip, FormatCSV, FormatJSON:
		return true
	}
	return false
}

// Exported 导出产物:内容 + 文件名规约 + 下载类型。
type Exported struct {
	Body     []byte
	Filename string
	MimeType string
}

// TaskExport 任务级导出(格式矩阵):scope 为空 = 全部发现。
// 文件名规约:report-<taskID>.md|zip|json、findings-<taskID>.csv。
func TaskExport(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID, format string, scope []string) (*Exported, error) {
	switch format {
	case FormatMD:
		md, err := Render(ctx, store, blobs, taskID, scope)
		if err != nil {
			return nil, err
		}
		return &Exported{Body: []byte(md), Filename: "report-" + taskID + ".md", MimeType: "text/markdown; charset=utf-8"}, nil
	case FormatZip:
		body, err := taskZip(ctx, store, blobs, taskID, scope)
		if err != nil {
			return nil, err
		}
		return &Exported{Body: body, Filename: "report-" + taskID + ".zip", MimeType: "application/zip"}, nil
	case FormatCSV:
		body, err := findingsCSV(ctx, store, taskID, scope)
		if err != nil {
			return nil, err
		}
		return &Exported{Body: body, Filename: "findings-" + taskID + ".csv", MimeType: "text/csv; charset=utf-8"}, nil
	case FormatJSON:
		body, err := reportJSON(ctx, store, blobs, taskID, scope)
		if err != nil {
			return nil, err
		}
		return &Exported{Body: body, Filename: "report-" + taskID + ".json", MimeType: "application/json; charset=utf-8"}, nil
	}
	return nil, fmt.Errorf("report: 不支持的导出格式 %q", format)
}

// ClaimExport 漏洞级导出:单份发现报告 md;zip 附关联流量证据包
// (该声明全部复现步骤的镜留痕原文全文)。
// 文件名规约:finding-<claimID>.md|zip。
func ClaimExport(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID, claimID, format string) (*Exported, error) {
	switch format {
	case FormatMD, FormatZip:
	default:
		return nil, fmt.Errorf("report: 漏洞级导出仅支持 md/zip,不支持 %q", format)
	}
	node, err := store.Node(ctx, taskID, claimID)
	if err != nil || node.Type != rings.NodeTestClaim {
		return nil, fmt.Errorf("report: 发现 %s 不存在", claimID)
	}
	cs, err := ClaimCaseOf(ctx, store, blobs, taskID, claimID)
	if err != nil {
		return nil, err
	}
	if format == FormatZip {
		EnrichReproForEvent(ctx, store, taskID, cs) // 证据包:Variants/Process/操作方法按定稿 rev 定向读事件
	}
	md, err := claimMD(ctx, store, taskID, *node, cs)
	if err != nil {
		return nil, err
	}
	if format == FormatMD {
		return &Exported{Body: []byte(md), Filename: "finding-" + claimID + ".md", MimeType: "text/markdown; charset=utf-8"}, nil
	}
	// zip:单份 md + 证据包。
	at, err := ledgerLastAt(ctx, store, taskID)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zipAdd(zw, "finding-"+claimID+".md", []byte(md), at); err != nil {
		return nil, err
	}
	if err := zipEvidence(zw, blobs, cs, "evidence", at); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("report: 收口 zip: %w", err)
	}
	return &Exported{Body: buf.Bytes(), Filename: "finding-" + claimID + ".zip", MimeType: "application/zip"}, nil
}

// CrossTaskExport 跨任务导出(一级导航·报告页):按传入任务序逐任务
// 装配;zip 为每任务一目录(任务 md + 逐发现分文件 + 证据包),md 为
// 逐任务全文顺排,csv/json 增任务列/任务对象。
// 文件名规约:reports.md|zip|json、findings.csv。
func CrossTaskExport(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, format string, taskIDs []string) (*Exported, error) {
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("report: 未选择任务")
	}
	switch format {
	case FormatMD:
		var parts []string
		for _, id := range taskIDs {
			md, err := Render(ctx, store, blobs, id, nil)
			if err != nil {
				return nil, err
			}
			parts = append(parts, md)
		}
		body := strings.Join(parts, "\n\n---\n\n")
		return &Exported{Body: []byte(body), Filename: "reports.md", MimeType: "text/markdown; charset=utf-8"}, nil
	case FormatZip:
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, id := range taskIDs {
			if err := taskZipInto(zw, ctx, store, blobs, id, nil, id+"/"); err != nil {
				return nil, err
			}
		}
		if err := zw.Close(); err != nil {
			return nil, fmt.Errorf("report: 收口 zip: %w", err)
		}
		return &Exported{Body: buf.Bytes(), Filename: "reports.zip", MimeType: "application/zip"}, nil
	case FormatCSV:
		var rows [][]string
		for i, id := range taskIDs {
			r, err := findingsCSVRows(ctx, store, id, nil)
			if err != nil {
				return nil, err
			}
			if i > 0 {
				r = r[1:] // 表头只留一份
			}
			rows = append(rows, r...)
		}
		body, err := csvBytes(rows)
		if err != nil {
			return nil, err
		}
		return &Exported{Body: body, Filename: "findings.csv", MimeType: "text/csv; charset=utf-8"}, nil
	case FormatJSON:
		var arr []json.RawMessage
		for _, id := range taskIDs {
			b, err := reportJSON(ctx, store, blobs, id, nil)
			if err != nil {
				return nil, err
			}
			arr = append(arr, json.RawMessage(b))
		}
		body, err := json.MarshalIndent(arr, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("report: 组装跨任务 json: %w", err)
		}
		body = append(body, '\n')
		return &Exported{Body: body, Filename: "reports.json", MimeType: "application/json; charset=utf-8"}, nil
	}
	return nil, fmt.Errorf("report: 不支持的导出格式 %q", format)
}

// ---- 内部装配 ----

// taskZip 任务 zip:任务报告 md(范围口径)+ 逐发现分文件(claims/)+
// 流量证据包(evidence/,scoped 声明的全部步骤原文全文)。
func taskZip(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string, scope []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := taskZipInto(zw, ctx, store, blobs, taskID, scope, ""); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("report: 收口 zip: %w", err)
	}
	return buf.Bytes(), nil
}

func taskZipInto(zw *zip.Writer, ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string, scope []string, prefix string) error {
	md, err := Render(ctx, store, blobs, taskID, scope)
	if err != nil {
		return err
	}
	at, err := ledgerLastAt(ctx, store, taskID)
	if err != nil {
		return err
	}
	if err := zipAdd(zw, prefix+"report-"+taskID+".md", []byte(md), at); err != nil {
		return err
	}
	claims, cases, err := scopedClaims(ctx, store, blobs, taskID, scope)
	if err != nil {
		return err
	}
	cn, pn := 0, 0
	for _, c := range claims {
		var idx, tag string
		// 复核标记不纳入报告的(P4-30 ③,信息性)不进分文件——与报告
		// 正文同口径,证据包随条目一并退出(zip 内审计链走报告 md 汇总行)。
		if cases[c.ID] != nil && cases[c.ID].ReportExcluded {
			continue
		}
		switch c.Status {
		case rings.TestClaimConfirmed, rings.TestClaimFixed:
			cn++
			idx, tag = strconv.Itoa(cn), "C"
		case rings.TestClaimPending:
			pn++
			idx, tag = strconv.Itoa(pn), "P"
		default:
			continue // 分文件只收已确认/证据不足(与报告正文章节同口径)
		}
		cmd, err := claimMD(ctx, store, taskID, c, cases[c.ID])
		if err != nil {
			return err
		}
		if err := zipAdd(zw, prefix+"claims/"+tag+idx+"-"+c.ID+".md", []byte(cmd), at); err != nil {
			return err
		}
		if blobs != nil {
			// 证据包:Variants/Process/操作方法按定稿 rev 定向读事件补全
			//(巨记录大数组不进投影,P4-36)。
			EnrichReproForEvent(ctx, store, taskID, cases[c.ID])
		}
		if err := zipEvidence(zw, blobs, cases[c.ID], prefix+"evidence", at); err != nil {
			return err
		}
	}
	return nil
}

// zipEvidence 证据包(P4-35 ①:维持全量,审计不丢):最小集复现步骤 +
// 全部过程流量(未进面板的对比交换/反证变体/枚举试错),原文全文从
// 仓库按编号取回(字节级保真,Burp 可验证);文件头注明步骤、操作方法
// (工具调用,面板/报告正文不显、证据包收)、目标证书结论(包外信息)
// 与 blob 引用。过程流量两代口径同收:新定稿读 Process 引用清单
// (P4-35,事件只存编号——数据库不内嵌原文,R18),旧定稿读 Variants
// (存量不动,字段保留)。
func zipEvidence(zw *zip.Writer, blobs *swarm.BlobStore, cs *ClaimCase, dir string, at time.Time) error {
	if cs == nil || cs.Repro == nil {
		return nil
	}
	// writeStep 定稿步骤条目(带操作方法行)。
	writeStep := func(st rings.ReproStep, label, name string) error {
		if st.BlobRef == "" || blobs == nil {
			return nil
		}
		ex, ok := readExchange(blobs, st.BlobRef)
		if !ok {
			return nil // 引用悬空如实跳过(报告 md 已带引用,审计可下钻)
		}
		head := exchangeHead{
			Label: label, Method: st.Method, URL: st.URL, Status: st.Status,
			Op: st.Op, TLSWord: exchangeTLSWord(ex), BlobRef: st.BlobRef,
		}
		return zipExchange(zw, name, at, head, ex)
	}
	for i, st := range cs.Repro.Steps {
		if err := writeStep(st.ReproStep, fmt.Sprintf("步骤 %d", i+1),
			path.Join(dir, cs.ClaimID, fmt.Sprintf("step-%d.txt", i+1))); err != nil {
			return err
		}
	}
	// 过程流量(旧口径):Variants 逐条(带操作方法行,账次序)。
	for i, st := range cs.Repro.Variants {
		if err := writeStep(st, fmt.Sprintf("变体 %d", i+1), path.Join(dir, cs.ClaimID, fmt.Sprintf("variant-%d.txt", i+1))); err != nil {
			return err
		}
	}
	// 过程流量(新口径):Process 引用清单逐条(账次序;事件只存编号,
	// 方法/URL/状态从仓库原文解出,操作方法不随清单携带——过程命令的
	// 完整参数在 swarm.record 审计流)。
	if blobs == nil {
		return nil
	}
	for i, ref := range cs.Repro.Process {
		if ref.BlobRef == "" {
			continue
		}
		ex, ok := readExchange(blobs, ref.BlobRef)
		if !ok {
			continue // 引用悬空如实跳过(审计可下钻)
		}
		var method, uurl string
		var status int
		if ex.Request != nil {
			method, uurl = ex.Request.Method, ex.Request.URL
		}
		if ex.Response != nil {
			status = ex.Response.Status
		}
		head := exchangeHead{
			Label: fmt.Sprintf("过程 %d", i+1), Method: method, URL: uurl, Status: status,
			TLSWord: exchangeTLSWord(ex), BlobRef: ref.BlobRef,
		}
		name := path.Join(dir, cs.ClaimID, fmt.Sprintf("process-%d.txt", i+1))
		if err := zipExchange(zw, name, at, head, ex); err != nil {
			return err
		}
	}
	return nil
}

// exchangeHead 证据文件头的事实行(与方法/操作/证书行解耦,新旧口径
// 共用同一装配)。
type exchangeHead struct {
	Label   string
	Method  string
	URL     string
	Status  int
	Op      string
	TLSWord string
	BlobRef string
}

// readExchange 按编号从仓库取回交换文档(失败返回 ok=false,调用方如实
// 跳过)。
func readExchange(blobs *swarm.BlobStore, blobRef string) (*mirror.Exchange, bool) {
	doc, err := blobs.Read(blobRef)
	if err != nil {
		return nil, false
	}
	var ex mirror.Exchange
	if err := json.Unmarshal(doc, &ex); err != nil {
		return nil, false
	}
	return &ex, true
}

// exchangeTLSWord https 交换的目标证书结论(包外信息行;http 为空)。
func exchangeTLSWord(ex *mirror.Exchange) string {
	if ex.Type == rings.MirrorRecHTTPS && ex.TLS != nil {
		return mirror.TLSLabel(ex.TLS.Validity)
	}
	return ""
}

// zipExchange 单条证据文件落 zip:头注(标签/操作/证书/原始数据引用)
// + 全文(不截断,字节级保真)。
func zipExchange(zw *zip.Writer, name string, at time.Time, head exchangeHead, ex *mirror.Exchange) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# 证据 · %s · %s %s → %d\n", head.Label, head.Method, head.URL, head.Status)
	if head.Op != "" {
		fmt.Fprintf(&b, "# 操作 %s\n", head.Op)
	}
	if head.TLSWord != "" {
		fmt.Fprintf(&b, "# 目标证书: %s\n", head.TLSWord)
	}
	fmt.Fprintf(&b, "# 原始数据 %s(内容寻址,字节级保真)\n\n", head.BlobRef)
	b.WriteString(renderExchangeFull(ex))
	return zipAdd(zw, name, []byte(b.String()), at)
}

// claimMD 单条发现的独立 md:与任务报告的发现小节同一 writeClaim 装配
// (面板所见即报告所印),外加任务上下文头。
func claimMD(ctx context.Context, store *rings.Store, taskID string, c rings.NodeFull, cs *ClaimCase) (string, error) {
	task, err := findTask(ctx, store, taskID)
	if err != nil {
		return "", err
	}
	ag, err := store.AssetGraph(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("report: 查资产图: %w", err)
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return "", fmt.Errorf("report: 查资产锚点边: %w", err)
	}
	assetTitle := map[string]string{}
	for _, a := range ag.Assets {
		assetTitle[a.ID] = a.Title
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 漏洞报告 · %s\n\n", c.Title)
	fmt.Fprintf(&b, "> 任务 `%s`(%s)· 单条发现 · 由年轮账本确定性组装,零 LLM\n\n", task.ID, task.Title)
	writeClaim(&b, 1, claimPrefixOf(c.Status), c, assetTitle, abouts, cs)
	b.WriteString("---\n证据全文见配套流量证据包(zip 同名目录);血缘与反证链可按节点 ID 在账本下钻。\n")
	return b.String(), nil
}

func claimPrefixOf(status string) string {
	// fixed(已修复)属 Confirmed 口径(P3-06:已修复的发现仍在成果与
	// 分文件清单内,条目内带复测行)。
	if status == rings.TestClaimConfirmed || status == rings.TestClaimFixed {
		return "C"
	}
	return "P"
}

// scopedClaims 范围内声明(入账序)+ 对应卷宗。
func scopedClaims(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string, scope []string) ([]rings.NodeFull, map[string]*ClaimCase, error) {
	claims, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, "")
	if err != nil {
		return nil, nil, fmt.Errorf("report: 查发现: %w", err)
	}
	if len(scope) > 0 {
		want := map[string]bool{}
		for _, s := range scope {
			want[s] = true
		}
		var filtered []rings.NodeFull
		for _, c := range claims {
			if want[c.ID] {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}
	cases, err := claimCases(ctx, store, blobs, taskID)
	if err != nil {
		return nil, nil, err
	}
	return claims, cases, nil
}

// csvHeader 发现清单 csv 固定列(中文表头,操作者语言)。
var csvHeader = []string{"任务ID", "发现ID", "状态", "级别", "类型", "存在性判断", "影响面判断", "资产锚点", "发现单位", "验证单位", "复现步骤数", "搁置理由"}

// findingsCSV 发现清单 csv(范围口径;级别序 critical→low、未定级沉底,
// 同级按入账序——与面板成果视图同序)。BOM 头保证 Excel 中文不乱码。
func findingsCSV(ctx context.Context, store *rings.Store, taskID string, scope []string) ([]byte, error) {
	rows, err := findingsCSVRows(ctx, store, taskID, scope)
	if err != nil {
		return nil, err
	}
	return csvBytes(rows)
}

func findingsCSVRows(ctx context.Context, store *rings.Store, taskID string, scope []string) ([][]string, error) {
	claims, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, "")
	if err != nil {
		return nil, fmt.Errorf("report: 查发现: %w", err)
	}
	// csv 不做原文回放(原文在 md/json/zip),卷宗装配允许无 blob。
	cases, err := claimCases(ctx, store, nil, taskID)
	if err != nil {
		return nil, err
	}
	if len(scope) > 0 {
		want := map[string]bool{}
		for _, s := range scope {
			want[s] = true
		}
		var filtered []rings.NodeFull
		for _, c := range claims {
			if want[c.ID] {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("report: 查资产锚点边: %w", err)
	}
	ordered := make([]rings.NodeFull, 0, len(claims))
	for _, c := range claims {
		// 复核标记不纳入报告的(P4-30 ③,信息性)不进 csv——报告零信息
		// 条目与 md/json 同口径。
		if cases[c.ID] != nil && cases[c.ID].ReportExcluded {
			continue
		}
		ordered = append(ordered, c)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		si, sj := severityRank((cases[ordered[i].ID]).Severity()), severityRank((cases[ordered[j].ID]).Severity())
		return si < sj || si == sj && ordered[i].CreatedRev < ordered[j].CreatedRev
	})
	rows := [][]string{csvHeader}
	for _, c := range ordered {
		cs := cases[c.ID]
		var anchors []string
		for _, e := range abouts {
			if e.From == c.ID {
				anchors = append(anchors, e.To)
			}
		}
		reproN := 0
		if cs != nil && cs.Repro != nil {
			reproN = len(cs.Repro.Steps)
		}
		vulnclass, _ := c.Detail["vulnclass"].(string)
		impact, _ := c.Detail["impact"].(string)
		unit, _ := c.Detail["unit_id"].(string)
		rows = append(rows, []string{
			taskID, c.ID, claimStatusWord(c.Status), cs.Severity(),
			vulnclass, c.Title, impact,
			strings.Join(anchors, " "),
			unit, cs.Verifier(), strconv.Itoa(reproN),
			cs.PendingReason(),
		})
	}
	return rows, nil
}

// reportJSON 结构化全量(报告的机器可读形态;时间戳取账本事件时刻,
// 无墙钟,字节级可重现)。
func reportJSON(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string, scope []string) ([]byte, error) {
	task, err := findTask(ctx, store, taskID)
	if err != nil {
		return nil, err
	}
	claims, cases, err := scopedClaims(ctx, store, blobs, taskID, scope)
	if err != nil {
		return nil, err
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("report: 查资产锚点边: %w", err)
	}
	type taskInfo struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Rev    int64  `json:"ledger_rev"`
	}
	type finding struct {
		ID            string      `json:"id"`
		Status        string      `json:"status"`
		StatusWord    string      `json:"status_word"`
		Severity      string      `json:"severity,omitempty"`
		Vulnclass     string      `json:"vulnclass,omitempty"`
		Statement     string      `json:"statement"`
		Impact        string      `json:"impact,omitempty"`
		Assets        []string    `json:"assets,omitempty"`
		Discoverer    string      `json:"discoverer_unit,omitempty"`
		Verifier      string      `json:"verifier_unit,omitempty"`
		PendingReason string      `json:"pending_reason,omitempty"`
		Alternatives  []AltView   `json:"alternatives,omitempty"`
		Timeline      []CaseEntry `json:"timeline"`
		Repro         *ReproFinal `json:"repro,omitempty"`
		Remediation   []string    `json:"remediation,omitempty"`
	}
	out := struct {
		Task     taskInfo  `json:"task"`
		Findings []finding `json:"findings"`
	}{Task: taskInfo{ID: task.ID, Title: task.Title, Status: task.Status, Rev: task.UpdatedRev}}
	for _, c := range claims {
		// 复核标记不纳入报告的(P4-30 ③,信息性)不进 json——报告零
		// 信息条目与 md/csv 同口径。
		if cases[c.ID] != nil && cases[c.ID].ReportExcluded {
			continue
		}
		cs := cases[c.ID]
		f := finding{
			ID: c.ID, Status: c.Status, StatusWord: claimStatusWord(c.Status),
			Severity: cs.Severity(), Statement: c.Title,
		}
		f.Vulnclass, _ = c.Detail["vulnclass"].(string)
		f.Impact, _ = c.Detail["impact"].(string)
		f.Discoverer, _ = c.Detail["unit_id"].(string)
		f.Verifier = cs.Verifier()
		f.PendingReason = cs.PendingReason()
		for _, e := range abouts {
			if e.From == c.ID {
				f.Assets = append(f.Assets, e.To)
			}
		}
		if cs != nil {
			f.Alternatives = cs.Alternatives
			f.Timeline = cs.Timeline
			f.Repro = cs.Repro
		}
		f.Remediation = Remediation(f.Vulnclass)
		out.Findings = append(out.Findings, f)
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("report: 组装 json: %w", err)
	}
	return append(body, '\n'), nil
}

// ---- 小工具 ----

// claimStatusWord 状态 → 操作者语言(rings.ClaimStatusWord 单一来源,
// 显示层合并口径 P4-28,与面板 CLAIM_STATUS 同词)。
func claimStatusWord(status string) string {
	return rings.ClaimStatusWord(status)
}

func severityRank(sev string) int {
	for i, s := range severityOrder {
		if s == sev {
			return i
		}
	}
	return len(severityOrder) // 未定级沉底
}

// ledgerLastAt 账本最后事件时刻(zip 条目时间戳的确定性来源)。
func ledgerLastAt(ctx context.Context, store *rings.Store, taskID string) (time.Time, error) {
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return time.Time{}, fmt.Errorf("report: 查事件流: %w", err)
	}
	var last time.Time
	for _, ev := range evs {
		if ev.OccurredAt.After(last) {
			last = ev.OccurredAt
		}
	}
	return last.UTC(), nil
}

// zipAdd 追加一个 zip 条目(固定时刻,Deflate;内容寻址确定性)。
func zipAdd(zw *zip.Writer, name string, body []byte, at time.Time) error {
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return fmt.Errorf("report: 写 zip 条目 %s: %w", name, err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("report: 写 zip 条目 %s: %w", name, err)
	}
	return nil
}

// csvBytes csv 序列化(表头 + 行;CRLF、字段含逗号/引号/换行自动转义)。
func csvBytes(rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\uFEFF") // UTF-8 BOM:Excel 直开中文不乱码(常量,确定性不变)
	cw := csv.NewWriter(&buf)
	if err := cw.WriteAll(rows); err != nil {
		return nil, fmt.Errorf("report: 写 csv: %w", err)
	}
	return buf.Bytes(), nil
}
