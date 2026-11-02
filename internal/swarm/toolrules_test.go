package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/tool"
)

// ---- §11 工具描述即规则:描述、参数描述、报错文案逐条对照核对 ----

// toolrulesEnv 枏述/报错核对用的单位环境(不入账,只建工具)。
func toolrulesEnv(t *testing.T) *unitEnv {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	return &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})}
}

// schemaText 工具参数 Schema 的字符串形态(断言参数描述用)。
func schemaText(t *testing.T, name string, tt tool.CoreTool) string {
	t.Helper()
	b, err := json.Marshal(tt.InputSchema())
	if err != nil {
		t.Fatalf("编码 %s schema 失败: %v", name, err)
	}
	return string(b)
}

// resultText 工具结果的文本形态。
func resultText(res tool.Result) string {
	var b strings.Builder
	for _, blk := range res.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// TestToolDescriptionsEmbedRules 完成标准④:工具描述逐条对照 §11——
// ①低价值面在描述内直接压制(banner 截短/响应体截取边界,"勿据此
// 认为已测完");②参数描述埋数据质量要求(证据须含可独立复核原文,
// 只写结论不算);③边界与上限写明(bash 限时默认/上限/超时转后台、
// 只读方法面)。
func TestToolDescriptionsEmbedRules(t *testing.T) {
	env := toolrulesEnv(t)

	if d := env.portProbeTool().Description(); !strings.Contains(d, "勿据此认为已测完") {
		t.Fatalf("port_probe 描述须压制低价值面(banner 截短/静默): %q", d)
	}
	if d := env.httpProbeTool().Description(); !strings.Contains(d, "勿据此认为已读全") {
		t.Fatalf("http_probe 描述须写明响应体截取边界: %q", d)
	}
	if d := env.bashTool().Description(); !strings.Contains(d, "默认限时 120 秒") ||
		!strings.Contains(d, "上限 600 秒") || !strings.Contains(d, "自动转入后台") {
		t.Fatalf("bash 描述须写明限时默认/上限与超时转后台: %q", d)
	}

	// 参数描述埋数据质量要求(§11:证据类参数必须教什么叫合格证据)。
	for name, want := range map[string][]string{
		"submit_candidate_fact":    {"可独立复核", "只写结论不附原文"},
		"submit_test_claim":        {"可独立重放", "只写结论不算证据"},
		"submit_falsify_result":    {"只写结论不参与机器判定"},
		"submit_hypothesis_result": {"只写结论不算证据"},
	} {
		var tt tool.CoreTool
		switch name {
		case "submit_candidate_fact":
			tt = env.submitCandidateFactTool()
		case "submit_test_claim":
			tt = env.submitTestClaimTool()
		case "submit_falsify_result":
			tt = env.submitFalsifyResultTool()
		case "submit_hypothesis_result":
			tt = env.submitHypothesisResultTool()
		}
		s := schemaText(t, name, tt)
		for _, w := range want {
			if !strings.Contains(s, w) {
				t.Fatalf("%s 参数描述缺数据质量要求 %q", name, w)
			}
		}
	}
}

// TestToolErrorsTeachRoutes 完成标准④:报错文案写成路由教学——错误不只
// 说"错了",还说"下一步该走哪条路"(§11:模型读错的瞬间读的就是
// 路由规则)。用真实工具调用走错误路径断言。
func TestToolErrorsTeachRoutes(t *testing.T) {
	env := toolrulesEnv(t)
	ctx := context.Background()

	run := func(name string, tt tool.CoreTool, args string) string {
		res, err := tt.Call(ctx, json.RawMessage(args), nil)
		if err != nil {
			t.Fatalf("%s 调用异常: %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s 坏参数应回错误结果", name)
		}
		return resultText(res)
	}

	if msg := run("bash 空命令", env.bashTool(), `{"command":"  "}`); !strings.Contains(msg, "填入要执行的 shell 命令") {
		t.Fatalf("bash 空命令报错须教学: %q", msg)
	}
	if msg := run("port_probe 坏端口", env.portProbeTool(), `{"host":"h","port":0}`); !strings.Contains(msg, "修正参数后重发") {
		t.Fatalf("port_probe 坏参数报错须教学: %q", msg)
	}
	if msg := run("http_probe 坏 URL", env.httpProbeTool(), `{"url":"ftp://x"}`); !strings.Contains(msg, "改用 bash") {
		t.Fatalf("http_probe 坏 URL 报错须指路(本地文件走 bash): %q", msg)
	}
	if msg := run("http_probe 坏方法", env.httpProbeTool(), `{"url":"http://127.0.0.1:1/","method":"POST"}`); !strings.Contains(msg, "走 bash 通道") {
		t.Fatalf("http_probe 坏方法报错须指路(带载荷走 bash): %q", msg)
	}
}

// TestClaimFieldStyleClause P4-11 ①:声明一句话摘要同款收口——
// submit_test_claim 的 statement/impact 参数描述引用共用文风句
// (swarm.StructuredFieldStyle,与主宰拆解契约同一份,防两处漂移)。
func TestClaimFieldStyleClause(t *testing.T) {
	env := toolrulesEnv(t)
	s := schemaText(t, "submit_test_claim", env.submitTestClaimTool())
	if n := strings.Count(s, StructuredFieldStyle); n != 2 {
		t.Fatalf("statement 与 impact 两个参数描述应各带一次共用文风句(实际 %d 次): %s", n, s)
	}
	for _, want := range []string{"存在性判断", "影响面判断"} {
		if !strings.Contains(s, want) {
			t.Fatalf("判断参数描述缺 %q", want)
		}
	}
}
