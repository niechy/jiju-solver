// 集句策略引擎: 参考 verse_ref 贝叶斯优化/蒙特卡洛模拟的 7 维加权评分,
// 在基础权重之外引入更多变量:
//   - 诗句自带灵犀(32502: 76 句 +2 / 60 句 +5)  → 净收益
//   - 诗句自带诗意(32501: +10/+20)             → 净收益
//   - 特殊诗人(温庭筠 16 / 鱼玄机 41)          → 免费刷新 +1, 高优先抢夺
//   - 已收诗人集合(首次收集触发诗人效果)        → 新诗人加分
//   - 换组: 当前韵组剩余比例跌破阈值 → 转向存量多的大组
//   - 连错计数: 连错 2 次出韵永久 +1 消耗     → 强规避
//   - 刷新决策: 免费优先, 灵犀刷新只在值得时
package jiju

import (
	"fmt"
	"sort"
)

// RefreshPoetIDs 提供"免费刷新次数+1"的特殊诗人(温庭筠/鱼玄机)。
var RefreshPoetIDs = map[int]bool{16: true, 41: true}

// Strategy 集句策略配置(权重可调)。
type Strategy struct {
	// 净收益倍率: 灵犀净收益 × 此值为基础分
	NetGainWeight float64
	// 押韵加分(基础保命, 保持连击不断)
	RhymeBonus float64
	// 相粘加分(押韵+相粘完美连击)
	XiangZhanBonus float64
	// 新诗人加分(首次收集触发诗人效果)
	NewPoetBonus float64
	// 特殊诗人(送免费刷新)抢夺加分
	RefreshPoetBonus float64
	// 高收益抢夺: 净灵犀 ≥ 此值时额外加分
	RichThreshold int
	RichBonus     float64
	// 换组阈值比例: 当前韵组剩余 < 总剩余 × 此比例时考虑换组
	SwitchThreshPct float64
	// 换组时下家大组加分(每句剩余)
	TargetPoolWeight float64
	// 换组时下家新诗人加分(每位)
	TargetNewPoetWeight float64
	// 富人挑剔底线: 有免费刷新且被迫换组时, 下家组剩余须 ≥ 此值否则刷
	MinTargetPool int
	// 中间态(副韵交集, 无奖惩)奖励分: 它是免费的换组通道, 比 0 分好
	MiddleBonus float64
	// 目标导向刷新: 当前组未枯竭时, 也允许刷新若提升期望够大(相对净灵犀阈值)
	RefreshGainFloor float64
	// 灵犀预算警戒线: 灵犀低于此值时进入"保命模式"(只押韵, 不花灵犀刷)
	LowLingxiFloor int
	// 稀有诗意分: 每点诗意的折算分(用于 32501 +20 稀有句/诗人13首收+30)
	ShiyiWeight float64
	// 连错惩罚规避分(出韵会推高消耗, 强规避)
	OffRhymePenalty float64
	// 灵犀刷新成本(灵犀, 用于刷新决策)
	RefreshCost int
	// 当前组剩余 ≤ 此句数时视为枯竭, 花灵犀刷新需更谨慎
	DryGroupFloor int
	// OffRhymeRespectsGate: 当最优候选是"出韵"时, 是否仍要接受
	// LowLingxiFloor / RefreshGainFloor 两道预算闸的约束。
	//
	// 历史行为(默认 false): 最优是出韵 → **必刷**, 且整体跳过两道闸
	// (两处实现都用 `bestKind != KindOffRhyme` 把出韵排除在闸外)。
	// 设计意图是"出韵会永久推高消耗, 不可逆, 值得不惜代价规避"。
	//
	// 但用精确解(ExactMulti, shiyi-first)在缩小池上量过: 部署策略与最优值的
	// 差距里约 68.9% 来自这条硬规则 —— 最优常常是"接受这个出韵", 而反复刷新
	// 在灵犀见底时会把最后一点灵犀烧掉、直接终结连锁。开启本项即让出韵也过闸。
	//
	// 默认保持 false 不动线上行为; 置 true 是待评测的候选改动, 不是已证结论。
	OffRhymeRespectsGate bool
	// ExactStopRefresh: 用精确一步最优停止(world.go stopRefresh)取代三段硬门控
	// 刷新规则。留出种子实测诗意 +56.58±9.64(n=600, t=11.7), 句数 +5.96,
	// 权重不需要重训; 见 _research_c/policy.md。默认 false 保持线上行为不变。
	ExactStopRefresh bool
	// StopMargin: 精确最优停止要求的领先余量(打分单位)。0 表示用 DefaultStopMargin。
	StopMargin float64
	// StopDepth: 精确最优停止展开的步数。<=1 表示只评估"刷一次"(默认, 与实测
	// +54.7 的那版一致); 2 表示再多展开一步, 把"刷完之后还能再刷"的期权算进去
	// (V₂ = Emax + E[(K−maxS)⁺] ≥ Emax, 是严格推广)。
	StopDepth int
}

