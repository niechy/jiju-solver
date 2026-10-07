// jijuresearch_c —— 集句决策表示研究用的一次性实验 CLI。
//
// 目的: 在同一协议(同一初始状态 / 同一 episode 种子 / 同一配对随机数)下,
// 把"当前冠军"(jiju.MultiRollout + shiyi-first + 线性启发式续跑)与若干
// 决策表示(线性复刻 / 精确最优停止 / 非线性概率特征)做配对比较。
//
// 本包只使用 features/jiju 的导出 API, 不修改任何既有文件。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jiju-solver/jiju"
)

const maxBits = 1024
const nWords = maxBits / 64 // 16
const poetWords = 4         // 最多 256 位诗人

// ---------------------------------------------------------------- 环境 / 特征表

type env struct {
	w        *jiju.World
	vs       []jiju.Verse
	idx      map[int]int
	poetIdx  map[int]int
	n        int
	pairKind []uint8    // 打包 kind | (xz?4:0), 索引 prev*n+cand
	adjRhyme [][]uint64 // 与 i 押韵(Group1 有交集)的句位集合
	adjMid   [][]uint64 // 与 i 构成中间态(Group3 有交集且 Group1 无交集)
	eligIdx  []int      // 可抽池的句位(不含已收集)
	poetOf   []int      // 句位 -> 诗人位数(-1 表示无)
	spec     jiju.WorldStart
	rules    jiju.WorldRules
}

func newEnv(lib *jiju.Library, spec jiju.WorldStart, rules jiju.WorldRules) (*env, error) {
	vs := lib.Verses()
	e := &env{vs: vs, n: len(vs), idx: map[int]int{}, poetIdx: map[int]int{}, rules: rules}
	if e.n > maxBits {
		return nil, fmt.Errorf("library too large for scratch bitsets: %d", e.n)
	}
	for i := range vs {
		e.idx[vs[i].ID] = i
		if p := vs[i].Poet; p != 0 {
			if _, ok := e.poetIdx[p]; !ok {
				if len(e.poetIdx) >= poetWords*64 {
					return nil, fmt.Errorf("too many poets")
				}
				e.poetIdx[p] = len(e.poetIdx)
			}
		}
	}
	e.poetOf = make([]int, e.n)
	for i := range vs {
		if p := vs[i].Poet; p != 0 {
			e.poetOf[i] = e.poetIdx[p]
		} else {
			e.poetOf[i] = -1
		}
	}
	e.pairKind = make([]uint8, e.n*e.n)
	e.adjRhyme = make([][]uint64, e.n)
	e.adjMid = make([][]uint64, e.n)
	for i := 0; i < e.n; i++ {
		e.adjRhyme[i] = make([]uint64, nWords)
		e.adjMid[i] = make([]uint64, nWords)
		for j := 0; j < e.n; j++ {
			k, xz, _ := jiju.Judge(&vs[i], &vs[j])
			v := uint8(k)
			if xz {
				v |= 4
			}
			e.pairKind[i*e.n+j] = v
			switch jiju.JudgeKind(v & 3) {
			case jiju.KindRhyme:
				e.adjRhyme[i][j>>6] |= 1 << uint(j&63)
			case jiju.KindMiddle:
				e.adjMid[i][j>>6] |= 1 << uint(j&63)
			}
		}
	}
	elig := map[int]bool{}
	if spec.Eligible == nil {
		for i := range vs {
			elig[i] = true
		}
	} else {
		for _, id := range spec.Eligible {
			ii, ok := e.idx[id]
			if !ok {
				return nil, fmt.Errorf("unknown eligible verse %d", id)
			}
			elig[ii] = true
		}
	}
	for i := 0; i < e.n; i++ {
		if elig[i] {
			e.eligIdx = append(e.eligIdx, i)
		}
	}
	e.spec = spec
	return e, nil
}

func (e *env) bit(b []uint64, i int) bool { return b[i>>6]&(1<<uint(i&63)) != 0 }
func setBit(b []uint64, i int)            { b[i>>6] |= 1 << uint(i&63) }

func popAnd(a, b []uint64) int {
	c := 0
	for i := range a {
		c += bitsOn(a[i] & b[i])
	}
	return c
}

func bitsOn(x uint64) int {
	c := 0
	for x != 0 {
		x &= x - 1
		c++
	}
	return c
}

// ---------------------------------------------------------------- 状态跟踪器

// track 用位集精确复刻 WorldState 的已收集 / 本轮已展示 / 已见诗人 / 各组剩余。
// 值类型, 克隆即复制, 用于 rollout 内部。
type track struct {
	collected [nWords]uint64
	blocked   [nWords]uint64
	seenPoet  [poetWords]uint64
	groups    [15]int
	left      int
	prevIdx   int
	cands     [3]int
	ncand     int
}

