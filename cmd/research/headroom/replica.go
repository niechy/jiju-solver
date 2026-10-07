package main

// replica.go: BaselineAction 的行为等价复刻 (含规则级消融开关)。
//
// 目的: features/jiju 的 BaselineAction 无法通过 Strategy 字段关闭
// "最优候选出韵 → 必刷" 这类硬规则。这里按 world.go:406-493 逐行复刻,
// 并用 -mode parity 与真实 BaselineAction 逐状态比对 (必须 100% 一致),
// 之后才允许用它做规则级消融。
//
// 复刻只使用导出 API: jiju.Judge / jiju.PoetFirstRewards / World.CandidateIDs。

import (
	"math/rand"

	"jiju-solver/jiju"
)

type repAblate struct {
	NoRefresh       bool // 完全禁止刷新
	NoOffRefresh    bool // 关闭规则1: 最优候选出韵 → 刷
	NoMiddleRefresh bool // 关闭规则2: 最优候选中间态且当前组未枯竭 → 刷
	NoPoolRefresh   bool // 关闭规则3: 换组时下家池过小 → 刷
	FreeOnly        bool // 只有免费刷新才允许刷
}

type repState struct {
	left      []int
	blocked   map[int]bool
	blockedN  int
	groups    map[int]int
	seen      map[int]bool
	prev      int
	cands     []int
	lingxi    int
	cost      int
	free      int
	freeLimit int
	offRun    int
	done      bool
}

type repCtx struct {
	vb            map[int]*jiju.Verse
	order         []int // 库内诗句 ID (用于构造 left)
	payOnEntry    bool
	refreshCost   int
	minCands      int
	middleClears  bool
	drawRound     bool
}

func newRepCtx(sc *scenario, rules jiju.WorldRules) *repCtx {
	return &repCtx{vb: sc.VB, order: sc.Order, payOnEntry: rules.PayOnEntry, refreshCost: rules.RefreshCost,
		minCands: rules.MinCandidates, middleClears: rules.MiddleClearsRun, drawRound: rules.DrawMode == jiju.DrawRound}
}

// newRepState 从 (spec, 真实 root state) 初始化复刻状态。调用时机: Start() 之后。
func newRepState(rc *repCtx, spec jiju.WorldStart, w *jiju.World, st *jiju.WorldState) *repState {
	coll := map[int]bool{spec.PrevID: true}
	for _, id := range spec.Collected {
		coll[id] = true
	}
	elig := map[int]bool{}
	explicit := spec.Eligible != nil
	for _, id := range spec.Eligible {
		elig[id] = true
	}
	rs := &repState{blocked: map[int]bool{}, groups: map[int]int{}, seen: map[int]bool{},
		prev: spec.PrevID, cost: spec.Cost, freeLimit: spec.FreeLimit, offRun: spec.OffRun,
		free: spec.FreeRemaining, lingxi: spec.Lingxi}
	for _, id := range rc.order {
		if coll[id] {
			continue
		}
		if explicit && !elig[id] {
			continue
		}
		rs.left = append(rs.left, id)
		if v := rc.vb[id]; v != nil {
			for _, g := range v.Group1 {
				rs.groups[g]++
			}
		}
	}
	for id := range coll {
		if v := rc.vb[id]; v != nil && v.Poet != 0 {
			rs.seen[v.Poet] = true
		}
	}
	for _, p := range spec.SeenPoets {
		rs.seen[p] = true
	}
	// Start() 在 Candidates 为空且未终局时调用 begin(): 扣除本轮入场费并重置免费刷新。
	if len(spec.Candidates) == 0 && !st.Done {
		if rc.payOnEntry {
			rs.lingxi -= rs.cost
		}
		rs.free = rs.freeLimit
	}
	// 已在一轮内: DrawRound 下 Displayed 在 left 内的会被标记为 blocked。
	if len(spec.Candidates) > 0 && rc.drawRound {
		in := map[int]bool{}
		for _, id := range rs.left {
			in[id] = true
		}
		for _, id := range spec.Displayed {
			if in[id] && !rs.blocked[id] {
				rs.blocked[id] = true
				rs.blockedN++
			}
		}
	}
	// 灵犀/消耗以真实 state 为准 (导出字段)。
	rs.lingxi = st.Lingxi
	rs.cost = st.Cost
	rs.done = st.Done
	rs.cands = w.CandidateIDs(st)
	return rs
}

