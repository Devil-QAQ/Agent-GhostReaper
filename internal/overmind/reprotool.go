package overmind

// P4-62 C(b),问题清单 #83:复现原料不再单靠镜——直连旁路流量(经镜
// 失败或绕开代理的命令)从工具调用记录确定性组包。两类来源:
//   - http_probe:参数本身结构化(url/method),请求包直接组;响应侧
//     从工具结果文本的确定性标记行还原(状态码/关键头/响应体开头)。
//   - bash curl:仅可静态解析的简单形态——单条 curl、单 URL、显式
//     方法/头/体、无变量拼接($、反引号)、无命令替换/管道/循环/重定向。
//     含变量等复杂形态跳过并计入缺料诊断(不编造);响应侧仅当命令带
//     -i(输出含状态行与头)时还原,否则如实不还原。
//
// 组包产物为与镜留痕同构的交换文档(mirror.Exchange),入内容寻址仓库
// (R18),读侧(面板/报告/证据包)按 blob 渲染同一装配——请求包 origin-form
// ＋Host,Burp 就绪(P4-08 ⑥ 口径复用,零平行实现)。确定性组包、零 LLM
// (R17);无法确定性还原的部分如实缺失,绝不推测补齐。

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// reproToolSource 标记工具调用组包来源(rings.ReproStep.Source 词表)。
const reproToolSource = "tool"

// toolPacket 工具调用组包结果:可组包时 Exchange 非空;不可静态解析的
// 流量命令返回 skipped=true(缺料诊断计数,调用方如实标注);非流量
// 命令两者皆空(不是复现原料,不进池不计数)。
type toolPacket struct {
	Ex      *mirror.Exchange
	Skipped bool
}

// assembleProbePacket http_probe 调用记录组包(参数结构化,直接组)。
func assembleProbePacket(taskID, stepID, unitID string, args json.RawMessage, result string) *toolPacket {
	var a struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.URL) == "" {
		return &toolPacket{Skipped: true}
	}
	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method == "" {
		method = "GET"
	}
	u, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &toolPacket{Skipped: true}
	}
	ex := &mirror.Exchange{
		ID: nextToolExchangeID(), Type: rings.MirrorRecHTTP,
		TaskID: taskID, StepID: stepID, UnitID: unitID,
		Request: &mirror.Request{Method: method, URL: u.String(), Proto: "HTTP/1.1", Host: u.Host},
	}
	if resp := parseProbeResult(result); resp != nil {
		ex.Response = resp
		ex.RespBytes = int64(len(resp.Body))
	}
	return &toolPacket{Ex: ex}
}

// probeResultKeys 工具结果标记行的已知键(值域按"下一已知键"切分——
// 工具自身输出格式确定,含空格的值如 content-type 的 charset 参数不误切)。
var probeResultKeys = []string{"url", "method", "status", "server", "content-type", "content-length"}

// probeMarkerValue 标记行里某键的值(止于行内下一个已知键;找不到键
// 返回空)。
func probeMarkerValue(line, key string) string {
	i := strings.Index(line, key+"=")
	if i < 0 {
		return ""
	}
	rest := line[i+len(key)+1:]
	end := len(rest)
	for _, k := range probeResultKeys {
		if j := strings.Index(rest, " "+k+"="); j >= 0 && j < end {
			end = j
		}
	}
	return strings.TrimSpace(rest[:end])
}

// parseProbeResult http_probe 结果文本 → 响应侧(确定性标记行:行 1
// url=/method=/status=,行 2 server=/content-type=/content-length=,其后
// "--- body(前 N 字节) ---" 段;头仅工具如实输出的三行,不补造其余头)。
// 读不出合法状态码时响应侧如实不还原。
func parseProbeResult(result string) *mirror.Response {
	lines := strings.SplitN(result, "\n", 3)
	if len(lines) == 0 {
		return nil
	}
	code, err := strconv.Atoi(probeMarkerValue(lines[0], "status"))
	if err != nil || code <= 0 {
		return nil
	}
	resp := &mirror.Response{Proto: "HTTP/1.1", Status: code}
	info := ""
	if len(lines) > 1 {
		info = lines[1]
	}
	for _, h := range []struct{ name, key string }{
		{"Server", "server"}, {"Content-Type", "content-type"}, {"Content-Length", "content-length"},
	} {
		if v := probeMarkerValue(info, h.key); v != "" {
			resp.Headers = append(resp.Headers, mirror.Header{Name: h.name, Value: v})
		}
	}
	if len(lines) > 2 && strings.Contains(lines[2], "--- body") {
		if _, body, ok := strings.Cut(lines[2], "--- body(前 1536 字节) ---"); ok {
			resp.Body = []byte(strings.TrimLeft(body, "\r\n"))
		}
	}
	return resp
}