// DefaultStrategy 返回历史基础策略。当前真机使用 RunOptimized 中的
// 自适应后续策略 + Rollout；此处保留历史权重供对照，不等于在线完整算法。
// 经四方向算法探索(进化/前瞻/EV/价值函数, 见 whhx_analysis/集句算法探索_*.md)验证:
// 进化搜索得到的冠军权重 MeanTurns 最优(+19%, 125.76→149.92, 多种子稳定非过拟合),
// 优于手调基线及前瞻/EV/价值函数方向(且各方向叠加冲突)。故默认为进化冠军。
func DefaultStrategy() Strategy {
	return EvolvedStrategy()
}

// BaselineStrategy 返回手工调优的基线权重(保留作对照与测试基准)。
// 设计: 净灵犀收益主导(实战攻略"前期押大组韵保灵犀收入");
// 相粘权重压低(攻略"相粘收益低, 实战不必强求");
// 押韵保命分最高(出韵推高消耗不可逆);
// 刷新设上限(枯竭组别刷新也难中押韵, 硬选中间态更划算)。
func BaselineStrategy() Strategy {
	// 以下权重由 jijusim -mode search 在"真实消耗8/25灵犀"场景下随机搜索得到
	// (均值 25.7→76.4 句, 见研究报告)。核心: 押韵链主导, 刷新找押韵, 抢夺温/鱼免费刷新。
	return Strategy{
		NetGainWeight:       50.8, // 净灵犀收益主导(搜索后上调, 强区分 +2/+5 灵犀句)
		RhymeBonus:          178,  // 押韵保命
		XiangZhanBonus:      17,   // 相粘(低优先, 实测收益低不必强求)
		NewPoetBonus:        60,   // 新诗人(首收触发效果)
		RefreshPoetBonus:    232,  // 温庭筠/鱼玄机(免费更换+1, 战略价值最高)
		RichThreshold:       3,
		RichBonus:           33,
		SwitchThreshPct:     0.22, // 换组阈值(搜索后上调: 更早考虑换大组)
		TargetPoolWeight:    1.13,
		TargetNewPoetWeight: 8,
		MinTargetPool:       3,
		MiddleBonus:         64,   // 中间态=免费换组通道
		RefreshGainFloor:    0.56, // 灵犀刷新期望净提升阈值
		LowLingxiFloor:      12,   // 灵犀见底只押韵保命
		ShiyiWeight:         4.5,  // 诗意折算
		OffRhymePenalty:     243,  // 出韵强规避
		RefreshCost:         1,
		DryGroupFloor:       10,
	}
}

// GameState 集句运行时状态(跨轮累积)。
type GameState struct {
	Collected   map[int]bool // 已收集诗句 ID
	SeenPoets   map[int]bool // 已收过诗句的诗人
	OffRhymeRun int          // 连续出韵次数(≥2 触发消耗+1)
	Cost        int          // 当前集句消耗(灵犀)
	Lingxi      int          // 当前灵犀
	FreeRefresh int          // 当前免费刷新次数
}