func (e *env) newTrack(st *jiju.WorldState) *track {
	t := &track{}
	setBit(t.collected[:], e.idx[e.spec.PrevID])
	for _, id := range e.spec.Collected {
		setBit(t.collected[:], e.idx[id])
	}
	t.prevIdx = e.idx[e.spec.PrevID]
	for i := 0; i < e.n; i++ {
		if e.bit(t.collected[:], i) {
			if p := e.poetOf[i]; p >= 0 {
				setBit(t.seenPoet[:], p)
			}
		}
	}
	for _, id := range e.spec.SeenPoets {
		setBit(t.seenPoet[:], e.poetIdx[id])
	}
	for _, i := range e.eligIdx {
		if !e.bit(t.collected[:], i) {
			t.left++
			for _, g := range e.vs[i].Group1 {
				t.groups[g]++
			}
		}
	}
	ids := e.w.CandidateIDs(st)
	for k, id := range ids {
		t.cands[k] = e.idx[id]
	}
	t.ncand = len(ids)
	return t
}

func (e *env) stepTracked(t *track, st *jiju.WorldState, action int, rng *rand.Rand) error {
	if err := e.w.Step(st, action, rng); err != nil {
		return err
	}
	after := e.w.CandidateIDs(st)
	if action == jiju.RefreshAction {
		for k := 0; k < t.ncand; k++ {
			setBit(t.blocked[:], t.cands[k])
		}
	} else {
		vi := t.cands[action]
		setBit(t.collected[:], vi)
		t.left--
		for _, g := range e.vs[vi].Group1 {
			t.groups[g]--
		}
		if p := e.poetOf[vi]; p >= 0 {
			setBit(t.seenPoet[:], p)
		}
		t.prevIdx = vi
		t.blocked = [nWords]uint64{}
	}
	t.cands = [3]int{}
	for k, id := range after {
		t.cands[k] = e.idx[id]
	}
	t.ncand = len(after)
	return nil
}

// ---------------------------------------------------------------- 权重

// nlW: 线性冠军权重 + 若干非线性附加项。
type nlW struct {
	NetGainWeight       float64 `json:"NetGainWeight"`
	RhymeBonus          float64 `json:"RhymeBonus"`
	XiangZhanBonus      float64 `json:"XiangZhanBonus"`
	NewPoetBonus        float64 `json:"NewPoetBonus"`
	RefreshPoetBonus    float64 `json:"RefreshPoetBonus"`
	RichThreshold       float64 `json:"RichThreshold"`
	RichBonus           float64 `json:"RichBonus"`
	ShiyiWeight         float64 `json:"ShiyiWeight"`
	OffRhymePenalty     float64 `json:"OffRhymePenalty"`
	MiddleBonus         float64 `json:"MiddleBonus"`
	SwitchThreshPct     float64 `json:"SwitchThreshPct"`
	TargetPoolWeight    float64 `json:"TargetPoolWeight"`
	TargetNewPoetWeight float64 `json:"TargetNewPoetWeight"`
	MinTargetPool       float64 `json:"MinTargetPool"`
	DryGroupFloor       float64 `json:"DryGroupFloor"`
	LowLingxiFloor      float64 `json:"LowLingxiFloor"`
	RefreshGainFloor    float64 `json:"RefreshGainFloor"`
	// ---- 非线性附加项(线性模型无法表达) ----
	HitProbWeight  float64 `json:"HitProbWeight"`     // × P(下一批出现押韵)
	OffProbPenalty float64 `json:"OffProbPenalty"`    // × P(下一批全出韵)
	DensityWeight  float64 `json:"DensityWeight"`     // × 剩余池中与该句押韵的占比
	StopMargin     float64 `json:"StopMargin"`        // 最优停止的接受边际(分)
	RefreshCostW   float64 `json:"RefreshCostWeight"` // 付费刷新在打分单位下的成本(默认 = NetGainWeight)
}

func defaultW() nlW {
	s := jiju.DefaultStrategy()
	return nlW{
		NetGainWeight: s.NetGainWeight, RhymeBonus: s.RhymeBonus, XiangZhanBonus: s.XiangZhanBonus,
		NewPoetBonus: s.NewPoetBonus, RefreshPoetBonus: s.RefreshPoetBonus,
		RichThreshold: float64(s.RichThreshold), RichBonus: s.RichBonus, ShiyiWeight: s.ShiyiWeight,
		OffRhymePenalty: s.OffRhymePenalty, MiddleBonus: s.MiddleBonus,
		SwitchThreshPct: s.SwitchThreshPct, TargetPoolWeight: s.TargetPoolWeight,
		TargetNewPoetWeight: s.TargetNewPoetWeight, MinTargetPool: float64(s.MinTargetPool),
		DryGroupFloor: float64(s.DryGroupFloor), LowLingxiFloor: float64(s.LowLingxiFloor),
		RefreshGainFloor: s.RefreshGainFloor, RefreshCostW: s.NetGainWeight,
	}
}

