// 集句进化得到的冠军权重(第一代搜索, 验证集 +15.2%)。
// 由 cmd/tools/jijuevolve 在 cost8/lingxi25/seed42 场景进化 20 代得到,
// 验证集(换种子, sims=5000) MeanTurns=144.93, 基线 125.76。
//
// 与基线的关键差异及解读:
//   - RhymeBonus 178→319: 押韵链是命根, 权重需压倒一切
//   - XiangZhanBonus 17→97: 相粘(+2灵犀)被严重低估, 进化发现其真实价值
//   - NetGainWeight 50.8→69.5: 即时净收益仍是基础
//   - MiddleBonus 64→17: 中间态大幅贬值——宁可花灵犀刷找押韵也不硬接中间态
//   - NewPoetBonus 60→0, RichBonus 33→3.5: 这些是噪声维度, 进化压为近零
//   - RefreshGainFloor 0.56→0.72, DryGroupFloor 10→15: 刷新闸微调
package jiju

// EvolvedStrategy 进化冠军权重(第三轮精炼, 从第二轮冠军出发, 验证集 150.88)。
// 由 cmd/tools/jijuevolve 进化得到: 第二轮(seed777)得 148.31,
// 第三轮(-fromevolved 精炼, seed2024)在代19~24 跳出收敛, 验证集(换种子, sims=8000)
// MeanTurns=150.88, 基线 125.76, 提升 +20.0%。
//
// 与基线的关键差异及解读:
//   - MiddleBonus 64→0: 中间态彻底贬值——宁可花灵犀刷找押韵也绝不硬接中间态
//   - LowLingxiFloor 12→5, RefreshGainFloor 0.56→0.88, MinTargetPool 3→2:
//     刷新更激进(保命线/干涸线/换组池线全降低)——刷找押韵回报比基线预期高,
//     这是提升的核心机制(冠军刷新364次/局 > 基线325次, 但每轮净收益更高)
//   - RhymeBonus 178→235, NetGainWeight 50.8→31.8: 押韵绝对主导, 相对比例重排
//   - NewPoetBonus 60→0: 新诗人分是噪声(真实首收奖励已计入净收益)
// 注: 不同种子收敛到不同局部最优(如 XiangZhanBonus 第一轮97/第三轮4.4),
//     该维度被 RhymeBonus 主导, 对决策序几乎无影响, 各解近似等价。
func EvolvedStrategy() Strategy {
	return Strategy{
		NetGainWeight:       31.84,
		RhymeBonus:          234.84,
		XiangZhanBonus:      4.36,
		NewPoetBonus:        0,
		RefreshPoetBonus:    246.83,
		RichThreshold:       0,
		RichBonus:           20.74,
		SwitchThreshPct:     0.1947,
		TargetPoolWeight:    0.829,
		TargetNewPoetWeight: 8,
		MinTargetPool:       2,
		MiddleBonus:         0,
		RefreshGainFloor:    0.8752,
		LowLingxiFloor:      5,
		ShiyiWeight:         5.09,
		OffRhymePenalty:     50,
		RefreshCost:         1,
		DryGroupFloor:       9,
		// 刷新规则改用精确一步最优停止(world.go stopRefresh)。
		// 真实代码路径实测(配对局, 快照 runtime-main, 128采样/32筛选, 诗意优先):
		//   诗意 473.7 -> 528.4 (+54.7, CI95 [38.6, 70.9], n=200, seed42)
		//   句数 43.33 -> 49.16 (+5.83, CI95 [4.18, 7.47])
		// 权重全部不动, 只换刷新判据。第三方复刻实现独立测得 +56.6±9.6(n=600),
		// 量级一致。
		ExactStopRefresh: true,
	}
}

// EvolvedStrategy373 针对最优开局句373(11组)精炼的权重, 验证集 166.73。
// 由 jijuevolve -init 373 进化得到。与通用冠军相比换组更激进
// (SwitchThreshPct 0.195→0.257, DryGroupFloor 9→17, MinTargetPool 2→11):
// 大组开局时, 当前组容易长期不枯竭, 提高干涸线能更果断换向更大的组。
// 注意: 此权重在大组开局(11/9)最优, 小组开局可能不如通用冠军。
func EvolvedStrategy373() Strategy {
	return Strategy{
		NetGainWeight:       41.00,
		RhymeBonus:          337.52,
		XiangZhanBonus:      31.32,
		NewPoetBonus:        0,
		RefreshPoetBonus:    214.88,
		RichThreshold:       0,
		RichBonus:           26.24,
		SwitchThreshPct:     0.2573,
		TargetPoolWeight:    0.167,
		TargetNewPoetWeight: 8,
		MinTargetPool:       11,
		MiddleBonus:         -60, // 惩罚中间态(隐性断链): 迫使押韵或换组, +7.1% 提升
		RefreshGainFloor:    0.775,
		LowLingxiFloor:      5,
		ShiyiWeight:         5.79,
		OffRhymePenalty:     50,
		RefreshCost:         1,
		DryGroupFloor:       17,
	}
}

// EvolvedStrategyV2 第二轮进化冠军(备用, 验证集 148.31)。
func EvolvedStrategyV2() Strategy {
	return Strategy{
		NetGainWeight:       28.44,
		RhymeBonus:          202.28,
		XiangZhanBonus:      4.36,
		NewPoetBonus:        0,
		RefreshPoetBonus:    231.27,
		RichThreshold:       0,
		RichBonus:           23.99,
		SwitchThreshPct:     0.1967,
		TargetPoolWeight:    0.768,
		TargetNewPoetWeight: 8,
		MinTargetPool:       2,
		MiddleBonus:         0,
		RefreshGainFloor:    0.9305,
		LowLingxiFloor:      6,
		ShiyiWeight:         5.23,
		OffRhymePenalty:     50,
		RefreshCost:         1,
		DryGroupFloor:       8,
	}
}

// EvolvedStrategyV1 第一代进化冠军(备用, 验证集 144.93)。
func EvolvedStrategyV1() Strategy {
	return Strategy{
		NetGainWeight:       69.51,
		RhymeBonus:          319.39,
		XiangZhanBonus:      97.13,
		NewPoetBonus:        0,
		RefreshPoetBonus:    298.33,
		RichThreshold:       0,
		RichBonus:           3.53,
		SwitchThreshPct:     0.2206,
		TargetPoolWeight:    1.127,
		TargetNewPoetWeight: 8,
		MinTargetPool:       4,
		MiddleBonus:         17.25,
		RefreshGainFloor:    0.7245,
		LowLingxiFloor:      10,
		ShiyiWeight:         8.79,
		OffRhymePenalty:     112.08,
		RefreshCost:         1,
		DryGroupFloor:       15,
	}
}