func pow1m(x float64, k int) float64 {
	r := 1.0
	for i := 0; i < k; i++ {
		r *= 1 - x
	}
	return r
}

func (rs *repState) canDraw(rc *repCtx) bool {
	n := len(rs.left) - rs.blockedN
	for _, c := range rs.cands {
		if !rs.blocked[c] {
			n--
		}
	}
	return n >= rc.minCands
}

func (rs *repState) legalRefresh(rc *repCtx) bool {
	minBalance := rc.refreshCost
	if !rc.payOnEntry {
		minBalance += rs.cost
	}
	return (rs.free > 0 || rs.lingxi >= minBalance) && rs.canDraw(rc)
}

// repDecide 复刻 BaselineAction; 返回 (动作, 触发刷新的规则名)。
func repDecide(rs *repState, s jiju.Strategy, ab repAblate, rc *repCtx) (int, string) {
	a, why, _, _, _ := repDecideEx(rs, s, ab, rc)
	return a, why
}

// repDecideEx 额外返回三条硬规则与灵犀闸是否触发, 用于归因诊断。
func repDecideEx(rs *repState, s jiju.Strategy, ab repAblate, rc *repCtx) (int, string, bool, bool, bool) {
	if rs.done || len(rs.cands) == 0 {
		return -1, "done", false, false, false
	}
	prevV := rc.vb[rs.prev]
	cur := 0
	if prevV != nil && len(prevV.Group1) > 0 {
		cur = rs.groups[prevV.Group1[0]]
	}
	threshold := int(float64(len(rs.left)) * s.SwitchThreshPct)
	best, bestScore := -1, -1e100
	bestKind, bestPool := jiju.KindOffRhyme, 0
	for idx, i := range rs.cands {
		v := rc.vb[i]
		if v == nil {
			continue
		}
		kind, xz, _ := jiju.Judge(prevV, v)
		newPoet := v.Poet != 0 && !rs.seen[v.Poet]
		net := v.Lingxi - rs.cost
		shiyi := v.Shiyi
		if kind == jiju.KindRhyme {
			net += 7
		}
		if xz {
			net += 2
		} else {
			shiyi -= 5
		}
		pr := jiju.PoetFirstRewards[v.Poet]
		if newPoet {
			net += pr.Lingxi
			shiyi += pr.Shiyi
		}
		score := float64(net)*s.NetGainWeight + float64(shiyi)*s.ShiyiWeight
		if kind == jiju.KindRhyme {
			score += s.RhymeBonus
			if xz {
				score += s.XiangZhanBonus
			}
		} else if kind == jiju.KindMiddle {
			score += s.MiddleBonus
		} else {
			score -= s.OffRhymePenalty
			if rs.offRun >= 1 {
				score -= s.OffRhymePenalty
			}
		}
		if newPoet {
			score += s.NewPoetBonus + float64(pr.Lingxi)*s.NetGainWeight*0.5
			if pr.FreeRefresh > 0 {
				score += s.RefreshPoetBonus
			}
		}
		if net >= s.RichThreshold {
			score += s.RichBonus
		}
		pool := 0
		for _, g := range v.Group1 {
			if rs.groups[g] > pool {
				pool = rs.groups[g]
			}
		}
		if cur <= threshold && kind == jiju.KindMiddle {
			score += float64(pool) * s.TargetPoolWeight
			if newPoet {
				score += s.TargetNewPoetWeight
			}
		}
		if score > bestScore {
			best, bestScore, bestKind, bestPool = idx, score, kind, pool
		}
	}
	if best < 0 {
		return -1, "done", false, false, false
	}
	rawOff := bestKind == jiju.KindOffRhyme
	rawMid := bestKind == jiju.KindMiddle && cur > s.DryGroupFloor
	rawPool := cur <= threshold && bestKind != jiju.KindRhyme && bestPool < s.MinTargetPool
	rOff := rawOff && !ab.NoOffRefresh
	rMid := rawMid && !ab.NoMiddleRefresh
	rPool := rawPool && !ab.NoPoolRefresh
	refresh := rOff || rMid || rPool
	why := ""
	switch {
	case rOff:
		why = "规则1:最优出韵"
	case rMid:
		why = "规则2:中间态"
	case rPool:
		why = "规则3:下家池小"
	}
	if ab.NoRefresh {
		refresh, why = false, ""
	}
	if ab.FreeOnly && rs.free <= 0 {
		refresh, why = false, ""
	}
	if refresh && rs.free <= 0 && bestKind != jiju.KindOffRhyme {
		balance := rs.lingxi
		if rc.payOnEntry {
			balance += rs.cost
		}
		p := 1 - pow1m(float64(cur)/float64(len(rs.left)), 3)
		if balance < s.LowLingxiFloor || p*8 < float64(rc.refreshCost)+s.RefreshGainFloor {
			refresh, why = false, ""
		}
	}
	if refresh && rs.legalRefresh(rc) {
		return jiju.RefreshAction, why, rawOff, rawMid, rawPool
	}
	return best, "", rawOff, rawMid, rawPool
}