// assembleBashPacket bash 调用记录组包:仅可静态解析的简单 curl 形态。
func assembleBashPacket(taskID, stepID, unitID string, args json.RawMessage, result string) *toolPacket {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil || !looksLikeTrafficCommand(a.Command) {
		return &toolPacket{} // 非流量命令:不是复现原料,不进池不计数
	}
	pc, ok := parseSimpleCurl(a.Command)
	if !ok {
		return &toolPacket{Skipped: true} // 流量命令但不可静态还原:缺料如实计
	}
	req := &mirror.Request{Method: pc.method, URL: pc.rawURL, Proto: "HTTP/1.1", Host: pc.host()}
	for _, h := range pc.headers {
		if strings.EqualFold(h.Name, "Host") {
			req.Host = h.Value // Host 头改写目标:并入请求行外独立位,不双写
			continue
		}
		req.Headers = append(req.Headers, h)
	}
	if len(pc.data) > 0 {
		req.Body = []byte(strings.Join(pc.data, "&"))
	}
	ex := &mirror.Exchange{
		ID: nextToolExchangeID(), Type: rings.MirrorRecHTTP,
		TaskID: taskID, StepID: stepID, UnitID: unitID,
		Request: req, ReqBytes: int64(len(req.Body)),
	}
	if pc.includeHeaders {
		if resp := parseCurlOutput(result); resp != nil {
			ex.Response = resp
			ex.RespBytes = int64(len(resp.Body))
		}
	}
	return &toolPacket{Ex: ex}
}

// looksLikeTrafficCommand 命令里是否出现流量形态(curl/wget/http URL)
// ——只有流量命令才谈得上"缺该步原料",其余 bash 命令不是复现原料。
func looksLikeTrafficCommand(cmd string) bool {
	return strings.Contains(cmd, "curl") || strings.Contains(cmd, "wget") ||
		strings.Contains(cmd, "http://") || strings.Contains(cmd, "https://")
}

// curlCommand 静态解析出的请求形状。
type curlCommand struct {
	method         string
	rawURL         string
	headers        []mirror.Header
	data           []string
	includeHeaders bool // -i:输出含状态行与头,响应侧可还原
}

func (c *curlCommand) host() string {
	if u, err := url.Parse(c.rawURL); err == nil {
		return u.Host
	}
	return ""
}

// curlSimpleForbidden 静态解析禁用形态:变量/命令替换/命令拼接/管道/
// 重定向/循环续行——出现即不可确定性还原。
const curlSimpleForbidden = "$`;&|><\n\r\\" 

// curlNoShapeFlags 不改变请求包形状、可直接忽略的无值开关(连接行为/
// 输出控制/重定向跟随等;-i 除外——它决定输出是否含响应头,见 parse)。
var curlNoShapeFlags = map[string]bool{
	"-s": true, "--silent": true, "-S": true, "--show-error": true,
	"-k": true, "--insecure": true, "-L": true, "--location": true,
	"-v": true, "--verbose": true,
	"-4": true, "-6": true, "--compressed": true,
}

// curlValueFlags 值参数开关(吃掉其后一个词,= 形态并入同词):不出现在
// 请求包里或已并入组包结果的开关(超时/输出/代理/重试/解析对照等)。
var curlValueFlags = map[string]bool{
	"-m": true, "--max-time": true, "--connect-timeout": true,
	"-o": true, "--output": true, "-w": true, "--write-out": true,
	"--noproxy": true, "-x": true, "--proxy": true, "--retry": true,
	"--resolve": true, "--interface": true, "-z": true, "--time-cond": true,
	"-c": true, "--cookie-jar": true, "-y": true, "-Y": true,
}

// curlHeaderFlags 进请求头的值开关(-A UA/-e Referer/-u 基本认证/
// -b Cookie/--H 自定义):与 -H 同通道入包。
var curlHeaderFlags = map[string]bool{
	"-A": true, "--user-agent": true,
	"-e": true, "--referer": true,
	"-u": true, "--user": true,
	"-b": true, "--cookie": true,
}

