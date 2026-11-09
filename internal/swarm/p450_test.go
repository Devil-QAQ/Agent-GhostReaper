package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-50(问题清单 #73 ①):围栏判定收窄治误伤 ----
// 账本实锤回归:v1.2.0 首日渗透任务 t-0320a9f5 的 10 条 roe_scope
// denied 命令(UA 版本号/XFF 与 --resolve 对照值/Referer/载荷参数/
// 爬虫 UA 声明网址被当目标,种子域名解析 IP 直连被拦)逐条复用,
// 新提取面下全部放行零误拦;真越界样本照拦留痕。

// p450LedgerSamples 账本 10 条误拦命令原文(逐字复用,rev 号对应
// approval_records)。
var p450LedgerSamples = []struct {
	rev int
	cmd string
}{
	// rev 109
	{109, `cd /tmp/t0320a9f5 && U='https://ef854.iujw.cn/app/EYHgFilsje/player/636114'
for ua in "Go-http-client/1.1" "" "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36"; do
  code=$(curl -sS --noproxy '*' -o /tmp/t0320a9f5/ua.html -w '%{http_code} %{size_download} %{redirect_url}' -A "$ua" -m 20 "$U")
  echo "UA=[${ua:0:40}] -> $code"
done
echo "=== XFF"
curl -sS --noproxy '*' -D - -o xff.html -H 'X-Forwarded-For: 127.0.0.1' -m 20 "$U" | head -8; wc -c xff.html
echo "=== h2 direct"
curl -sS --noproxy '*' --http2 -D - -o h2d.html -m 20 "$U" | head -8; wc -c h2d.html`},
	// rev 142
	{142, `cd /var/folders/_h/p627c3494t78h53546h2_jp40000gn/T/tmp.RQ5SIu9t91
U="https://ef854.iujw.cn/app/EYHgFilsje/player/636114"
c(){ desc="$1"; shift; printf "%-28s " "$desc"; curl -sS -o "/tmp/o.bin" -D /tmp/h.txt -w "code=%{http_code} size=%{size_download}\n" "$@" "$U" >/tmp/w.txt 2>/dev/null; cat /tmp/w.txt; grep -i -E "^HTTP/1.1 [0-9]|^location:|^set-cookie:" /tmp/h.txt | sed 's/^/    /'; if [ "$(stat -c %s /tmp/o.bin 2>/dev/null || stat -f %z /tmp/o.bin)" -gt 600 ]; then echo "    [body>600B] title: $(grep -o '<title>[^<]*' /tmp/o.bin | head -1)"; fi; }
c "no UA header" -H "User-Agent:"
c "UA curl/8" -A "curl/8.4.0"
c "UA empty" -A ""
c "XFF 127.0.0.1" -H "X-Forwarded-For: 127.0.0.1"
c "X-Real-IP" -H "X-Real-IP: 127.0.0.1"
c "Sec-Fetch nav" -H "Sec-Fetch-Mode: navigate" -H "Sec-Fetch-Dest: document" -H "Upgrade-Insecure-Requests: 1"
c "Accept */*" -H "Accept: */*"
c "Cookie PHPSESSID" -H "Cookie: PHPSESSID=1"
c "accept-encoding identity" -H "Accept-Encoding: identity"`},
	// rev 202
	{202, `cd /tmp/t0320_s4 && UA='Mozilla/5.0 (Linux; Android 8.0; MIX 2 Build/OPR1.170623.027; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/57.0.2987.132 MQQBrowser/6.2 TBS/044113 Mobile Safari/537.36 MicroMessenger/6.6.7.1321(0x26060739) NetType/WIFI Language/zh_CN'; for i in 1 2 3; do echo "== wx req $i =="; curl -sS -k --max-time 20 -o /dev/null -D - -A "$UA" 'https://ef854.iujw.cn/app/EYHgFilsje/player/636114' -w 'code=%{http_code} redirect=%{redirect_url}\n' | egrep -i 'HTTP/|Location|code=|Set-Cookie|Server'; sleep 1; done; echo '== wx UA + wechat referer =='; curl -sS -k --max-time 20 -o /dev/null -D - -A "$UA" -H 'Referer: https://servicewechat.com/' 'https://ef854.iujw.cn/app/EYHgFilsje/player/636114' -w 'code=%{http_code} redirect=%{redirect_url}\n' | egrep -i 'HTTP/|Location|code='`},
	// rev 242
	{242, `cd work && U='https://ef854.iujw.cn/app/EYHgFilsje/player/636114'; while IFS='|' read -r name ua; do code=$(curl -sS -k -o /tmp/ua.html -w '%{http_code}' --max-time 20 -A "$ua" "$U"); loc=$(curl -sS -k -D - -o /dev/null --max-time 20 -A "$ua" "$U" | grep -i '^location' | tr -d '\r'); t=$(grep -o '<title>[^<]*</title>' /tmp/ua.html | head -1); echo "[$name] $code $t | $loc"; done <<'EOF'
Baidu|Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)
Google|Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)
Sogou|Sogou web spider/4.0(+http://www.sogou.com/docs/help/webmasters.htm#07)
360|Mozilla/5.0 (compatible; MSIE 9.0; Windows NT 6.1; Trident/5.0); 360Spider
Yisou|Mozilla/5.0 (compatible; YisouSpider; +http://www.yisou.com/)
Wechat|Mozilla/5.0 (iPhone; CPU iPhone OS 14_0 like Mac OS X) AppleWebKit/605.1.15 MicroMessenger/8.0.20
QQ|Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 Mobile Safari/537.36 V1_AND_SQ_8.8 QQ/8.8
EOF`},
	// rev 275
	{275, `timeout 25 openssl s_client -connect 220.167.100.103:443 -servername ef854.iujw.cn -showcerts </dev/null 2>&1 | head -60`},
	// rev 349
	{349, `for p in "url=https://attacker.example.org/x" "redirect=https://attacker.example.org/x" "redirect_url=https://attacker.example.org/x" "next=https://attacker.example.org/x"; do
  echo "=== param: $p"
  for i in 1 2; do
    curl -sS -m 20 -o /dev/null -D - "https://ef854.iujw.cn/app/EYHgFilsje/player/636114?$p" 2>&1 | grep -iE '^(HTTP/|Location:|Server:|X-Cache|Via|X-Served)' | tr -d '\r'
    echo '---'
  done
done`},
	// rev 512
	{512, `cd /tmp && U='https://ef854.iujw.cn/app/EYHgFilsje/player/636114'
echo "== no UA =="; curl -sS --path-as-is -m 15 -A '' -o /dev/null -w 'code=%{http_code} size=%{size_download}\n' "$U"
echo "== curl UA =="; curl -sS --path-as-is -m 15 -A 'curl/8.5.0' -o /dev/null -w 'code=%{http_code} size=%{size_download}\n' "$U"
echo "== desktop chrome UA =="; curl -sS --path-as-is -m 15 -A 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36' -o /tmp/ua.body -w 'code=%{http_code} size=%{size_download}\n' "$U"; head -c 160 /tmp/ua.body | tr '\n' ' '; echo
echo "== iPhone Safari UA =="; curl -sS --path-as-is -m 15 -A 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1' -o /dev/null -w 'code=%{http_code} size=%{size_download}\n' "$U"
echo "== no UA + Accept html =="; curl -sS --path-as-is -m 15 -A '' -H 'Accept: text/html,application/xhtml+xml' -o /dev/null -w 'code=%{http_code} size=%{size_download}\n' "$U"`},
	// rev 1304
	{1304, `echo '--- direct no-proxy root ---'; curl -sv --noproxy '*' --max-time 12 http://ef854.iujw.cn:1234/ 2>&1 | sed -n '1,40p'; echo '--- direct no-proxy getsheet ---'; curl -sv --noproxy '*' --max-time 12 http://ef854.iujw.cn:1234/app/getsheet 2>&1 | sed -n '1,40p'; echo '--- resolve ---'; (python3 -c "import socket;print(socket.getaddrinfo('ef854.iujw.cn',1234,proto=socket.IPPROTO_TCP))" 2>&1 | head -5); echo '--- tcp direct /dev/tcp ---'; timeout 8 bash -c 'exec 3<>/dev/tcp/121.11.204.242/1234 && echo OPEN' 2>&1; echo 'rc='$?`},
	// rev 1914
	{1914, `cd /tmp/ua_probe && probe(){ label="$1"; ua="$2"; code=$(curl -sS -o "r_$label.html" -D "r_$label.hdr" --max-time 20 -A "$ua" -w '%{http_code} %{size_download}' https://ef854.iujw.cn/app/getsheet); echo "[$label] $code"; }; \
probe wx_ios 'Mozilla/5.0 (iPhone; CPU iPhone OS 14_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.20(0x1800142c) NetType/WIFI Language/zh_CN'; \
probe wx_android_new 'Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/107.0.0.0 Mobile Safari/537.36 MicroMessenger/8.0.42.2460(0x28002A35) WeChat/arm64 Weixin Android Tablet NetType/WIFI Language/zh_CN'; \
probe wework 'Mozilla/5.0 (Linux; Android 11; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/86.0.4240.99 Mobile Safari/537.36 wxwork/4.1.0 MicroMessenger/7.0.1 NetType/WIFI Language/zh_CN'; \
probe wx_desktop 'Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/53.0.2785.116 Safari/537.36 QBCore/4.0.1301.400 QQBrowser/9.0.2524.400 MicroMessenger/6.7.0'; \
probe emptyua ''; \
probe curlua 'curl/8.5.0'; \
for f in r_*.html; do python3 -c "
import re,sys,hashlib
p='$f'
s=open(p,'rb').read()
s=re.sub(rb'(name=\"_token\" content=\")[^\"]*(\")',rb'\1TOKEN\2',s)
s=re.sub(rb'(name=\"_timestamp\" content=\")[^\"]*(\")',rb'\1TS\2',s)
print(p,len(s),hashlib.sha256(s).hexdigest()[:16])
"; done; echo '--- 200-header set of wx_ios ---'; cat r_wx_ios.hdr`},
	// rev 4845
	{4845, `cd /tmp/p80cmp
for port in 80 443; do
  scheme=http; [ $port = 443 ] && scheme=https
  echo "######## PORT $port ########"
  echo "--- bad Host ---"
  curl -sS -o /dev/null -D - -m 15 -H 'Host: nonexistent-host-zzz.invalid' "$scheme://127.0.0.1/" --resolve ef854.iujw.cn:$port:127.0.0.1 2>&1 | head -5
  echo "--- GET / with Host: nonexistent via target ---"
  curl -sS -i -m 15 -H 'Host: nonexistent-host-zzz.invalid' "$scheme://ef854.iujw.cn/" 2>&1 | head -14
done`},
}

// p450Env 渗透任务(种子 ef854.iujw.cn)+ 执行器/单位环境,DNS 打桩:
// 种子域名解析 IP 集 = 账本当日两笔解析(220.167.100.103 与
// 121.11.204.242)。
func p450Env(t *testing.T) (*rings.Store, *Executor, *unitEnv, *TaskPack) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p450-task"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "ef854.iujw.cn",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "UA 与缓存对比探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
	); err != nil {
		t.Fatal(err)
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	env.scopeResolve = func(host string) []string {
		if strings.EqualFold(host, "ef854.iujw.cn") {
			return []string{"220.167.100.103", "121.11.204.242"}
		}
		return nil
	}
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	return store, ex, env, pack
}

