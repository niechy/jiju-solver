package jiju

import (
	"context"
	"math"
	"math/rand"
	"time"
)

type RolloutOptions struct {
	ActionPolicies bool // Screen a frozen continuation separately for each root action.
	TerminalPolicy bool // Objective-aware final-pick repair in multi-objective continuations.
	Samples        int
	MaxSamples     int // Optional second stage for statistically close root actions.
	Seed           int64
	Budget         time.Duration
	// Paired uncertainty threshold relative to the baseline action.
	ConfidenceZ   float64
	Base          Strategy
	PolicySamples int // Independent paired screening of state-specific policies.
	ExactLimit    int
	ExactNodes    int
}

func DefaultRolloutOptions() RolloutOptions {
	return RolloutOptions{Samples: 4096, Seed: 42, ConfidenceZ: 1.96, Base: EvolvedStrategy(), PolicySamples: 128, ExactLimit: 6, ExactNodes: 100000}
}

type ActionValue struct {
	Action  int     `json:"action"`
	Mean    float64 `json:"mean"`
	Delta   float64 `json:"delta"`
	DeltaSE float64 `json:"delta_se"`
}

type RolloutResult struct {
	Action        int                `json:"action"`
	Baseline      int                `json:"baseline"`
	Samples       int                `json:"samples"`
	Values        []ActionValue      `json:"values"`
	Elapsed       time.Duration      `json:"elapsed_ns"`
	Exact         bool               `json:"exact"`
	Comparisons   []ActionComparison `json:"comparisons,omitempty"`
	Continuation  string             `json:"continuation,omitempty"`
	PolicySamples int                `json:"policy_samples,omitempty"`
}

// Diagnostic paired error relative to the chosen action, not a guarantee.
// Adaptive sampling and action selection preclude a fixed-coverage claim.
type ActionComparison struct {
	Alternative int     `json:"alternative"`
	Delta       float64 `json:"delta"`
	SE          float64 `json:"se"`
}

func pairedSE(sum, squares float64, n int) float64 {
	if n < 2 {
		return 0
	}
	count := float64(n)
	return math.Sqrt(math.Max(0, (squares-sum*sum/count)/(count-1)) / count)
}

func closeRootActions(actions []int, sums [4]float64, pairSquares [4][4]float64, n int) bool {
	if len(actions) < 2 || n < 2 {
		return false
	}
	best := actions[0]
	for _, a := range actions {
		if sums[a] > sums[best] {
			best = a
		}
	}
	for _, a := range actions {
		if a == best {
			continue
		}
		diff := sums[best] - sums[a]
		se := pairedSE(diff, pairSquares[best][a], n)
		if se > 0 && diff/float64(n) <= 1.96*se {
			return true
		}
	}
	return false
}

