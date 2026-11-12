package swarm

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---- P4-66(问题清单 #86 A①,RoE 机制重构批次第一层):围栏判定面补齐
// ——确定性豁免层 ----
// 本机环回(127.0.0.0/8 全段、::1、localhost)与本机网络地址上的提取
// 目标不进围栏判定(视同非目标,放行零卡);命令中的代理位置(-x/
// --proxy 值、代理环境变量赋值与引用、--resolve 对照侧)不算目标
//(P4-50 提取面口径,类级钉住);范围外真实 host 照拦照卡——豁免不
// 扩大到真目标。环境复用 p450Env(种子 ef854.iujw.cn,DNS 打桩),本机
// 地址集打桩为 192.168.31.232 / fd00::1(不依赖运行机器的真实接口,
// 判定确定性)。

// TestP466LoopbackExemptClass 完成标准①:环回类级样本各形态放行且零卡
// (127.0.0.1:任意端口 / 127.0.0.0/8 段内非首位 / localhost / [::1] /
// 本机 IP;探查设施组合命令实锤形态回归)。
func TestP466LoopbackExemptClass(t *testing.T) {
	store, _, env, pack := p450Env(t)
	env.scopeLocalAddrs = func() map[string]bool {
		return map[string]bool{"192.168.31.232": true, "fd00::1": true}
	}
	cases := []struct {
		name string
		tool string
		args string
	}{
		{"curl 环回镜端口(实锤形态)", "bash", `{"command":"curl -sv --max-time 5 http://127.0.0.1:60821/ 2>&1 | head -20"}`},
		{"127 段非首位", "bash", `{"command":"curl -s http://127.0.0.2:60822/"}`},
		{"localhost 域名", "bash", `{"command":"curl -s -o /dev/null -w '%{http_code}\n' http://localhost:60823/"}`},
		{"[::1] 环回", "bash", `{"command":"curl -s 'http://[::1]:60824/'"}`},
		{"/dev/tcp 环回", "bash", `{"command":"timeout 5 bash -c 'exec 3<>/dev/tcp/127.0.0.1/60825 && echo OPEN'"}`},
		{"nc 环回", "bash", `{"command":"nc -z -w 2 127.0.0.1 60826"}`},
		{"Host 头环回值", "bash", `{"command":"curl -s -H 'Host: localhost:60827' http://ef854.iujw.cn/"}`},
		{"openssl 环回对照", "bash", `{"command":"timeout 8 openssl s_client -connect 127.0.0.1:60828 -servername localhost </dev/null 2>&1 | head -10"}`},
		{"本机 IPv4(打桩)", "bash", `{"command":"curl -s http://192.168.31.232:9921/"}`},
		{"本机 IPv6(打桩)", "bash", `{"command":"curl -s 'http://[fd00::1]:9922/'"}`},
		{"nmap 本机 IP(打桩)", "bash", `{"command":"nmap -p 80,443 192.168.31.232"}`},
		{"http_probe 环回参数", "http_probe", `{"url":"http://localhost:60830/"}`},
		{"port_probe 环回参数", "port_probe", `{"host":"127.0.0.1","port":60831}`},
		{"探查设施组合命令(实锤形态)", "bash", `{"command":"echo '--- curl 本机代理端口 ---'; curl -sv --max-time 5 http://127.0.0.1:60821/ 2>&1 | head -20; echo '--- 监听端口 ---'; (lsof -iTCP -sTCP:LISTEN -n -P 2>/dev/null || ss -ltnp) | head -30; echo '--- 环回连通 ---'; curl -s -o /dev/null -w '%{http_code}\\n' http://localhost:60832/"}`},
	}
	for _, c := range cases {
		env.scopeHold = nil
		env.scopeReclaim = false
		if reason := env.denyScope(pack, c.tool, json.RawMessage(c.args)); reason != "" {
			t.Errorf("%s: 应确定性豁免放行,得拦截: %s", c.name, reason)
		}
		if env.scopeHold != nil {
			t.Errorf("%s: 豁免放行不得置位冻结(得 %+v)", c.name, env.scopeHold)
		}
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("环回类级样本应零卡,得 %d 张: %+v", len(cards), cards)
	}
	if recs := p444DenyRecords(t, store, env.TaskID); len(recs) != 0 {
		t.Fatalf("环回类级样本应零拒绝留痕,得 %d 笔 denied", len(recs))
	}
}

// TestP466ProxyPositionNotTarget 完成标准②:代理位类级样本各形态不算
// 目标——提取面不含代理地址(位置语义,-x 值对非环回代理同样不算);
// 豁免放行零卡。
func TestP466ProxyPositionNotTarget(t *testing.T) {
	store, _, env, pack := p450Env(t)
	cases := []struct {
		name string
		cmd  string
	}{
		{"-x 环回代理", `curl -x http://127.0.0.1:8080 http://ef854.iujw.cn/`},
		{"-x 非环回代理(位置语义)", `curl -x http://proxy.example.net:8080 http://ef854.iujw.cn/`},
		{"-x 裸地址形态", `curl -x 127.0.0.1:8080 http://ef854.iujw.cn/`},
		{"--proxy 分离值", `curl --proxy http://127.0.0.1:8080 http://ef854.iujw.cn/`},
		{"--proxy 内联值", `curl --proxy=http://127.0.0.1:8080 http://ef854.iujw.cn/`},
		{"wget -e 代理变量", `wget -e http_proxy=http://127.0.0.1:8080 http://ef854.iujw.cn/`},
		{"env 赋值小写", `export https_proxy=http://127.0.0.1:8080; curl -s http://ef854.iujw.cn/`},
		{"env 赋值大写", `export HTTPS_PROXY=http://127.0.0.1:8080 && export ALL_PROXY=socks5://127.0.0.1:1080 && curl -s http://ef854.iujw.cn/`},
		{"env 前缀赋值", `http_proxy=http://127.0.0.1:8080 curl -s http://ef854.iujw.cn/`},
		{"env 引用", `echo "proxy=$https_proxy"; curl -s http://ef854.iujw.cn/`},
		{"env 引用经 -x", `curl -x "$https_proxy" http://ef854.iujw.cn/`},
	}
	for _, c := range cases {
		ts := scopeCallTargets("bash", json.RawMessage(mustJSON(t, c.cmd)))
		for _, tp := range ts {
			if strings.Contains(tp.host, "proxy") || tp.host == "127.0.0.1" {
				t.Errorf("%s: 代理位置不得成目标,提取到 %q (全部: %+v)", c.name, tp.host, ts)
			}
		}
		env.scopeHold = nil
		if reason := env.denyScope(pack, "bash", json.RawMessage(mustJSON(t, c.cmd))); reason != "" {
			t.Errorf("%s: 代理位形态应放行,得拦截: %s", c.name, reason)
		}
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("代理位类级样本应零卡,得 %d 张: %+v", len(cards), cards)
	}
}

// TestP466ResolveBidirectional 完成标准②后半:--resolve 双向口径——
// 对照侧 IP(解析中转)不算目标不许拦;目标侧照常判定,真目标不许
// 借对照侧溜过。
func TestP466ResolveBidirectional(t *testing.T) {
	store, _, env, pack := p450Env(t)
	// 对照侧不算目标:提取面只含目标侧 host,对照 IP(任意地址形态)
	// 一概不提取、不判定、不拦。
	for _, addr := range []string{"9.9.9.9", "127.0.0.1", "1.2.3.4"} {
		cmd := `curl --resolve ef854.iujw.cn:1234:` + addr + ` http://ef854.iujw.cn/`
		ts := scopeCallTargets("bash", json.RawMessage(mustJSON(t, cmd)))
		for _, tp := range ts {
			if tp.host == addr {
				t.Errorf("--resolve 对照侧 %s 不得成目标(全部: %+v)", addr, ts)
			}
		}
		if reason := env.denyScope(pack, "bash", json.RawMessage(mustJSON(t, cmd))); reason != "" {
			t.Errorf("对照侧 %s 不许拦(目标侧在范围内): %s", addr, reason)
		}
	}
	// 目标侧照算:目标侧在范围外时,对照侧是环回(豁免形态)也不救——
	// 真目标不许借道溜过。
	env.scopeHold = nil
	reason := env.denyScope(pack, "bash", json.RawMessage(
		mustJSON(t, `curl --resolve evil.example.net:443:127.0.0.1 https://evil.example.net/`)))
	if !strings.Contains(reason, "等待批准") {
		t.Fatalf("目标侧范围外应照拦(对照侧环回不救),得: %s", reason)
	}
	if env.scopeHold == nil || env.scopeHold.host != "evil.example.net" {
		t.Fatalf("应冻结真目标 evil.example.net,得 %+v", env.scopeHold)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 1 ||
		cards[0].RequestID != "sc-evil.example.net:443" {
		t.Fatalf("应恰一张真目标卡(sc-evil.example.net:443),得: %+v", cards)
	}
}

// TestP466RealTargetStillBlocked 完成标准③:真越界回归——命令含范围外
// 真实 host 照拦照卡(环回/代理位/本机 IP 豁免不扩大到同命令里的真目标)。
func TestP466RealTargetStillBlocked(t *testing.T) {
	store, _, env, pack := p450Env(t)
	env.scopeLocalAddrs = func() map[string]bool {
		return map[string]bool{"192.168.31.232": true}
	}
	cases := []struct {
		name string
		cmd  string
		host string
	}{
		{"环回与真目标同命令", `curl -s http://127.0.0.1:60821/ && curl -s http://evil.example.net/x`, "evil.example.net"},
		{"经代理访问真目标", `curl -x http://127.0.0.1:8080 http://evil.example.net/x`, "evil.example.net"},
		{"nmap 环回与真目标混扫", `nmap -p 80 localhost evil.example.net`, "evil.example.net"},
		{"本机 IP 豁免与真目标", `curl -s http://192.168.31.232:9921/ ; curl -s http://evil.example.net/x`, "evil.example.net"},
	}
	for _, c := range cases {
		env.scopeHold = nil
		reason := env.denyScope(pack, "bash", json.RawMessage(mustJSON(t, c.cmd)))
		if !strings.Contains(reason, "等待批准") {
			t.Errorf("%s: 真目标应照拦照卡,得: %s", c.name, reason)
			continue
		}
		if env.scopeHold == nil || env.scopeHold.host != c.host {
			t.Errorf("%s: 应冻结真目标 %s,得 %+v", c.name, c.host, env.scopeHold)
		}
	}
	// 四次拦截同一目标一张卡(去重),且卡只属真目标。
	cards := p451ScopeWaitCards(t, store, env.TaskID)
	if len(cards) != 1 || cards[0].RequestID != "sc-evil.example.net:80" {
		t.Fatalf("四条混扫命令应去重为一张真目标卡(sc-evil.example.net:80),得: %+v", cards)
	}
}

// TestP467ScanNumericValueOptionNotTarget P4-67 活体实锤回归:nmap
// --top-ports N 的数值 N 是端口数不是主机——不得提取为目标(实锤
// 形态 nmap --top-ports 100 github.com 曾把 100 当目标出卡)。
func TestP467ScanNumericValueOptionNotTarget(t *testing.T) {
	for _, cmd := range []string{
		"nmap --top-ports 100 github.com",
		"nmap --top-ports 100 -Pn github.com",
		"masscan --top-ports 100 10.0.0.9",
		"nmap --max-hostgroup 64 10.0.0.9",
	} {
		ts := scopeCallTargets("bash", json.RawMessage(mustJSON(t, cmd)))
		for _, tp := range ts {
			if isAllDigits(tp.host) {
				t.Errorf("%s: 数值参数不得提取为目标,得 %q (全部: %+v)", cmd, tp.host, ts)
			}
		}
	}
	// 目标侧照提。
	ts := scopeCallTargets("bash", json.RawMessage(mustJSON(t, "nmap --top-ports 100 github.com")))
	if len(ts) != 1 || ts[0].host != "github.com" {
		t.Fatalf("目标侧应照提 github.com: %+v", ts)
	}
}
