package jiju

import (
	"fmt"
	"math/rand"
	"sort"
)

// DrawMode makes unresolved refresh semantics explicit and testable.
type DrawMode string

const (
	DrawReplace        DrawMode = "replace"
	DrawRound          DrawMode = "round"
	DrawExcludeCurrent DrawMode = "exclude-current"
)

type WorldRules struct {
	DrawMode        DrawMode `json:"draw_mode"`
	PayOnEntry      bool     `json:"pay_on_entry"`
	MinCandidates   int      `json:"min_candidates"`
	RefreshCost     int      `json:"refresh_cost"`
	MiddleClearsRun bool     `json:"middle_clears_run"`
}

func DefaultWorldRules() WorldRules {
	return WorldRules{DrawRound, true, 1, 1, false}
}

// World contains immutable data shared by simulations and online planning.
// Policies and economic side effects are deliberately outside this model.
type World struct {
	verses   []Verse
	ids      map[int]int
	poets    map[int]uint64
	poetBits []uint64
	rewards  []PoetFirstReward
	pairs    []uint8
	rules    WorldRules
}

type WorldStart struct {
	PrevID    int   `json:"prev_id"`
	Collected []int `json:"collected"`
	SeenPoets []int `json:"seen_poets"`
	// nil means all library verses; an empty non-nil slice means no eligible verses.
	Eligible      []int `json:"eligible"`
	Lingxi        int   `json:"lingxi"` // Actual balance, never re-award historical bonuses.
	Cost          int   `json:"cost"`
	FreeLimit     int   `json:"free_limit"`
	FreeRemaining int   `json:"free_remaining"`
	OffRun        int   `json:"off_run"`
	// Nonempty candidates mean the player is already inside a paid round.
	Candidates []int `json:"candidates"`
	Displayed  []int `json:"displayed"`
}

type WorldState struct {
	left                                              []int
	where                                             []int
	blocked                                           []bool
	blockedN                                          int
	seen                                              uint64
	groups                                            [15]int
	prev                                              int
	cands                                             [3]int
	n                                                 int
	Lingxi, Cost, Free, FreeLimit, OffRun             int
	Turns, Shiyi, Refreshes, PaidRefreshes, OffRhymes int
	Done                                              bool
}

const RefreshAction = 3

func NewWorld(lib *Library, rules WorldRules) (*World, error) {
	if rules.DrawMode != DrawReplace && rules.DrawMode != DrawRound && rules.DrawMode != DrawExcludeCurrent {
		return nil, fmt.Errorf("invalid draw mode %q", rules.DrawMode)
	}
	if rules.RefreshCost < 1 || rules.MinCandidates < 1 || rules.MinCandidates > 3 || lib == nil || len(lib.verses) == 0 {
		return nil, fmt.Errorf("invalid world rules or empty library")
	}
	w := &World{verses: append([]Verse(nil), lib.verses...), ids: map[int]int{}, poets: map[int]uint64{}, rules: rules}
	n := len(w.verses)
	w.poetBits = make([]uint64, n)
	w.rewards = make([]PoetFirstReward, n)
	w.pairs = make([]uint8, n*n)
	for i, v := range w.verses {
		if _, ok := w.ids[v.ID]; ok {
			return nil, fmt.Errorf("duplicate verse %d", v.ID)
		}
		w.ids[v.ID] = i
		for _, g := range v.Group1 {
			if g < 1 || g >= 15 {
				return nil, fmt.Errorf("unsupported rhyme group %d", g)
			}
		}
		if v.Poet != 0 {
			if _, ok := w.poets[v.Poet]; !ok {
				if len(w.poets) >= 64 {
					return nil, fmt.Errorf("too many poets")
				}
				w.poets[v.Poet] = uint64(1) << len(w.poets)
			}
			w.poetBits[i] = w.poets[v.Poet]
			w.rewards[i] = PoetFirstRewards[v.Poet]
		}
	}
	for i := range w.verses {
		for j := range w.verses {
			k, x, _ := Judge(&w.verses[i], &w.verses[j])
			w.pairs[i*n+j] = uint8(k)
			if x {
				w.pairs[i*n+j] |= 4
			}
		}
	}
	return w, nil
}