func loadW(path string) nlW {
	W := defaultW()
	if path == "" {
		return W
	}
	raw, err := os.ReadFile(path)
	check(err)
	var m map[string]float64
	check(json.Unmarshal(raw, &m))
	b, _ := json.Marshal(W)
	var all map[string]float64
	check(json.Unmarshal(b, &all))
	for k, v := range m {
		if _, ok := all[k]; ok {
			all[k] = v
		}
	}
	b, _ = json.Marshal(all)
	check(json.Unmarshal(b, &W))
	return W
}

// ---------------------------------------------------------------- 打分与续跑

type scratch struct {
	pool []int
	vals []float64
}

func (e *env) curGroup(t *track) int {
	pg := e.vs[t.prevIdx].Group1
	if len(pg) == 0 {
		return 0
	}
	return t.groups[pg[0]]
}

// score 复刻 world.go 的 BaselineAction 打分, 可选叠加非线性特征。
func (e *env) score(t *track, st *jiju.WorldState, W nlW, vi int, extras bool) float64 {
	v := &e.vs[vi]
	pk := e.pairKind[t.prevIdx*e.n+vi]
	kind, xz := jiju.JudgeKind(pk&3), pk&4 != 0
	newPoet := v.Poet != 0 && !e.bit(t.seenPoet[:], e.poetOf[vi])
	net := v.Lingxi - st.Cost
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
	sc := float64(net)*W.NetGainWeight + float64(shiyi)*W.ShiyiWeight
	if kind == jiju.KindRhyme {
		sc += W.RhymeBonus
		if xz {
			sc += W.XiangZhanBonus
		}
	} else if kind == jiju.KindMiddle {
		sc += W.MiddleBonus
	} else {
		sc -= W.OffRhymePenalty
		if st.OffRun >= 1 {
			sc -= W.OffRhymePenalty
		}
	}
	if newPoet {
		sc += W.NewPoetBonus + float64(pr.Lingxi)*W.NetGainWeight*0.5
		if pr.FreeRefresh > 0 {
			sc += W.RefreshPoetBonus
		}
	}
	if net >= int(W.RichThreshold) {
		sc += W.RichBonus
	}
	cur := e.curGroup(t)
	threshold := int(float64(t.left) * W.SwitchThreshPct)
	if cur <= threshold && kind == jiju.KindMiddle {
		pool := 0
		for _, g := range v.Group1 {
			if t.groups[g] > pool {
				pool = t.groups[g]
			}
		}
		sc += float64(pool) * W.TargetPoolWeight
		if newPoet {
			sc += W.TargetNewPoetWeight
		}
	}
	if extras {
		r, m := e.afterCounts(t, vi)
		n := t.left - 1
		if n > 0 {
			sc += W.HitProbWeight * hitProb(n, r, 3)
			sc -= W.OffProbPenalty * offProb(n, r, m, 3)
			sc += W.DensityWeight * float64(r) / float64(n)
		}
	}
	return sc
}

// afterCounts: 假定选中 vi 之后(该轮结束, blocked 清空), 池中与 vi 押韵 / 中间态的句数。
func (e *env) afterCounts(t *track, vi int) (r, m int) {
	for i := 0; i < nWords; i++ {
		w := e.adjRhyme[vi][i] &^ t.collected[i]
		w2 := e.adjMid[vi][i] &^ t.collected[i]
		if vi>>6 == i {
			w &^= 1 << uint(vi&63)
			w2 &^= 1 << uint(vi&63)
		}
		r += bitsOn(w)
		m += bitsOn(w2)
	}
	return
}

func hitProb(n, r, k int) float64 {
	if n < k || k <= 0 || r <= 0 {
		return 0
	}
	if n-r < k {
		return 1
	}
	num, den := 1.0, 1.0
	for i := 0; i < k; i++ {
		num *= float64(n - r - i)
		den *= float64(n - i)
	}
	return 1 - num/den
}

func offProb(n, r, m, k int) float64 {
	if n < k || k <= 0 {
		return 0
	}
	off := n - r - m
	if off < k {
		return 0
	}
	num, den := 1.0, 1.0
	for i := 0; i < k; i++ {
		num *= float64(off - i)
		den *= float64(n - i)
	}
	return num / den
}