// TestP450LedgerSamplesNoFalsePositive 完成标准①:账本实锤 10 条误拦
// 命令全部放行(零拒绝零留痕)。
func TestP450LedgerSamplesNoFalsePositive(t *testing.T) {
	store, _, env, pack := p450Env(t)
	for _, s := range p450LedgerSamples {
		input, err := json.Marshal(map[string]any{"command": s.cmd})
		if err != nil {
			t.Fatal(err)
		}
		if reason := env.denyScope(pack, "bash", input); reason != "" {
			t.Errorf("rev %d 账本实锤命令应为误拦修复后放行,得拒绝: %s", s.rev, reason)
		}
	}
	if recs := p444DenyRecords(t, store, env.TaskID); len(recs) != 0 {
		t.Fatalf("10 条样本零误拦应零留痕,得 %d 笔 denied", len(recs))
	}
}

// TestP450RealOutOfScopeDenied 完成标准①后半:真越界样本(命令目标为
// 闭包外 host/IP)照拦——P4-51 ①起语义为拦下出待决卡＋单位冻结收场
// 让位(留痕 scope_wait 卡,不熔断不 denied);理由人话含"等待批准"。
func TestP450RealOutOfScopeDenied(t *testing.T) {
	store, _, env, pack := p450Env(t)
	denied := []struct {
		name string
		cmd  string
	}{
		{"字面 URL 越界", `curl -s http://evil.example.net/x`},
		{"Host 头越界", `curl -s -H 'Host: evil.example.net' http://ef854.iujw.cn/`},
		{"openssl -connect 越界", `timeout 25 openssl s_client -connect 9.9.9.9:443 -servername ef854.iujw.cn`},
		{"openssl -servername 越界", `openssl s_client -connect 220.167.100.103:443 -servername evil.example.net`},
		{"nmap 越界", `nmap -p 80,443 9.9.9.9`},
		{"nc 越界", `nc -z -w 3 9.9.9.9 443`},
		{"/dev/tcp 越界", `timeout 8 bash -c 'exec 3<>/dev/tcp/9.9.9.9/80 && echo OPEN'`},
		{"--resolve 目标侧越界", `curl --resolve evil.example.net:443:127.0.0.1 https://ef854.iujw.cn/`},
		{"解析 IP 之外的直连", `curl -s http://220.167.100.104/`},
	}
	for i, d := range denied {
		env.deniedReason = ""
		env.scopeHold = nil
		input, _ := json.Marshal(map[string]any{"command": d.cmd})
		reason := env.denyScope(pack, "bash", input)
		if !strings.Contains(reason, "等待批准") {
			t.Errorf("%s 应拦下等待批准(%s)", d.name, reason)
			continue
		}
		if env.deniedReason != "" {
			t.Errorf("%s 等待批准不是拒绝,不得熔断: %q", d.name, env.deniedReason)
		}
		if env.scopeHold == nil {
			t.Errorf("%s 应置位冻结目标", d.name)
		}
		if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) == 0 {
			t.Fatalf("第 %d 条 %s 拦截后应已有待决卡", i, d.name)
		}
	}
	// 九次拦截去重后恰 6 张卡(同一目标一张:evil.example.net 裸域两次
	// 共卡、9.9.9.9:443 两次共卡、9.9.9.9:80 两次共卡)。
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 6 {
		t.Fatalf("九次拦截应去重为 6 张卡(同一目标一张),得 %d: %+v", len(cards), cards)
	}
	// 不出 denied 记录(冻结不是拒绝;存量 denied 语义留给已排除目标)。
	if recs := p444DenyRecords(t, store, env.TaskID); len(recs) != 0 {
		t.Fatalf("真越界拦截应零 denied(走待决卡),得 %d 笔", len(recs))
	}
}

