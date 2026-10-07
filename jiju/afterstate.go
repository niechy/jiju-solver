package jiju

import "fmt"

// AfterState 只应用"可观测的确定性效果", 不抽任何新候选。
//
// 选句: 结算该句并进入下一轮(扣入场费、重置免费额度和本轮屏蔽集),
// 但不抽新的三句 —— 调用方之后可用独立的 RNG 抽样。
// 刷新: 扣费并把当前候选计入阻塞集、清空候选, 同样不抽新句。
//
// 返回值第二个分量是本次动作带来的"新增句数"(选句 1, 刷新 0)。
// 这样调用方可以在不引入随机性的前提下评估"动作之后的状态"。
func (w *World) AfterState(st *WorldState, action int) (*WorldState, int, error) {
	if w.rules.DrawMode != DrawRound || !w.Legal(st, action) {
		return nil, 0, fmt.Errorf("unsupported or illegal afterstate action %d", action)
	}
	s := st.Clone()
	if action == RefreshAction {
		if s.Free > 0 {
			s.Free--
		} else {
			s.Lingxi -= w.rules.RefreshCost
			s.PaidRefreshes++
		}
		s.Refreshes++
		for i := 0; i < s.n; i++ {
			v := s.cands[i]
			if !s.blocked[v] {
				s.blocked[v] = true
				s.blockedN++
			}
		}
		s.n = 0
		return s, 0, nil
	}
	w.selectVerse(s, action)
	if s.Lingxi < s.Cost || len(s.left) < w.rules.MinCandidates {
		s.Done = true
		return s, 1, nil
	}
	if w.rules.PayOnEntry {
		s.Lingxi -= s.Cost
	}
	s.Free = s.FreeLimit
	clear(s.blocked)
	s.blockedN = 0
	return s, 1, nil
}