func pow1m(x float64, k int) float64 {
	r := 1.0
	for i := 0; i < k; i++ {
		r *= 1 - x
	}
	return r
}

// refreshPool 返回"刷新后会从中抽句"的句位集合
// (排除已收集 / 本轮已展示 / 当前候选)。
func (e *env) refreshPool(t *track, sc *scratch) []int {
	sc.pool = sc.pool[:0]
	for _, i := range e.eligIdx {
		if e.bit(t.collected[:], i) || e.bit(t.blocked[:], i) {
			continue
		}
		dup := false
		for k := 0; k < t.ncand; k++ {
			if t.cands[k] == i {
				dup = true
				break
			}
		}
		if !dup {
			sc.pool = append(sc.pool, i)
		}
	}
	return sc.pool
}

// ---------------------------------------------------------------- 续跑策略(4 种表示)

const (
	modeLin   = 0 // 线性复刻: 完全复刻 BaselineAction(含刷新门控级联)
	modeStop  = 1 // 线性打分 + 精确最优停止刷新规则
	modeNL    = 2 // 非线性特征 + 精确最优停止
	modeLinNL = 3 // 非线性特征 + 原门控级联
)

func (e *env) continueAction(t *track, st *jiju.WorldState, W nlW, mode int, sc *scratch) int {
	if st.Done || t.ncand == 0 {
		return -1
	}
	if !disableExact && mode != modeLin && t.left <= 6 {
		if r := e.w.ExactMulti(context.Background(), st, 6, 200000,
			jiju.VerseObjective{Priority: "shiyi-first"}); r.Complete && r.Action >= 0 {
			return r.Action
		}
	}
	extras := mode == modeNL || mode == modeLinNL
	stop := mode == modeStop || mode == modeNL
	cur := e.curGroup(t)
	threshold := int(float64(t.left) * W.SwitchThreshPct)

	best, bestScore := -1, -1e100
	bestKind, bestPool := jiju.KindOffRhyme, 0
	for idx := 0; idx < t.ncand; idx++ {
		vi := t.cands[idx]
		s := e.score(t, st, W, vi, extras)
		kind := jiju.JudgeKind(e.pairKind[t.prevIdx*e.n+vi] & 3)
		pool := 0
		for _, g := range e.vs[vi].Group1 {
			if t.groups[g] > pool {
				pool = t.groups[g]
			}
		}
		if s > bestScore {
			best, bestScore, bestKind, bestPool = idx, s, kind, pool
		}
	}
	if best < 0 {
		return -1
	}

	if stop {
		if !e.w.Legal(st, jiju.RefreshAction) {
			return best
		}
		pool := e.refreshPool(t, sc)
		if len(pool) < 1 {
			return best
		}
		ev := e.emaxScore(t, st, W, pool, extras, sc)
		cost := 0.0
		if st.Free <= 0 {
			cost = W.RefreshCostW * float64(e.rules.RefreshCost)
		}
		if ev-cost > bestScore+W.StopMargin {
			return jiju.RefreshAction
		}
		return best
	}

	// mode 0 / 3: 原封不动的线性门控级联(world.go BaselineAction)
	refresh := bestKind == jiju.KindOffRhyme || bestKind == jiju.KindMiddle && cur > int(W.DryGroupFloor)
	refresh = refresh || cur <= threshold && bestKind != jiju.KindRhyme && bestPool < int(W.MinTargetPool)
	if refresh && st.Free <= 0 && bestKind != jiju.KindOffRhyme {
		balance := st.Lingxi
		if e.rules.PayOnEntry {
			balance += st.Cost
		}
		p := 1 - pow1m(float64(cur)/float64(t.left), 3)
		if balance < int(W.LowLingxiFloor) || p*8 < float64(e.rules.RefreshCost)+W.RefreshGainFloor {
			refresh = false
		}
	}
	if refresh && e.w.Legal(st, jiju.RefreshAction) {
		return jiju.RefreshAction
	}
	return best
}

// emaxScore: 从 pool 均匀无放回抽 min(3,|pool|) 句后最优句打分的期望。
// 精确次序统计量权: P(最大值排名 i) = C(i-1,k-1)/C(R,k)。
func (e *env) emaxScore(t *track, st *jiju.WorldState, W nlW, pool []int, extras bool, sc *scratch) float64 {
	R := len(pool)
	k := 3
	if R < k {
		k = R
	}
	if k <= 0 {
		return -1e100
	}
	if cap(sc.vals) < R {
		sc.vals = make([]float64, R)
	}
	vals := sc.vals[:R]
	for i, vi := range pool {
		vals[i] = e.score(t, st, W, vi, extras)
	}
	sort.Float64s(vals)
	switch k {
	case 1:
		s := 0.0
		for _, v := range vals {
			s += v
		}
		return s / float64(R)
	case 2:
		s, den := 0.0, float64(R)*float64(R-1)/2
		for i := 2; i <= R; i++ {
			s += vals[i-1] * float64(i-1) / den
		}
		return s
	default:
		s, den := 0.0, float64(R)*float64(R-1)*float64(R-2)/6
		for i := 3; i <= R; i++ {
			s += vals[i-1] * (float64(i-1) * float64(i-2) / 2) / den
		}
		return s
	}
}