// advance 按 world.go 的语义推进复刻状态, 并与真实 state 对齐候选。
func (rs *repState) advance(rc *repCtx, w *jiju.World, st *jiju.WorldState, a int) {
	if a < 0 {
		return
	}
	if a == jiju.RefreshAction {
		if rs.free > 0 {
			rs.free--
		} else {
			rs.lingxi -= rc.refreshCost
		}
		for _, c := range rs.cands {
			if !rs.blocked[c] {
				rs.blocked[c] = true
				rs.blockedN++
			}
		}
		rs.cands = w.CandidateIDs(st)
		return
	}
	if a >= len(rs.cands) {
		return
	}
	pick := rs.cands[a]
	pv := rc.vb[rs.prev]
	v := rc.vb[pick]
	kind, xz, _ := jiju.Judge(pv, v)
	if !rc.payOnEntry {
		rs.lingxi -= rs.cost
	}
	rs.lingxi += v.Lingxi
	if kind == jiju.KindRhyme {
		rs.lingxi += 7
	}
	if xz {
		rs.lingxi += 2
	}
	if kind == jiju.KindOffRhyme {
		rs.offRun++
		if rs.offRun >= 2 {
			rs.cost++
			rs.offRun = 0
		}
	} else if kind == jiju.KindRhyme || rc.middleClears {
		// 注意: 中间态在 MiddleClearsRun=false 时 **不** 清除连错计数。
		rs.offRun = 0
	}
	if v.Poet != 0 && !rs.seen[v.Poet] {
		pr := jiju.PoetFirstRewards[v.Poet]
		rs.lingxi += pr.Lingxi
		rs.freeLimit += pr.FreeRefresh
		rs.seen[v.Poet] = true
	}
	// 从 left 移除
	for i, id := range rs.left {
		if id == pick {
			rs.left[i] = rs.left[len(rs.left)-1]
			rs.left = rs.left[:len(rs.left)-1]
			break
		}
	}
	for _, g := range v.Group1 {
		rs.groups[g]--
	}
	rs.prev = pick
	// begin()
	rs.cands = nil
	if rs.lingxi < rs.cost || len(rs.left) < rc.minCands {
		rs.done = true
		return
	}
	if rc.payOnEntry {
		rs.lingxi -= rs.cost
	}
	rs.free = rs.freeLimit
	rs.blocked = map[int]bool{}
	rs.blockedN = 0
	rs.cands = w.CandidateIDs(st)
	rs.done = st.Done
	// 以真实 state 的权威值校正灵犀/消耗, 消除任何记账漂移。
	rs.lingxi = st.Lingxi
	rs.cost = st.Cost
}

// repRollout 用复刻策略跑完整一局 (真实 World 负责随机与合法性)。
func repRollout(w *jiju.World, sc *scenario, s jiju.Strategy, ab repAblate, rng *rand.Rand) (shiyi, turns, refreshes, offRhymes int, mismatch int) {
	rc := newRepCtx(sc, sc.Rules)
	st, err := w.Start(sc.Spec, rng)
	if err != nil {
		panic(err)
	}
	rs := newRepState(rc, sc.Spec, w, st)
	s0, t0 := st.Shiyi, st.Turns
	r0, o0 := st.Refreshes, st.OffRhymes
	for !st.Done {
		a, _ := repDecide(rs, s, ab, rc)
		real := w.BaselineAction(st, s)
		if a != real {
			mismatch++
		}
		if a < 0 || !w.Legal(st, a) {
			a = real
		}
		if a < 0 {
			break
		}
		if err := w.Step(st, a, rng); err != nil {
			panic(err)
		}
		rs.advance(rc, w, st, a)
	}
	return st.Shiyi - s0, st.Turns - t0, st.Refreshes - r0, st.OffRhymes - o0, mismatch
}
