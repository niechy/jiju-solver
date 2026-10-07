package jiju

import "context"

type ExactMultiResult struct {
	Action   int
	Value    VerseReturn
	Values   []MultiActionValue
	Nodes    int
	Complete bool
}

type exactMultiSolver struct {
	w               *World
	ctx             context.Context
	objective       VerseObjective
	memo            map[string]VerseReturn
	nodes, maxNodes int
}

func (o VerseObjective) better(a, b VerseReturn) bool {
	return o.Value(a) > o.Value(b) || o.betterTie(a, b)
}

// ExactMulti enumerates all future candidate sets under DrawRound. Expectations
// use float64 arithmetic. Incomplete searches expose no partial recommended action.
func (w *World) ExactMulti(ctx context.Context, st *WorldState, limit, maxNodes int, objective VerseObjective) ExactMultiResult {
	out := ExactMultiResult{Action: -1}
	if objective.Validate() != nil || w.rules.DrawMode != DrawRound || ctx.Err() != nil {
		return out
	}
	if st.Done {
		out.Complete = true
		return out
	}
	if len(st.left) > limit || maxNodes < 1 {
		return out
	}
	x := exactMultiSolver{w: w, ctx: ctx, objective: objective, memo: map[string]VerseReturn{}, maxNodes: maxNodes}
	for a := 0; a <= RefreshAction; a++ {
		if !w.Legal(st, a) {
			continue
		}
		v, ok := x.action(st, a)
		if !ok {
			return ExactMultiResult{Action: -1, Nodes: x.nodes}
		}
		out.Values = append(out.Values, MultiActionValue{Action: a, Mean: v, Utility: objective.Value(v)})
		if out.Action < 0 || objective.better(v, out.Value) {
			out.Action, out.Value = a, v
		}
	}
	if ctx.Err() != nil || out.Action < 0 {
		return ExactMultiResult{Action: -1, Nodes: x.nodes}
	}
	markMultiDominance(out.Values)
	out.Nodes, out.Complete = x.nodes, true
	return out
}

func (x *exactMultiSolver) value(st *WorldState) (VerseReturn, bool) {
	if x.ctx.Err() != nil || x.nodes >= x.maxNodes {
		return VerseReturn{}, false
	}
	if st.Done {
		return VerseReturn{}, true
	}
	key := exactStateKey(st)
	if v, ok := x.memo[key]; ok {
		return v, true
	}
	x.nodes++
	var best VerseReturn
	found := false
	for a := 0; a <= RefreshAction; a++ {
		if !x.w.Legal(st, a) {
			continue
		}
		v, ok := x.action(st, a)
		if !ok {
			return VerseReturn{}, false
		}
		if !found || x.objective.better(v, best) {
			best, found = v, true
		}
	}
	if !found {
		return VerseReturn{}, false
	}
	x.memo[key] = best
	return best, true
}

func (x *exactMultiSolver) action(st *WorldState, a int) (VerseReturn, bool) {
	if x.ctx.Err() != nil || x.nodes >= x.maxNodes {
		return VerseReturn{}, false
	}
	after, reward, err := x.w.AfterState(st, a)
	if err != nil {
		return VerseReturn{}, false
	}
	v := VerseReturn{Count: float64(reward), Shiyi: float64(after.Shiyi - st.Shiyi)}
	if after.Done {
		return v, true
	}
	future, ok := x.draw(after)
	v.Count += future.Count
	v.Shiyi += future.Shiyi
	return v, ok
}

func (x *exactMultiSolver) draw(st *WorldState) (VerseReturn, bool) {
	var pool []int
	for _, v := range st.left {
		if !st.blocked[v] {
			pool = append(pool, v)
		}
	}
	k := min(3, len(pool))
	if k < x.w.rules.MinCandidates {
		return VerseReturn{}, true
	}
	var sum VerseReturn
	n := 0
	var visit func(int, int) bool
	visit = func(start, depth int) bool {
		if x.ctx.Err() != nil {
			return false
		}
		if depth == k {
			st.n = k
			v, ok := x.value(st)
			if !ok {
				return false
			}
			sum.Count += v.Count
			sum.Shiyi += v.Shiyi
			n++
			return true
		}
		for i := start; i <= len(pool)-(k-depth); i++ {
			st.cands[depth] = pool[i]
			if !visit(i+1, depth+1) {
				return false
			}
		}
		return true
	}
	if !visit(0, 0) {
		return VerseReturn{}, false
	}
	return VerseReturn{Count: sum.Count / float64(n), Shiyi: sum.Shiyi / float64(n)}, true
}