// ---------------------------------------------------------------- 根决策

// myRoot: 用自己的续跑策略做 1-ply rollout(与 MultiRollout 同构)。
func (e *env) myRoot(t *track, st *jiju.WorldState, samples int, z float64, mode int, W nlW, seed int64) int {
	sc := &scratch{}
	base := e.continueAction(t, st, W, mode, sc)
	var acts []int
	for a := 0; a <= jiju.RefreshAction; a++ {
		if e.w.Legal(st, a) {
			acts = append(acts, a)
		}
	}
	if len(acts) <= 1 || samples <= 0 {
		return base
	}
	var sums, diffs, diffSq [4]float64
	seedRNG := rand.New(rand.NewSource(seed))
	n := 0
	for s := 0; s < samples; s++ {
		sd := seedRNG.Int63()
		var vals [4]float64
		ok := true
		for _, a := range acts {
			w2 := st.Clone()
			t2 := *t
			rng := rand.New(rand.NewSource(sd))
			if e.stepTracked(&t2, w2, a, rng) != nil {
				ok = false
				break
			}
			for !w2.Done {
				act := e.continueAction(&t2, w2, W, mode, sc)
				if act < 0 || e.stepTracked(&t2, w2, act, rng) != nil {
					break
				}
			}
			vals[a] = float64(w2.Shiyi - st.Shiyi)
		}
		if !ok {
			continue
		}
		n++
		for _, a := range acts {
			d := vals[a] - vals[base]
			sums[a] += vals[a]
			diffs[a] += d
			diffSq[a] += d * d
		}
	}
	if n == 0 {
		return base
	}
	fn := float64(n)
	best, bestMean := base, sums[base]/fn
	for _, a := range acts {
		m := sums[a] / fn
		if m > bestMean {
			se := pairedSE(diffs[a], diffSq[a], n)
			if z <= 0 || n > 1 && diffs[a]/fn > z*se {
				best, bestMean = a, m
			}
		}
	}
	return best
}

func pairedSE(sum, squares float64, n int) float64 {
	if n < 2 {
		return 0
	}
	c := float64(n)
	return math.Sqrt(math.Max(0, (squares-sum*sum/c)/(c-1)) / c)
}

// champRoot: 直接调用 jiju 的冠军实现(MultiRollout + shiyi-first)。
func (e *env) champRoot(st *jiju.WorldState, samples, policySamples int, z float64, seed int64) int {
	opt := jiju.DefaultRolloutOptions()
	opt.Samples = samples
	opt.MaxSamples = 0
	opt.Budget = 0
	opt.PolicySamples = policySamples
	opt.ConfidenceZ = z
	opt.Seed = seed
	opt.Base = jiju.DefaultStrategy()
	m, err := e.w.MultiRollout(context.Background(), st, opt, jiju.VerseObjective{Priority: "shiyi-first"})
	if err != nil {
		return e.w.BaselineAction(st, jiju.DefaultStrategy())
	}
	return m.Action
}

// ---------------------------------------------------------------- 策略规格与运行

type policy struct {
	label string
	kind  string
	p1    int
	p2    int
	p3    float64
	mode  int
	W     nlW
}

func parsePolicies(spec string, W nlW) ([]policy, error) {
	var out []policy
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		f := strings.Split(tok, ":")
		p := policy{label: tok, kind: f[0], W: W}
		num := func(i int, def float64) float64 {
			if i < len(f) {
				if v, err := strconv.ParseFloat(f[i], 64); err == nil {
					return v
				}
			}
			return def
		}
		switch f[0] {
		case "heur":
		case "champ":
			p.p1, p.p2, p.p3 = int(num(1, 128)), int(num(2, 32)), num(3, 1)
		case "my":
			p.mode = int(num(1, modeStop))
		case "myroll":
			p.p1, p.mode, p.p3 = int(num(1, 128)), int(num(2, modeStop)), num(3, 1)
		default:
			return nil, fmt.Errorf("unknown policy %q", tok)
		}
		out = append(out, p)
	}
	return out, nil
}

type epResult struct {
	count     float64
	shiyi     float64
	refresh   float64
	decisions float64
}