// parseSimpleCurl 简单 curl 静态解析;false = 复杂形态(跳过,如实缺料)。
// 分词遵循 shell 单双引号成对原则(仅静态形状判定,不做展开);未识别
// 的开关一律按复杂跳过(不猜语义)。
func parseSimpleCurl(cmd string) (*curlCommand, bool) {
	if strings.ContainsAny(cmd, curlSimpleForbidden) {
		return nil, false
	}
	if strings.Count(cmd, "http://")+strings.Count(cmd, "https://") != 1 {
		return nil, false // 无 URL 或多 URL 不组
	}
	fields, ok := shellFields(cmd)
	if !ok || len(fields) == 0 || fields[0] != "curl" {
		return nil, false
	}
	pc := &curlCommand{}
	getMode := false
	i := 1
	for i < len(fields) {
		f := fields[i]
		name := f
		if j := strings.Index(f, "="); j > 0 && strings.HasPrefix(f, "--") {
			name = f[:j] // --key=value 归一为 --key
		}
		valOf := func(next bool) string { // 开关值:= 形态就地取,空格形态取下一词
			if strings.Contains(f, "=") && strings.HasPrefix(f, "--") {
				return f[strings.Index(f, "=")+1:]
			}
			if next && i+1 < len(fields) {
				return fields[i+1]
			}
			return ""
		}
		hasValArg := !(strings.Contains(f, "=") && strings.HasPrefix(f, "--"))
		switch {
		case name == "-X" || name == "--request":
			if pc.method != "" || hasValArg && i+1 >= len(fields) {
				return nil, false
			}
			if v := valOf(hasValArg); v == "" {
				return nil, false
			} else {
				pc.method = strings.ToUpper(v)
			}
			if hasValArg {
				i++
			}
			i++
		case name == "-I" || name == "--head":
			if pc.method != "" {
				return nil, false
			}
			pc.method = "HEAD"
			i++
		case name == "-i" || name == "--include":
			pc.includeHeaders = true // 输出含状态行与头:响应侧可还原
			i++
		case name == "-H" || name == "--header":
			v := valOf(hasValArg)
			if v == "" {
				return nil, false
			}
			hname, hvalue, ok := strings.Cut(v, ":")
			if !ok || strings.TrimSpace(hname) == "" {
				return nil, false
			}
			pc.headers = append(pc.headers, mirror.Header{Name: strings.TrimSpace(hname), Value: strings.TrimSpace(hvalue)})
			if hasValArg {
				i++
			}
			i++
		case name == "-d" || name == "--data" || name == "--data-raw" || name == "--data-binary" || name == "--data-ascii":
			v := valOf(hasValArg)
			if v == "" || strings.HasPrefix(v, "@") {
				return nil, false // 文件体不可静态还原
			}
			pc.data = append(pc.data, v)
			if hasValArg {
				i++
			}
			i++
		case name == "-G" || name == "--get":
			getMode = true // -d 数据移入 URL 查询串,循环后统一应用
			i++
		case curlNoShapeFlags[name]:
			i++
		case curlHeaderFlags[name]:
			v := valOf(hasValArg)
			if v == "" {
				return nil, false
			}
			pc.headers = append(pc.headers, curlFlagHeader(name, v))
			if hasValArg {
				i++
			}
			i++
		case curlValueFlags[name]:
			if hasValArg {
				i++ // 空格分隔形态:值在下一词,一并跳过
			}
			i++
		case strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://"):
			if pc.rawURL != "" {
				return nil, false
			}
			pc.rawURL = f
			i++
		default:
			return nil, false // 未识别开关/裸词:不猜语义,按复杂跳过
		}
	}
	if pc.rawURL == "" {
		return nil, false
	}
	u, err := url.Parse(pc.rawURL)
	if err != nil || u.Host == "" {
		return nil, false
	}
	if getMode && !pc.moveDataToQuery(u) {
		return nil, false
	}
	if pc.method == "" {
		if len(pc.data) > 0 {
			pc.method = "POST" // curl 语义:-d 未显式指定方法时用 POST
		} else {
			pc.method = "GET"
		}
	}
	return pc, true
}

// moveDataToQuery -G 语义:-d 数据移入 URL 查询串(单 -d 且原查询串
// 为空的形态可确定性改写,其余保守放弃)。
func (c *curlCommand) moveDataToQuery(u *url.URL) bool {
	if len(c.data) == 0 {
		return true
	}
	if len(c.data) != 1 || u.RawQuery != "" || strings.Contains(c.data[0], " ") {
		return false
	}
	u.RawQuery = c.data[0]
	c.rawURL = u.String()
	c.data = nil
	return true
}

// curlFlagHeader 头类开关 → 请求头(curl 语义:-u 生成基本认证头)。
func curlFlagHeader(name, v string) mirror.Header {
	switch name {
	case "-A", "--user-agent":
		return mirror.Header{Name: "User-Agent", Value: v}
	case "-e", "--referer":
		return mirror.Header{Name: "Referer", Value: v}
	case "-b", "--cookie":
		return mirror.Header{Name: "Cookie", Value: v}
	case "-u", "--user":
		return mirror.Header{Name: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte(v))}
	}
	return mirror.Header{}
}

