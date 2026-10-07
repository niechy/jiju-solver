package jiju

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
)

// ExactResult is an expected-value optimum under WorldRules, not foresight of
// the game's future random draws. Only a fully evaluated root is returned.
type ExactResult struct {
	Action   int
	Value    float64
	Values   []ActionValue
	Nodes    int
	Complete bool
}

type exactSolver struct {
	w               *World
	ctx             context.Context
	memo            map[string]float64
	nodes, maxNodes int
}

// Exact enumerates unordered candidate sets, which all have equal probability
// under the verified draw model. Limit and node budget bound time and memory.
func (w *World) Exact(ctx context.Context, st *WorldState, limit, maxNodes int) ExactResult {
	out := ExactResult{Action: -1}
	if st.Done {
		out.Complete = true
		return out
	}
	if len(st.left) > limit || maxNodes < 1 || w.rules.DrawMode != DrawRound {
		return out
	}
	solver := exactSolver{w: w, ctx: ctx, memo: map[string]float64{}, maxNodes: maxNodes}
	best := -math.MaxFloat64
	for a := 0; a <= RefreshAction; a++ {
		if !w.Legal(st, a) {
			continue
		}
		q, ok := solver.action(st, a)
		if !ok {
			out.Nodes = solver.nodes
			return out
		}
		out.Values = append(out.Values, ActionValue{Action: a, Mean: q})
		if q > best {
			out.Action, out.Value, best = a, q, q
		}
		if best >= float64(len(st.left)) {
			break
		}
	}
	out.Nodes, out.Complete = solver.nodes, true
	return out
}

func (x *exactSolver) key(s *WorldState) string {
	return exactStateKey(s)
}

func exactStateKey(s *WorldState) string {
	left := append([]int(nil), s.left...)
	sort.Ints(left)
	cs := append([]int(nil), s.cands[:s.n]...)
	sort.Ints(cs)
	var b []byte
	add := func(v int) { b = binary.LittleEndian.AppendUint64(b, uint64(v)) }
	for _, v := range []int{s.prev, s.Lingxi, s.Cost, s.Free, s.FreeLimit, s.OffRun, s.n} {
		add(v)
	}
	b = binary.LittleEndian.AppendUint64(b, s.seen)
	for _, v := range cs {
		add(v)
	}
	add(-1)
	for _, v := range left {
		add(v)
		if s.blocked[v] {
			add(1)
		} else {
			add(0)
		}
	}
	return string(b)
}

func (x *exactSolver) value(s *WorldState) (float64, bool) {
	if x.ctx.Err() != nil || x.nodes >= x.maxNodes {
		return 0, false
	}
	key := x.key(s)
	if v, ok := x.memo[key]; ok {
		return v, true
	}
	x.nodes++
	best := 0.0
	for a := 0; a <= RefreshAction; a++ {
		if !x.w.Legal(s, a) {
			continue
		}
		v, ok := x.action(s, a)
		if !ok {
			return 0, false
		}
		best = math.Max(best, v)
		if best >= float64(len(s.left)) {
			break
		}
	}
	x.memo[key] = best
	return best, true
}

func (x *exactSolver) action(st *WorldState, a int) (float64, bool) {
	if x.ctx.Err() != nil || x.nodes >= x.maxNodes {
		return 0, false
	}
	s := st.Clone()
	if a == RefreshAction {
		if s.Free > 0 {
			s.Free--
		} else {
			s.Lingxi -= x.w.rules.RefreshCost
		}
		for i := 0; i < s.n; i++ {
			v := s.cands[i]
			if !s.blocked[v] {
				s.blocked[v] = true
				s.blockedN++
			}
		}
		s.n = 0
		return x.draw(s)
	}
	x.w.selectVerse(s, a)
	if s.Lingxi < s.Cost || len(s.left) < x.w.rules.MinCandidates {
		return 1, true
	}
	if x.w.rules.PayOnEntry {
		s.Lingxi -= s.Cost
	}
	s.Free = s.FreeLimit
	clear(s.blocked)
	s.blockedN = 0
	v, ok := x.draw(s)
	return 1 + v, ok
}

func (x *exactSolver) draw(s *WorldState) (float64, bool) {
	var pool []int
	for _, v := range s.left {
		if !s.blocked[v] {
			pool = append(pool, v)
		}
	}
	k := min(3, len(pool))
	if k < x.w.rules.MinCandidates {
		return 0, true
	}
	var sum float64
	count := 0
	var visit func(int, int) bool
	visit = func(start, depth int) bool {
		if depth == k {
			s.n = k
			value, ok := x.value(s)
			if !ok {
				return false
			}
			sum += value
			count++
			return true
		}
		for i := start; i <= len(pool)-(k-depth); i++ {
			s.cands[depth] = pool[i]
			if !visit(i+1, depth+1) {
				return false
			}
		}
		return true
	}
	if !visit(0, 0) {
		return 0, false
	}
	return sum / float64(count), true
}
