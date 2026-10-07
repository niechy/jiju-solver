package jiju

// CountStrategy is fitted against full-episode collected count in World,
// not the legacy replace-pool simulator or Shiyi. See the September 7 report.
// It is a continuation heuristic, not a claim of global optimality.
func CountStrategy() Strategy {
	return Strategy{
		NetGainWeight:    32,
		RhymeBonus:       406.72072517747586,
		RefreshPoetBonus: 370.50936030728354,
		SwitchThreshPct:  0.751698984508426,
		TargetPoolWeight: 59.32915286748699,
		MinTargetPool:    8,
		MiddleBonus:      -296.05263916749965,
		RefreshGainFloor: 0.13160729949480032,
		LowLingxiFloor:   11,
		OffRhymePenalty:  62.052394569592906,
		RefreshCost:      1,
		DryGroupFloor:    26,
	}
}
