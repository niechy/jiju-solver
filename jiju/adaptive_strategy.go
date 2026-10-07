package jiju

import "math"

// AdaptiveStrategies builds a small, state-specific policy family. The
// library/pool shape matters: a policy trained on a sparse qualification pool
// can be badly over-aggressive when most of the library is eligible.
func (w *World) AdaptiveStrategies(st *WorldState) []Strategy {
	n := len(st.left)
	if n == 0 {
		return []Strategy{EvolvedStrategy()}
	}
	maxGroup, nonEmpty := 0, 0
	for _, count := range st.groups {
		if count > 0 {
			nonEmpty++
			if count > maxGroup {
				maxGroup = count
			}
		}
	}
	concentration := float64(maxGroup) / float64(n)
	out := []Strategy{EvolvedStrategy()}
	count := CountStrategy()
	// The fitted sparse-pool policy is useful only when the eligible pool is
	// materially smaller than the library and not spread across many groups.
	if n <= 120 && (concentration >= 0.18 || nonEmpty <= 8) {
		out = append(out, count)
	}
	// A middle point reduces the risk of switching too aggressively when the
	// pool is borderline; it is still evaluated rather than assumed superior.
	if n > 40 && n <= 180 {
		mid := count
		factor := math.Min(1, math.Max(0, (120-float64(n))/80))
		mid.RhymeBonus = EvolvedStrategy().RhymeBonus + factor*(count.RhymeBonus-EvolvedStrategy().RhymeBonus)
		mid.RefreshPoetBonus = EvolvedStrategy().RefreshPoetBonus + factor*(count.RefreshPoetBonus-EvolvedStrategy().RefreshPoetBonus)
		mid.MiddleBonus = factor * count.MiddleBonus
		mid.TargetPoolWeight = factor * count.TargetPoolWeight
		mid.SwitchThreshPct = EvolvedStrategy().SwitchThreshPct + factor*(count.SwitchThreshPct-EvolvedStrategy().SwitchThreshPct)
		mid.RefreshGainFloor = EvolvedStrategy().RefreshGainFloor + factor*(count.RefreshGainFloor-EvolvedStrategy().RefreshGainFloor)
		mid.DryGroupFloor = int(math.Round(float64(EvolvedStrategy().DryGroupFloor) + factor*float64(count.DryGroupFloor-EvolvedStrategy().DryGroupFloor)))
		out = append(out, mid)
	}
	return out
}
