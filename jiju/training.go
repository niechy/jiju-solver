package jiju

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

type TrainingOptions struct {
	Objective                                     VerseObjective
	Seed                                          int64
	Generations, Population, Episodes, Validation int
	Budget                                        time.Duration
	Base                                          Strategy
}

func DefaultTrainingOptions() TrainingOptions {
	return TrainingOptions{Seed: 20260907, Generations: 24, Population: 24, Episodes: 256, Validation: 4096, Budget: 30 * time.Second, Base: EvolvedStrategy()}
}

type TrainingResult struct {
	Objective         VerseObjective `json:"objective"`
	ValidationMeans   []VerseReturn  `json:"validation_means,omitempty"`
	Strategy          Strategy       `json:"strategy"`
	Accepted          bool           `json:"accepted"`
	CandidateAccepted bool           `json:"candidate_accepted"`
	Generations       int            `json:"generations"`
	Validation        int            `json:"validation"`
	Delta             float64        `json:"delta"`
	SE                float64        `json:"se"`
	Elapsed           time.Duration  `json:"elapsed_ns"`
}

// TrainContinuation fits only to the supplied pool and current economic state.
// Search and validation use separate streams. Only a complete validation batch
// can promote a frozen winner; neither a timeout nor a partial pair can do so.
func (w *World) TrainContinuation(ctx context.Context, spec WorldStart, opt TrainingOptions) (out TrainingResult, err error) {
	started := time.Now()
	if opt.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Budget)
		defer cancel()
	}
	defer func() {
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			out.Strategy, out.Accepted, out.CandidateAccepted = opt.Base, false, false
		}
		out.Elapsed = time.Since(started)
	}()
	out.Strategy = opt.Base
	objective := opt.Objective
	if objective == (VerseObjective{}) {
		objective.Count = 1 // Preserve historical count-only callers.
	}
	out.Objective = objective
	if err := objective.Validate(); err != nil {
		return out, err
	}
	if spec.Eligible == nil || opt.Generations < 1 || opt.Population < 4 || opt.Episodes < 2 || opt.Validation < 2 || opt.Budget < 0 {
		return out, fmt.Errorf("training requires an explicit pool and positive search limits")
	}
	root, err := w.Start(spec, rand.New(rand.NewSource(0)))
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if root.Done {
		return out, nil
	}
	searchCtx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		searchCtx, cancel = context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)*2/3))
		defer cancel()
	}
	anchors := []Strategy{opt.Base}
	family := w.AdaptiveStrategies(root)
	if objective.Priority == "shiyi-first" || objective.Shiyi > 0 {
		for _, base := range []Strategy{opt.Base, CountStrategy(), EvolvedStrategy()} {
			for _, weight := range []float64{4, 16} {
				p := base
				p.ShiyiWeight = weight
				family = append(family, p)
			}
		}
	}
	for _, s := range family {
		duplicate := false
		for _, anchor := range anchors {
			if s == anchor {
				duplicate = true
				break
			}
		}
		if !duplicate {
			anchors = append(anchors, s)
		}
	}
	// Reserve room for mutation even with a small requested population.
	if len(anchors) > opt.Population-2 {
		anchors = anchors[:opt.Population-2]
	}
	elites := append([]Strategy(nil), anchors...)
	rng := rand.New(rand.NewSource(opt.Seed))
	seeds := rand.New(rand.NewSource(opt.Seed ^ 0x239417ace))
	winner := opt.Base
	incumbent := opt.Base
	for gen := 0; gen < opt.Generations; gen++ {
		policies := append([]Strategy(nil), anchors...)
		policies = append(policies, elites...)
		if len(policies) > opt.Population-2 {
			policies = policies[:opt.Population-2]
		}
		for len(policies) < opt.Population {
			scale := .22 / (1 + float64(gen)/5)
			parent := elites[rng.Intn(len(elites))]
			child := mutateContinuation(parent, rng, scale)
			if objective.Priority == "shiyi-first" || objective.Shiyi > 0 {
				child.ShiyiWeight = math.Max(0, math.Min(64, parent.ShiyiWeight+rng.NormFloat64()*64*scale))
			}
			policies = append(policies, child)
		}
		values, e := w.trainingBatch(searchCtx, spec, policies, opt.Episodes, seeds.Int63())
		if e != nil {
			if searchCtx.Err() != nil {
				break
			}
			return out, e
		}
		sums := make([]VerseReturn, len(policies))
		order := make([]int, len(policies))
		for p := range policies {
			order[p] = p
			for _, v := range values[p] {
				sums[p].Count += v.Count
				sums[p].Shiyi += v.Shiyi
			}
		}
		sort.SliceStable(order, func(i, j int) bool { return objective.better(sums[order[i]], sums[order[j]]) })
		winner = policies[order[0]]
		bestAnchor := 0
		for i := 1; i < len(anchors); i++ {
			if objective.better(sums[i], sums[bestAnchor]) {
				bestAnchor = i
			}
		}
		incumbent = anchors[bestAnchor]
		elites = nil
		for _, p := range order[:max(2, opt.Population/4)] {
			elites = append(elites, policies[p])
		}
		out.Generations++
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if winner == opt.Base {
		return out, nil
	}
	values, err := w.trainingBatch(ctx, spec, []Strategy{opt.Base, incumbent, winner}, opt.Validation, opt.Seed^0x6813abc910)
	if err != nil {
		return out, err
	}
	compare := func(a, b int) (float64, float64) {
		var sum, squares float64
		for i := range values[0] {
			d := objective.Value(values[a][i]) - objective.Value(values[b][i])
			sum += d
			squares += d * d
		}
		return sum / float64(opt.Validation), pairedSE(sum, squares, opt.Validation)
	}
	out.Validation = opt.Validation
	out.ValidationMeans = make([]VerseReturn, len(values))
	for p, episodes := range values {
		for _, v := range episodes {
			out.ValidationMeans[p].Count += v.Count / float64(opt.Validation)
			out.ValidationMeans[p].Shiyi += v.Shiyi / float64(opt.Validation)
		}
	}
	// Three fixed comparisons use a conservative normal-approximation margin.
	// This is an empirical guard, not a distribution-free dominance guarantee.
	anchorDelta, anchorSE := compare(1, 0)
	if anchorDelta > 2.4*anchorSE {
		out.Strategy, out.Accepted = incumbent, true
		out.Delta, out.SE = anchorDelta, anchorSE
	}
	delta, se := compare(2, 0)
	gap, gapSE := compare(2, 1)
	if delta > 2.4*se && (winner == incumbent || gap > 2.4*gapSE) {
		out.Strategy, out.Accepted, out.CandidateAccepted = winner, true, winner != incumbent
		out.Delta, out.SE = delta, se
	}
	return out, nil
}

