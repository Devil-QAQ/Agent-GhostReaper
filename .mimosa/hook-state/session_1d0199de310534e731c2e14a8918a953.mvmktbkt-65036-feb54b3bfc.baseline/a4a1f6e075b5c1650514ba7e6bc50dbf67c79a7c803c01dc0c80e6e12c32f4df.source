package swarm

// P3-09 目标凭据的执行侧调用测试:任务包携带名称/用户名/环境变量名
//(无秘密)、bash 子进程环境注入 GR_CRED_*=秘密、领取路径端到端。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/vault"
)

// seedCredStep 铺垫一个带步骤的任务图(seedGraph 同款事件书写)。
func seedCredStep(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "确认靶标", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "观察", Status: "dispatched"}},
	); err != nil {
		t.Fatalf("铺垫图失败: %v", err)
	}
}

// TestClaimCarriesCredentials 领取路径端到端:凭据库存值后,Claim 返回
// 的任务包自带凭据清单(名称/用户名/环境变量名),渲染呈现且不含秘密。
func TestClaimCarriesCredentials(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	seedCredStep(t, store, "claim-cred")
	if _, err := vault.Save(ctx, store, vault.Credential{
		Name: "dvwa-admin", Username: "admin", Secret: "S3cret-value",
	}, "creator"); err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := e.Claim(ctx, "claim-cred", "s1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(pack.Credentials) != 1 {
		t.Fatalf("任务包应带 1 条凭据: %+v", pack.Credentials)
	}
	c := pack.Credentials[0]
	if c.Name != "dvwa-admin" || c.Username != "admin" || c.EnvVar != "GR_CRED_DVWA_ADMIN" {
		t.Fatalf("凭据条目字段不符: %+v", c)
	}
	rendered := pack.Render()
	for _, want := range []string{"可用凭据", "dvwa-admin", "用户名 admin", "$GR_CRED_DVWA_ADMIN"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("任务包渲染缺 %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "S3cret-value") {
		t.Fatal("任务包渲染不得含秘密明文")
	}
}

// TestClaimNoCredentials 凭据库为空:任务包凭据清单为空,渲染不出现
// 凭据段。
func TestClaimNoCredentials(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedCredStep(t, store, "claim-nocred")
	e := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := e.Claim(context.Background(), "claim-nocred", "s1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(pack.Credentials) != 0 {
		t.Fatalf("空库任务包不应带凭据: %+v", pack.Credentials)
	}
	if strings.Contains(pack.Render(), "可用凭据") {
		t.Fatal("空库渲染不应出现凭据段")
	}
}

// TestBashEnvCredentialInjection bash 子进程环境注入(P3-09 调用面):
// 凭据库有值时命令可经 $GR_CRED_* 引用秘密;库空时零注入(命令不受
// 影响)。
func TestBashEnvCredentialInjection(t *testing.T) {
	env := bgEnv(t)
	ctx := context.Background()
	if _, err := vault.Save(ctx, env.Executor.Store, vault.Credential{
		Name: "dvwa", Username: "admin", Secret: "env-secret-42",
	}, "creator"); err != nil {
		t.Fatal(err)
	}
	res, err := env.bashTool().Call(ctx, json.RawMessage(
		`{"command":"printf 'user=admin pass=%s' \"$GR_CRED_DVWA\""}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	if res.IsError {
		t.Fatalf("命令失败: %s", resultText(res))
	}
	if text := resultText(res); !strings.Contains(text, "pass=env-secret-42") {
		t.Fatalf("bash 环境应注入凭据秘密: %q", text)
	}

	// 空库:变量为空但命令照常执行。
	env2 := bgEnv(t)
	res2, err := env2.bashTool().Call(ctx, json.RawMessage(
		`{"command":"printf 'pass=[%s]' \"${GR_CRED_DVWA:-}\"; echo ok"}`), nil)
	if err != nil || res2.IsError {
		t.Fatalf("空库调用异常: err=%v isErr=%v", err, res2.IsError)
	}
	if text := resultText(res2); !strings.Contains(text, "pass=[]") || !strings.Contains(text, "ok") {
		t.Fatalf("空库不应注入: %q", text)
	}
}