// NewGameState 初始化状态。
func NewGameState(cost, lingxi, freeRefresh int) *GameState {
	return &GameState{
		Collected:   map[int]bool{},
		SeenPoets:   map[int]bool{},
		OffRhymeRun: 0,
		Cost:        cost,
		Lingxi:      lingxi,
		FreeRefresh: freeRefresh,
	}
}

// candidate 一个候选的评分结果。
type candidate struct {
	idx       int
	verse     *Verse
	kind      JudgeKind
	xiangZhan bool
	net       int     // 净灵犀收益(含判定+诗句效果-消耗)
	score     float64 // 综合加权分
	g1Match   bool
	newPoet   bool
	desc      string
}

// Decision 策略决策结果。
type Decision struct {
	Action  string // "select" / "refresh"
	PickIdx int    // select 时的候选下标(-1 = refresh)
	Kind    JudgeKind
	XiangZhan bool
	Desc    string
}

// g1Remaining 统计可选池里各主韵组的剩余句数(未收集)。
func (l *Library) g1Remaining(collected map[int]bool) map[int]int {
	m := map[int]int{}
	for i := range l.verses {
		v := &l.verses[i]
		if collected[v.ID] {
			continue
		}
		for _, g := range v.Group1 {
			m[g]++ // 一句可属多组(斜字句), 各计
		}
	}
	return m
}

// totalRemaining 未收集总句数。
func (l *Library) totalRemaining(collected map[int]bool) int {
	n := 0
	for i := range l.verses {
		if !collected[l.verses[i].ID] {
			n++
		}
	}
	return n
}

// scoreCandidate 给单个候选打分。
func (s *Strategy) scoreCandidate(
	prev *Verse, v *Verse, idx int, st *GameState,
	g1Rem map[int]int, totalRem int,
) candidate {
	kind, xz, _ := Judge(prev, v)
	g1Match := kind == KindRhyme

	// 连错计数变化
	newRun := st.OffRhymeRun
	switch kind {
	case KindOffRhyme:
		newRun++
	default:
		newRun = 0
	}

	// 净灵犀收益: 判定奖励 + 诗句效果 - 消耗
	gained := 0
	if kind == KindRhyme {
		gained += 7
	}
	if xz {
		gained += 2
	}
	gained += v.Lingxi
	isNewPoet := v.Poet != 0 && !st.SeenPoets[v.Poet]
	// 诗人首次收集的真实奖励(以 PolicyConfig 218xx 为准):
	//   仅 孟浩然/常建/贾岛/刘长卿/孟郊/韦应物 首收回灵犀(+10/+5)
	pr := PoetFirstRewards[v.Poet]
	if isNewPoet {
		gained += pr.Lingxi
	}
	net := gained - st.Cost

	score := float64(net) * s.NetGainWeight

	// 出韵强规避(尤其会触发连错+1 消耗时)
	if kind == KindOffRhyme {
		score -= s.OffRhymePenalty
		if newRun >= 2 {
			score -= s.OffRhymePenalty // 连错2次额外重罚
		}
	}

	// 押韵保命分
	if g1Match {
		score += s.RhymeBonus
	}
	// 中间态 = 免费换组通道(无奖惩且净0), 给正分以区别于"不押韵硬选"
	if kind == KindMiddle {
		score += s.MiddleBonus
	}
	// 相粘完美连击分
	if g1Match && xz {
		score += s.XiangZhanBonus
	}
	// 诗意折算(普通+10/稀有+20; 李贺首收+30; 不粘-5)
	shiyiGain := v.Shiyi
	if isNewPoet {
		shiyiGain += pr.Shiyi
	}
	if !xz { // 不相粘 → 诗意-5
		shiyiGain -= 5
	}
	score += float64(shiyiGain) * s.ShiyiWeight
	// 新诗人分
	if isNewPoet {
		score += s.NewPoetBonus
		// 首收回灵犀的诗人(孟浩然等) 额外加分(已含进 net, 这里给一点抢夺溢价)
		if pr.Lingxi > 0 {
			score += float64(pr.Lingxi) * s.NetGainWeight * 0.5
		}
		// 特殊诗人(送免费刷新) 抢夺: 温庭筠/鱼玄机, 战略价值最高
		if pr.FreeRefresh > 0 {
			score += s.RefreshPoetBonus
		}
	}
	// 高收益抢夺
	if net >= s.RichThreshold {
		score += s.RichBonus
	}

	// 换组: 当前组剩余跌破阈值, 且此候选不押韵(要换走), 评估下家池
	curG1Rem := 0
	if len(prev.Group1) > 0 {
		curG1Rem = g1Rem[prev.Group1[0]]
	}
	threshold := int(float64(totalRem) * s.SwitchThreshPct)
	if curG1Rem <= threshold && !g1Match && kind != KindOffRhyme {
		// 下家组剩余越多越优
		pool := 0
		for _, g := range v.Group1 {
			if g1Rem[g] > pool {
				pool = g1Rem[g]
			}
		}
		score += float64(pool) * s.TargetPoolWeight
		// 下家组新诗人越多越优(估: 此句诗人是新即算)
		if isNewPoet {
			score += s.TargetNewPoetWeight
		}
	}

	return candidate{
		idx: idx, verse: v, kind: kind, xiangZhan: xz,
		net: net, score: score, g1Match: g1Match, newPoet: isNewPoet,
	}
}

