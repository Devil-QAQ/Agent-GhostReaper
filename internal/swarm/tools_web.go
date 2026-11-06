package swarm

// tools_web.go P3-03 工具扩充:read(只读文件)/ web_search(外网搜索,
// 多后端)/ web_fetch(外网取文,强制经镜)。三者都不是攻击工具——
// 攻击仍由 bash 自由组合(工具哲学红线:系统只做基础设施,不引导)。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/websearch"
)

// ---- read:独立只读文件工具(渗透下载物分析) ----

// read 工具的取行边界(描述即规则 §11:边界写进描述,模型排读取时
// 自行掂量;单行截断防超长行刷屏)。
const (
	readDefaultLines = 400  // 未指定 limit 时的默认行数
	readMaxLines     = 1000 // limit 上限
	readLineClip     = 2000 // 单行字符截断
	readSniffBytes   = 512  // 二进制嗅探窗口
)

// readTool 只读文件工具:读取本机文本文件(带行号),供分析执行中
// 下载/落盘的产物。无任何写操作;二进制与目录不归本工具(指路 bash)。
func (env *unitEnv) readTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read",
		Description: toolDescRead,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "文件路径(相对路径按当前工作目录解析)"},
				"offset": map[string]any{"type": "integer", "description": "起始行号(从 1 起,默认 1)"},
				"limit":  map[string]any{"type": "integer", "description": "本次读取的行数(默认 400,上限 1000)"},
			},
			"required": []string{"path"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			p := strings.TrimSpace(args.Path)
			if p == "" {
				return tool.Errorf("path 不能为空:填入要读的文件路径后重发"), nil
			}
			if args.Offset < 0 || args.Limit < 0 {
				return tool.Errorf("offset/limit 须为非负整数,修正参数后重发"), nil
			}
			offset := args.Offset
			if offset == 0 {
				offset = 1
			}
			limit := args.Limit
			if limit == 0 {
				limit = readDefaultLines
			}
			if limit > readMaxLines {
				limit = readMaxLines
			}
			// 存档引用直接回读(P4-39 ①:工具输出超长落盘后,按返回文本中的
			// sha256: 引用取回全文,offset/limit 分段与文件同款)。
			if ref := extractBlobRef(p); ref != "" && ref != p {
				return tool.Errorf("path 含多余内容:存档引用须原样填入(如 sha256:<64 位十六进制>);多余前缀会挡住引用解析"), nil
			}
			if strings.HasPrefix(p, BlobRefPrefix) {
				path, rerr := env.Executor.Blobs.Path(p)
				if rerr != nil {
					return tool.Errorf(fmt.Sprintf("存档引用非法: %v", rerr)), nil
				}
				if _, serr := os.Stat(path); serr != nil {
					return tool.Errorf(fmt.Sprintf("存档原文不存在: %s;引用指向的内容寻址文件缺失(可能库目录被移动),可重跑原命令重新取得", p)), nil
				}
				p = path
			}
			// 相对路径按步骤工作目录解析(与 bash 同一基准)。
			if !filepath.IsAbs(p) {
				p = filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), p)
			}
			st, err := os.Stat(p)
			if err != nil {
				if os.IsNotExist(err) {
					return tool.Errorf(fmt.Sprintf("文件不存在: %s;核对路径(相对路径按当前工作目录解析)", args.Path)), nil
				}
				return tool.Errorf("读取文件状态失败: " + err.Error()), nil
			}
			if st.IsDir() {
				return tool.Errorf(fmt.Sprintf("是目录: %s;浏览目录用 bash(ls -la),本工具只读单个文件", args.Path)), nil
			}
			if st.Size() == 0 {
				return tool.Text(fmt.Sprintf("%s:空文件(0 字节)", args.Path)), nil
			}
			f, err := os.Open(p)
			if err != nil {
				return tool.Errorf("打开文件失败: " + err.Error()), nil
			}
			defer f.Close()
			// 二进制嗅探:窗口内含 NUL 判二进制,不硬解(指路 bash 分析)。
			head := make([]byte, readSniffBytes)
			n, _ := io.ReadFull(f, head)
			if bytes.IndexByte(head[:n], 0) >= 0 {
				return tool.Errorf(fmt.Sprintf("二进制文件不归本工具: %s(%d 字节);用 bash 分析(file/strings/xxd)", args.Path, st.Size())), nil
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return tool.Errorf("重读文件失败: " + err.Error()), nil
			}
			// 逐行读取:跳到 offset,收 limit 行;行号与截断如实标注。
			var b strings.Builder
			fmt.Fprintf(&b, "%s 第 %d 行起", args.Path, offset)
			br := bufio.NewReader(f)
			lineno := 0
			collected := 0
			more := false
			for {
				line, rerr := br.ReadString('\n')
				if rerr == io.EOF {
					if line == "" {
						break
					}
				} else if rerr != nil {
					return tool.Errorf("读文件失败: " + rerr.Error()), nil
				}
				lineno++
				if lineno < offset {
					continue
				}
				if collected >= limit {
					more = true
					break
				}
				clipped := clipTail(strings.TrimRight(line, "\n"), readLineClip)
				fmt.Fprintf(&b, "\n%6d\t%s", lineno, clipped)
				collected++
				if rerr == io.EOF {
					break
				}
			}
			if collected == 0 {
				return tool.Errorf(fmt.Sprintf("第 %d 行起没有内容:文件共 %d 行,offset 超出范围", offset, lineno)), nil
			}
			b.WriteString(fmt.Sprintf("\n(本次 %d 行", collected))
			if more {
				fmt.Fprintf(&b, ",未读完,继续读用 offset=%d", offset+collected)
			}
			b.WriteString(")")
			text := b.String()
			// P4-39 ①:read 是存档引用的钦定回读通道,阈值取 30,000 定值
			//——沿用旧 4,096 会套娃 spill(读回的段又被 spill,段后半永远
			// 读不到)。超 30,000 的整段落 blob,对话内留截头+引用;其余
			// 工具(bash 等)的 4,096 R18 spill 不变(瘦身优先)。落盘记
			// 黑匣子 tool_spill(P4-39 新落盘面;bash 家族的 R18 落盘账
			// 在年轮 SwarmRecord.ResultRef,不动)。
			if len(text) > convSpillThreshold {
				if ref, werr := env.Executor.Blobs.Write([]byte(text)); werr == nil {
					detail := map[string]any{"tool": "read", "ref": ref, "bytes": len(text)}
					if berr := blackbox.Record(context.Background(), env.Executor.Store, env.TaskID, "swarm:"+env.UnitID,
						rings.BlackboxRecord{Type: rings.BlackboxToolSpill, StepID: env.StepID, UnitID: env.UnitID, Detail: detail}); berr != nil {
						runlog.Errorf("黑匣子写入失败: read 落盘 %s/%s: %v", env.TaskID, env.UnitID, berr)
					}
					return tool.Text(fmt.Sprintf("%s\n[输出超长,全文已存档: %s]", text[:blobThreshold], ref)), nil
				}
			}
			return tool.Text(text), nil
		},
	})
}

