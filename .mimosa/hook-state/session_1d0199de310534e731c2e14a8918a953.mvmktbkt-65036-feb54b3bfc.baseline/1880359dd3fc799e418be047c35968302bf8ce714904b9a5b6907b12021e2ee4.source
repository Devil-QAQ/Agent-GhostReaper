// Package server 的错误人话化测试(P3-11a ②,8.5 第七条):writeErr
// 是 API 错误单一流出口,内部模块名前缀在边界剥除——正则单元 + HTTP
// 端到端双层验证;无前缀的人话错误原样透传。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	httptest "net/http/httptest"
	"strings"
	"testing"
)

// TestHumanErrStripsModulePrefix 正则单元:单前缀、嵌套包装(链式
// fmt.Errorf %w 的典型形态)、非前缀文本不误伤、极端全前缀退原文。
func TestHumanErrStripsModulePrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 子计划典型样本:failover: 前缀不上界面。
		{`failover: 模型名"glm5.3"含非法字符`, `模型名"glm5.3"含非法字符`},
		// 嵌套包装:外层 swarm + 内层 rings(经 %w 带入)。
		{"swarm: 舵杆指向步骤 s1: rings: 节点 s1 不存在", "舵杆指向步骤 s1: 节点 s1 不存在"},
		// 相邻双前缀(单轮替换吃不到第二段,须循环剥净)。
		{"rings: swarm: 节点 s1 不存在", "节点 s1 不存在"},
		// 无前缀的人话错误:逐字原样。
		{"该名称已存在", "该名称已存在"},
		{"任务 t-x 不存在", "任务 t-x 不存在"},
		// 英文库错误(网络类)不属内部模块名,保留事实原文。
		{`测试失败: Post "https://api.example.com/v1": dial tcp: connection refused`,
			`测试失败: Post "https://api.example.com/v1": dial tcp: connection refused`},
	}
	for _, c := range cases {
		if got := humanErr(errors.New(c.in)); got != c.want {
			t.Fatalf("humanErr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWriteErrHumanizesOnWire HTTP 端到端:错误响应体里的 error 字段
// 不含内部模块名前缀,人话原因保留(toast 直接展示的就是这个字段)。
func TestWriteErrHumanizesOnWire(t *testing.T) {
	inner := errors.New("rings: 节点 s9 不存在")
	wrapped := fmt.Errorf("swarm: 重开受阻步骤: %w", inner)
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusBadRequest, wrapped)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码应保持: %d", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	for _, pkg := range []string{"swarm:", "rings:", "failover:", "overmind:"} {
		if strings.Contains(body.Error, pkg) {
			t.Fatalf("界面错误不得含内部模块名 %q: %q", pkg, body.Error)
		}
	}
	if body.Error != "重开受阻步骤: 节点 s9 不存在" {
		t.Fatalf("人话原因应保留: %q", body.Error)
	}
}
