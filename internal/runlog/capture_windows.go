//go:build windows

package runlog

// 标准错误接管(P4-47 C)的 Windows 形态:空操作。fd 级重定向依赖
// dup2 语义,Windows 侧(SameFile/句柄复制)另立实现成本不成比例
// ——主运行平台为 darwin,Windows 构建保持"仅标准错误"的既有降级
// 行为(与日志开档失败同语义),不加半吊子实现。
func TakeoverStderr(l *Logger) error { return nil }

// rebindStderrFDLocked 滚动重绑钩子的 Windows 空操作(未接管恒空转)。
func (l *Logger) rebindStderrFDLocked() {}
