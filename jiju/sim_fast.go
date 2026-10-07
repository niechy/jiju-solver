// 优化版模拟内核: 消除热点 map 操作。
// pprof 显示 66% CPU 在 g1Remaining(46%)+totalRemaining(20%)——它们每次 Decide
// (含每次刷新)都从 573 句全量重建。本优化版:
//   - 开局构建一次 g1Rem/totalRem, 此后增量维护(收集一句 O(组数) 递减, 刷新不变)。
//   - collected 用 bool 切片(ID 1~573)替代 map[int]bool。
//   - Decide 逻辑与库内完全一致(逐行对齐), 保证结果一致。
package jiju

import (
	"math/rand"
	"sort"
)

// FastSimStats 与 SimStats 相同(独立类型避免与库冲突, 字段一致)。
type FastSimStats = SimStats

// fastState 增量维护的模拟状态。
type fastState struct {
	gs       *GameState
	collected []bool // ID 1..maxID
	maxFree  int
	totalShiyi int
	offRhymes int
	last     *Verse
	refreshes int
	// 增量缓存
	g1Rem    map[int]int
	totalRem int
	maxID    int
	// 复用缓冲(减少每轮分配)
	availBuf []*Verse
}

// RunSimOnceFast 优化版单局。决策逻辑与 RunSimOnce+Decide 完全一致。
func (l *Library) RunSimOnceFast(cfg SimConfig, s Strategy, rng *rand.Rand) SimResult {
	maxID := 0
	for i := range l.verses {
		if l.verses[i].ID > maxID {
			maxID = l.verses[i].ID
		}
	}
	fs := &fastState{
		gs:        NewGameState(cfg.InitialCost, cfg.InitialLingxi, cfg.InitialFreeRefresh),
		collected: make([]bool, maxID+1),
		maxFree:   cfg.InitialFreeRefresh,
		g1Rem:     map[int]int{},
		maxID:     maxID,
		availBuf:  make([]*Verse, 0, len(l.verses)),
	}
	// 初始 g1Rem/totalRem(全库)
	for i := range l.verses {
		v := &l.verses[i]
		for _, g := range v.Group1 {
			fs.g1Rem[g]++
		}
		fs.totalRem++
	}
	byID := make([]*Verse, maxID+1)
	for i := range l.verses {
		byID[l.verses[i].ID] = &l.verses[i]
	}

	// 初始诗句
	var lastG2 []int
	for _, id := range cfg.InitialVerseIDs {
		if id < 0 || id > maxID {
			continue
		}
		v := byID[id]
		if v == nil {
			continue
		}
		fs.collected[id] = true
		for _, g := range v.Group1 {
			fs.g1Rem[g]--
		}
		fs.totalRem--
		fs.totalShiyi += v.Shiyi
		if v.Poet != 0 && !fs.gs.SeenPoets[v.Poet] {
			fs.gs.SeenPoets[v.Poet] = true
			pr := PoetFirstRewards[v.Poet]
			fs.totalShiyi += pr.Shiyi
			fs.gs.Lingxi += pr.Lingxi
			if pr.FreeRefresh > 0 {
				fs.maxFree += pr.FreeRefresh
			}
		}
		if lastG2 != nil && intSliceEqual(lastG2, v.Group2) {
			fs.totalShiyi -= 5
		}
		lastG2 = v.Group2
		fs.last = v
	}
	if fs.last == nil {
		fs.last = &l.verses[0]
	}
	fs.gs.FreeRefresh = fs.maxFree

	st := fs.gs
	turns := len(cfg.InitialVerseIDs)
	for st.Lingxi >= st.Cost && turns < cfg.MaxTurns {
		// avail: 用收集标记重建(保持与库版一致的 shuffle 语义), 复用缓冲
		avail := fs.availBuf[:0]
		for i := range l.verses {
			if !fs.collected[l.verses[i].ID] {
				avail = append(avail, &l.verses[i])
			}
		}
		if len(avail) == 0 {
			break
		}
		rng.Shuffle(len(avail), func(i, j int) { avail[i], avail[j] = avail[j], avail[i] })
		poolIdx := 0
		refreshUsed := 0
		var picked *Verse
		var kind JudgeKind
		var xz bool

		for {
			if poolIdx+3 > len(avail) && poolIdx >= len(avail) {
				return fs.result(turns)
			}
			n := 3
			if poolIdx+n > len(avail) {
				n = len(avail) - poolIdx
			}
			cands := avail[poolIdx : poolIdx+n]

			dec := l.decideFast(fs, cands, s, refreshUsed)
			if dec.Action == "refresh" {
				refreshUsed++
				fs.refreshes++
				if st.FreeRefresh > 0 {
					st.FreeRefresh--
				} else {
					st.Lingxi -= s.RefreshCost
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

		prevG2 := fs.last.Group2
		isNewPoet := picked.Poet != 0 && !st.SeenPoets[picked.Poet]
		l.ApplySelect(st, fs.last, picked, kind, xz)
		// 增量维护
		fs.collected[picked.ID] = true
		for _, g := range picked.Group1 {
			fs.g1Rem[g]--
		}
		fs.totalRem--
		if kind == KindOffRhyme {
			fs.offRhymes++
		}
		fs.totalShiyi += picked.Shiyi
		if isNewPoet {
			pr := PoetFirstRewards[picked.Poet]
			fs.totalShiyi += pr.Shiyi
			if pr.FreeRefresh > 0 {
				fs.maxFree += pr.FreeRefresh
			}
		}
		if intSliceEqual(prevG2, picked.Group2) {
			fs.totalShiyi -= 5
		}
		fs.last = picked
		turns++
		st.FreeRefresh = fs.maxFree
	}
	return fs.result(turns)
}

func (fs *fastState) result(turns int) SimResult {
	return SimResult{
		Turns: turns, Shiyi: fs.totalShiyi, LingxiLeft: fs.gs.Lingxi,
		CostEnd: fs.gs.Cost, OffRhymes: fs.offRhymes, Refreshes: fs.refreshes,
	}
}

// decideFast 与库内 Decide 逐行一致, 但用增量缓存的 g1Rem/totalRem。
func (l *Library) decideFast(fs *fastState, cands []*Verse, s Strategy, refreshUsed int) Decision {
	st := fs.gs
	g1Rem := fs.g1Rem
	totalRem := fs.totalRem
	curG1Rem := 0
	if len(fs.last.Group1) > 0 {
		curG1Rem = g1Rem[fs.last.Group1[0]]
	}
	threshold := int(float64(totalRem) * s.SwitchThreshPct)

	var scored []candidate
	for i, c := range cands {
		if c == nil {
			continue
		}
		scored = append(scored, s.scoreCandidate(fs.last, c, i, st, g1Rem, totalRem))
	}
	if len(scored) == 0 {
		return Decision{Action: "refresh", PickIdx: -1, Desc: "无可用候选, 刷新"}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	best := scored[0]

	shouldRefresh := false
	if best.kind == KindOffRhyme {
		shouldRefresh = true
	}
	if !best.g1Match && best.kind == KindMiddle {
		if curG1Rem > s.DryGroupFloor {
			shouldRefresh = true
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
		}
	}

	if shouldRefresh {
		_ = refreshUsed
		if st.FreeRefresh <= 0 && totalRem > 0 {
			p := 1.0 - pow1m(float64(curG1Rem)/float64(totalRem), 3)
			expectedGain := p * 8.0
			if st.Lingxi < s.LowLingxiFloor && best.kind != KindOffRhyme {
				shouldRefresh = false
			} else if expectedGain < float64(s.RefreshCost)+s.RefreshGainFloor && best.kind != KindOffRhyme {
				shouldRefresh = false
			}
		}
	}

	if shouldRefresh {
		if st.FreeRefresh > 0 {
			return Decision{Action: "refresh", PickIdx: -1}
		}
		if st.Lingxi >= st.Cost+s.RefreshCost {
			return Decision{Action: "refresh", PickIdx: -1}
		}
	}

	// 不构建 Desc 调试串(热路径, sim 内不读), 避免 fmt.Sprintf 开销。
	return Decision{Action: "select", PickIdx: best.idx, Kind: best.kind, XiangZhan: best.xiangZhan}
}

// RunSimFast 串行跑 n 局(优化版)。
func (l *Library) RunSimFast(cfg SimConfig, s Strategy, n int, seed int64) SimStats {
	rng := rand.New(rand.NewSource(seed))
	turns := make([]int, 0, n)
	var sumT, sumS, sumL, sumO, sumR, sumC float64
	for i := 0; i < n; i++ {
		r := l.RunSimOnceFast(cfg, s, rng)
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