// ---- web_search:外网搜索(多后端) ----

// webSearchTool 外网查证 CVE / 公开信息:后端与出口在系统设置·搜索
// 配置(ddgs 默认免 key / brave / tavily + 独立出口代理),每次调用
// 现读配置——面板切后端对下一次搜索即生效。搜索流量走独立出口
// (不经镜:镜管目标流量,外网查证不混入目标对账)。
func (env *unitEnv) webSearchTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "web_search",
		Description: toolDescWebSearch,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "搜索词(CVE 编号、漏洞名、报错原文等;换关键词比重发同一查询有效)"},
			},
			"required": []string{"query"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			cfg := websearch.Load(ctx, env.Executor.Store)
			results, err := websearch.Search(ctx, cfg, args.Query)
			if err != nil {
				return tool.Errorf("搜索失败: " + err.Error()), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "搜索完成(后端 %s,%d 条):\n", websearch.BackendLabel(cfg.Backend), len(results))
			for i, r := range results {
				fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
				if r.Snippet != "" {
					fmt.Fprintf(&b, "   %s\n", clipLine(r.Snippet, 300))
				}
			}
			return tool.Text(b.String()), nil
		},
	})
}

// ---- web_fetch:外网取文(强制经镜留痕) ----

// web_fetch 的取文边界。
const (
	webFetchBodyLimit = 2 << 20   // 单次下载上限(2 MiB)
	webFetchTextClip  = 64 * 1024 // 转出正文的上限(完整原文在镜 blob 留痕)
	webFetchTimeout   = 30 * time.Second
)

// webFetchTool 取回外网 URL 的正文原文(网页转正文文本)。流量强制
// 经镜(与 http_probe 同一镜客户端):请求/响应留痕入账本,流量页可
// 回放——外网查证与目标流量同一条审计链。无镜装配时拒绝直连
// (fail-safe:宁可不可用,不出旁路)。
func (env *unitEnv) webFetchTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "web_fetch",
		Description: toolDescWebFetch,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "要取原文的 URL(http/https);网页转为正文文本返回,非 HTML 源码"},
			},
			"required": []string{"url"},
		},
		ReadOnly:    alwaysRO,
		Concurrent:  alwaysRO,
		Permissions: allowReadOnly,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			u, err := url.Parse(strings.TrimSpace(args.URL))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return tool.Errorf("url 非法:须为 http/https 且含主机(如 https://cve.org/CVERecord?id=CVE-2021-44228)"), nil
			}
			if env.mirror == nil {
				return tool.Errorf("镜未装配:外网取文必须经镜留痕,本环境无镜时不可用(不设直连旁路)"), nil
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return tool.Errorf("构造请求失败: " + err.Error()), nil
			}
			req.Header.Set("User-Agent", env.userAgent(ctx))
			resp, err := env.mirror.HTTPClient(webFetchTimeout).Do(req)
			if err != nil {
				return tool.Errorf("取文失败: " + err.Error()), nil
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchBodyLimit))
			if err != nil {
				return tool.Errorf("读取响应失败: " + err.Error()), nil
			}
			ctype := resp.Header.Get("Content-Type")
			var b strings.Builder
			fmt.Fprintf(&b, "url=%s status=%d content-type=%s size=%d bytes\n", u.String(), resp.StatusCode, ctype, len(body))
			switch {
			case strings.Contains(ctype, "text/html"):
				b.WriteString("--- 正文(网页转文本) ---\n")
				b.WriteString(htmlToText(body))
			case isTexty(ctype) || ctype == "":
				b.WriteString("--- 正文 ---\n")
				b.Write(body)
			default:
				// 二进制:只报事实(类型/大小),不灌二进制字节给模型。
				fmt.Fprintf(&b, "--- 二进制内容不展开(类型 %s);确需内容时用 bash 下载后分析 ---", ctype)
			}
			text := b.String()
			if ref, head := env.Executor.Blobs.spill(text); ref != "" {
				return tool.Text(fmt.Sprintf("%s\n[输出超长,全文已存档: %s]", head, ref)), nil
			}
			return tool.Text(text), nil
		},
	})
}

