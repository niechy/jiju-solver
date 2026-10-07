// 自适应(状态条件)刷新闸 —— 实验性新策略维度, 独立文件不改已提交代码。
//
// 与 EvolvedStrategy(常数闸)的本质差异:
//   刷新闸(GainFloor/DryFloor)随当前灵犀存量分段变化。
//   灵犀充裕(建设期): 更低门槛敢刷, 建立押韵链。
//   灵犀见底(保命期): 更高门槛, 只在出韵时刷(押韵本身正EV, 但见底下容错低)。
package jiju

import (
	"fmt"
	"math/rand"
	"sort"
)

// AdaptiveStrategy 状态条件刷新闸策略。
type AdaptiveStrategy struct {
	Base          Strategy
	LowLingxi     int     // 灵犀 < 此值进入"保命期"
	GainFloorLow  float64 // 保命期刷新期望收益闸(更高=更保守)
	GainFloorHigh float64 // 建设期刷新期望收益闸(更低=更激进)
	DryFloorLow   int     // 保命期干涸线
	DryFloorHigh  int     // 建设期干涸线
}

// effective 返回当前状态下的有效闸值。
func (a *AdaptiveStrategy) effective(lingxi int) (gainFloor float64, dryFloor int) {
	if lingxi < a.LowLingxi {
		return a.GainFloorLow, a.DryFloorLow
	}
	return a.GainFloorHigh, a.DryFloorHigh
}