func (w *World) Start(spec WorldStart, rng *rand.Rand) (*WorldState, error) {
	prev, ok := w.ids[spec.PrevID]
	if !ok || spec.Cost < 1 || spec.Lingxi < 0 || spec.FreeLimit < 0 || spec.FreeRemaining < 0 ||
		spec.FreeRemaining > spec.FreeLimit || spec.OffRun < 0 || spec.OffRun > 1 {
		return nil, fmt.Errorf("invalid starting state")
	}
	s := &WorldState{prev: prev, Lingxi: spec.Lingxi, Cost: spec.Cost, FreeLimit: spec.FreeLimit,
		Free: spec.FreeRemaining, OffRun: spec.OffRun, where: make([]int, len(w.verses)), blocked: make([]bool, len(w.verses))}
	for i := range s.where {
		s.where[i] = -1
	}
	collected := make(map[int]bool, len(spec.Collected)+1)
	collected[spec.PrevID] = true
	for _, id := range spec.Collected {
		if _, ok := w.ids[id]; !ok {
			return nil, fmt.Errorf("unknown collected verse %d", id)
		}
		collected[id] = true
	}
	for id := range collected {
		s.seen |= w.poetBits[w.ids[id]]
	}
	for _, id := range spec.SeenPoets {
		bit, ok := w.poets[id]
		if !ok {
			return nil, fmt.Errorf("unknown poet %d", id)
		}
		s.seen |= bit
	}
	eligible := map[int]bool{}
	for _, id := range spec.Eligible {
		if _, ok := w.ids[id]; !ok {
			return nil, fmt.Errorf("unknown eligible verse %d", id)
		}
		eligible[id] = true
	}
	for i, v := range w.verses {
		if collected[v.ID] || spec.Eligible != nil && !eligible[v.ID] {
			continue
		}
		s.where[i] = len(s.left)
		s.left = append(s.left, i)
		for _, g := range v.Group1 {
			s.groups[g]++
		}
	}
	if len(spec.Candidates) > 0 {
		if len(spec.Candidates) < w.rules.MinCandidates || len(spec.Candidates) > 3 {
			return nil, fmt.Errorf("invalid candidate count")
		}
		for _, id := range spec.Candidates {
			i, ok := w.ids[id]
			if !ok || s.where[i] < 0 {
				return nil, fmt.Errorf("candidate %d is not in remaining eligible pool", id)
			}
			for j := 0; j < s.n; j++ {
				if s.cands[j] == i {
					return nil, fmt.Errorf("duplicate candidate %d", id)
				}
			}
			s.cands[s.n] = i
			s.n++
		}
		if w.rules.DrawMode == DrawRound {
			for _, id := range spec.Displayed {
				i, ok := w.ids[id]
				if !ok {
					return nil, fmt.Errorf("unknown displayed verse %d", id)
				}
				if s.where[i] >= 0 && !s.blocked[i] {
					s.blocked[i] = true
					s.blockedN++
				}
			}
		}
	} else {
		w.begin(s, rng)
	}
	return s, nil
}

func (s *WorldState) Clone() *WorldState {
	c := *s
	c.left = append([]int(nil), s.left...)
	c.where = append([]int(nil), s.where...)
	c.blocked = append([]bool(nil), s.blocked...)
	return &c
}

// resetFrom reuses simulation buffers without aliasing the observed root.
func (s *WorldState) resetFrom(root *WorldState) {
	left := append(s.left[:0], root.left...)
	where := append(s.where[:0], root.where...)
	blocked := append(s.blocked[:0], root.blocked...)
	*s = *root
	s.left, s.where, s.blocked = left, where, blocked
}