// shellFields 简单 shell 分词(空格分隔,单双引号成对包裹取整;引号不
// 闭合返回 false——不可静态解析)。不做变量展开与转义解释,只识别
// "带引号的整词"形态。
func shellFields(cmd string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range cmd {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case (r == ' ' || r == '\t') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if inSingle || inDouble {
		return nil, false
	}
	flush()
	return out, true
}

// parseCurlOutput curl -i 输出 → 响应侧(状态行+头+体,curl 如实输出的
// 原文形态,\r\n 与 \n 行尾都认);不以 HTTP 状态行开头(输出为空/非响应)
// 返回 nil。
func parseCurlOutput(result string) *mirror.Response {
	s := strings.TrimLeft(result, " \t\r\n")
	if s == "" || !strings.HasPrefix(s, "HTTP/") {
		return nil
	}
	head, body, hasBody := cutHTTPBody(s)
	lines := strings.Split(head, "\n")
	parts := strings.Fields(strings.TrimRight(lines[0], "\r"))
	if len(parts) < 2 {
		return nil
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil || code <= 0 {
		return nil
	}
	resp := &mirror.Response{Proto: parts[0], Status: code}
	if len(parts) >= 3 {
		resp.StatusText = strings.Join(parts[2:], " ")
	}
	for _, line := range lines[1:] {
		line = strings.TrimRight(line, "\r")
		if name, value, ok := strings.Cut(line, ":"); ok {
			resp.Headers = append(resp.Headers, mirror.Header{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
		}
	}
	if hasBody {
		resp.Body = []byte(body)
	}
	return resp
}

// cutHTTPBody 头/体分段(空行界,\r\n 与 \n 行尾都认;先按规范 \r\n 找)。
func cutHTTPBody(s string) (head, body string, hasBody bool) {
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[:i], s[i+4:], true
	}
	if i := strings.Index(s, "\n\n"); i >= 0 {
		return s[:i], s[i+2:], true
	}
	return s, "", false
}

// nextToolExchangeID 组包交换的 ID 形态("ta-" 前缀区别于镜 "ms-")。
func nextToolExchangeID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "ta-err"
	}
	return "ta-" + hex.EncodeToString(b[:])
}

// toolBlobWriter 组包文档的仓库写入侧(窄接口,swarm.BlobStore 满足)。
type toolBlobWriter interface {
	Write(data []byte) (string, error)
}

// writeToolPacket 组包文档入仓库,返回 ReproStep 形态(StepID/UnitID/
// 方法/URL/状态如实;Note 说明原料来源与响应侧还原情况)。
func writeToolPacket(blobs toolBlobWriter, taskID string, ex *mirror.Exchange, round int, stepID, unitID string) (rings.ReproStep, bool) {
	doc, err := json.Marshal(ex)
	if err != nil {
		return rings.ReproStep{}, false
	}
	ref, err := blobs.Write(doc)
	if err != nil {
		return rings.ReproStep{}, false
	}
	st := rings.ReproStep{
		Round: round, StepID: stepID, UnitID: unitID,
		Short: toolStepShort(ex), Desc: toolStepDesc(ex),
		Method: ex.Request.Method, URL: ex.Request.URL,
		BlobRef: ref, ExchangeID: ex.ID, Source: reproToolSource,
	}
	if ex.Response != nil {
		st.Status = ex.Response.Status
	}
	st.Note = "请求包按工具调用记录还原;返回包" + map[bool]string{true: "来自工具输出", false: "未留存"}[ex.Response != nil]
	return st, true
}

// toolStepShort 组包步骤短名确定性兜底(方法+路径;同镜交换口径,定稿
// 时模型改写)。
func toolStepShort(ex *mirror.Exchange) string {
	path := ex.Request.URL
	if u, err := url.Parse(ex.Request.URL); err == nil && u.Host != "" {
		path = u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
	}
	return clipLine(ex.Request.Method+" "+path, 32)
}

// toolStepDesc 组包步骤一句话描述(确定性兜底;同镜交换 reproStepDesc
// 口径,响应缺失如实说"返回未留存")。
func toolStepDesc(ex *mirror.Exchange) string {
	path := ex.Request.URL
	if u, err := url.Parse(ex.Request.URL); err == nil && u.Path != "" {
		path = u.Path
	}
	path = "/" + strings.TrimPrefix(clipLine(path, 90), "/")
	verb := "请求"
	switch ex.Request.Method {
	case "POST", "PUT", "PATCH":
		verb = "提交"
	case "DELETE":
		verb = "请求删除"
	}
	head := verb + " " + path
	if len(ex.Request.Body) > 0 {
		head += fmt.Sprintf("(请求体 %d 字节)", len(ex.Request.Body))
	}
	if ex.Response != nil {
		if len(ex.Response.Body) > 0 {
			return head + "," + fmt.Sprintf("目标返回 %d(%d 字节)", ex.Response.Status, len(ex.Response.Body))
		}
		return head + "," + fmt.Sprintf("目标返回 %d", ex.Response.Status)
	}
	return head + ",返回未留存"
}
