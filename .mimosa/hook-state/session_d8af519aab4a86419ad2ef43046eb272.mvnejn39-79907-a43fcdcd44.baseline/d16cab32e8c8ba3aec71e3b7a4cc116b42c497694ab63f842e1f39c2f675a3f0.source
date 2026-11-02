// Package overmind 实现主宰(Overmind):态势研判、拆解、派发、收割。
// P1-04 落地最简派发(SeedGoal/DispatchStep)与确定性收割(Harvest:
// 候选证据 → 正式 Fact 事件);P1-05 落地事件唤醒的主宰循环(Loop:
// 唤醒 → LLM 拆解派发 → 虫群并发执行(上限默认 3)→ 收割 → 循环,
// 经 Manager.OnTaskRun 挂入任务 goroutine);P1-08 落地候选声明收割
// (HarvestClaims:候选 TestClaim → 反证 Step 派发,R12 独立单位)与
// 声明确认判定(judgeClaims:仲裁者机器裁决 → Confirmed/待确认);
// P2-03 落地候选假设收割(harvestHypotheses:主宰评估三去向——转
// Step 派发验证/挂账/废弃 + 验证结论确定性迁移,第四章 §5 直觉回流);
// P3-05b 收割定稿(AssembleRepro)按目标流量过滤(请求 host 在种子
// 资产授权范围内才入复现步骤,外网查证流量不进,流量页全量保留)并
// 为每步生成一句话描述(确定性,面板与报告同源);P3-07 断点冻结信号
// (Loop.Ckpt):存档断点武装后循环在轮次边界退出(不再拆解派发,在飞
// 单位由虫群侧同一信号收场落地),任务让位为已存档断点、进程可关;
// P3-11a 常驻文风(第四章 §10 作用域 v2):拆解会话与假设评估会话挂
// swarm.ResidentStyleRules——研判叙述(五问/理由/意图文本)按人话写,
// JSON 契约原样(结构化照契约作用域行在条款尾注)。
// 设计依据:《设计决策关键思想》第三章 §5、第四章。
package overmind
