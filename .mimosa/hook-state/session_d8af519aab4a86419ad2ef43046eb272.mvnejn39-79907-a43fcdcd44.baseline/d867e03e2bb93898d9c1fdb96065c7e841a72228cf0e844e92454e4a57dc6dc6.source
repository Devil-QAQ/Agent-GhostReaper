// Package arbiter 实现仲裁者(Arbiter):机器纯函数判官,
// Goal 完成判定与 TestClaim 确认共用一套机制(四点最小契约,R2)。
// P1-06 落地 Goal 完成判定(受限结构谓词 + 只读账本求值);
// P1-08 落地 TestClaim 确认(单轮反证判定标准 + JudgeClaim,见 claim.go);
// P2-02 升级完整版:full_falsify 判定标准(多轮反证 + 竞争性替代解释
// 全灭 + 末轮影响面证据 + 各轮验证者独立),三态裁决
// (Confirmed/待确认/待下轮反证),重开以战役边界隔离旧证据。
// 设计依据:《设计决策关键思想》第三章第 5、6 节。
package arbiter