// isTexty 文本类内容类型判定(正文直读;其余按二进制处理)。
func isTexty(ctype string) bool {
	c := strings.ToLower(ctype)
	return strings.HasPrefix(c, "text/") ||
		strings.Contains(c, "json") ||
		strings.Contains(c, "xml") ||
		strings.Contains(c, "javascript") ||
		strings.Contains(c, "yaml")
}

// htmlToText HTML 转正文文本:去 script/style/noscript 与注释,块级
// 标签断行,剥全部标签,还原实体,收空行。不追求排版保真,追求模型
// 可读(完整 HTML 源码在镜的 blob 留痕里,审计可下钻)。
func htmlToText(body []byte) string {
	s := string(body)
	for _, tag := range []string{"script", "style", "noscript", "svg"} {
		s = stripTagBlock(s, tag)
	}
	// 注释。
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "-->")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + s[i+j+3:]
	}
	// 块级标签断行(换行符形态统一,后续收空行;大小写不敏感,不动正文)。
	for _, t := range []string{"</p>", "<br>", "<br/>", "<br />", "</div>", "</tr>", "</li>", "</h1>", "</h2>", "</h3>", "</h4>", "</h5>", "</h6>", "</pre>", "</title>"} {
		s = replaceAllFold(s, t, "\n")
	}
	// 剥全部标签。
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	// 还原实体、逐行收空白、折叠连续空行。
	lines := strings.Split(html.UnescapeString(b.String()), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, l)
	}
	text := strings.Join(out, "\n")
	return clipTail(text, webFetchTextClip)
}

// stripTagBlock 整块剥掉一对标签及其内层(script/style 等不构成正文的
// 块;大小写不敏感近似——HTML 源码标签多为小写)。
func stripTagBlock(s, tag string) string {
	ls := strings.ToLower(s)
	open := "<" + tag
	closeTag := "</" + tag + ">"
	for {
		i := strings.Index(ls, open)
		if i < 0 {
			return s
		}
		j := strings.Index(ls[i:], closeTag)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len(closeTag):]
		ls = strings.ToLower(s)
	}
}

// replaceAllFold 大小写不敏感的全串替换(正文大小写原样保留)。
func replaceAllFold(s, old, new string) string {
	ls := strings.ToLower(s)
	lo := strings.ToLower(old)
	var b strings.Builder
	for from := 0; ; {
		i := strings.Index(ls[from:], lo)
		if i < 0 {
			b.WriteString(s[from:])
			return b.String()
		}
		b.WriteString(s[from : from+i])
		b.WriteString(new)
		from += i + len(old)
	}
}

// ---- 工具目录条目(描述与 tools.go 的目录单一来源共用) ----

const (
	toolDescRead      = "只读文件工具:读取本机文本文件内容(带行号),用于分析执行中下载或落盘的产物(配置、源码、响应存档等)。一次最多 1000 行、每行截 2000 字符,大文件用 offset/limit 分段读;工具输出超长被存档时,把返回文本里的存档引用(sha256: 开头)原样作为 path 填入即可读回全文,同样支持 offset/limit 分段。本工具无任何写操作,二进制文件不归本工具(用 bash 的 file/strings/xxd 分析)。"
	toolDescWebSearch = "外网搜索公开信息(CVE 详情、公开漏洞分析、报错原文查证),返回标题、链接与摘要。默认 DuckDuckGo 免 key;后端(Brave/Tavily 需 key)与独立出口代理在系统设置·搜索配置。结果不足时换关键词重搜,不要重复同一查询。"
	toolDescWebFetch  = "取回外网 URL 的原文(网页返回正文文本而非 HTML 源码;JSON/XML 等文本原样;二进制只报类型与大小)。流量强制经镜留痕,可在流量页回放查证。正文超长会截断存档;查 CVE 详情、 advisory 原文、公开报告时用本工具。"
)
