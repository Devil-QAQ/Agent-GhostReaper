package websearch

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var ddpocFile = regexp.MustCompile(`^DVB-[0-9]{4}-[0-9]+\.html$`)

func ddpocCookie(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("登录 Cookie 须为单行")
	}
	raw = strings.TrimPrefix(raw, "Cookie:")
	value := ""
	for _, part := range strings.Split(raw, ";") {
		name, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name == "web1" {
			value = strings.Trim(strings.TrimSpace(v), `"`)
			break
		}
	}
	if value == "" && !strings.Contains(raw, ";") && !strings.Contains(raw, "web1=") {
		value = strings.Trim(strings.TrimSpace(raw), `"`)
	}
	if value == "" || strings.ContainsAny(value, ";\t \"\\") {
		return "", errors.New("填写有效的 web1 登录 Cookie")
	}
	if err := (&http.Cookie{Name: "web1", Value: value}).Valid(); err != nil {
		return "", errors.New("web1 登录 Cookie 格式无效")
	}
	return value, nil
}

type ddpocClue struct {
	Name     string            `json:"name"`
	ID       string            `json:"id"`
	URL      string            `json:"url"`
	Category string            `json:"category"`
	Fields   map[string]string `json:"fields"`
}

func parseDDPocList(body []byte, base *url.URL, size int) (map[string]any, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("DDPoc 列表无法解析")
	}
	if findNode(root, func(n *html.Node) bool { return n.Data == "input" && attr(n, "type") == "password" }) != nil {
		return nil, errors.New("DDPoc 登录已失效，更新登录 Cookie 后重试")
	}
	table := findNode(root, func(n *html.Node) bool {
		if n.Data != "table" {
			return false
		}
		headers := []string{}
		var collect func(*html.Node)
		collect = func(x *html.Node) {
			if x.Data == "th" {
				headers = append(headers, nodeText(x))
			}
			for c := x.FirstChild; c != nil; c = c.NextSibling {
				collect(c)
			}
		}
		collect(n)
		text := strings.Join(headers, " ")
		return strings.Contains(text, "编号") && strings.Contains(text, "漏洞名称") && strings.Contains(text, "类型")
	})
	if table == nil {
		return nil, errors.New("DDPoc 未返回可识别的漏洞列表，检查登录状态或页面变化")
	}
	rows := []ddpocClue{}
	headers := []string{}
	var headerWalk func(*html.Node)
	headerWalk = func(n *html.Node) {
		if n.Data == "th" {
			headers = append(headers, nodeText(n))
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			headerWalk(c)
		}
	}
	headerWalk(table)
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Data == "tr" {
			cells := []*html.Node{}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Data == "td" {
					cells = append(cells, c)
				}
			}
			if len(cells) >= 3 && len(rows) < size {
				a := findNode(cells[1], func(x *html.Node) bool { return x.Data == "a" })
				if a != nil {
					u, e := url.Parse(attr(a, "href"))
					if e == nil {
						u = base.ResolveReference(u)
						parts := strings.Split(u.Path, "/")
						file := parts[len(parts)-1]
						expected := strings.TrimSuffix(base.Path, "list.html") + file
						if u.Scheme == base.Scheme && u.Host == base.Host && u.Path == expected && ddpocFile.MatchString(file) && u.RawQuery == "" && !seen[file] {
							seen[file] = true
							fields := map[string]string{}
							for i, cell := range cells {
								if i < len(headers) {
									fields[headers[i]] = nodeText(cell)
								}
							}
							rows = append(rows, ddpocClue{Name: nodeText(a), ID: strings.TrimSuffix(file, ".html"), URL: u.String(), Category: nodeText(cells[2]), Fields: fields})
						}
					}
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(table)
	more := false
	current := base.Query().Get("page")
	if current == "" {
		current = "1"
	}
	var pageLinks func(*html.Node)
	pageLinks = func(n *html.Node) {
		if n.Data == "a" {
			if u, e := url.Parse(attr(n, "href")); e == nil && u.Query().Get("page") != "" && u.Query().Get("page") != current {
				more = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			pageLinks(c)
		}
	}
	pageLinks(root)
	return map[string]any{"results": rows, "has_other_pages": more, "read_hint": "可用 action=read、path=编号或链接读取登录账号可见的页面"}, nil
}

// Return the article as rendered text; the website decides the account's access.
// No point exchange, unlock, download or script execution is performed.
func parseDDPocPage(body []byte, base *url.URL) (map[string]any, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("DDPoc 页面无法解析")
	}
	if findNode(root, func(n *html.Node) bool { return n.Data == "input" && attr(n, "type") == "password" }) != nil {
		return nil, errors.New("DDPoc 登录已失效，更新登录 Cookie 后重试")
	}
	article := findNode(root, func(n *html.Node) bool { return hasClass(n, "project-details-area") })
	if article == nil {
		return nil, errors.New("DDPoc 未返回可识别的漏洞页面")
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Data == "script" || n.Data == "style" || n.Data == "noscript" || n.Data == "form" || n.Data == "input" || n.Data == "button" {
			return
		}
		for _, a := range n.Attr {
			if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true") {
				return
			}
		}
		style := strings.ReplaceAll(strings.ToLower(attr(n, "style")), " ", "")
		if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") || hasClass(n, "hidden") || hasClass(n, "d-none") {
			return
		}
		block := n.Data == "div" || n.Data == "p" || n.Data == "br" || n.Data == "pre" || n.Data == "li" || n.Data == "h1" || n.Data == "h2" || n.Data == "tr"
		if block {
			b.WriteByte('\n')
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Data == "a" {
			if link, e := url.Parse(attr(n, "href")); e == nil && attr(n, "href") != "" {
				link = base.ResolveReference(link)
				if link.Scheme == "http" || link.Scheme == "https" {
					b.WriteString(" (" + link.String() + ")")
				}
			}
		}

		if block {
			b.WriteByte('\n')
		}
	}
	walk(article)
	lines := []string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			lines = append(lines, s)
		}
	}
	return map[string]any{"url": base.String(), "text": strings.Join(lines, "\n")}, nil
}