// DecideAdaptive 自适应刷新决策(复用 scoreCandidate, 仅改刷新闸)。
func (l *Library) DecideAdaptive(prev *Verse, cands []*Verse, st *GameState, a AdaptiveStrategy) Decision {
	s := a.Base
	g1Rem := l.g1Remaining(st.Collected)
	totalRem := l.totalRemaining(st.Collected)
	curG1Rem := 0
	if len(prev.Group1) > 0 {
		curG1Rem = g1Rem[prev.Group1[0]]
	}
	threshold := int(float64(totalRem) * s.SwitchThreshPct)
	gainFloor, dryFloor := a.effective(st.Lingxi)

	var scored []candidate
	for i, c := range cands {
		if c == nil {
			continue
		}
		scored = append(scored, s.scoreCandidate(prev, c, i, st, g1Rem, totalRem))
	}
	if len(scored) == 0 {
		return Decision{Action: "refresh", PickIdx: -1, Desc: "无可用候选, 刷新"}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	best := scored[0]

	shouldRefresh := false
	refreshWhy := ""
	if best.kind == KindOffRhyme {
		shouldRefresh = true
		refreshWhy = "规避出韵"
	}
	if !best.g1Match && best.kind == KindMiddle {
		if curG1Rem > dryFloor { // 状态相关干涸线
			shouldRefresh = true
			refreshWhy = "刷找押韵保+7"
		}
	}
	if curG1Rem <= threshold && !best.g1Match {
		pool := 0
		for _, g := range best.verse.Group1 {
			if g1Rem[g] > pool {
				pool = g1Rem[g]
			}
		}
		if pool < s.MinTargetPool {
			shouldRefresh = true
			refreshWhy = fmt.Sprintf("下家池%d句过小, 刷找大组", pool)
		}
	}

	if shouldRefresh {
		if st.FreeRefresh <= 0 && totalRem > 0 {
			p := 1.0 - pow1m(float64(curG1Rem)/float64(totalRem), 3)
			expectedGain := p * 8.0
			if st.Lingxi < s.LowLingxiFloor && best.kind != KindOffRhyme {
				shouldRefresh = false
			} else if expectedGain < float64(s.RefreshCost)+gainFloor && best.kind != KindOffRhyme {
				shouldRefresh = false
			}
		}
	}

	if shouldRefresh {
		if st.FreeRefresh > 0 {
			return Decision{Action: "refresh", PickIdx: -1, Desc: refreshWhy + "(免费)"}
		}
		if st.Lingxi >= st.Cost+s.RefreshCost {
			return Decision{Action: "refresh", PickIdx: -1, Desc: refreshWhy + "(花1灵犀)"}
		}
	}

	best.desc = fmt.Sprintf("净%d 分%.0f", best.net, best.score)
	return Decision{Action: "select", PickIdx: best.idx, Kind: best.kind, XiangZhan: best.xiangZhan, Desc: best.desc}
}

// RunSimOnceAdaptive 用 AdaptiveStrategy 跑一局。
func (l *Library) RunSimOnceAdaptive(cfg SimConfig, a AdaptiveStrategy, rng *rand.Rand) SimResult {
	byID := make(map[int]*Verse, len(l.verses))
	for i := range l.verses {
		byID[l.verses[i].ID] = &l.verses[i]
	}
	st := NewGameState(cfg.InitialCost, cfg.InitialLingxi, cfg.InitialFreeRefresh)
	ss := &simState{gs: st, maxFreeRefresh: cfg.InitialFreeRefresh, verseByID: byID}

	var lastG2 []int
	for _, id := range cfg.InitialVerseIDs {
		v := byID[id]
		if v == nil {
			continue
		}
		st.Collected[id] = true
		ss.totalShiyi += v.Shiyi
		if v.Poet != 0 && !st.SeenPoets[v.Poet] {
			st.SeenPoets[v.Poet] = true
			pr := PoetFirstRewards[v.Poet]
			ss.totalShiyi += pr.Shiyi
			st.Lingxi += pr.Lingxi
			if pr.FreeRefresh > 0 {
				ss.maxFreeRefresh += pr.FreeRefresh
			}
		}
		if lastG2 != nil && intSliceEqual(lastG2, v.Group2) {
			ss.totalShiyi -= 5
		}
		lastG2 = v.Group2
		ss.last = v
	}
	if ss.last == nil {
		ss.last = l.verses[0].self(byID)
	}
	st.FreeRefresh = ss.maxFreeRefresh

	turns := len(cfg.InitialVerseIDs)
	for st.Lingxi >= st.Cost && turns < cfg.MaxTurns {
		avail := l.available(st.Collected)
		if len(avail) == 0 {
			break
		}
		rng.Shuffle(len(avail), func(i, j int) { avail[i], avail[j] = avail[j], avail[i] })
		poolIdx := 0
		var picked *Verse
		var kind JudgeKind
		var xz bool

		for {
			if poolIdx+3 > len(avail) && poolIdx >= len(avail) {
				return ss.result(turns)
			}
			n := 3
			if poolIdx+n > len(avail) {
				n = len(avail) - poolIdx
			}
			cands := make([]*Verse, n)
			copy(cands, avail[poolIdx:poolIdx+n])

			dec := l.DecideAdaptive(ss.last, cands, st, a)
			if dec.Action == "refresh" {
				ss.refreshes++
				if st.FreeRefresh > 0 {
					st.FreeRefresh--
				} else {
					st.Lingxi -= a.Base.RefreshCost
				}
				poolIdx += n
				continue
			}
			picked = cands[dec.PickIdx]
			kind = dec.Kind
			xz = dec.XiangZhan
			break
		}
		if picked == nil {
			break
		}

		prevG2 := ss.last.Group2
		isNewPoet := picked.Poet != 0 && !st.SeenPoets[picked.Poet]
		l.ApplySelect(st, ss.last, picked, kind, xz)
		if kind == KindOffRhyme {
			ss.offRhymes++
		}
		ss.totalShiyi += picked.Shiyi
		if isNewPoet {
			pr := PoetFirstRewards[picked.Poet]
			ss.totalShiyi += pr.Shiyi
			if pr.FreeRefresh > 0 {
				ss.maxFreeRefresh += pr.FreeRefresh
			}
		}
		if intSliceEqual(prevG2, picked.Group2) {
			ss.totalShiyi -= 5
		}
		ss.last = picked
		turns++
		st.FreeRefresh = ss.maxFreeRefresh
	}
	return ss.result(turns)
}

// RunSimAdaptive 跑 n 局并汇总。
func (l *Library) RunSimAdaptive(cfg SimConfig, a AdaptiveStrategy, n int, seed int64) SimStats {
	rng := rand.New(rand.NewSource(seed))
	turns := make([]int, 0, n)
	var sumT, sumS, sumL, sumO, sumR, sumC float64
	for i := 0; i < n; i++ {
		r := l.RunSimOnceAdaptive(cfg, a, rng)
		turns = append(turns, r.Turns)
		sumT += float64(r.Turns)
		sumS += float64(r.Shiyi)
		sumL += float64(r.LingxiLeft)
		sumO += float64(r.OffRhymes)
		sumR += float64(r.Refreshes)
		sumC += float64(r.CostEnd)
	}
	sort.Ints(turns)
	pct := func(p float64) int {
		if n == 0 {
			return 0
		}
		return turns[int(p*float64(n-1))]
	}
	return SimStats{
		N: n, MeanTurns: sumT / float64(n),
		P5: pct(0.05), P50: pct(0.5), P95: pct(0.95),
		MeanShiyi: sumS / float64(n), MeanLingxi: sumL / float64(n), MeanOff: sumO / float64(n),
		MeanRef: sumR / float64(n), MeanCostEnd: sumC / float64(n),
	}
}
