package jiju

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// VerseObjective selects a lexicographic priority or explicit weighted gains.
// It never chooses a hindsight objective separately for each simulated future.
type VerseObjective struct {
	Priority string  `json:"priority"`
	Count    float64 `json:"count_weight"`
	Shiyi    float64 `json:"shiyi_weight"`
}

type VerseReturn struct {
	Count float64 `json:"count"`
	Shiyi float64 `json:"shiyi"`
}

func (o VerseObjective) Validate() error {
	if o.Priority == "count-first" || o.Priority == "shiyi-first" {
		return nil
	}
	if o.Priority != "" {
		return fmt.Errorf("unsupported objective priority")
	}
	if !finite(o.Count) || !finite(o.Shiyi) || o.Count < 0 || o.Shiyi < 0 || o.Count+o.Shiyi == 0 {
		return fmt.Errorf("objective requires finite nonnegative weights and a positive component")
	}
	return nil
}

func (o VerseObjective) Value(v VerseReturn) float64 {
	if o.Priority == "count-first" {
		return v.Count
	}
	if o.Priority == "shiyi-first" {
		return v.Shiyi
	}
	return o.Count*v.Count + o.Shiyi*v.Shiyi
}

func (o VerseObjective) betterTie(a, b VerseReturn) bool {
	return o.Priority == "count-first" && a.Count == b.Count && a.Shiyi > b.Shiyi ||
		o.Priority == "shiyi-first" && a.Shiyi == b.Shiyi && a.Count > b.Count
}

type MultiActionValue struct {
	Action    int         `json:"action"`
	Mean      VerseReturn `json:"mean"`
	Utility   float64     `json:"utility"`
	DeltaSE   float64     `json:"paired_utility_se"`
	Dominated bool        `json:"mean_dominated"`
}

type MultiResult struct {
	Comparisons         []ActionComparison `json:"comparisons,omitempty"`
	ActionContinuations map[int]Strategy   `json:"action_continuations,omitempty"`
	Exact               bool               `json:"exact"`
	ExactNodes          int                `json:"exact_nodes"`
	Action              int                `json:"action"`
	Samples             int                `json:"samples"`
	Values              []MultiActionValue `json:"values"`
	Elapsed             time.Duration      `json:"elapsed_ns"`
	Continuation        Strategy           `json:"continuation"`
}

// MultiRollout reports both terminal gains and the empirical Pareto frontier.
// Endgames and continuation screening use the requested objective.
// Base is held fixed in each simulated future.
func (w *World) MultiRollout(ctx context.Context, st *WorldState, opt RolloutOptions, objective VerseObjective) (out MultiResult, err error) {
	started := time.Now()
	defer func() { out.Elapsed = time.Since(started) }()
	out.Action = w.BaselineAction(st, opt.Base)
	if err = objective.Validate(); err != nil {
		return out, err
	}
	if opt.Samples < 1 || opt.PolicySamples < 0 || opt.MaxSamples < 0 || opt.Budget < 0 {
		return out, fmt.Errorf("positive samples and nonnegative planning limits required")
	}
	if st.Done {
		return out, nil
	}
	if opt.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Budget)
		defer cancel()
	}
	if opt.ExactLimit > 0 && len(st.left) <= opt.ExactLimit {
		r := w.ExactMulti(ctx, st, opt.ExactLimit, opt.ExactNodes, objective)
		out.ExactNodes = r.Nodes
		if r.Complete {
			out.Action, out.Values, out.Exact = r.Action, r.Values, true
			return out, nil
		}
	}
	if opt.PolicySamples > 0 {
		opt.Base = w.screenMultiContinuation(ctx, st, opt, objective, -1)
	}
	out.Action = w.BaselineAction(st, opt.Base)
	out.Continuation = opt.Base
	var actions []int
	for a := 0; a <= RefreshAction; a++ {
		if w.Legal(st, a) {
			actions = append(actions, a)
		}
	}
	if opt.ActionPolicies && opt.PolicySamples > 0 {
		// Retain the screened baseline and refine on a separate stream. This
		// keeps the uncertainty fallback identical to ordinary multi-rollout.
		actionOpt := opt
		actionOpt.Seed ^= 0x285ace1
		out.ActionContinuations = make(map[int]Strategy, len(actions))
		for _, a := range actions {
			out.ActionContinuations[a] = w.screenMultiContinuation(ctx, st, actionOpt, objective, a)
		}
	}
	var sums [4]VerseReturn
	var differences, squares [4]float64
	var primarySums [4]float64
	var pairSquares [4][4]float64
	seedRNG := rand.New(rand.NewSource(opt.Seed))
	rng := rand.New(rand.NewSource(0))
	work := st.Clone()
	for n := 0; n < max(opt.Samples, opt.MaxSamples); n++ {
		if n == opt.Samples && !closeRootActions(actions, primarySums, pairSquares, out.Samples) {
			break
		}
		seed := seedRNG.Int63()
		var returns [4]VerseReturn
		for _, a := range actions {
			if ctx.Err() != nil {
				goto finished
			}
			work.resetFrom(st)
			rng.Seed(seed)
			if err = w.Step(work, a, rng); err != nil {
				return out, err
			}
			continuation := opt.Base
			if selected, ok := out.ActionContinuations[a]; ok {
				continuation = selected
			}
			for !work.Done {
				if ctx.Err() != nil {
					goto finished
				}
				if err = w.Step(work, w.multiContinuationAction(work, continuation, objective, opt.TerminalPolicy), rng); err != nil {
					return out, err
				}
			}
			returns[a] = VerseReturn{float64(work.Turns - st.Turns), float64(work.Shiyi - st.Shiyi)}
		}
		for _, a := range actions {
			sums[a].Count += returns[a].Count
			sums[a].Shiyi += returns[a].Shiyi
			primarySums[a] += objective.Value(returns[a])
			for _, b := range actions {
				d := objective.Value(returns[a]) - objective.Value(returns[b])
				pairSquares[a][b] += d * d
			}
			d := objective.Value(returns[a]) - objective.Value(returns[out.Action])
			differences[a] += d
			squares[a] += d * d
		}
		out.Samples++
	}