// runEval 在配对 episode 种子上并行评估一组策略, 返回 [episode][policy]。
func runEval(e *env, w *jiju.World, spec jiju.WorldStart, pols []policy,
	sims int, seed int64, workers int, verbose bool) [][]epResult {
	results := make([][]epResult, sims)
	var completed atomic.Int32
	started := time.Now()
	jobs := make(chan int)
	var wg sync.WaitGroup
	nw := min(workers, sims)
	if nw < 1 {
		nw = 1
	}
	for k := 0; k < nw; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ep := range jobs {
				sd := seed + int64(ep)*1000003
				rs := make([]epResult, len(pols))
				for pi, p := range pols {
					rng := rand.New(rand.NewSource(sd))
					st, err := w.Start(spec, rng)
					check(err)
					if st.Done {
						continue
					}
					t := e.newTrack(st)
					sc := &scratch{}
					decisions := 0
					for !st.Done {
						decisions++
						var a int
						switch p.kind {
						case "heur":
							a = w.BaselineAction(st, jiju.DefaultStrategy())
						case "champ":
							a = e.champRoot(st, p.p1, p.p2, p.p3, sd^int64(decisions)*32452843)
						case "my":
							a = e.continueAction(t, st, p.W, p.mode, sc)
						case "myroll":
							a = e.myRoot(t, st, p.p1, p.p3, p.mode, p.W, sd^int64(decisions)*32452843)
						}
						if a < 0 {
							break
						}
						check(e.stepTracked(t, st, a, rng))
						if decisions > 100000 {
							panic("runaway episode")
						}
					}
					rs[pi] = epResult{count: float64(st.Turns), shiyi: float64(st.Shiyi),
						refresh: float64(st.Refreshes), decisions: float64(decisions)}
				}
				results[ep] = rs
				if verbose {
					done := completed.Add(1)
					if int(done)%50 == 0 || int(done) == sims {
						fmt.Fprintf(os.Stderr, "  %d/%d episodes (%s)\n", done, sims, time.Since(started).Round(time.Second))
					}
				}
			}
		}()
	}
	for i := 0; i < sims; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// pairedDelta 返回 (平均差, 标准误) ; 以 results[i][0] 为参考。
func pairedDelta(results [][]epResult, pi int) (ds, ses, dc, sec float64) {
	n := len(results)
	var s1, s2, c1, c2 float64
	for i := 0; i < n; i++ {
		d := results[i][pi].shiyi - results[i][0].shiyi
		e := results[i][pi].count - results[i][0].count
		s1 += d
		s2 += d * d
		c1 += e
		c2 += e * e
	}
	fn := float64(n)
	return s1 / fn, pairedSE(s1, s2, n), c1 / fn, pairedSE(c1, c2, n)
}

