package shepherd

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Autumn-27/norma/skill"
)

// 文风完整版 skill 包(第四章 §10 按需层):humanizer 全模式中文化,
// 单二进制内嵌,经 Norma 原生 skill 机制按需加载——deferred:平时只在
// Skill 工具描述里带名字与触发条件(完整正文不进上下文),造物主要求
// "把这段改像人话"时模型调用 Skill 工具,完整指令才作为独立指令消息
// 注入会话。与常驻精简版 C 段的分工:常驻管生成,skill 管改写。

//go:embed skills/humanizer/SKILL.md
var skillFS embed.FS

// LoadSkills 物化内嵌 skill 包到 dir 并用 Norma 原生 LoadDir 加载。
// SKILL.md 是 agentskills.io 标准格式,进门走 Norma 自己的解析器——
// 格式不合规在加载处暴露,不自建平行解析。dir 里同名文件与内嵌内容
// 一致时不动(保留盘上查看与人工修改);内嵌内容更新后覆盖刷新。
// dir 里由造物主放入的其他 skill 子目录会一并加载(skill 目录可扩展)。
func LoadSkills(dir string) (*skill.Registry, error) {
	src, err := skillFS.ReadFile("skills/humanizer/SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("shepherd: 读内嵌 skill 包: %w", err)
	}
	dst := filepath.Join(dir, "humanizer", "SKILL.md")
	if cur, rerr := os.ReadFile(dst); rerr != nil || !bytes.Equal(cur, src) {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, fmt.Errorf("shepherd: 建 skill 目录: %w", err)
		}
		if err := os.WriteFile(dst, src, 0o644); err != nil {
			return nil, fmt.Errorf("shepherd: 落 skill 包: %w", err)
		}
	}
	return skill.LoadDir(dir)
}