func (w *World) begin(s *WorldState, rng *rand.Rand) {
	s.n = 0
	if s.Lingxi < s.Cost || len(s.left) < w.rules.MinCandidates {
		s.Done = true
		return
	}
	if w.rules.PayOnEntry {
		s.Lingxi -= s.Cost
	}
	s.Free = s.FreeLimit
	clear(s.blocked)
	s.blockedN = 0
	w.draw(s, rng)
}

func (w *World) canDraw(s *WorldState) bool {
	n := len(s.left)
	switch w.rules.DrawMode {
	case DrawRound:
		n -= s.blockedN
		for i := 0; i < s.n; i++ {
			if !s.blocked[s.cands[i]] {
				n--
			}
		}
	case DrawExcludeCurrent:
		n -= s.n
	}
	return n >= w.rules.MinCandidates
}

func (w *World) Legal(s *WorldState, action int) bool {
	if s.Done {
		return false
	}
	if action == RefreshAction {
		minBalance := w.rules.RefreshCost
		if !w.rules.PayOnEntry {
			minBalance += s.Cost
		}
		return (s.Free > 0 || s.Lingxi >= minBalance) && w.canDraw(s)
	}
	return action >= 0 && action < s.n && (w.rules.PayOnEntry || s.Lingxi >= s.Cost)
}

func (w *World) draw(s *WorldState, rng *rand.Rand) {
	previous := s.cands
	prevN := s.n
	if w.rules.DrawMode == DrawRound {
		for j := 0; j < prevN; j++ {
			if !s.blocked[previous[j]] {
				s.blocked[previous[j]] = true
				s.blockedN++
			}
		}
	}
	available := len(s.left)
	if w.rules.DrawMode == DrawRound {
		available -= s.blockedN
	}
	if w.rules.DrawMode == DrawExcludeCurrent {
		available -= prevN
	}
	k := min(3, available)
	s.n = 0
	// Small/exhausted pools use a finite scan, avoiding unbounded rejection.
	if available < 12 {
		var storage [12]int
		pool := storage[:0]
		for _, v := range s.left {
			excluded := w.rules.DrawMode == DrawRound && s.blocked[v]
			if w.rules.DrawMode == DrawExcludeCurrent {
				for j := 0; j < prevN; j++ {
					excluded = excluded || previous[j] == v
				}
			}
			if !excluded {
				pool = append(pool, v)
			}
		}
		for j := 0; j < k; j++ {
			i := j + rng.Intn(len(pool)-j)
			pool[j], pool[i] = pool[i], pool[j]
			s.cands[j] = pool[j]
		}
		s.n = k
		return
	}
	for s.n < k {
		v := s.left[rng.Intn(len(s.left))]
		excluded := w.rules.DrawMode == DrawRound && s.blocked[v]
		if w.rules.DrawMode == DrawExcludeCurrent {
			for j := 0; j < prevN; j++ {
				excluded = excluded || previous[j] == v
			}
		}
		for j := 0; j < s.n; j++ {
			excluded = excluded || s.cands[j] == v
		}
		if excluded {
			continue
		}
		s.cands[s.n] = v
		s.n++
	}
}

// Step includes entry into the next round; it never exposes future random draws
// to the policy before that action has actually been selected.
func (w *World) Step(s *WorldState, action int, rng *rand.Rand) error {
	if !w.Legal(s, action) {
		return fmt.Errorf("illegal action %d", action)
	}
	if action == RefreshAction {
		if s.Free > 0 {
			s.Free--
		} else {
			s.Lingxi -= w.rules.RefreshCost
			s.PaidRefreshes++
		}
		s.Refreshes++
		w.draw(s, rng)
		return nil
	}
	w.selectVerse(s, action)
	w.begin(s, rng)
	return nil
}

