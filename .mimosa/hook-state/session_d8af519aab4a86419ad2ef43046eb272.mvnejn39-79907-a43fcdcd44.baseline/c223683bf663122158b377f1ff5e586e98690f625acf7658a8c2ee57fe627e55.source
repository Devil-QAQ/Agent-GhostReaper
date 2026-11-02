package vault

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/secrets"

	_ "modernc.org/sqlite"
)

func openVaultStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("打开存储: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestVaultSaveListMasked 存取与打码(P3-09 完成标准:凭据可存取,界面
// 打码):存 → 打码视图(秘密不出明文)/ 服务端清单(含秘密,装配用)
// 各归其位;编辑留空秘密保留旧值。
func TestVaultSaveListMasked(t *testing.T) {
	store := openVaultStore(t)
	ctx := context.Background()

	c, err := Save(ctx, store, Credential{
		Name: "dvwa-admin", Username: "admin", Secret: "p4ssw0rd-secret", Note: "DVWA 默认管理员",
	}, "creator")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if c.ID == "" {
		t.Fatal("新建应生成 ID")
	}

	views, err := Views(ctx, store)
	if err != nil || len(views) != 1 {
		t.Fatalf("Views: err=%v len=%d", err, len(views))
	}
	v := views[0]
	if v.Name != "dvwa-admin" || v.Username != "admin" || v.EnvVar != "GR_CRED_DVWA_ADMIN" {
		t.Fatalf("视图字段不符: %+v", v)
	}
	if !v.HasSecret || v.SecretMask == "p4ssw0rd-secret" || v.SecretMask == "" {
		t.Fatalf("秘密应打码呈现: %q", v.SecretMask)
	}
	if v.UpdatedBy != "creator" || v.UpdatedAt == "" {
		t.Fatalf("审计字段缺失: %+v", v)
	}

	// 服务端清单含秘密(执行侧装配用)。
	creds, err := List(ctx, store)
	if err != nil || len(creds) != 1 || creds[0].Secret != "p4ssw0rd-secret" {
		t.Fatalf("List(服务端): err=%v creds=%+v", err, creds)
	}

	// 编辑:秘密留空 = 保留旧值;名称与说明可改。
	if _, err := Save(ctx, store, Credential{
		ID: c.ID, Name: "dvwa-admin", Username: "admin", Secret: "", Note: "改说明",
	}, "creator"); err != nil {
		t.Fatalf("编辑: %v", err)
	}
	after, ok, err := ByID(ctx, store, c.ID)
	if err != nil || !ok {
		t.Fatalf("ByID: ok=%v err=%v", ok, err)
	}
	if after.Secret != "p4ssw0rd-secret" || after.Note != "改说明" {
		t.Fatalf("编辑后不符(秘密应保留): %+v", after)
	}

	// 删除幂等。
	if err := Delete(ctx, store, c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := Delete(ctx, store, c.ID); err != nil {
		t.Fatalf("幂等 Delete: %v", err)
	}
	if views, _ := Views(ctx, store); len(views) != 0 {
		t.Fatalf("删除后应为空: %+v", views)
	}
}

// TestVaultNameUniqueness 名称全库唯一;纯符号名不可用;新建缺秘密拒。
func TestVaultNameUniqueness(t *testing.T) {
	store := openVaultStore(t)
	ctx := context.Background()
	if _, err := Save(ctx, store, Credential{Name: "dvwa", Secret: "s1"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(ctx, store, Credential{Name: "dvwa", Secret: "s2"}, "creator"); err == nil {
		t.Fatal("重名应拒")
	}
	if _, err := Save(ctx, store, Credential{Name: " --- ", Secret: "s3"}, "creator"); err == nil {
		t.Fatal("纯符号名应拒(归一后无有效调用名)")
	}
	if _, err := Save(ctx, store, Credential{Name: "no-secret"}, "creator"); err == nil {
		t.Fatal("新建缺秘密应拒")
	}
	if _, err := Save(ctx, store, Credential{ID: "c-none", Name: "x", Secret: "s"}, "creator"); err == nil {
		t.Fatal("编辑不存在的 ID 应拒")
	}
}

// TestEnvName 名称归一:大小写折叠、非法字符折叠为单 _、去首尾 _。
func TestEnvName(t *testing.T) {
	cases := map[string]string{
		"dvwa-admin":  "GR_CRED_DVWA_ADMIN",
		"DVWA admin":  "GR_CRED_DVWA_ADMIN",
		"靶场管理员":       "",
		"靶场-管理员2":     "GR_CRED_2",
		"web_api.key": "GR_CRED_WEB_API_KEY",
		"__x__":       "GR_CRED_X",
		"a---b":       "GR_CRED_A_B",
	}
	for name, want := range cases {
		got := EnvName(name)
		if want == "" {
			if envUsable(name) {
				t.Errorf("纯符号名 %q 不应可用(得 %q)", name, got)
			}
			continue
		}
		if got != want {
			t.Errorf("EnvName(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestEnvVarsBriefs 执行侧装配:环境变量组含秘密,任务包条目无秘密。
func TestEnvVarsBriefs(t *testing.T) {
	store := openVaultStore(t)
	ctx := context.Background()
	if _, err := Save(ctx, store, Credential{Name: "dvwa", Username: "admin", Secret: "sec-1"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(ctx, store, Credential{Name: "api token", Secret: "sec-2"}, "creator"); err != nil {
		t.Fatal(err)
	}
	envs, err := EnvVars(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 || envs[0] != "GR_CRED_API_TOKEN=sec-2" || envs[1] != "GR_CRED_DVWA=sec-1" {
		t.Fatalf("EnvVars 不符: %v", envs)
	}
	briefs, err := Briefs(ctx, store)
	if err != nil || len(briefs) != 2 {
		t.Fatalf("Briefs: err=%v len=%d", err, len(briefs))
	}
	for _, b := range briefs {
		if b.EnvVar == "" || (b.Username != "" && b.Username == "sec-1") {
			t.Fatalf("任务包条目异常: %+v", b)
		}
	}
}

// TestVaultStoredSealed 存储层密文(P3-09 完成标准:静态加密落地):启用
// 静态加密后落库的凭据,库内原始值为 v1 密文、不含秘密明文(独立连接
// 直读库文件,不经过解封读路径)。
func TestVaultStoredSealed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault-sealed.db")
	s, err := rings.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cipher, err := secrets.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableAtRest(context.Background(), cipher); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Save(ctx, s, Credential{Name: "dvwa", Secret: "plain-secret-value"}, "creator"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key LIKE 'cred.%' LIMIT 1`).Scan(&raw); err != nil {
		t.Fatalf("直读库: %v", err)
	}
	if !secrets.IsSealed(raw) {
		t.Fatalf("库内应为密文: %q", raw)
	}
	if strings.Contains(raw, "plain-secret-value") {
		t.Fatal("库内密文含秘密明文")
	}
}
