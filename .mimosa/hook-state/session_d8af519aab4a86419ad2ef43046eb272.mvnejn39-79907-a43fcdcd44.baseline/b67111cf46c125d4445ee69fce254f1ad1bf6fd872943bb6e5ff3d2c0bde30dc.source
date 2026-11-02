// Package swarm 实现虫群(Swarm)执行器:领取任务包(七要素)、执行前
// 复检图版本(R14)、Norma Session 工具调用、原始执行记录与候选证据经
// 受控入口实时入账(R8/R18)。设计依据:《设计决策关键思想》第四章;
// 实施于 P1-04;P1-05 增设起飞临界区与 RunStep 并发入口(供主宰循环
// 并发调度,单位间记账互不误判 R14 过期);P1-08 增设 TestClaim 受控
// 入口(submit_test_claim 可证伪声明 / submit_falsify_result 反证写回,
// R12 发现者不得自证)与反证任务包上下文;P3-03 增设只读辅助三工具
// (read 本机文件 / web_search 外网搜索(配置见 internal/websearch)/
// web_fetch 外网取文·强制经镜留痕),攻击动作仍全由 bash 自由组合;
// P3-05 bash 限时放开(模型自调 timeout_ms、默认 120 秒封顶 600 秒、
// 超时自动转后台/显式后台,输出落步骤目录 bg-N.output,单位落地整组
// 回收;审批与留痕照旧);P3-05b 硬规则新增「代理旁路」组(curl/wget
// 绕代理、env -i 清空环境、unset/空赋值清除代理变量,默认启用——旁路
// 破坏流量审计根基,拒绝原因提示流量必须经镜);P3-05c ① 恢复闭环:
// 模型故障受阻(blocked)收场时 session_finished 带 Resumable 标记——
// 会话保留,恢复重开该步骤后新单位优先经转录 Resume 原会话原地续跑
// (上下文零重建,与审批续跑/冬眠/重启收养同语义);转录缺失/为空降级
// 回全新会话接力(exhausted 同款);预算耗尽的接力语义不变;P3-06 增设
// 复测会话(retest.go):对锤炼完成的发现(confirmed/pending/fixed)发起
// 修复状态复核——自动首条消息绑定漏洞上下文、专用受限工具集(bash/
// 只读探测/read + submit_retest_result,结论写回目标服务端定死防串号)、
// 结论 fixed/unfixed 自动迁移声明状态(inconclusive 只入记录);复测不
// 进图(伪步骤锚 RetestSID),drive 循环全套复用(审批门/镜留痕/门卫/
// 模型重试/记账),同声明同时至多一轮,悬空会话下次发起时收口;P3-07
// 增设重放校验(replay.go,零 LLM):对声明的绑定流量证据确定性原样重放
// ——留痕原文重建请求经镜重发(新流量归因重放锚 ReplaySID)、原响应
// 逐字节比对,结论 reproduced|diverged|unreachable 与逐条交换事实入账,
// 不迁移声明状态(修复状态轴归复测);断点续接(ckpt.Gate):存档断点
// 武装后单位在工具调用边界落地(session_finished 带 Checkpointed 标记、
// 步骤回可领取、复测单位豁免),恢复领取时优先原会话 Resume 续跑
// (【断点 · 续跑】),pendingSession 按 adoption/blocked/checkpoint 三种
// 可续来源区分续跑指令措辞;P3-11a 常驻文风(第四章 §10 作用域 v2):
// worker/复测基线各挂 ResidentStyleRules 固定段——执行叙述与舵杆回执
// 解释按人话写,工具参数与结构化写回照契约(牧者/主宰/插话同款共用)。
package swarm