// Decide 从 3 个候选中决策: 选最优 / 刷新。
// cands 与屏幕行对齐(可含 nil=反查失败)。
// refreshUsed 本轮已刷新次数(含免费); 保留以便日志/扩展, 决策本身不限次数。
func (l *Library) Decide(prev *Verse, cands []*Verse, st *GameState, s Strategy, refreshUsed int) Decision {
	g1Rem := l.g1Remaining(st.Collected)
	totalRem := l.totalRemaining(st.Collected)
	curG1Rem := 0
	if len(prev.Group1) > 0 {
		curG1Rem = g1Rem[prev.Group1[0]]
	}
	threshold := int(float64(totalRem) * s.SwitchThreshPct)

	// 评分所有可用候选
	var scored []candidate
	for i, c := range cands {
		if c == nil {
			continue
		}
		scored = append(scored, s.scoreCandidate(prev, c, i, st, g1Rem, totalRem))
	}
	if len(scored) == 0 {
		// 候选全反查失败: 刷新也要留够本轮消耗底线, 避免词库缺句时无限烧钱
		if st.FreeRefresh > 0 || st.Lingxi >= st.Cost+s.RefreshCost {
			return Decision{Action: "refresh", PickIdx: -1, Desc: "无可用候选, 刷新"}
		}
		return Decision{Action: "select", PickIdx: -1, Desc: "无可用候选且灵犀不足, 跳过"}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	best := scored[0]

	// ---- 刷新决策(核心: 押韵链是命根, 不押韵就刷) ----
	// Historical approximation retained for baseline comparability.
	// Evidence excludes all displayed verses within a round;
	// World/Rollout models that pool explicitly instead of this approximation.
	shouldRefresh := false
	refreshWhy := ""

	// 1. 最优是出韵 → 必刷(出韵会连错推高消耗, 不可逆)
	if best.kind == KindOffRhyme {
		shouldRefresh = true
		refreshWhy = "规避出韵"
	}
	// 2. 最优不是押韵(中间态) → 当前组还有余量时, 刷找押韵以维持+7净流入
	if !best.g1Match && best.kind == KindMiddle {
		// 押韵命中率够高才值得刷(组未枯竭)
		if curG1Rem > s.DryGroupFloor {
			shouldRefresh = true
			refreshWhy = "刷找押韵保+7"
		}
	}
	// 3. 当前组枯竭被迫换组, 下家池太小 → 刷找大组
	if curG1Rem <= threshold && !best.g1Match {
		pool := 0
		for _, g := range best.verse.Group1 {
			if g1Rem[g] > pool {
				pool = g1Rem[g]
			}
		}
		if pool < s.MinTargetPool {
			shouldRefresh = true
			refreshWhy = fmt.Sprintf("下家池%d句过小, 刷找大组", pool)
		}
	}

	if shouldRefresh {
		// 刷新次数不设硬上限: 押韵链是集句命根, 限制刷新次数会直接压低集句数
		// (实测: 上限3→39句, 无上限→123句, 见研究)。真正防烧钱的是下面两道收益闸,
		// 而非次数上限——回不了本的刷新早被闸拦下, 能过闸的刷新都是净赚的, 不该限量。
		_ = refreshUsed // 保留参数以兼容签名, 不再用于限流
		// 要花灵犀刷时的两道闸(语义分层, 不冗余):
		//   闸A LowLingxiFloor(=12): 策略性保命——灵犀偏紧时只在"最优是出韵"才刷
		//   闸B cost+RefreshCost(=9, 见下方返回处): 硬底线——任何情况不得刷光导致透支
		//   因 12>9, 闸A通常先生效(更保守), 闸B是最后防线。
		if st.FreeRefresh <= 0 && totalRem > 0 {
			p := 1.0 - pow1m(float64(curG1Rem)/float64(totalRem), 3)
			expectedGain := p * 8.0 // 押韵+7 + 相粘期望≈+1
			// 闸A: 灵犀偏紧 → 只有最优真是出韵才值得花灵犀刷
			// (OffRhymeRespectsGate=true 时出韵也受闸约束, 见字段注释)
			exempt := best.kind == KindOffRhyme && !s.OffRhymeRespectsGate
			if st.Lingxi < s.LowLingxiFloor && !exempt {
				shouldRefresh = false
			} else if expectedGain < float64(s.RefreshCost)+s.RefreshGainFloor && !exempt {
				// 期望回不了本 → 硬选中间态止损
				shouldRefresh = false
			}
		}
	}

	if shouldRefresh {
		// 有免费刷新 → 直接用
		if st.FreeRefresh > 0 {
			return Decision{Action: "refresh", PickIdx: -1, Desc: refreshWhy + "(免费)"}
		}
		// Legacy state uses pre-entry balance. The live adapter adds back the
		// already-paid entry fee; this is not an extra selection charge.
		if st.Lingxi >= st.Cost+s.RefreshCost {
			return Decision{Action: "refresh", PickIdx: -1, Desc: refreshWhy + "(花1灵犀)"}
		}
		// 钱不够留底 → 硬选
	}

	best.desc = fmt.Sprintf("净%d 分%.0f", best.net, best.score)
	return Decision{
		Action: "select", PickIdx: best.idx,
		Kind: best.kind, XiangZhan: best.xiangZhan,
		Desc: best.desc,
	}
}

// pow1m 返回 (1-x)^k, 用于"3 抽都不中"概率。
func pow1m(x float64, k int) float64 {
	r := 1.0
	for i := 0; i < k; i++ {
		r *= (1 - x)
	}
	return r
}

// ApplySelect 选定后更新状态(净灵犀/连错/已收集/已见诗人)。
func (l *Library) ApplySelect(st *GameState, prev, picked *Verse, kind JudgeKind, xz bool) {
	gained := 0
	if kind == KindRhyme {
		gained += 7
	}
	if xz {
		gained += 2
	}
	gained += picked.Lingxi
	st.Lingxi += gained - st.Cost

	if kind == KindOffRhyme {
		st.OffRhymeRun++
		if st.OffRhymeRun >= 2 {
			st.Cost++ // 连错2次永久+1
			st.OffRhymeRun = 0
		}
	} else {
		st.OffRhymeRun = 0
	}

	st.Collected[picked.ID] = true
	if picked.Poet != 0 {
		first := !st.SeenPoets[picked.Poet]
		st.SeenPoets[picked.Poet] = true
		if first {
			pr := PoetFirstRewards[picked.Poet]
			if pr.Lingxi > 0 {
				st.Lingxi += pr.Lingxi // 首收回灵犀(孟浩然等)
			}
			if pr.FreeRefresh > 0 {
				st.FreeRefresh++ // 温庭筠/鱼玄机送免费刷新
			}
		}
	}
}
