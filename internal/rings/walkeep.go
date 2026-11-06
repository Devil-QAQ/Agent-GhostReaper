package rings

// walkeep.go P4-38 数据库暂存保养(问题清单 #57 ④):SQLite WAL 无
// checkpoint 策略时,巨事务(如存量 819MB 记录单事务入账)会把暂存
// 文件顶到高水位后常驻(实测 855MB 拖慢全部读)。保养三件套:
// ① WAL 超过约 100MB 触发 checkpoint(被动模式,尽力回收);
// ② 系统空闲(无活跃写入)时执行 checkpoint(TRUNCATE,全量回收归零);
// ③ journal_size_limit 收缩暂存文件(连接级 pragma,Open 时随 DSN
//    下发),checkpoint 重置后暂存不再停在历史高水位。
// 协同纪律(条目②):保养动作全部在单写者循环内执行——空闲计时器由
// 写者 goroutine 自持,checkpoint 进行期间结构上不可能并发写(不与写
// 路径打架);读路径不受影响(WAL 下读与 checkpoint 并发,TRUNCATE 经
// busy_timeout 等在途读者读毕,读者自身不被阻塞)。定值不做配置项;
// var 形态仅供测试注入缩时/缩量参数。

import (
	"context"
	"fmt"
	"os"
	"time"

	"ghostreaper/internal/runlog"
)

const walJournalSizeLimit int64 = 32 << 20 // journal_size_limit:32MB(checkpoint 重置后的暂存上限)

var (
	walKeeperThreshold int64         = 100 << 20        // 超阈值触发被动回收(P4-38 定值:约 100MB)
	walKeeperIdle      time.Duration = 15 * time.Second // 空闲窗:写请求静默至此,写者转入保养
)

// walExceedsThreshold 暂存文件是否超回收阈值(无文件/读失败按未超)。
func (s *Store) walExceedsThreshold() bool {
	st, err := os.Stat(s.walPath)
	return err == nil && st.Size() >= walKeeperThreshold
}

// walKeepPostWrite 写后保养(写者 goroutine 内,事务已提交):暂存超
// 阈值则补一发被动回收——此刻无并发写(结构保证),读者不挡被动模式。
func (s *Store) walKeepPostWrite() {
	if s.walExceedsThreshold() {
		s.walCheckpoint("PASSIVE")
	}
}

// walKeepIdle 空闲保养(写者 goroutine 内,空闲计时器触发):全量回收,
// 暂存归零;busy_timeout 内等不齐读者就下轮再试(纯保养,不致命)。
func (s *Store) walKeepIdle() {
	if st, err := os.Stat(s.walPath); err != nil || st.Size() == 0 {
		return // 无暂存(尚无写或已收缩)
	}
	s.walCheckpoint("TRUNCATE")
}

// walCheckpoint 执行一次 checkpoint 并按收缩量分级留痕(明显收缩记
// INFO,常态小回收记 DEBUG 防刷屏)。TRUNCATE 需等在途读者读毕
// (busy_timeout 内重试),读者不被阻塞;PASSIVE 能收多少收多少。
func (s *Store) walCheckpoint(mode string) {
	var busy, logFrames, ckptFrames int
	err := s.db.QueryRowContext(context.Background(),
		fmt.Sprintf("PRAGMA wal_checkpoint(%s)", mode)).Scan(&busy, &logFrames, &ckptFrames)
	after := int64(-1)
	if st, err := os.Stat(s.walPath); err == nil {
		after = st.Size()
	}
	if err != nil {
		runlog.Debugf("WAL %s 回收未完成(下轮再试): %v", mode, err)
		return
	}
	if after < 0 || after >= 1<<20 {
		runlog.Infof("WAL %s 回收: 暂存收缩到 %dMB(回收 %d 帧,%d 帧被旧读者挡住)", mode, after>>20, ckptFrames, logFrames-ckptFrames)
	}
}