// selectVerse settles only the current paid round, without inventing a live
// future candidate batch or spending the next entry fee.
func (w *World) selectVerse(s *WorldState, action int) {
	v := s.cands[action]
	pair := w.pairs[s.prev*len(w.verses)+v]
	kind, xz := JudgeKind(pair&3), pair&4 != 0
	if !w.rules.PayOnEntry {
		s.Lingxi -= s.Cost
	}
	s.Lingxi += w.verses[v].Lingxi
	s.Shiyi += w.verses[v].Shiyi
	if kind == KindRhyme {
		s.Lingxi += 7
	}
	if xz {
		s.Lingxi += 2
	} else {
		s.Shiyi -= 5
	}
	if kind == KindOffRhyme {
		s.OffRhymes++
		s.OffRun++
		if s.OffRun >= 2 {
			s.Cost++
			s.OffRun = 0
		}
	} else if kind == KindRhyme || w.rules.MiddleClearsRun {
		s.OffRun = 0
	}
	if w.poetBits[v] != 0 && s.seen&w.poetBits[v] == 0 {
		r := w.rewards[v]
		s.Lingxi += r.Lingxi
		s.Shiyi += r.Shiyi
		s.FreeLimit += r.FreeRefresh
		s.seen |= w.poetBits[v]
	}
	p := s.where[v]
	last := s.left[len(s.left)-1]
	s.left[p] = last
	s.where[last] = p
	s.left = s.left[:len(s.left)-1]
	s.where[v] = -1
	for _, g := range w.verses[v].Group1 {
		s.groups[g]--
	}
	s.prev = v
	s.Turns++
	s.n = 0
}

func (w *World) CandidateIDs(s *WorldState) []int {
	out := make([]int, s.n)
	for i := range out {
		out[i] = w.verses[s.cands[i]].ID
	}
	return out
}