func main() {
	libPath := flag.String("lib", "data/inscribe/inscribe_VerseConfig.json", "Verse library")
	snapPath := flag.String("snapshot", "screenshots/jiju-research-20260906/runtime-main.json", "WorldStart snapshot")
	sims := flag.Int("sims", 200, "Paired episodes")
	seed := flag.Int64("seed", 202609202, "Episode seed base")
	workers := flag.Int("workers", 16, "Parallel workers")
	outPath := flag.String("out", "", "Report JSON")
	polSpec := flag.String("policies", "champ:128:32:1", "Comma list of policy specs")
	checkN := flag.Int("check", 0, "Cross-check my linear replica against BaselineAction on N episodes")
	weightsJSON := flag.String("weights", "", "JSON overrides for the weights")
	noExact := flag.Bool("no-exact", false, "Disable endgame exact solver in my continuation")
	quiet := flag.Bool("quiet", false, "Suppress JSON on stdout")
	trace := flag.Bool("trace", false, "Print per-decision trace for episode 0")
	tuneSec := flag.Int("tune", 0, "Time-boxed weight search budget in seconds (0 = off)")
	tuneSims := flag.Int("tune-sims", 64, "Episodes per search generation")
	tuneMode := flag.Int("tune-mode", modeStop, "Continuation mode used as the search objective")
	weightsOut := flag.String("weights-out", "", "Write tuned weights JSON here")
	flag.Parse()
	_ = trace
	disableExact = *noExact

	W := loadW(*weightsJSON)
	pols, err := parsePolicies(*polSpec, W)
	check(err)

	libRaw, err := os.ReadFile(*libPath)
	check(err)
	lib, err := jiju.LoadLibrary(*libPath)
	check(err)
	snapRaw, err := os.ReadFile(*snapPath)
	check(err)
	var spec jiju.WorldStart
	check(json.Unmarshal(snapRaw, &spec))
	rules := jiju.DefaultWorldRules()
	w, err := jiju.NewWorld(lib, rules)
	check(err)
	e, err := newEnv(lib, spec, rules)
	check(err)
	e.w = w

	if *checkN > 0 {
		runCrossCheck(e, *checkN, *seed)
		return
	}

	if *tuneSec > 0 {
		tuned := runTuner(e, w, spec, W, *tuneSec, *tuneSims, *tuneMode, *seed, *workers)
		W = tuned
		pols, err = parsePolicies(*polSpec, W)
		check(err)
		b, _ := json.MarshalIndent(W, "", " ")
		fmt.Fprintf(os.Stderr, "tuned weights:\n%s\n", b)
		if *weightsOut != "" {
			check(os.WriteFile(*weightsOut, append(b, '\n'), 0o600))
		}
		if *outPath == "" {
			return
		}
	}

	started := time.Now()
	results := runEval(e, w, spec, pols, *sims, *seed, *workers, true)
	nw := min(*workers, *sims)

	type stat struct {
		Name         string    `json:"name"`
		ShiyiMean    float64   `json:"shiyi_mean"`
		CountMean    float64   `json:"count_mean"`
		RefreshMean  float64   `json:"refresh_mean"`
		DecisionMean float64   `json:"decision_mean"`
		ShiyiVals    []float64 `json:"shiyi_values"`
		CountVals    []float64 `json:"count_values"`
	}
	type cmp struct {
		Policy     string  `json:"policy"`
		DeltaShiyi float64 `json:"delta_shiyi"`
		SEShiyi    float64 `json:"se_shiyi"`
		DeltaCount float64 `json:"delta_count"`
		SECount    float64 `json:"se_count"`
		WinShiyi   int     `json:"win_shiyi"`
		N          int     `json:"n"`
	}
	stats := make([]stat, len(pols))
	for pi, p := range pols {
		stats[pi].Name = p.label
		stats[pi].ShiyiVals = make([]float64, *sims)
		stats[pi].CountVals = make([]float64, *sims)
		for i := 0; i < *sims; i++ {
			stats[pi].ShiyiVals[i] = results[i][pi].shiyi
			stats[pi].CountVals[i] = results[i][pi].count
			stats[pi].ShiyiMean += results[i][pi].shiyi / float64(*sims)
			stats[pi].CountMean += results[i][pi].count / float64(*sims)
			stats[pi].RefreshMean += results[i][pi].refresh / float64(*sims)
			stats[pi].DecisionMean += results[i][pi].decisions / float64(*sims)
		}
	}
	cmps := make([]cmp, 0, len(pols))
	for pi := 1; pi < len(pols); pi++ {
		var s1, s2, c1, c2 float64
		win := 0
		for i := 0; i < *sims; i++ {
			d := results[i][pi].shiyi - results[i][0].shiyi
			dc := results[i][pi].count - results[i][0].count
			s1 += d
			s2 += d * d
			c1 += dc
			c2 += dc * dc
			if d > 0 {
				win++
			}
		}
		cmps = append(cmps, cmp{
			Policy: pols[pi].label, DeltaShiyi: s1 / float64(*sims), SEShiyi: pairedSE(s1, s2, *sims),
			DeltaCount: c1 / float64(*sims), SECount: pairedSE(c1, c2, *sims),
			WinShiyi: win, N: *sims,
		})
	}
	rep := map[string]any{
		"sims": *sims, "seed": *seed, "snapshot": *snapPath, "weights": W,
		"library_sha256":  fmt.Sprintf("%x", sha256.Sum256(libRaw)),
		"snapshot_sha256": fmt.Sprintf("%x", sha256.Sum256(snapRaw)),
		"go_version":      runtime.Version(), "gomaxprocs": runtime.GOMAXPROCS(0), "workers": nw,
		"seconds":  time.Since(started).Seconds(),
		"policies": stats, "comparisons": cmps,
	}
	raw, _ := json.MarshalIndent(rep, "", " ")
	if *outPath != "" {
		check(os.WriteFile(*outPath, append(raw, '\n'), 0o600))
	}
	if !*quiet {
		fmt.Println(string(raw))
	}
}

var disableExact bool

// ---------------------------------------------------------------- 权重搜索

type tuneParam struct {
	name   string
	lo, hi float64
}

// 只搜非线性附加项; 线性核心保持冠军权重不变, 使"表示"与"调参"可分离。
var tuneParams = []tuneParam{
	{"StopMargin", -100, 800},
	{"HitProbWeight", -300, 800},
	{"OffProbPenalty", -300, 1500},
	{"DensityWeight", -600, 800},
	{"RefreshCostW", 0, 300},
}