finished:
	if out.Samples == 0 {
		return out, nil
	}
	n := float64(out.Samples)
	base := out.Action
	best := objective.Value(VerseReturn{sums[base].Count / n, sums[base].Shiyi / n})
	bestReturn := VerseReturn{sums[base].Count / n, sums[base].Shiyi / n}
	for _, a := range actions {
		mean := VerseReturn{sums[a].Count / n, sums[a].Shiyi / n}
		v := MultiActionValue{Action: a, Mean: mean, Utility: objective.Value(mean), DeltaSE: pairedSE(differences[a], squares[a], out.Samples)}
		if !finite(v.Utility) {
			return out, fmt.Errorf("nonfinite objective return")
		}
		if v.Utility > best && (opt.ConfidenceZ <= 0 || out.Samples > 1 && differences[a]/n > opt.ConfidenceZ*v.DeltaSE) {
			out.Action, best = a, v.Utility
			bestReturn = mean
		} else if objective.betterTie(mean, bestReturn) {
			out.Action, bestReturn = a, mean
		}
		out.Values = append(out.Values, v)
	}
	markMultiDominance(out.Values)
	if out.Samples > 1 {
		for _, a := range actions {
			if a == out.Action {
				continue
			}
			d := primarySums[out.Action] - primarySums[a]
			out.Comparisons = append(out.Comparisons, ActionComparison{Alternative: a, Delta: d / n, SE: pairedSE(d, pairSquares[out.Action][a], out.Samples)})
		}
	}
	return out, nil
}

func markMultiDominance(values []MultiActionValue) {
	for i := range values {
		a := values[i].Mean
		for _, b := range values {
			if b.Mean.Count >= a.Count && b.Mean.Shiyi >= a.Shiyi && (b.Mean.Count > a.Count || b.Mean.Shiyi > a.Shiyi) {
				values[i].Dominated = true
			}
		}
	}
}

// firstAction=-1 screens from the current state; otherwise force the root action
// before following each candidate policy. Selection never uses evaluation seeds.
func (w *World) screenMultiContinuation(ctx context.Context, st *WorldState, opt RolloutOptions, objective VerseObjective, firstAction int) Strategy {
	policies := []Strategy{opt.Base}
	for _, base := range []Strategy{CountStrategy(), EvolvedStrategy()} {
		for _, weight := range []float64{0, 4, 16} {
			p := base
			p.ShiyiWeight = weight
			policies = append(policies, p)
		}
	}
	sums := make([]VerseReturn, len(policies))
	differences, squares := make([]float64, len(policies)), make([]float64, len(policies))
	seeds := rand.New(rand.NewSource(opt.Seed ^ 0x732916b))
	rng := rand.New(rand.NewSource(0))
	work := st.Clone()
	for n := 0; n < opt.PolicySamples; n++ {
		seed := seeds.Int63()
		baseValue := 0.0
		for i, p := range policies {
			work.resetFrom(st)
			rng.Seed(seed)
			if ctx.Err() != nil {
				return opt.Base
			}
			if firstAction >= 0 {
				if w.Step(work, firstAction, rng) != nil {
					return opt.Base
				}
			}
			for !work.Done {
				if ctx.Err() != nil {
					return opt.Base
				}
				if w.Step(work, w.multiContinuationAction(work, p, objective, opt.TerminalPolicy), rng) != nil {
					return opt.Base
				}
			}
			v := VerseReturn{float64(work.Turns - st.Turns), float64(work.Shiyi - st.Shiyi)}
			sums[i].Count += v.Count
			sums[i].Shiyi += v.Shiyi
			if i == 0 {
				baseValue = objective.Value(v)
			}
			d := objective.Value(v) - baseValue
			differences[i] += d
			squares[i] += d * d
		}
	}
	best := 0
	for i := 1; i < len(policies); i++ {
		if objective.Value(sums[i]) > objective.Value(sums[best]) && (opt.ConfidenceZ <= 0 || opt.PolicySamples > 1 && differences[i]/float64(opt.PolicySamples) > opt.ConfidenceZ*pairedSE(differences[i], squares[i], opt.PolicySamples)) {
			best = i
		} else if objective.betterTie(sums[i], sums[best]) {
			best = i
		}
	}
	return policies[best]
}

// When every legal action ends the episode, strategic proxies have no future
// value. AfterState supplies the same reward and termination rules as simulation.
func (w *World) multiContinuationAction(st *WorldState, base Strategy, objective VerseObjective, terminal bool) int {
	fallback := w.BaselineAction(st, base)
	if !terminal || st.Done || w.Legal(st, RefreshAction) {
		return fallback
	}
	best := -1
	var value VerseReturn
	for a := 0; a < st.n; a++ {
		if !w.Legal(st, a) {
			continue
		}
		after, count, err := w.AfterState(st, a)
		if err != nil || !after.Done {
			return fallback
		}
		v := VerseReturn{Count: float64(count), Shiyi: float64(after.Shiyi - st.Shiyi)}
		if best < 0 || objective.better(v, value) {
			best, value = a, v
		}
	}
	if best < 0 {
		return fallback
	}
	return best
}
