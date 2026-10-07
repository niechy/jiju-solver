// 集句蒙特卡洛模拟 + 策略基准测试。
// 完全复用 verse.go 的 Judge/Library 与 strategy.go 的 Decide/ApplySelect,
// 只把"随机抽3候选 / 刷新重抽"抽象出来离线跑 N 局, 用于:
//   1. 评估任意一套 Strategy 权重的期望集句数/诗意/灵犀
//   2. 随机/网格搜索权重 (见 cmd/jijusim)
// 与参考实现的关键差异(均按实测规则修正):
//   - 押韵 = Group1 有交集(支持 [1,3] 双韵斜字句), 参考版是单值相等(偏严)
//   - 免费刷新 = 每轮重置到上限(温/鱼首收永久+1上限), 参考版一致
//   - 出韵连错 2 次消耗永久+1, 参考版一致
//   - 诗人首收奖励(CSV lingxi_from_poet/shiyi_from_poet)接入
package jiju

import (
	"math/rand"
	"sort"
)

// SimConfig 一局模拟的初始条件(与游戏一致)。
type SimConfig struct {
	InitialLingxi      int   // 初始灵犀
	InitialCost        int   // 初始集句消耗
	InitialFreeRefresh int   // 初始免费刷新上限
	InitialVerseIDs    []int // 开局已收集诗句(如剧情送的)
	MaxTurns           int   // 轮数上限(防御)
}

// DefaultSimConfig 与 verse_ref 对齐的默认初始条件。
func DefaultSimConfig() SimConfig {
	return SimConfig{
		InitialLingxi:      25,
		InitialCost:        6,
		InitialFreeRefresh: 1,
		InitialVerseIDs:    []int{369},
		MaxTurns:           1000,
	}
}

// SimResult 一局模拟的产出指标。
type SimResult struct {
	Turns      int // 集句数(含初始)
	Shiyi      int // 总诗意
	LingxiLeft int // 结束剩余灵犀
	CostEnd    int // 结束时消耗
	OffRhymes  int // 全程出韵次数
	Refreshes  int // 全程刷新次数
	DieReason  string
}

// simState 模拟内部状态(在 GameState 基础上补充诗意/上限/初始)。
type simState struct {
	gs             *GameState
	maxFreeRefresh int
	totalShiyi     int
	offRhymes      int
	verseByID      map[int]*Verse
	last           *Verse
	refreshes      int
}

// RunSimOnce 用策略 s 跑一局, 返回指标。
func (l *Library) RunSimOnce(cfg SimConfig, s Strategy, rng *rand.Rand) SimResult {
	byID := make(map[int]*Verse, len(l.verses))
	for i := range l.verses {
		byID[l.verses[i].ID] = &l.verses[i]
	}
	st := NewGameState(cfg.InitialCost, cfg.InitialLingxi, cfg.InitialFreeRefresh)
	ss := &simState{gs: st, maxFreeRefresh: cfg.InitialFreeRefresh, verseByID: byID}

	// 初始诗句结算
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
			ss.totalShiyi -= 5 // 相邻同平仄
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
		// 抽 3 候选; 刷新则向后取 3
		rng.Shuffle(len(avail), func(i, j int) { avail[i], avail[j] = avail[j], avail[i] })
		poolIdx := 0
		refreshUsed := 0
		var picked *Verse
		var kind JudgeKind
		var xz bool

		for {
			if poolIdx+3 > len(avail) {
				// 无足够候选可刷, 直接选当前最优(若有)否则结束
				if poolIdx >= len(avail) {
					return ss.result(turns)
				}
			}
			n := 3
			if poolIdx+n > len(avail) {
				n = len(avail) - poolIdx
			}
			cands := make([]*Verse, n)
			copy(cands, avail[poolIdx:poolIdx+n])

			dec := l.Decide(ss.last, cands, st, s, refreshUsed)
			if dec.Action == "refresh" {
				refreshUsed++
				ss.refreshes++
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

		// 结算
		prevG2 := ss.last.Group2
		isNewPoet := picked.Poet != 0 && !st.SeenPoets[picked.Poet]
		l.ApplySelect(st, ss.last, picked, kind, xz) // ApplySelect 已发放诗人首收灵犀
		if kind == KindOffRhyme {
			ss.offRhymes++
		}
		// 诗意
		ss.totalShiyi += picked.Shiyi
		if isNewPoet {
			pr := PoetFirstRewards[picked.Poet]
			ss.totalShiyi += pr.Shiyi
			// 灵犀已由 ApplySelect 发放, 此处不重复
			if pr.FreeRefresh > 0 {
				ss.maxFreeRefresh += pr.FreeRefresh
			}
		}
		if intSliceEqual(prevG2, picked.Group2) {
			ss.totalShiyi -= 5
		}
		ss.last = picked
		turns++
		st.FreeRefresh = ss.maxFreeRefresh // 每轮重置到上限
	}
	return ss.result(turns)
}

func (ss *simState) result(turns int) SimResult {
	return SimResult{
		Turns:      turns,
		Shiyi:      ss.totalShiyi,
		LingxiLeft: ss.gs.Lingxi,
		CostEnd:    ss.gs.Cost,
		OffRhymes:  ss.offRhymes,
		Refreshes:  ss.refreshes,
	}
}

func (v *Verse) self(byID map[int]*Verse) *Verse { return v }

// available 未收集列表(模拟用, 每次重建以便 shuffle)。
func (l *Library) available(collected map[int]bool) []*Verse {
	out := make([]*Verse, 0, len(l.verses)-len(collected))
	for i := range l.verses {
		if !collected[l.verses[i].ID] {
			out = append(out, &l.verses[i])
		}
	}
	return out
}

// SimStats 多局统计。
type SimStats struct {
	N          int
	MeanTurns  float64
	P5, P50, P95 int // 集句数分位
	MeanShiyi  float64
	MeanLingxi float64
	MeanOff    float64
	MeanRef    float64 // 平均刷新次数
	MeanCostEnd float64
}

// RunSim 跑 n 局并汇总。
func (l *Library) RunSim(cfg SimConfig, s Strategy, n int, seed int64) SimStats {
	rng := rand.New(rand.NewSource(seed))
	turns := make([]int, 0, n)
	var sumT, sumS, sumL, sumO, sumR, sumC float64
	for i := 0; i < n; i++ {
		r := l.RunSimOnce(cfg, s, rng)
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
		i := int(p * float64(n-1))
		return turns[i]
	}
	return SimStats{
		N: n, MeanTurns: sumT / float64(n),
		P5: pct(0.05), P50: pct(0.5), P95: pct(0.95),
		MeanShiyi: sumS / float64(n), MeanLingxi: sumL / float64(n), MeanOff: sumO / float64(n),
		MeanRef: sumR / float64(n), MeanCostEnd: sumC / float64(n),
	}
}
