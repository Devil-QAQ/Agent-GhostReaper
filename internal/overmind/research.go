package overmind

// 主宰完整研判的研判侧件(P2-06,第四章 §1 主宰职责"态势研判"):
//
//   - 态势研判五问:新身份能访问哪些已有资产?新权限是否改变其他漏洞
//     的影响?两条单独影响有限的线索是否组合成新的边界问题?新的可达
//     关系是否解锁之前受阻的步骤?新证据是否推翻旧结论?五问是启发式
//     起点(保证下限),不是封闭问题清单(不设上限)——每轮拆解会话
//     按五问审视态势,结论落 overmind.record(assessment)审计流,
//     "研判发生过、研判了什么"从账本可验证;
// 组合方向与单步方向均派普通意图，经真实执行后走统一交卷。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Step 优先级词汇(P2-06 派发策略:并发满员时空出名额按优先级领取)。
// 三档受控词汇;缺省 normal(既有行为)。
const (
	PriorityHigh   = "high"
	PriorityNormal = "normal"
	PriorityLow    = "low"
)

// PriorityRank 优先级排序权重(high=3 > normal=2 > low=1;未知=0 拒绝
// 派发侧使用,排序侧按最低处理兜底)。
func PriorityRank(p string) int {
	switch strings.TrimSpace(p) {
	case PriorityHigh:
		return 3
	case PriorityNormal, "": // 缺省即常规
		return 2
	case PriorityLow:
		return 1
	default:
		return 0
	}
}

// shortIDSuffix 节点 ID 的随机后缀(同秒多条不撞 ID;与虫群侧
// shortID 同风格)。rand 失败退时间戳,不因 ID 生成失败丢研判产物。
func shortIDSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
