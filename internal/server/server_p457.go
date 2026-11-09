// P4-57 ④ 死锁重开与受阻归因拆分(问题清单 #76):
//
// 死锁形态在案:确定性 400 类 blocked 的自动重开只挂在"模型配置恢复"
// (保存/删除/切换)上,而配置没坏(模型是通的、其他单位在跑)时重开
// 条件永不满足——v1.2.0 实锤 s87/s91 至今零接手。思考回传规则修复后,
// 这类程序缺陷造成的存量受阻步骤随升级自动重开(修复上线即解死锁);
// 此后同类确定性 400 由出站按腿规则根治,不再产生。
package server

import (
	"context"
	"fmt"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// keyP457BlockedReopened P4-57 ④ 存量受阻步骤一次性重开标记键。
const keyP457BlockedReopened = "migrate.p457.blocked_reopen"

// ReopenBlockedStockOnce 升级一次性重开存量受阻步骤(P4-57 ④):标记键
// 已写直接返回;否则把非终态任务里 blocked 的 Step 重开回 dispatched 并
// 唤醒拉起(与 reopenBlockedSteps 同一条路),完成后写标记键。在
// main.go 装配完成后调用(Manager 已收养、执行器已挂接)。幂等。
func (s *Server) ReopenBlockedStockOnce() {
	ctx := context.Background()
	if e, ok, err := s.Store.Setting(ctx, keyP457BlockedReopened); err != nil {
		runlog.Errorf("读受阻存量重开标记失败(跳过,下次启动再试): %v", err)
		return
	} else if ok && e.Value != "" {
		return
	}
	tasks, steps := s.reopenBlockedSteps(ctx, "system", "版本升级自动重开(受阻步骤存量清理)", "")
	if tasks > 0 || steps > 0 {
		runlog.Infof("受阻存量重开: %d 个任务的 %d 个受阻步骤已重开(版本升级自动重开)",
			tasks, steps)
	}
	if err := s.Store.SetSetting(ctx, keyP457BlockedReopened,
		fmt.Sprintf("tasks=%d steps=%d", tasks, steps), "system"); err != nil {
		runlog.Errorf("写受阻存量重开标记失败(下次启动会重跑,重开本身幂等): %v", err)
	}
}

// blockedStepsByClassOf 图上 blocked 步骤数按归因拆分(P4-57 ⑤ 受阻
// 红点悬停按归因分文案的数据源):model_fault = 模型故障(等待模型
// 恢复),determinant = 程序异常(确定性拒绝,已记录、修复后自动恢复)。
func (s *Server) blockedStepsByClassOf(ctx context.Context, taskID string) (fault, defect int) {
	steps, err := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "blocked")
	if err != nil {
		return 0, 0
	}
	for _, st := range steps {
		if class, _ := st.Detail["blocked_class"].(string); class == swarm.BlockedClassDeterminant {
			defect++
		} else {
			fault++
		}
	}
	return fault, defect
}