func (w *World) trainingBatch(ctx context.Context, spec WorldStart, policies []Strategy, episodes int, seed int64) ([][]VerseReturn, error) {
	values := make([][]VerseReturn, len(policies))
	for p := range policies {
		values[p] = make([]VerseReturn, episodes)
	}
	seeds := rand.New(rand.NewSource(seed))
	for ep := 0; ep < episodes; ep++ {
		sd := seeds.Int63()
		for p, policy := range policies {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rng := rand.New(rand.NewSource(sd))
			st, err := w.Start(spec, rng)
			if err != nil {
				return nil, err
			}
			for step := 0; !st.Done; step++ {
				if step%32 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				if err := w.Step(st, w.BaselineAction(st, policy), rng); err != nil {
					return nil, err
				}
			}
			values[p][ep] = VerseReturn{Count: float64(st.Turns), Shiyi: float64(st.Shiyi)}
		}
	}
	return values, nil
}

func mutateContinuation(s Strategy, rng *rand.Rand, scale float64) Strategy {
	change := func(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x+rng.NormFloat64()*(hi-lo)*scale)) }
	s.RhymeBonus = change(s.RhymeBonus, -100, 500)
	s.RefreshPoetBonus = change(s.RefreshPoetBonus, 0, 1600)
	s.TargetPoolWeight = change(s.TargetPoolWeight, 0, 100)
	s.MiddleBonus = change(s.MiddleBonus, -300, 300)
	s.OffRhymePenalty = change(s.OffRhymePenalty, 0, 1000)
	s.RefreshGainFloor = change(s.RefreshGainFloor, -1, 5)
	s.SwitchThreshPct = change(s.SwitchThreshPct, 0, 1)
	s.DryGroupFloor = int(math.Round(change(float64(s.DryGroupFloor), 0, 30)))
	s.MinTargetPool = int(math.Round(change(float64(s.MinTargetPool), 0, 20)))
	s.LowLingxiFloor = int(math.Round(change(float64(s.LowLingxiFloor), 0, 30)))
	s.ShiyiWeight = 0
	return s
}