// BaselineAction preserves the old weighting and gates, but all legality and
// accounting now come from World. It is also the rollout continuation policy.
// actionScore 是"把第 i 句接上去"的打分。纯函数: 只读 st, 不改动它。
// BaselineAction 的选择与 stopRefresh 的期望值共用它, 保证两条路径打分完全一致
// (抽取自原 BaselineAction 内联打分, 逐位等价)。
func (w *World) actionScore(st *WorldState, s Strategy, i, cur, threshold int) (float64, JudgeKind, int) {
	v := &w.verses[i]
	pair := w.pairs[st.prev*len(w.verses)+i]
	kind, xz := JudgeKind(pair&3), pair&4 != 0
	newPoet := w.poetBits[i] != 0 && st.seen&w.poetBits[i] == 0
	net := v.Lingxi - st.Cost
	shiyi := v.Shiyi
	if kind == KindRhyme {
		net += 7
	}
	if xz {
		net += 2
	} else {
		shiyi -= 5
	}
	pr := w.rewards[i]
	if newPoet {
		net += pr.Lingxi
		shiyi += pr.Shiyi
	}
	score := float64(net)*s.NetGainWeight + float64(shiyi)*s.ShiyiWeight
	if kind == KindRhyme {
		score += s.RhymeBonus
		if xz {
			score += s.XiangZhanBonus
		}
	} else if kind == KindMiddle {
		score += s.MiddleBonus
	} else {
		score -= s.OffRhymePenalty
		if st.OffRun >= 1 {
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
		pool = max(pool, st.groups[g])
	}
	if cur <= threshold && kind == KindMiddle {
		score += float64(pool) * s.TargetPoolWeight
		if newPoet {
			score += s.TargetNewPoetWeight
		}
	}
	return score, kind, pool
}

// DefaultStopMargin 是精确最优停止的默认余量(打分单位), 由留出种子评测确定。
const DefaultStopMargin = 1.0

// stopRefresh 用精确最优停止回答"要不要刷新", 取代三段硬门控。
//
// 判据: 刷新后的期望最优分(扣掉刷新花的灵犀) 若明显高于当前最优分, 就刷。
//
//	E[max 分] 用精确次序统计量算, 不是抽样: 池里有 R 句、无放回抽 k=min(3,R) 句,
//	则某句成为最大值当且仅当其余 k-1 句都比它小, 故 P(排名 i 者为最大)=C(i-1,k-1)/C(R,k)。
//	成本按 1 灵犀 = NetGainWeight 分换算(与 actionScore 的量纲一致)。
//
// 池必须是**真实未被屏蔽的池**: 未收集(left) ∩ 未被本轮屏蔽(blocked) ∩ 不在当前候选里。
// 这正是原门控用近似概率 p=1-(1-cur/left)^3 想表达的东西, 这里换成精确值。
//
// StopDepth>=2 时再多展开一步 —— 刷完之后手里还握着"再刷一次"的期权:
//
//	K  = Emax(P) - 再刷一次的成本
//	V2 = E[max(maxS, K)] = Emax(P) + E[(K - maxS)+]      (因为 E[maxS] = Emax(P))
//
// 注意 V2 >= Emax(P): 两步值 = 单步值 + 一个**非负的期权价值**, 是严格推广。
// 两项共用同一套次序统计量权, 仍是 O(R)。
// 唯一的近似: 把"再刷时的池"当成 P 本身, 而它实际是 P 去掉刚展示的 k 句 ——
// 对 R 是小扰动(池越小时越粗, 但那时 R 也小)。
func (w *World) stopRefresh(st *WorldState, s Strategy, bestScore float64) bool {
	scores, ok := w.refreshPoolScores(st, s)
	if !ok {
		return false
	}
	sort.Float64s(scores)
	k := min(3, len(scores))

	value := orderStatMax(scores, k)
	if s.StopDepth >= 2 {
		value += optionValue(scores, k, value-w.refreshScoreCost(st, s, 1))
	}
	margin := s.StopMargin
	if margin == 0 {
		margin = DefaultStopMargin
	}
	return value-w.refreshScoreCost(st, s, 0) > bestScore+margin
}

// refreshScoreCost 返回"第 nth 次刷新"(nth=0 是紧接着那次)在打分单位下的成本。
// 免费刷新次数按顺序消耗, 所以后续的刷新才可能要花钱。
func (w *World) refreshScoreCost(st *WorldState, s Strategy, nth int) float64 {
	if st.Free > nth {
		return 0
	}
	return float64(s.RefreshCost) * s.NetGainWeight
}

// refreshPoolScores 给出真实未屏蔽池里每一句的打分。
// 返回 false 表示当前不允许刷新或池为空。
func (w *World) refreshPoolScores(st *WorldState, s Strategy) ([]float64, bool) {
	if !w.Legal(st, RefreshAction) {
		return nil, false
	}
	cur, threshold := 0, int(float64(len(st.left))*s.SwitchThreshPct)
	if len(w.verses[st.prev].Group1) > 0 {
		cur = st.groups[w.verses[st.prev].Group1[0]]
	}
	scores := make([]float64, 0, len(st.left))
pool:
	for _, i := range st.left {
		if st.blocked[i] {
			continue
		}
		for j := 0; j < st.n; j++ {
			if st.cands[j] == i {
				continue pool
			}
		}
		score, _, _ := w.actionScore(st, s, i, cur, threshold)
		scores = append(scores, score)
	}
	if len(scores) == 0 {
		return nil, false
	}
	return scores, true
}

// orderStatMax 返回"从 R 个(升序)分数里无放回抽 k 个, 最大值"的期望。
func orderStatMax(scores []float64, k int) float64 {
	R := len(scores)
	switch k {
	case 1:
		sum := 0.0
		for _, v := range scores {
			sum += v
		}
		return sum / float64(R)
	case 2:
		den := float64(R) * float64(R-1) / 2
		sum := 0.0
		for i := 2; i <= R; i++ {
			sum += scores[i-1] * float64(i-1) / den
		}
		return sum
	default:
		den := float64(R) * float64(R-1) * float64(R-2) / 6
		sum := 0.0
		for i := 3; i <= R; i++ {
			sum += scores[i-1] * (float64(i-1) * float64(i-2) / 2) / den
		}
		return sum
	}
}

// optionValue 是"还能再刷一次"这个期权的价值: E[(K - maxS)+]。
// maxS 的分布同样是精确次序统计量, 与 orderStatMax 共用同一套权。
func optionValue(scores []float64, k int, K float64) float64 {
	R := len(scores)
	sum := 0.0
	switch k {
	case 1:
		den := float64(R)
		for i := 1; i <= R; i++ {
			if gap := K - scores[i-1]; gap > 0 {
				sum += gap / den
			}
		}
	case 2:
		den := float64(R) * float64(R-1) / 2
		for i := 2; i <= R; i++ {
			if gap := K - scores[i-1]; gap > 0 {
				sum += gap * float64(i-1) / den
			}
		}
	default:
		den := float64(R) * float64(R-1) * float64(R-2) / 6
		for i := 3; i <= R; i++ {
			if gap := K - scores[i-1]; gap > 0 {
				sum += gap * (float64(i-1) * float64(i-2) / 2) / den
			}
		}
	}
	return sum
}

func (w *World) BaselineAction(st *WorldState, s Strategy) int {
	if st.Done {
		return -1
	}
	cur := 0
	if len(w.verses[st.prev].Group1) > 0 {
		cur = st.groups[w.verses[st.prev].Group1[0]]
	}
	threshold := int(float64(len(st.left)) * s.SwitchThreshPct)
	best, bestScore := -1, -1e100
	bestKind, bestPool := KindOffRhyme, 0
	for idx := 0; idx < st.n; idx++ {
		score, kind, pool := w.actionScore(st, s, st.cands[idx], cur, threshold)
		if score > bestScore {
			best, bestScore, bestKind, bestPool = idx, score, kind, pool
		}
	}
	// 精确一步最优停止: 用"重抽后的期望最优分"整体取代下面的三段硬门控。
	// 实测(配对整局, 权重不动只换刷新判据): 本仓库真实代码路径 n=200/seed42 得
	// +54.73 诗意 CI95 [38.56, 70.89]; 独立复刻脚本另在两个留出种子上合计 n=600 得
	// +56.58±9.64。两者量级一致, 见 docs/algorithm.md。
	if s.ExactStopRefresh {
		if w.stopRefresh(st, s, bestScore) {
			return RefreshAction
		}
		return best
	}
	refresh := bestKind == KindOffRhyme || bestKind == KindMiddle && cur > s.DryGroupFloor
	refresh = refresh || cur <= threshold && bestKind != KindRhyme && bestPool < s.MinTargetPool
	// OffRhymeRespectsGate=false 复刻历史行为: 出韵被排除在两道预算闸之外。
	if refresh && st.Free <= 0 && (bestKind != KindOffRhyme || s.OffRhymeRespectsGate) {
		// Express the legacy gate in pre-entry units to avoid accidentally
		// changing its semantics while comparing policies.
		balance := st.Lingxi
		if w.rules.PayOnEntry {
			balance += st.Cost
		}
		p := 1 - pow1m(float64(cur)/float64(len(st.left)), 3)
		if balance < s.LowLingxiFloor || p*8 < float64(w.rules.RefreshCost)+s.RefreshGainFloor {
			refresh = false
		}
	}
	if refresh && w.Legal(st, RefreshAction) {
		return RefreshAction
	}
	return best
}

// Remaining 返回当前**尚未收集**的诗句数, 也就是后续还能抽到多少句的上限。
//
// 这是判断"这一轮还能撑多久"的关键量: 它随每次选句单调减少, 也决定了刷新后
// 期望最优分的计算范围。注意它只排除已收集的, 不排除被本轮屏蔽的 —— 后者在
// 本轮结束后会重新可抽。
func (s *WorldState) Remaining() int { return len(s.left) }
