// Package vault 目标凭据库(P3-09,第七章 §4/P2 砍掉的「请示」以配置
// 形态回归):授权目标测试凭据的存储与调用。凭据是操作者预置的配置,
// 不是 agent 问答——存储走通用配置键值表(cred.<id>,敏感键,存储层
// 静态加密),调用走执行侧环境变量(GR_CRED_*),秘密不进任务包、不进
// 提示词正文。
//
// 调用形态:名称归一为环境变量名(字母数字保留、其余折叠为 _),bash
// 工具的子进程环境注入 GR_CRED_<归一名>=<秘密>;任务包只携带名称 /
// 用户名 / 环境变量名(无秘密),模型据任务包知道"有什么、从哪取"。
package vault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ghostreaper/internal/rings"
)

// keyPrefix 凭据键空间(存储层敏感键清单含此前缀)。
const keyPrefix = "cred."

// EnvPrefix 环境变量前缀(bash 子进程注入侧与任务包呈现侧共用)。
const EnvPrefix = "GR_CRED_"

// Credential 一条目标凭据。Secret 仅存在于服务端(存储层密文)与
// 执行侧环境变量;一切对外视图必须先转 View(打码)。
type Credential struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
	Note     string `json:"note,omitempty"`
}

// View 凭据的对外视图(P3-09 完成标准:界面打码):秘密只给打码形态
// 与"是否已配置"。EnvVar 是执行侧调用名(操作者核对用)。
type View struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	Note       string `json:"note,omitempty"`
	SecretMask string `json:"secret_mask,omitempty"`
	HasSecret  bool   `json:"has_secret"`
	EnvVar     string `json:"env_var"`
	UpdatedBy  string `json:"updated_by"`
	UpdatedAt  string `json:"updated_at"`
}

// MaskSecret 打码规则:保留前 2 后 2,中段折叠;过短只露尾 1 位,
// ≤1 位全折叠(凭据常短于模型 key,露面比 key 更少)。
func MaskSecret(s string) string {
	switch {
	case s == "":
		return ""
	case len(s) <= 1:
		return "…"
	case len(s) <= 4:
		return "…" + s[len(s)-1:]
	default:
		return s[:2] + "…" + s[len(s)-2:]
	}
}

// Masked 打码视图转换(审计字段由调用方从键值表补)。
func (c Credential) Masked() View {
	return View{
		ID: c.ID, Name: c.Name, Username: c.Username, Note: c.Note,
		SecretMask: MaskSecret(c.Secret), HasSecret: c.Secret != "",
		EnvVar: EnvName(c.Name),
	}
}