func tuneFields(W *nlW) map[string]*float64 {
	return map[string]*float64{
		"StopMargin":     &W.StopMargin,
		"HitProbWeight":  &W.HitProbWeight,
		"OffProbPenalty": &W.OffProbPenalty,
		"DensityWeight":  &W.DensityWeight,
		"RefreshCostW":   &W.RefreshCostW,
	}
}

// runTuner 用 (1+λ) 爬山在训练种子上搜索非线性附加权重, 时间盒到 budgetSec。
// 评估目标 = 直接使用该续跑策略(my:mode)的期望诗意。
func runTuner(e *env, w *jiju.World, spec jiju.WorldStart, base nlW,
	budgetSec, sims, mode int, seed int64, workers int) nlW {
	deadline := time.Now().Add(time.Duration(budgetSec) * time.Second)
	rng := rand.New(rand.NewSource(0x5eed1))
	cur := base
	curV := meanShiyi(runEval(e, w, spec, []policy{{label: "cur", kind: "my", mode: mode, W: cur}},
		sims, seed, workers, false), 0)
	best, bestV := cur, curV
	gen := 0
	nEval := 1
	scales := []float64{0.02, 0.05, 0.15, 0.40}
	for time.Now().Before(deadline) {
		gen++
		lambda := 8
		scale := scales[rng.Intn(len(scales))]
		pols := []policy{{label: "parent", kind: "my", mode: mode, W: cur}}
		cands := []nlW{cur}
		for i := 0; i < lambda; i++ {
			c := cur
			f := tuneFields(&c)
			for _, tp := range tuneParams {
				p := f[tp.name]
				*p += rng.NormFloat64() * scale * (tp.hi - tp.lo)
				if *p < tp.lo {
					*p = tp.lo
				}
				if *p > tp.hi {
					*p = tp.hi
				}
			}
			pols = append(pols, policy{label: fmt.Sprintf("c%d", i), kind: "my", mode: mode, W: c})
			cands = append(cands, c)
		}
		res := runEval(e, w, spec, pols, sims, seed, workers, false)
		nEval += lambda + 1
		bestIdx, bestD := -1, 0.0
		for i := 1; i < len(pols); i++ {
			d, se, _, _ := pairedDelta(res, i)
			if d > 1.5*se && d > bestD {
				bestIdx, bestD = i, d
			}
		}
		if bestIdx > 0 {
			cur = cands[bestIdx]
			curV = meanShiyi(res, bestIdx)
			if curV > bestV+1e-9 {
				best, bestV = cur, curV
				fmt.Fprintf(os.Stderr, "[tune] gen %d NEW BEST d=%.2f mean=%.2f %s\n", gen, bestD, curV, fmtW(cur))
			} else {
				// 接受等价邻域, 但不要漂移出已确认的最好点
				cur = best
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[tune] done gens=%d evals=%d best mean=%.2f\n", gen, nEval, bestV)
	return best
}

func meanShiyi(res [][]epResult, pi int) float64 {
	s := 0.0
	for i := range res {
		s += res[i][pi].shiyi
	}
	return s / float64(len(res))
}

func fmtW(W nlW) string {
	return fmt.Sprintf("stop=%.0f hit=%.0f off=%.0f dens=%.0f", W.StopMargin, W.HitProbWeight, W.OffProbPenalty, W.DensityWeight)
}

func runCrossCheck(e *env, n int, seedBase int64) {
	mismatch, total := 0, 0
	for ep := 0; ep < n; ep++ {
		sd := seedBase + int64(ep)*1000003
		rng := rand.New(rand.NewSource(sd))
		st, err := e.w.Start(e.spec, rng)
		check(err)
		t := e.newTrack(st)
		sc := &scratch{}
		for !st.Done {
			got := e.continueAction(t, st, defaultW(), modeLin, sc)
			ref := e.w.BaselineAction(st, jiju.DefaultStrategy())
			total++
			if ep == 0 && total <= 12 {
				fmt.Fprintf(os.Stderr, "  CHECK ep0 d=%d got=%d ref=%d left=%d ncand=%d lingxi=%d free=%d\n",
					total, got, ref, t.left, t.ncand, st.Lingxi, st.Free)
			}
			if got != ref {
				mismatch++
				if mismatch <= 10 {
					fmt.Fprintf(os.Stderr, "  MISMATCH ep=%d dec=%d mine=%d ref=%d left=%d ncand=%d\n",
						ep, total, got, ref, t.left, t.ncand)
				}
			}
			if err := e.stepTracked(t, st, ref, rng); err != nil {
				panic(err)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "cross-check: %d/%d decisions mismatch (%.4f%%)\n",
		mismatch, total, 100*float64(mismatch)/float64(total))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