// Rollout evaluates every legal root action in the same environment, then
// follows Base to the end. Only complete paired sample rounds are committed.
func (w *World) Rollout(ctx context.Context, st *WorldState, opt RolloutOptions) RolloutResult {
	started := time.Now()
	base := w.BaselineAction(st, opt.Base)
	out := RolloutResult{Action: base, Baseline: base}
	if st.Done || opt.Samples < 1 {
		out.Elapsed = time.Since(started)
		return out
	}
	if opt.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Budget)
		defer cancel()
	}
	if opt.ExactLimit > 0 && len(st.left) <= opt.ExactLimit {
		exact := w.Exact(ctx, st, opt.ExactLimit, opt.ExactNodes)
		if exact.Complete {
			out.Action, out.Values, out.Exact = exact.Action, exact.Values, true
			out.Elapsed = time.Since(started)
			return out
		}
	}
	if opt.PolicySamples > 0 {
		selected, samples := w.screenContinuation(ctx, st, opt)
		out.PolicySamples = samples
		opt.Base = selected
		out.Continuation = "adaptive"
		if selected == EvolvedStrategy() {
			out.Continuation = "legacy"
		} else if selected == CountStrategy() {
			out.Continuation = "count"
		}
		base = w.BaselineAction(st, opt.Base)
		out.Action, out.Baseline = base, base
	}
	var actions []int
	for a := 0; a <= RefreshAction; a++ {
		if w.Legal(st, a) {
			actions = append(actions, a)
		}
	}
	var sums, diffs, diffSq [4]float64
	var pairSquares [4][4]float64
	rngSeeds := rand.New(rand.NewSource(opt.Seed))
	s := st.Clone()
	rng := rand.New(rand.NewSource(0))
	for sample := 0; sample < max(opt.Samples, opt.MaxSamples); sample++ {
		if sample == opt.Samples && !closeRootActions(actions, sums, pairSquares, out.Samples) {
			break
		}
		seed := rngSeeds.Int63()
		var values [4]float64
		complete := true
		for _, a := range actions {
			if ctx.Err() != nil {
				complete = false
				break
			}
			s.resetFrom(st)
			rng.Seed(seed)
			if w.Step(s, a, rng) != nil {
				complete = false
				break
			}
			step := 0
			for !s.Done {
				if step%32 == 0 && ctx.Err() != nil {
					complete = false
					break
				}
				next := w.BaselineAction(s, opt.Base)
				if w.Step(s, next, rng) != nil {
					complete = false
					break
				}
				step++
			}
			if !complete {
				break
			}
			values[a] = float64(s.Turns - st.Turns)
		}
		if !complete {
			break
		}
		out.Samples++
		for _, a := range actions {
			d := values[a] - values[base]
			sums[a] += values[a]
			diffs[a] += d
			diffSq[a] += d * d
			for _, b := range actions {
				difference := values[a] - values[b]
				pairSquares[a][b] += difference * difference
			}
		}
	}
	if out.Samples == 0 {
		out.Elapsed = time.Since(started)
		return out
	}
	bestMean := -math.MaxFloat64
	if out.Samples > 0 {
		bestMean = sums[base] / float64(out.Samples)
	}
	for _, a := range actions {
		v := ActionValue{Action: a}
		if out.Samples > 0 {
			n := float64(out.Samples)
			v.Mean, v.Delta = sums[a]/n, diffs[a]/n
			if out.Samples > 1 {
				v.DeltaSE = pairedSE(diffs[a], diffSq[a], out.Samples)
			}
			if v.Mean > bestMean && (opt.ConfidenceZ <= 0 || out.Samples > 1 && v.Delta > opt.ConfidenceZ*v.DeltaSE) {
				bestMean, out.Action = v.Mean, a
			}
		}
		out.Values = append(out.Values, v)
	}
	if out.Samples > 1 {
		for _, a := range actions {
			if a == out.Action {
				continue
			}
			difference := sums[out.Action] - sums[a]
			out.Comparisons = append(out.Comparisons, ActionComparison{a, difference / float64(out.Samples), pairedSE(difference, pairSquares[out.Action][a], out.Samples)})
		}
	}
	out.Elapsed = time.Since(started)
	return out
}

// Screen on a separate random stream before estimating root action values.
// A policy is fixed for each entire trajectory; never choose its hindsight
// best outcome per random future. This is an estimate, not an optimality proof.
func (w *World) screenContinuation(ctx context.Context, st *WorldState, opt RolloutOptions) (Strategy, int) {
	candidates := w.AdaptiveStrategies(st)
	policies := []Strategy{opt.Base}
	for _, candidate := range candidates {
		duplicate := false
		for _, policy := range policies {
			if candidate == policy {
				duplicate = true
				break
			}
		}
		if !duplicate {
			policies = append(policies, candidate)
		}
	}
	if len(policies) < 2 {
		return opt.Base, 0
	}
	seeds := rand.New(rand.NewSource(opt.Seed ^ 0x672ba351c982))
	rng := rand.New(rand.NewSource(0))
	s := st.Clone()
	var sums, squares []float64
	sums = make([]float64, len(policies))
	squares = make([]float64, len(policies))
	n := 0
	for sample := 0; sample < opt.PolicySamples; sample++ {
		seed := seeds.Int63()
		values := make([]int, len(policies))
		for p, policy := range policies {
			s.resetFrom(st)
			rng.Seed(seed)
			steps := 0
			for !s.Done {
				if steps%32 == 0 && ctx.Err() != nil {
					return opt.Base, n
				}
				if w.Step(s, w.BaselineAction(s, policy), rng) != nil {
					return opt.Base, n
				}
				steps++
			}
			values[p] = s.Turns - st.Turns
		}
		for p := range values {
			d := float64(values[p] - values[0])
			sums[p] += d
			squares[p] += d * d
		}
		n++
	}
	best := 0
	for p := 1; p < len(policies); p++ {
		if sums[p] > sums[best] {
			best = p
		}
	}
	if best == 0 || n < 2 || sums[best]/float64(n) <= opt.ConfidenceZ*pairedSE(sums[best], squares[best], n) {
		return opt.Base, n
	}
	return policies[best], n
}