// TestP450ExtractionSurface 收窄面单元断言:UA 串/头值/载荷/保留 TLD/
// heredoc 数据/代理地址一概不当目标;实际连接位置照提取。
func TestP450ExtractionSurface(t *testing.T) {
	noTarget := []struct {
		name string
		cmd  string
	}{
		{"UA 版本号", `curl -A "Mozilla/5.0 (Windows NT 10.0) Chrome/120.0.0.0 Safari/537.36" http://ef854.iujw.cn/`},
		{"XFF 头值", `curl -H 'X-Forwarded-For: 127.0.0.1' http://ef854.iujw.cn/`},
		{"Referer 头值", `curl -H 'Referer: https://servicewechat.com/' http://ef854.iujw.cn/`},
		{"载荷参数值", `curl -d "url=https://attacker.example.org/x" http://ef854.iujw.cn/`},
		{"URL query 载荷", `curl 'http://ef854.iujw.cn/redir?next=https://evil.example.net/'`},
		{"保留 TLD 假 Host", `curl -H 'Host: nonexistent-host-zzz.invalid' http://ef854.iujw.cn/`},
		{"heredoc 数据", "cat <<'EOF'\nBaidu|Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/)\nEOF"},
		{"代理地址", `curl -x http://127.0.0.1:7890 http://ef854.iujw.cn/`},
		{"nc 监听模式", `nc -l -p 8080`},
		{"本地命令", `ls -la /tmp`},
	}
	for _, c := range noTarget {
		ts := scopeCallTargets("bash", json.RawMessage(mustJSON(t, c.cmd)))
		for _, tp := range ts {
			if tp.host != "ef854.iujw.cn" {
				t.Errorf("%s: 不应提取范围外目标 %q (全部: %+v)", c.name, tp.host, ts)
			}
		}
	}
	extract := []struct {
		name string
		cmd  string
		host string
		port string
	}{
		{"curl 目标 URL 默认端口", `curl http://ef854.iujw.cn/app`, "ef854.iujw.cn", "80"},
		{"curl https 默认端口", `curl https://ef854.iujw.cn/app`, "ef854.iujw.cn", "443"},
		{"Host 头值", `curl -H 'Host: ef854.iujw.cn:8443' http://220.167.100.103/`, "ef854.iujw.cn", "8443"},
		{"--resolve 目标侧", `curl --resolve ef854.iujw.cn:1234:127.0.0.1 http://ef854.iujw.cn/`, "ef854.iujw.cn", "1234"},
		{"openssl -connect", `openssl s_client -connect 220.167.100.103:443`, "220.167.100.103", "443"},
		{"openssl -servername", `openssl s_client -connect 220.167.100.103:443 -servername ef854.iujw.cn`, "ef854.iujw.cn", ""},
		{"/dev/tcp 形态", `exec 3<>/dev/tcp/121.11.204.242/1234`, "121.11.204.242", "1234"},
		{"nc 位置参数", `nc -w 3 ef854.iujw.cn 1234`, "ef854.iujw.cn", "1234"},
		{"nmap 目标参数位", `nmap -sV --script=http-enum ef854.iujw.cn`, "ef854.iujw.cn", ""},
	}
	for _, c := range extract {
		ts := scopeCallTargets("bash", json.RawMessage(mustJSON(t, c.cmd)))
		found := false
		for _, tp := range ts {
			if tp.host == c.host && tp.port == c.port {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: 应提取 %s:%s (得 %+v)", c.name, c.host, c.port, ts)
		}
	}
}

func mustJSON(t *testing.T, cmd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
