package server

import (
	"os"
	"strings"
	"testing"
)

// ---- P4-70 ①(问题清单 #87 ①):资产审批卡收两键的面板文案断言 ----
// 三型范围卡按钮区统一"加入资产/禁止测试"两键,卡面说明行明示批的是
// "要不要把它当测试目标"(锚例逐字);单卡与批量的 allow_access 第三键
// 入口清除(前端零调用);服务端单卡决策端点保留由 server_p468_test.go
// 的端点回归承载,批量入口清除由 TestP468BatchAllowAccess 承载。

// TestP470PanelCardTwoKeys 面板源码级断言:零 allow_access 调用(单卡与
// 批量入口同步清除)、两键说明行锚例逐字在、存量 access 记录呈现词与
// RoE 信息源区标记入口保留。
func TestP470PanelCardTwoKeys(t *testing.T) {
	raw, err := os.ReadFile("../../web/views/roe-page.js")
	if err != nil {
		t.Fatalf("读面板源码失败: %v", err)
	}
	src := string(raw)
	// 第三键入口清除:前端零 allow_access 调用(单卡按钮与批量条同步移除)。
	if strings.Contains(src, "allow_access") {
		t.Fatal("面板应零 allow_access 调用(单卡与批量第三键入口已移除)")
	}
	// 说明行锚例逐字(P4-70 ① 契约文案)。
	if !strings.Contains(src, "是否把这个地址纳入测试范围") {
		t.Fatal("资产审批卡说明行锚例缺失:是否把这个地址纳入测试范围")
	}
	if !strings.Contains(src, "是否允许对这个信息源发起攻击动作") {
		t.Fatal("信息源攻击卡说明行锚例缺失:是否允许对这个信息源发起攻击动作")
	}
	// 保留面:存量 access 记录呈现词如实、RoE 信息源区标记入口照常。
	if !strings.Contains(src, `access: "允许访问"`) {
		t.Fatal("存量 access 记录的呈现词应保留")
	}
	if !strings.Contains(src, "允许访问:域名或 IP(可带端口)") {
		t.Fatal("RoE 信息源区标记入口(只授访问权的日常入口)应保留")
	}
}
