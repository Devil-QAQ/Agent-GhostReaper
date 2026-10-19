package overmind

import (
	"context"
	"strconv"
	"strings"

	"ghostreaper/internal/rings"
)

// Agent 栏运行参数的全局覆盖(P2-08d):虫群并发上限与心跳巡检间隔自
// 系统设置迁入 Agent 栏后可在线修改。语义与预算覆盖(P2-06c)同族:
// 装配显式值优先(演示/测试的确定性场景不受全局值干扰),装配零值时
// 全局键生效,再退代码默认;读失败/值非法一律退默认(fail-safe,配置
// 故障不挡运行)。生效节奏 = 消费点活读:并发上限在每轮派发决策时读
// (下一轮派发生效,在飞单位不硬中断),心跳间隔每拍重读(下一拍生效)。

const (
	// SettingKeyUnitsLimit 虫群并发上限全局键(单任务同时运行的虫群
	// 单位数)。
	SettingKeyUnitsLimit = "overmind.units.max_concurrent"
	// SettingKeyHeartbeatSeconds 心跳巡检间隔全局键(秒)。
	SettingKeyHeartbeatSeconds = "overmind.heartbeat.seconds"
)

// settingPositiveInt 读一条应为正整数的配置;读失败/缺省/非法值返回
// ok=false(fail-safe 退默认,与 swarm 包同语义)。
func settingPositiveInt(ctx context.Context, store *rings.Store, key string) (int, bool) {
	if store == nil {
		return 0, false
	}
	e, ok, err := store.Setting(ctx, key)
	if err != nil || !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(e.Value))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