// EnvName 名称归一为环境变量名:字母数字保留、其余折叠为 _、去首尾 _;
// 归一后须含至少一个字母或数字(纯符号名不可用)。前缀自带尾 _,
// 折叠状态以"刚写过 _"起步,防首部分隔符重复。
func EnvName(name string) string {
	var b strings.Builder
	b.WriteString(EnvPrefix)
	lastUS := true
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUS = false
		default:
			if !lastUS {
				b.WriteByte('_')
				lastUS = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// envUsable 环境变量名是否可用(归一后除前缀外仍有有效字符)。
func envUsable(name string) bool {
	return len(EnvName(name)) > len(EnvPrefix)
}

// Save 写入(或编辑)一条凭据:名称必填 ≤64 字;名称全库唯一(环境
// 变量名与任务包呈现都以名称为锚);环境变量名归一后不得与其他凭据
// 撞名(名称唯一已保证归一名唯一,此处仍拦"归一名无有效字符"的名);
// 编辑时 Secret 留空 = 保留旧值,新建必填。id 为空时生成。
func Save(ctx context.Context, store *rings.Store, c Credential, by string) (Credential, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Username = strings.TrimSpace(c.Username)
	c.Note = strings.TrimSpace(c.Note)
	if c.Name == "" || len(c.Name) > 64 {
		return Credential{}, errors.New("vault: 凭据名称须为 1~64 个字符")
	}
	if !envUsable(c.Name) {
		return Credential{}, errors.New("vault: 凭据名称须含字母或数字(用于生成调用名)")
	}
	if c.ID != "" {
		old, ok, err := ByID(ctx, store, c.ID)
		if err != nil {
			return Credential{}, err
		}
		if !ok {
			return Credential{}, fmt.Errorf("vault: 凭据 %s 不存在", c.ID)
		}
		if c.Secret == "" {
			c.Secret = old.Secret // 编辑未重填秘密:保留旧值
		}
	} else {
		c.ID = newID()
	}
	if c.Secret == "" {
		return Credential{}, errors.New("vault: 秘密不能为空(新凭据必填)")
	}
	// 名称唯一(排除自身)。
	existing, err := List(ctx, store)
	if err != nil {
		return Credential{}, err
	}
	for _, o := range existing {
		if o.Name == c.Name && o.ID != c.ID {
			return Credential{}, fmt.Errorf("vault: 该名称已存在")
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return Credential{}, fmt.Errorf("vault: 序列化凭据: %w", err)
	}
	if err := store.SetSetting(ctx, keyPrefix+c.ID, string(raw), by); err != nil {
		return Credential{}, fmt.Errorf("vault: 存储: %w", err)
	}
	return c, nil
}

// Delete 删除一条凭据(幂等)。
func Delete(ctx context.Context, store *rings.Store, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("vault: 凭据 ID 不能为空")
	}
	return store.DeleteSetting(ctx, keyPrefix+id)
}

// List 全库清单(含秘密,仅供服务端调用侧装配;对外呈现必须先 Masked)。
// 按名称排序稳定输出。
func List(ctx context.Context, store *rings.Store) ([]Credential, error) {
	entries, err := store.SettingsWithPrefix(ctx, keyPrefix)
	if err != nil {
		return nil, fmt.Errorf("vault: 读凭据库: %w", err)
	}
	out := make([]Credential, 0, len(entries))
	for _, e := range entries {
		var c Credential
		if err := json.Unmarshal([]byte(e.Value), &c); err != nil {
			continue // 脏值跳过(fail-safe:单条坏凭据不拖垮整库)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ByID 按ID 取一条;ok=false 表示不存在。
func ByID(ctx context.Context, store *rings.Store, id string) (Credential, bool, error) {
	e, ok, err := store.Setting(ctx, keyPrefix+strings.TrimSpace(id))
	if err != nil || !ok {
		return Credential{}, false, err
	}
	var c Credential
	if err := json.Unmarshal([]byte(e.Value), &c); err != nil {
		return Credential{}, false, fmt.Errorf("vault: 凭据 %s 损坏: %w", id, err)
	}
	return c, true, nil
}

// Views 打码视图清单(审计字段自键值表补齐)。
func Views(ctx context.Context, store *rings.Store) ([]View, error) {
	entries, err := store.SettingsWithPrefix(ctx, keyPrefix)
	if err != nil {
		return nil, fmt.Errorf("vault: 读凭据库: %w", err)
	}
	out := make([]View, 0, len(entries))
	for _, e := range entries {
		var c Credential
		if err := json.Unmarshal([]byte(e.Value), &c); err != nil {
			continue
		}
		v := c.Masked()
		v.UpdatedBy = e.UpdatedBy
		v.UpdatedAt = e.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---- 执行侧调用装配 ----

// EnvVars 全库凭据的 bash 环境变量组("GR_CRED_X=secret" 列表,注入
// 单位 bash 子进程)。读失败如实返回错误(调用侧决定降级与否)。
func EnvVars(ctx context.Context, store *rings.Store) ([]string, error) {
	creds, err := List(ctx, store)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		out = append(out, EnvName(c.Name)+"="+c.Secret)
	}
	return out, nil
}

// Brief 任务包凭据条目(无秘密):名称 / 用户名 / 调用环境变量名。
type Brief struct {
	Name     string
	Username string
	EnvVar   string
}

// Briefs 全库凭据的任务包条目(无秘密)。
func Briefs(ctx context.Context, store *rings.Store) ([]Brief, error) {
	creds, err := List(ctx, store)
	if err != nil {
		return nil, err
	}
	out := make([]Brief, 0, len(creds))
	for _, c := range creds {
		out = append(out, Brief{Name: c.Name, Username: c.Username, EnvVar: EnvName(c.Name)})
	}
	return out, nil
}

// newID 面板侧短随机 ID(c-*)。
func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("c-%d", 0)
	}
	return "c-" + hex.EncodeToString(b[:])
}
