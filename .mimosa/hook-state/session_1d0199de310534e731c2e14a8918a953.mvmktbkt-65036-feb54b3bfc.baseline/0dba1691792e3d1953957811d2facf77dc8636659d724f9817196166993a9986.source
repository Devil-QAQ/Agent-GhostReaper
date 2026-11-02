package overmind

import "sync"

// Registry 运行中主宰循环的注册表(P2-08 单位控制的主宰循环粒度):
// 每任务的 Loop 由 Manager 的 OnTaskRun 闭包构造并 Run——注册表让面板
// (经 server)能找到在跑的循环实例下达暂停/继续。循环退出即注销;
// 未注册 = 该任务当前无在跑主宰循环(重启收养前的空窗或未配模型),
// 控制端点如实报不可用。同一条主装配路径的查询面,不是旁路——控制
// 仍经 Loop 实例落 unit.control 账。
type Registry struct {
	mu sync.Mutex
	m  map[string]*Loop
}

// NewRegistry 构建注册表。
func NewRegistry() *Registry { return &Registry{m: map[string]*Loop{}} }

// Register 登记一个任务的在跑循环;返回注销句柄(循环退出时调用)。
func (r *Registry) Register(taskID string, l *Loop) (unregister func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[taskID] = l
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			// 只注销仍是自己的一格(重启收养换新循环时不误删)。
			if r.m[taskID] == l {
				delete(r.m, taskID)
			}
		})
	}
}

// Get 取任务的在跑循环(nil = 当前无循环)。
func (r *Registry) Get(taskID string) *Loop {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[taskID]
}
