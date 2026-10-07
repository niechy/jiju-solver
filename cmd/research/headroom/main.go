// jijuresearch_b: 集句策略 headroom 研究 (scratch, workstream B).
//
// 只读复用 features/jiju 的导出 API:
//   - World / World.Start / World.Step / World.Legal / World.BaselineAction
//   - World.ExactMulti  (DrawRound 下的精确期望最优, shiyi-first)
//   - World.MultiRollout (在线战术求解器)
//   - Library.Verses / Judge (用于离线分类决策)
//
// 不修改任何既有文件; "缩减世界"通过临时 verse-config JSON 子集构造
// (LoadLibrary 从文件读), 因此无需访问任何未导出字段。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"jiju-solver/jiju"
)

// ---------------------------------------------------------------- library meta

type verseMeta struct {
	ID         int   `json:"ID"`
	BelongPoet []int `json:"BelongPoet"`
	Group1     []int `json:"Group1"`
}

type libMeta struct {
	recs  []json.RawMessage
	id2i  map[int]int
	metas []verseMeta
}

func loadMeta(path string) (*libMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recs []json.RawMessage
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	m := &libMeta{recs: recs, id2i: map[int]int{}}
	m.metas = make([]verseMeta, len(recs))
	for i, r := range recs {
		if err := json.Unmarshal(r, &m.metas[i]); err != nil {
			return nil, err
		}
		m.id2i[m.metas[i].ID] = i
	}
	return m, nil
}

func (m *libMeta) g1(id int) int {
	mm := m.metas[m.id2i[id]]
	if len(mm.Group1) > 0 {
		return mm.Group1[0]
	}
	return 0
}

func (m *libMeta) poet(id int) int {
	mm := m.metas[m.id2i[id]]
	if len(mm.BelongPoet) > 0 {
		return mm.BelongPoet[0]
	}
	return 0
}

func (m *libMeta) allIDs() []int {
	out := make([]int, 0, len(m.metas))
	for _, mm := range m.metas {
		out = append(out, mm.ID)
	}
	sort.Ints(out)
	return out
}

func (m *libMeta) writeSubset(path string, ids []int) error {
	seen := map[int]bool{}
	var out []json.RawMessage
	for _, id := range ids {
		if seen[id] {
			continue
		}
		i, ok := m.id2i[id]
		if !ok {
			return fmt.Errorf("unknown verse id %d", id)
		}
		seen[id] = true
		out = append(out, m.recs[i])
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0o644)
}

func buildWorld(m *libMeta, tmpPath string, ids []int, rules jiju.WorldRules) (*jiju.World, map[int]*jiju.Verse, error) {
	if err := m.writeSubset(tmpPath, ids); err != nil {
		return nil, nil, err
	}
	lib, err := jiju.LoadLibrary(tmpPath)
	if err != nil {
		return nil, nil, err
	}
	w, err := jiju.NewWorld(lib, rules)
	if err != nil {
		return nil, nil, err
	}
	vs := lib.Verses()
	vb := make(map[int]*jiju.Verse, len(vs))
	for i := range vs {
		vb[vs[i].ID] = &vs[i]
	}
	return w, vb, nil
}

// stratified 按主韵组比例从 ids 中确定性地抽取 n 句 (保持真实韵组偏斜)。
func stratified(m *libMeta, ids []int, n int) []int {
	byG := map[int][]int{}
	for _, id := range ids {
		byG[m.g1(id)] = append(byG[m.g1(id)], id)
	}
	type gs struct{ g, size int }
	var list []gs
	total := 0
	for g, v := range byG {
		sort.Ints(v)
		byG[g] = v
		list = append(list, gs{g, len(v)})
		total += len(v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].size != list[j].size {
			return list[i].size > list[j].size
		}
		return list[i].g < list[j].g
	})
	remaining := n
	alloc := map[int]int{}
	for _, e := range list {
		if remaining <= 0 {
			break
		}
		a := int(math.Round(float64(n) * float64(e.size) / float64(total)))
		if a < 2 {
			a = 2
		}
		if a > e.size {
			a = e.size
		}
		if a > remaining {
			a = remaining
		}
		alloc[e.g] = a
		remaining -= a
	}
	var out []int
	used := map[int]bool{}
	for _, e := range list {
		for i := 0; i < alloc[e.g] && i < len(byG[e.g]); i++ {
			out = append(out, byG[e.g][i])
			used[byG[e.g][i]] = true
		}
	}
	if len(out) < n {
		for _, id := range ids {
			if len(out) >= n {
				break
			}
			if !used[id] {
				out = append(out, id)
				used[id] = true
			}
		}
	}
	sort.Ints(out)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// stratifiedOrdered 返回"逐组轮转"的顺序, 使任意前缀都保持韵组均衡,
// 便于构造嵌套池 (P7 ⊂ P8 ⊂ P9 ⊂ P10) 以隔离池大小效应。
func stratifiedOrdered(m *libMeta, ids []int, n int) []int {
	byG := map[int][]int{}
	for _, id := range ids {
		byG[m.g1(id)] = append(byG[m.g1(id)], id)
	}
	type gs struct{ g, size int }
	var list []gs
	total := 0
	for g, v := range byG {
		sort.Ints(v)
		byG[g] = v
		list = append(list, gs{g, len(v)})
		total += len(v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].size != list[j].size {
			return list[i].size > list[j].size
		}
		return list[i].g < list[j].g
	})
	remaining := n
	for i := range list {
		a := int(math.Round(float64(n) * float64(list[i].size) / float64(total)))
		if a < 1 {
			a = 1
		}
		if a > list[i].size {
			a = list[i].size
		}
		if a > remaining {
			a = remaining
		}
		remaining -= a
		list[i].size = a // 复用 size 字段存配额
	}
	var out []int
	for pass := 0; ; pass++ {
		progress := false
		for _, e := range list {
			if pass >= e.size {
				continue
			}
			out = append(out, byG[e.g][pass])
			progress = true
		}
		if !progress {
			break
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// groupMix 按 "韵组:句数" 构造池, 用于激活按"当前组剩余句数"触发的规则
// (DryGroupFloor / SwitchThreshPct 在真实 70 句池里是活跃的, 但在均衡小池里不活跃)。
func groupMix(m *libMeta, spec string) ([]int, error) {
	var parts []string
	start := 0
	for i := 0; i <= len(spec); i++ {
		if i == len(spec) || spec[i] == ',' || spec[i] == ';' {
			if i > start {
				parts = append(parts, spec[start:i])
			}
			start = i + 1
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("bad mix spec %q", spec)
	}
	var out []int
	for _, p := range parts {
		var g, n int
		if _, err := fmt.Sscanf(p, "%d:%d", &g, &n); err != nil {
			return nil, fmt.Errorf("bad mix part %q", p)
		}
		found := 0
		for _, mm := range m.metas {
			if found >= n {
				break
			}
			if len(mm.Group1) > 0 && mm.Group1[0] == g {
				out = append(out, mm.ID)
				found++
			}
		}
		if found < n {
			return nil, fmt.Errorf("group %d has only %d verses, need %d", g, found, n)
		}
	}
	return out, nil
}

// ------------------------------------------------------------------- helpers

func shiyiObjective() jiju.VerseObjective {
	return jiju.VerseObjective{Priority: "shiyi-first"}
}

func solve(w *jiju.World, st *jiju.WorldState, limit, maxNodes int, timeout time.Duration) (jiju.ExactMultiResult, time.Duration) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	t0 := time.Now()
	r := w.ExactMulti(ctx, st.Clone(), limit, maxNodes, shiyiObjective())
	return r, time.Since(t0)
}

func valueOf(r jiju.ExactMultiResult, a int) (float64, bool) {
	for _, v := range r.Values {
		if v.Action == a {
			return v.Utility, true
		}
	}
	return 0, false
}

type pairedResult struct {
	Means    []float64
	SEs      []float64
	Deltas   []float64
	DeltaSEs []float64
	Turns    []float64
	Off      []float64
	Refr     []float64
}

// evalPaired 同一组 episode 种子 (公共随机数) 下逐局配对评估多个纯策略。
func evalPaired(w *jiju.World, spec jiju.WorldStart, strategies []jiju.Strategy, n int, seed int64) pairedResult {
	k := len(strategies)
	sums := make([]float64, k)
	sqs := make([]float64, k)
	dsums := make([]float64, k)
	dsqs := make([]float64, k)
	turns := make([]float64, k)
	off := make([]float64, k)
	refr := make([]float64, k)
	for i := 0; i < n; i++ {
		es := seed + int64(i)*1000003
		vals := make([]float64, k)
		for j, s := range strategies {
			rng := rand.New(rand.NewSource(es))
			st, err := w.Start(spec, rng)
			if err != nil {
				panic(err)
			}
			s0, t0 := st.Shiyi, st.Turns
			r0, o0 := st.Refreshes, st.OffRhymes
			for !st.Done {
				a := w.BaselineAction(st, s)
				if a < 0 {
					break
				}
				if err := w.Step(st, a, rng); err != nil {
					panic(err)
				}
			}
			vals[j] = float64(st.Shiyi - s0)
			turns[j] += float64(st.Turns - t0)
			refr[j] += float64(st.Refreshes - r0)
			off[j] += float64(st.OffRhymes - o0)
		}
		for j := range vals {
			sums[j] += vals[j]
			sqs[j] += vals[j] * vals[j]
			d := vals[j] - vals[0]
			dsums[j] += d
			dsqs[j] += d * d
		}
	}
	fn := float64(n)
	out := pairedResult{Means: make([]float64, k), SEs: make([]float64, k), Deltas: make([]float64, k), DeltaSEs: make([]float64, k),
		Turns: make([]float64, k), Off: make([]float64, k), Refr: make([]float64, k)}
	for j := 0; j < k; j++ {
		out.Means[j] = sums[j] / fn
		out.Turns[j] = turns[j] / fn
		out.Off[j] = off[j] / fn
		out.Refr[j] = refr[j] / fn
		if n > 1 {
			out.SEs[j] = math.Sqrt(math.Max(0, (sqs[j]-sums[j]*sums[j]/fn)/(fn-1)) / fn)
			out.DeltaSEs[j] = math.Sqrt(math.Max(0, (dsqs[j]-dsums[j]*dsums[j]/fn)/(fn-1)) / fn)
		}
		out.Deltas[j] = dsums[j] / fn
	}
	return out
}

// ------------------------------------------------------------------ ablations

type namedPolicy struct {
	Name  string
	Why   string
	Strat jiju.Strategy
	Ab    repAblate
}

func policies(base jiju.Strategy) []namedPolicy {
	noSwitchScore := base
	noSwitchScore.SwitchThreshPct = 0
	noSwitchScore.TargetPoolWeight = 0
	noSwitchScore.TargetNewPoetWeight = 0
	noSwitchScore.MinTargetPool = 0

	noOffPenalty := base
	noOffPenalty.OffRhymePenalty = 0

	noPoet := base
	noPoet.NewPoetBonus = 0
	noPoet.RefreshPoetBonus = 0
	noPoet.TargetNewPoetWeight = 0

	allWeights := noSwitchScore
	allWeights.OffRhymePenalty = 0
	allWeights.NewPoetBonus = 0
	allWeights.RefreshPoetBonus = 0

	return []namedPolicy{
		{"P0 current(EvolvedStrategy)", "在线后续打法", base, repAblate{}},
		{"a1 完全不刷新", "NoRefresh: 永远选当前最优候选", base, repAblate{NoRefresh: true}},
		{"a2 只允许免费刷新", "FreeOnly", base, repAblate{FreeOnly: true}},
		{"a3 关规则1(出韵→刷)", "NoOffRefresh", base, repAblate{NoOffRefresh: true}},
		{"a4 关规则2(中间态→刷)", "NoMiddleRefresh", base, repAblate{NoMiddleRefresh: true}},
		{"a5 关规则3(下家池小→刷)", "NoPoolRefresh", base, repAblate{NoPoolRefresh: true}},
		{"a6 刷新只保留规则1", "NoMiddleRefresh+NoPoolRefresh", base, repAblate{NoMiddleRefresh: true, NoPoolRefresh: true}},
		{"b 关换组/目标大组权重", "SwitchThreshPct/TargetPoolWeight/TargetNewPoetWeight/MinTargetPool=0", noSwitchScore, repAblate{}},
		{"c 关出韵评分惩罚", "OffRhymePenalty=0", noOffPenalty, repAblate{}},
		{"d 关诗人首收加分", "NewPoetBonus/RefreshPoetBonus/TargetNewPoetWeight=0", noPoet, repAblate{}},
		{"e 全部关闭", "规则1/2/3 + 权重全关", allWeights, repAblate{NoOffRefresh: true, NoMiddleRefresh: true, NoPoolRefresh: true}},
		{"ref BaselineStrategy", "手工基线权重", jiju.BaselineStrategy(), repAblate{}},
		{"ref CountStrategy", "句数目标后续打法", jiju.CountStrategy(), repAblate{}},
	}
}

func evalPolicies(sc *scenario, pols []namedPolicy, n int, seed int64) pairedResult {
	k := len(pols)
	sums := make([]float64, k)
	sqs := make([]float64, k)
	dsums := make([]float64, k)
	dsqs := make([]float64, k)
	turns := make([]float64, k)
	off := make([]float64, k)
	refr := make([]float64, k)
	for i := 0; i < n; i++ {
		es := seed + int64(i)*1000003
		vals := make([]float64, k)
		for j, p := range pols {
			rng := rand.New(rand.NewSource(es))
			sh, tn, rf, of, _ := repRollout(sc.World, sc, p.Strat, p.Ab, rng)
			vals[j] = float64(sh)
			turns[j] += float64(tn)
			refr[j] += float64(rf)
			off[j] += float64(of)
		}
		for j := range vals {
			sums[j] += vals[j]
			sqs[j] += vals[j] * vals[j]
			d := vals[j] - vals[0]
			dsums[j] += d
			dsqs[j] += d * d
		}
	}
	fn := float64(n)
	out := pairedResult{Means: make([]float64, k), SEs: make([]float64, k), Deltas: make([]float64, k), DeltaSEs: make([]float64, k),
		Turns: make([]float64, k), Off: make([]float64, k), Refr: make([]float64, k)}
	for j := 0; j < k; j++ {
		out.Means[j] = sums[j] / fn
		out.Turns[j] = turns[j] / fn
		out.Off[j] = off[j] / fn
		out.Refr[j] = refr[j] / fn
		if n > 1 {
			out.SEs[j] = math.Sqrt(math.Max(0, (sqs[j]-sums[j]*sums[j]/fn)/(fn-1)) / fn)
			out.DeltaSEs[j] = math.Sqrt(math.Max(0, (dsqs[j]-dsums[j]*dsums[j]/fn)/(fn-1)) / fn)
		}
		out.Deltas[j] = dsums[j] / fn
	}
	return out
}

// ------------------------------------------------------------------ scenarios

type scenario struct {
	Name      string
	World     *jiju.World
	VB        map[int]*jiju.Verse
	Spec      jiju.WorldStart
	Pool      int
	WorldSize int
	Order     []int // 库内诗句 ID 顺序 (与写入临时词库一致)
	Rules     jiju.WorldRules
}

func dedupOrder(ids []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func mkScenario(name, tmpPath string, m *libMeta, libIDs []int, spec jiju.WorldStart, rules jiju.WorldRules) (*scenario, error) {
	w, vb, err := buildWorld(m, tmpPath, libIDs, rules)
	if err != nil {
		return nil, err
	}
	coll := map[int]bool{spec.PrevID: true}
	for _, id := range spec.Collected {
		coll[id] = true
	}
	elig := map[int]bool{}
	explicit := spec.Eligible != nil
	for _, id := range spec.Eligible {
		elig[id] = true
	}
	pool := 0
	for _, id := range libIDs {
		if coll[id] {
			continue
		}
		if explicit && !elig[id] {
			continue
		}
		pool++
	}
	return &scenario{Name: name, World: w, VB: vb, Spec: spec, Pool: pool, WorldSize: len(libIDs),
		Order: dedupOrder(libIDs), Rules: rules}, nil
}

// ------------------------------------------------------------------- tracker

type tracker struct {
	vb   map[int]*jiju.Verse
	seen map[int]bool
	prev int
}

func newTracker(sc *scenario) *tracker {
	seen := map[int]bool{}
	add := func(id int) {
		if v := sc.VB[id]; v != nil && v.Poet != 0 {
			seen[v.Poet] = true
		}
	}
	add(sc.Spec.PrevID)
	for _, id := range sc.Spec.Collected {
		add(id)
	}
	for _, p := range sc.Spec.SeenPoets {
		seen[p] = true
	}
	return &tracker{vb: sc.VB, seen: seen, prev: sc.Spec.PrevID}
}

func (t *tracker) label(w *jiju.World, st *jiju.WorldState, a int) (jiju.JudgeKind, bool, bool, bool) {
	ids := w.CandidateIDs(st)
	if a < 0 || a >= len(ids) {
		return jiju.KindOffRhyme, false, false, false
	}
	v := t.vb[ids[a]]
	pv := t.vb[t.prev]
	if v == nil || pv == nil {
		return jiju.KindOffRhyme, false, false, false
	}
	k, xz, _ := jiju.Judge(pv, v)
	newPoet := v.Poet != 0 && !t.seen[v.Poet]
	r := jiju.PoetFirstRewards[v.Poet]
	richPoet := newPoet && (r.Lingxi > 0 || r.FreeRefresh > 0 || r.Shiyi > 0)
	return k, xz, newPoet, richPoet
}

func (t *tracker) advance(w *jiju.World, st *jiju.WorldState, a int) {
	if a == jiju.RefreshAction {
		return
	}
	ids := w.CandidateIDs(st)
	if a < 0 || a >= len(ids) {
		return
	}
	id := ids[a]
	t.prev = id
	if v := t.vb[id]; v != nil && v.Poet != 0 {
		t.seen[v.Poet] = true
	}
}

// ---------------------------------------------------------------------- main

type cfg struct {
	m        *libMeta
	rules    jiju.WorldRules
	tmpDir   string
	maxNodes int
	timeout  time.Duration
	mcN      int
	seed     int64
}

func main() {
	mode := flag.String("mode", "scale", "scale|real|headroom")
	libPath := flag.String("lib", "data/inscribe/inscribe_VerseConfig.json", "verse library")
	snapshot := flag.String("snapshot", "_research_b/snapshot-v6.json", "WorldStart snapshot JSON")
	snapshot2 := flag.String("snapshot2", "", "第二快照 (真实残局), 生成 depleted-* 场景")
	tmpDir := flag.String("tmpdir", "_research_b/tmp", "temp subset dir")
	outPath := flag.String("out", "_research_b/headroom-raw.json", "raw result JSON")
	maxNodes := flag.Int("maxnodes", 200000000, "ExactMulti node budget")
	timeout := flag.Duration("solvetimeout", 900*time.Second, "per-solve timeout")
	minPool := flag.Int("minpool", 5, "scale mode: first pool size")
	maxPool := flag.Int("maxpool", 13, "scale mode: last pool size")
	mcN := flag.Int("mc", 2000, "MC episodes for pure-strategy evaluation")
	mcSeed := flag.Int64("seed", 20260906, "deterministic MC seed")
	deltaEpisodes := flag.Int("delta-episodes", 400, "episodes for exact one-step regret decomposition")
	pools := flag.String("pools", "7,8,9", "headroom mode: pool sizes")
	liveSamples := flag.Int("live-samples", 0, ">0 → 额外评估在线 rollout 策略 (该采样数)")
	deltaMaxPool := flag.Int("delta-maxpool", 8, "只对 pool<=该值的状态做精确 δ 分解")
	liveMaxPool := flag.Int("live-maxpool", 0, "只对 pool<=该值的状态评估在线 rollout; 0=关闭")
	liveEpisodes := flag.Int("live-episodes", 30, "在线 rollout 评估局数")
	livePolicySamples := flag.Int("live-policysamples", 32, "在线 rollout 的后续策略预筛样本数")
	liveBudget := flag.Duration("live-budget", 0, "在线 rollout 每决策预算 (0=不限, 与线上 1500ms 区分)")
	only := flag.String("only", "", "只跑名字包含该子串的场景")
	nested := flag.Bool("nested", false, "fresh 场景使用嵌套池 (P7⊂P8⊂P9⊂P10, 固定韵组构成)")
	mix := flag.String("mix", "", "用 韵组:句数 构造池, 如 11:10,9:3 (激活按当前组剩余句数触发的规则)")
	poolLingxi := flag.String("feas-lingxi", "9,10,12,14,16,20,25,41", "feas mode: 灵犀取值")
	flag.Parse()

	m, err := loadMeta(*libPath)
	if err != nil {
		panic(err)
	}
	c := cfg{m: m, rules: jiju.DefaultWorldRules(), tmpDir: *tmpDir, maxNodes: *maxNodes, timeout: *timeout, mcN: *mcN, seed: *mcSeed}
	fmt.Printf("library=%s verses=%d rules=%+v\n", *libPath, len(m.recs), c.rules)

	switch *mode {
	case "scale":
		runScale(c, *minPool, *maxPool, *outPath)
	case "real":
		runReal(c, *snapshot)
	case "parity":
		runParity(c, *snapshot, parsePools(*pools), *mcN, *only)
	case "feas":
		runFeas(c, parsePools(*pools), parsePools(*poolLingxi))
	case "rules":
		runRules(c, parsePools(*pools), *nested, *mix, *mcN)
	case "headroom":
		hc := headroomCfg{cfg: c, outPath: *outPath, pools: parsePools(*pools), deltaEpisodes: *deltaEpisodes,
			deltaMaxPool: *deltaMaxPool, liveMaxPool: *liveMaxPool, liveEpisodes: *liveEpisodes,
			liveSamples: *liveSamples, livePolicySamples: *livePolicySamples, liveBudget: *liveBudget, only: *only, nested: *nested, mix: *mix}
		runHeadroom(hc, *snapshot, *snapshot2)
	default:
		panic("unknown mode")
	}
}

func parsePools(s string) []int {
	var out []int
	cur := 0
	has := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur = cur*10 + int(r-'0')
			has = true
			continue
		}
		if has {
			out = append(out, cur)
			cur, has = 0, false
		}
	}
	if has {
		out = append(out, cur)
	}
	return out
}

// ------------------------------------------------------------------- parity

// runParity 校验 repDecide 与真实 BaselineAction 逐状态一致。
func runParity(c cfg, snapshotPath string, ns []int, episodes int, only string) {
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		panic(err)
	}
	var snap jiju.WorldStart
	if err := json.Unmarshal(raw, &snap); err != nil {
		panic(err)
	}
	all := c.m.allIDs()
	collSnap := map[int]bool{}
	for _, id := range snap.Collected {
		collSnap[id] = true
	}
	var eligFree []int
	for _, id := range snap.Eligible {
		if !collSnap[id] && id != snap.PrevID {
			eligFree = append(eligFree, id)
		}
	}
	sort.Ints(eligFree)

	strategies := []struct {
		name string
		s    jiju.Strategy
	}{
		{"Evolved", jiju.EvolvedStrategy()},
		{"Baseline", jiju.BaselineStrategy()},
		{"Count", jiju.CountStrategy()},
		{"AdaptiveLow", func() jiju.Strategy { x := jiju.EvolvedStrategy(); x.LowLingxiFloor = 30; x.DryGroupFloor = 20; return x }()},
		{"ZeroSwitch", func() jiju.Strategy { x := jiju.EvolvedStrategy(); x.SwitchThreshPct = 0; x.MinTargetPool = 0; return x }()},
	}
	var total, mism, totalDecisions int
	type spec2 struct {
		name string
		spec jiju.WorldStart
	}
	var specs []spec2
	for _, n := range ns {
		// fresh
		ids := stratified(c.m, all, n)
		prev := 0
		for _, id := range all {
			if !containsInt(ids, id) {
				prev = id
				break
			}
		}
		specs = append(specs, spec2{fmt.Sprintf("fresh%d", n), jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: 25, FreeLimit: 1, FreeRemaining: 1, Eligible: ids, Collected: []int{prev}}})
		specs = append(specs, spec2{fmt.Sprintf("fresh%d-lowlingxi", n), jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: 14, FreeLimit: 1, FreeRemaining: 1, Eligible: ids, Collected: []int{prev}}})
		// 已在一轮内 (Candidates + Displayed)
		if n >= 3 {
			cands := ids[:3]
			disp := ids[3:min(len(ids), 4)]
			specs = append(specs, spec2{fmt.Sprintf("midround%d", n), jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: 25, FreeLimit: 1, FreeRemaining: 1,
				Eligible: ids, Collected: []int{prev}, Candidates: cands, Displayed: disp}})
		}
		// 快照型
		if len(eligFree) >= n {
			ids2 := stratified(c.m, eligFree, n)
			specs = append(specs, spec2{fmt.Sprintf("snap%d", n), jiju.WorldStart{PrevID: snap.PrevID, Cost: snap.Cost, Lingxi: 41,
				FreeLimit: snap.FreeLimit, FreeRemaining: snap.FreeLimit, OffRun: 1,
				Collected: append([]int(nil), snap.Collected...), SeenPoets: append([]int(nil), snap.SeenPoets...), Eligible: ids2}})
		}
	}
	// 共享世界: 每个 spec 单独建世界
	for _, sp := range specs {
		if only != "" && !containsStr(sp.name, only) {
			continue
		}
		var libIDs []int
		if containsStr(sp.name, "snap") {
			libIDs = append([]int{}, snap.Collected...)
			libIDs = append(libIDs, snap.PrevID)
			libIDs = append(libIDs, sp.spec.Eligible...)
			libIDs = addPoetAnchors(c.m, libIDs, snap.SeenPoets)
		} else {
			libIDs = append([]int{sp.spec.PrevID}, sp.spec.Eligible...)
		}
		sc, err := mkScenario(sp.name, filepath.Join(c.tmpDir, "parity-"+sp.name+".json"), c.m, libIDs, sp.spec, c.rules)
		if err != nil {
			fmt.Printf("[parity %s] SKIP %v\n", sp.name, err)
			continue
		}
		for _, st := range strategies {
			local, localMism, decisions := 0, 0, 0
			for i := 0; i < episodes; i++ {
				rng := rand.New(rand.NewSource(c.seed + int64(i)*1000003))
				_, tn, rf, _, mm := repRollout(sc.World, sc, st.s, repAblate{}, rng)
				localMism += mm
				local++
				decisions += tn + rf
			}
			total += local
			mism += localMism
			totalDecisions += decisions
			fmt.Printf("[parity %-22s %-10s] episodes=%d decisions=%d mismatch=%d\n", sp.name, st.name, local, decisions, localMism)
		}
	}
	fmt.Printf("\nPARITY TOTAL: episodes=%d decisions=%d mismatch=%d\n", total, totalDecisions, mism)
}

// ------------------------------------------------------------------- rules

// runRules: 沿当前策略轨迹统计三条硬规则的实际触发频次与"联合触发"情况。
func runRules(c cfg, ns []int, nested bool, mix string, episodes int) {
	all := c.m.allIDs()
	fmt.Printf("\n== rules: 刷新硬规则触发频次 (当前策略 EvolvedStrategy, episodes=%d) ==\n", episodes)
	fmt.Printf("%-28s %6s %7s %8s %8s %8s %9s %9s %9s\n", "state", "decis", "刷新", "off独", "mid独", "pool独", "off&pool", "三条全", "闸后刷")
	for _, n := range ns {
		var base []int
		switch {
		case mix != "":
			var err error
			base, err = groupMix(c.m, mix)
			if err != nil {
				panic(err)
			}
		case nested:
			maxN := 0
			for _, x := range ns {
				if x > maxN {
					maxN = x
				}
			}
			base = stratifiedOrdered(c.m, all, maxN)
		default:
			base = stratified(c.m, all, n)
		}
		ids := base
		if len(ids) > n {
			ids = ids[:n]
		}
		prev := 0
		for _, id := range all {
			if !containsInt(ids, id) {
				prev = id
				break
			}
		}
		libIDs := append([]int{prev}, ids...)
		name := fmt.Sprintf("pool%d", n)
		if mix != "" {
			name += "(mix " + mix + ")"
		}
		for _, lx := range []int{14, 25, 41} {
			spec := jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: lx, FreeLimit: 1, FreeRemaining: 1, Eligible: ids, Collected: []int{prev}}
			sc, err := mkScenario(name, filepath.Join(c.tmpDir, fmt.Sprintf("rules-%d-%d.json", n, lx)), c.m, libIDs, spec, c.rules)
			if err != nil {
				panic(err)
			}
			rc := newRepCtx(sc, sc.Rules)
			cur := jiju.DefaultStrategy()
			var decis, refr, offOnly, midOnly, poolOnly, offPool, allThree, gated int
			for i := 0; i < episodes; i++ {
				rng := rand.New(rand.NewSource(c.seed + int64(i)*1000003))
				st, err := sc.World.Start(sc.Spec, rng)
				if err != nil {
					panic(err)
				}
				rs := newRepState(rc, sc.Spec, sc.World, st)
				for !st.Done {
					if len(sc.World.CandidateIDs(st)) == 0 {
						break
					}
					a, why, o, m, p := repDecideEx(rs, cur, repAblate{}, rc)
					if a < 0 {
						break
					}
					decis++
					if a == jiju.RefreshAction {
						refr++
						cnt := 0
						if o {
							cnt++
						}
						if m {
							cnt++
						}
						if p {
							cnt++
						}
						switch {
						case o && !m && !p:
							offOnly++
						case !o && m && !p:
							midOnly++
						case !o && !m && p:
							poolOnly++
						case o && !m && p:
							offPool++
						}
						if cnt >= 3 {
							allThree++
						}
						if why == "" {
							gated++
						}
					}
					if err := sc.World.Step(st, a, rng); err != nil {
						panic(err)
					}
					rs.advance(rc, sc.World, st, a)
				}
			}
			fmt.Printf("%-28s %6d %7d %8d %8d %8d %9d %9d %9d   lingxi=%d\n", name, decis, refr, offOnly, midOnly, poolOnly, offPool, allThree, gated, lx)
		}
	}
}

// ------------------------------------------------------------------- feas

// runFeas: 真实池规模下, 灵犀(可用回合数)对精确求解可行性的影响。
func runFeas(c cfg, ns, lingxis []int) {
	all := c.m.allIDs()
	fmt.Printf("\n== feas: 池大小 × 灵犀 → ExactMulti 可行性 (cost=8 free=1, 新开局) ==\n")
	fmt.Printf("%5s %7s %10s %13s %11s %8s %9s\n", "pool", "lingxi", "complete", "nodes", "ms", "V*诗", "V*句")
	for _, n := range ns {
		ids := stratified(c.m, all, n)
		prev := 0
		for _, id := range all {
			if !containsInt(ids, id) {
				prev = id
				break
			}
		}
		libIDs := append([]int{prev}, ids...)
		for _, lx := range lingxis {
			tmp := filepath.Join(c.tmpDir, fmt.Sprintf("feas-%d-%d.json", n, lx))
			sc, err := mkScenario("feas", tmp, c.m, libIDs,
				jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: lx, FreeLimit: 1, FreeRemaining: 1, Eligible: ids, Collected: []int{prev}}, c.rules)
			if err != nil {
				panic(err)
			}
			rng := rand.New(rand.NewSource(c.seed))
			st, err := sc.World.Start(sc.Spec, rng)
			if err != nil {
				panic(err)
			}
			r, el := solve(sc.World, st, sc.Pool, c.maxNodes, c.timeout)
			fmt.Printf("%5d %7d %10v %13d %11.1f %8.1f %9.1f\n", sc.Pool, lx, r.Complete, r.Nodes, float64(el.Microseconds())/1000, r.Value.Shiyi, r.Value.Count)
		}
	}
}

// ------------------------------------------------------------------ scale

func runScale(c cfg, minPool, maxPool int, outPath string) {
	all := c.m.allIDs()
	type row struct {
		Pool      int     `json:"pool"`
		Complete  bool    `json:"complete"`
		Nodes     int     `json:"nodes"`
		Millis    float64 `json:"ms"`
		RootShiyi float64 `json:"root_shiyi"`
		RootCount float64 `json:"root_count"`
		Groups    string  `json:"groups"`
	}
	var rows []row
	fmt.Printf("\n== scale: ExactMulti 规模 (lingxi=41 cost=8 free=1, 无已收集, prev 不在池内) ==\n")
	fmt.Printf("%5s %9s %13s %10s %9s %7s %s\n", "pool", "complete", "nodes", "ms", "V*诗", "V*句", "韵组分布")
	for n := minPool; n <= maxPool; n++ {
		ids := stratified(c.m, all, n)
		prev := 0
		for _, id := range all {
			if !containsInt(ids, id) {
				prev = id
				break
			}
		}
		libIDs := append([]int{prev}, ids...)
		tmp := filepath.Join(c.tmpDir, fmt.Sprintf("scale-%d.json", n))
		sc, err := mkScenario(fmt.Sprintf("scale-%d", n), tmp, c.m, libIDs,
			jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: 41, FreeLimit: 1, FreeRemaining: 1, Eligible: ids, Collected: []int{prev}}, c.rules)
		if err != nil {
			panic(err)
		}
		rng := rand.New(rand.NewSource(c.seed))
		st, err := sc.World.Start(sc.Spec, rng)
		if err != nil {
			panic(err)
		}
		r, el := solve(sc.World, st, sc.Pool, c.maxNodes, c.timeout)
		g := groupHist(c.m, ids)
		rows = append(rows, row{sc.Pool, r.Complete, r.Nodes, float64(el.Microseconds()) / 1000, r.Value.Shiyi, r.Value.Count, g})
		fmt.Printf("%5d %9v %13d %10.1f %9.1f %7.1f %s\n", sc.Pool, r.Complete, r.Nodes, float64(el.Microseconds())/1000, r.Value.Shiyi, r.Value.Count, g)
		if !r.Complete {
			fmt.Println("  → 预算/超时耗尽, 停止放大")
			break
		}
	}
	writeJSON(outPath, map[string]any{"scale": rows})
}

func groupHist(m *libMeta, ids []int) string {
	h := map[int]int{}
	for _, id := range ids {
		h[m.g1(id)]++
	}
	var ks []int
	for k := range h {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	var sb []string
	for _, k := range ks {
		sb = append(sb, fmt.Sprintf("g%d:%d", k, h[k]))
	}
	s := ""
	for i, x := range sb {
		if i > 0 {
			s += ","
		}
		s += x
	}
	return s
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func writeJSON(path string, v any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s\n", path)
}

// ------------------------------------------------------------------- real

func runReal(c cfg, snapshotPath string) {
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		panic(err)
	}
	var spec jiju.WorldStart
	if err := json.Unmarshal(raw, &spec); err != nil {
		panic(err)
	}
	coll := map[int]bool{spec.PrevID: true}
	for _, id := range spec.Collected {
		coll[id] = true
	}
	left := 0
	for _, id := range spec.Eligible {
		if !coll[id] {
			left++
		}
	}
	fmt.Printf("\n== real: 在线规模 ==\n")
	fmt.Printf("snapshot=%s\neligible=%d collected=%d prev=%d left=%d lingxi=%d cost=%d free=%d/%d offrun=%d seen_poets=%d\n",
		snapshotPath, len(spec.Eligible), len(spec.Collected), spec.PrevID, left, spec.Lingxi, spec.Cost,
		spec.FreeRemaining, spec.FreeLimit, spec.OffRun, len(spec.SeenPoets))
	all := c.m.allIDs()
	fmt.Printf("full library eligible(Eligible=nil) left=%d\n", len(all)-1)
	w, _, err := buildWorld(c.m, filepath.Join(c.tmpDir, "real-full.json"), all, c.rules)
	if err != nil {
		panic(err)
	}
	full := spec
	full.Eligible, full.Collected, full.SeenPoets, full.Candidates, full.Displayed = nil, nil, nil, nil, nil
	rng := rand.New(rand.NewSource(c.seed))
	st, err := w.Start(full, rng)
	if err != nil {
		panic(err)
	}
	r, el := solve(w, st, len(all), c.maxNodes, 90*time.Second)
	fmt.Printf("full-pool(%d) exact attempt: complete=%v nodes=%d ms=%.1f\n", len(all)-1, r.Complete, r.Nodes, float64(el.Microseconds())/1000)
	// 快照 eligible 池 (71) 的精确尝试
	libIDs := append([]int{}, spec.Collected...)
	libIDs = append(libIDs, spec.PrevID)
	libIDs = append(libIDs, spec.Eligible...)
	libIDs = addPoetAnchors(c.m, libIDs, spec.SeenPoets)
	w2, _, err := buildWorld(c.m, filepath.Join(c.tmpDir, "real-snap.json"), libIDs, c.rules)
	if err != nil {
		panic(err)
	}
	rng2 := rand.New(rand.NewSource(c.seed))
	st2, err := w2.Start(spec, rng2)
	if err != nil {
		fmt.Printf("snapshot start err: %v\n", err)
		return
	}
	r2, el2 := solve(w2, st2, len(spec.Eligible), c.maxNodes, 90*time.Second)
	fmt.Printf("snapshot-pool(%d) exact attempt: complete=%v nodes=%d ms=%.1f\n", left, r2.Complete, r2.Nodes, float64(el2.Microseconds())/1000)
}

// ---------------------------------------------------------------- headroom

type headroomCfg struct {
	cfg               cfg
	outPath           string
	pools             []int
	deltaEpisodes     int
	deltaMaxPool      int
	liveMaxPool       int
	liveEpisodes      int
	liveSamples       int
	livePolicySamples int
	liveBudget        time.Duration
	only              string
	nested            bool
	mix               string
}

func runHeadroom(hc headroomCfg, snapshotPath, snapshot2Path string) {
	c := hc.cfg
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		panic(err)
	}
	var snap jiju.WorldStart
	if err := json.Unmarshal(raw, &snap); err != nil {
		panic(err)
	}
	all := c.m.allIDs()
	collSnap := map[int]bool{}
	for _, id := range snap.Collected {
		collSnap[id] = true
	}
	var eligFree []int
	for _, id := range snap.Eligible {
		if !collSnap[id] && id != snap.PrevID {
			eligFree = append(eligFree, id)
		}
	}
	sort.Ints(eligFree)
	fmt.Printf("snapshot=%s eligible=%d collected=%d prev=%d lingxi=%d cost=%d free=%d/%d offrun=%d\n",
		snapshotPath, len(snap.Eligible), len(snap.Collected), snap.PrevID, snap.Lingxi, snap.Cost, snap.FreeRemaining, snap.FreeLimit, snap.OffRun)
	fmt.Printf("snapshot free-eligible (未收集且非 prev) = %d\n", len(eligFree))

	var rows []map[string]any

	for _, n := range hc.pools {
		if len(eligFree) < n {
			continue
		}
		ids := stratified(c.m, eligFree, n)
		libIDs := append([]int{}, snap.Collected...)
		libIDs = append(libIDs, snap.PrevID)
		libIDs = append(libIDs, ids...)
		libIDs = addPoetAnchors(c.m, libIDs, snap.SeenPoets)
		baseSpec := func() jiju.WorldStart {
			return jiju.WorldStart{PrevID: snap.PrevID, Cost: snap.Cost, Lingxi: snap.Lingxi,
				FreeLimit: snap.FreeLimit, FreeRemaining: snap.FreeRemaining, OffRun: snap.OffRun,
				Collected: append([]int(nil), snap.Collected...), SeenPoets: append([]int(nil), snap.SeenPoets...),
				Eligible: ids}
		}
		s := baseSpec()
		hc.maybeRun("midrun-snap", fmt.Sprintf("midrun-snap-pool%d(lingxi%d,free%d)", n, s.Lingxi, s.FreeLimit), libIDs, s, &rows)
		s = baseSpec()
		s.Lingxi = 12
		hc.maybeRun("lowlingxi-snap", fmt.Sprintf("lowlingxi-snap-pool%d(lingxi12,free%d)", n, s.FreeLimit), libIDs, s, &rows)
		s = baseSpec()
		s.FreeLimit, s.FreeRemaining = 4, 4
		hc.maybeRun("highfree-snap", fmt.Sprintf("highfree-snap-pool%d(lingxi%d,free4)", n, s.Lingxi), libIDs, s, &rows)
	}

	for _, n := range hc.pools {
		var base []int
		switch {
		case hc.mix != "":
			var err error
			base, err = groupMix(c.m, hc.mix)
			if err != nil {
				panic(err)
			}
		case hc.nested:
			maxN := 0
			for _, x := range hc.pools {
				if x > maxN {
					maxN = x
				}
			}
			base = stratifiedOrdered(c.m, all, maxN)
		default:
			base = stratified(c.m, all, n)
		}
		ids := base
		if len(ids) > n {
			ids = ids[:n]
		}
		prev := 0
		for _, id := range all {
			if !containsInt(ids, id) {
				prev = id
				break
			}
		}
		libIDs := append([]int{prev}, ids...)
		tag := ""
		if hc.nested {
			tag = "nested-"
		}
		if hc.mix != "" {
			tag = "mix-" + sanitize(hc.mix) + "-"
		}
		s := jiju.WorldStart{PrevID: prev, Cost: 8, Lingxi: 25, FreeLimit: 1, FreeRemaining: 1, Eligible: ids, Collected: []int{prev}}
		hc.maybeRun("fresh", fmt.Sprintf("%sfresh-pool%d(cost8,lingxi25,free1)", tag, n), libIDs, s, &rows)
		s.Lingxi = 14
		hc.maybeRun("lowlingxi-fresh", fmt.Sprintf("%slowlingxi-fresh-pool%d(lingxi14,free1)", tag, n), libIDs, s, &rows)
		s.Lingxi = 41
		hc.maybeRun("rich-fresh", fmt.Sprintf("%srich-fresh-pool%d(lingxi41,free1)", tag, n), libIDs, s, &rows)
		s.Lingxi = 25
		s.FreeLimit, s.FreeRemaining = 4, 4
		hc.maybeRun("highfree-fresh", fmt.Sprintf("%shighfree-fresh-pool%d(lingxi25,free4)", tag, n), libIDs, s, &rows)
	}

	// 真实残局快照 (depleted)
	if snapshot2Path != "" {
		raw2, err := os.ReadFile(snapshot2Path)
		if err != nil {
			panic(err)
		}
		var snap2 jiju.WorldStart
		if err := json.Unmarshal(raw2, &snap2); err != nil {
			panic(err)
		}
		coll2 := map[int]bool{}
		for _, id := range snap2.Collected {
			coll2[id] = true
		}
		var eligFree2 []int
		for _, id := range snap2.Eligible {
			if !coll2[id] && id != snap2.PrevID {
				eligFree2 = append(eligFree2, id)
			}
		}
		sort.Ints(eligFree2)
		fmt.Printf("snapshot2=%s eligible=%d collected=%d prev=%d left(真实)=%d lingxi=%d free=%d/%d offrun=%d | 可用实验池=%d\n",
			snapshot2Path, len(snap2.Eligible), len(snap2.Collected), snap2.PrevID, len(eligFree2), snap2.Lingxi,
			snap2.FreeRemaining, snap2.FreeLimit, snap2.OffRun, len(eligFree2))
		for _, n := range hc.pools {
			if len(eligFree2) < n {
				continue
			}
			ids := stratified(c.m, eligFree2, n)
			libIDs := append([]int{}, snap2.Collected...)
			libIDs = append(libIDs, snap2.PrevID)
			libIDs = append(libIDs, ids...)
			libIDs = addPoetAnchors(c.m, libIDs, snap2.SeenPoets)
			mk := func(name string, lingxi int) {
				sp := jiju.WorldStart{PrevID: snap2.PrevID, Cost: snap2.Cost, Lingxi: lingxi,
					FreeLimit: snap2.FreeLimit, FreeRemaining: snap2.FreeLimit, OffRun: snap2.OffRun,
					Collected: append([]int(nil), snap2.Collected...), SeenPoets: append([]int(nil), snap2.SeenPoets...),
					Eligible: ids}
				hc.maybeRun("depleted-"+name, fmt.Sprintf("depleted-pool%d(lingxi%d,free%d)", n, lingxi, snap2.FreeLimit), libIDs, sp, &rows)
			}
			mk("as-is", snap2.Lingxi)
			mk("playable20", 20)
		}
	}

	writeJSON(hc.outPath, map[string]any{"rows": rows, "mc_seed": c.seed, "mc_episodes": c.mcN,
		"snapshot": snapshotPath, "pools": hc.pools, "delta_maxpool": hc.deltaMaxPool,
		"live_samples": hc.liveSamples, "live_maxpool": hc.liveMaxPool, "live_episodes": hc.liveEpisodes,
		"live_policysamples": hc.livePolicySamples, "live_budget": hc.liveBudget.String()})
}

func (hc headroomCfg) maybeRun(tag, name string, libIDs []int, spec jiju.WorldStart, rows *[]map[string]any) {
	if hc.only != "" && !containsStr(name, hc.only) {
		return
	}
	hc.runOne(name, libIDs, spec, rows)
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (hc headroomCfg) runOne(name string, libIDs []int, spec jiju.WorldStart, rows *[]map[string]any) {
	c := hc.cfg
	tmp := filepath.Join(c.tmpDir, sanitize(name)+".json")
	sc, err := mkScenario(name, tmp, c.m, libIDs, spec, c.rules)
	if err != nil {
		fmt.Printf("[%s] SKIP: %v\n", name, err)
		return
	}
	rng := rand.New(rand.NewSource(c.seed))
	st, err := sc.World.Start(sc.Spec, rng)
	if err != nil {
		fmt.Printf("[%s] SKIP start: %v\n", name, err)
		return
	}
	exact, el := solve(sc.World, st, sc.Pool, c.maxNodes, c.timeout)
	if !exact.Complete {
		fmt.Printf("\n---- %s pool=%d: 精确求解未完成 (nodes=%d, %.1fms) → 跳过\n", name, sc.Pool, exact.Nodes, float64(el.Microseconds())/1000)
		return
	}
	row := map[string]any{
		"state": name, "pool": sc.Pool, "world_size": sc.WorldSize,
		"lingxi": spec.Lingxi, "cost": spec.Cost, "free_limit": spec.FreeLimit, "free_remaining": spec.FreeRemaining, "off_run": spec.OffRun,
		"exact_nodes": exact.Nodes, "exact_ms": float64(el.Microseconds()) / 1000,
		"optimal_shiyi": exact.Value.Shiyi, "optimal_count": exact.Value.Count, "optimal_action": exact.Action,
		"done_at_root": st.Done,
	}
	fmt.Printf("\n---- %s pool=%d lingxi=%d cost=%d free=%d/%d | exact nodes=%d %.1fms V*=(诗%.2f 句%.2f) a*=%d done=%v\n",
		name, sc.Pool, spec.Lingxi, spec.Cost, spec.FreeRemaining, spec.FreeLimit, exact.Nodes, float64(el.Microseconds())/1000, exact.Value.Shiyi, exact.Value.Count, exact.Action, st.Done)

	var rootVals []map[string]any
	for _, v := range exact.Values {
		rootVals = append(rootVals, map[string]any{"action": v.Action, "shiyi": v.Mean.Shiyi, "count": v.Mean.Count})
	}
	row["root_values"] = rootVals

	cur := jiju.DefaultStrategy()
	tr := newTracker(sc)
	aCur := sc.World.BaselineAction(st, cur)
	if aCur >= 0 {
		if q, ok := valueOf(exact, aCur); ok {
			row["current_action"] = aCur
			row["current_q_at_root"] = q
			row["root_regret"] = exact.Value.Shiyi - q
			k, _, np, rp := tr.label(sc.World, st, aCur)
			row["current_kind"] = k.String()
			row["current_new_poet"] = np
			row["current_rich_poet"] = rp
			fmt.Printf("     current@root: a=%d kind=%s newPoet=%v Q=%.2f → root regret=%.2f\n", aCur, k.String(), np, q, exact.Value.Shiyi-q)
		}
	}

	pols := policies(cur)
	pr := evalPolicies(sc, pols, c.mcN, c.seed)
	var stratRows []map[string]any
	for i, a := range pols {
		stratRows = append(stratRows, map[string]any{
			"name": a.Name, "why": a.Why,
			"mean_shiyi": pr.Means[i], "se": pr.SEs[i],
			"gap_vs_optimal": exact.Value.Shiyi - pr.Means[i],
			"delta_vs_current": pr.Deltas[i], "delta_se": pr.DeltaSEs[i],
			"mean_turns": pr.Turns[i], "mean_refresh": pr.Refr[i], "mean_off": pr.Off[i],
		})
		fmt.Printf("     %-32s 诗 %8.2f ±%.2f  gap %7.2f  Δ%+7.2f ±%.2f (句%.1f 刷%.1f 出韵%.1f)\n",
			a.Name, pr.Means[i], pr.SEs[i], exact.Value.Shiyi-pr.Means[i], pr.Deltas[i], pr.DeltaSEs[i], pr.Turns[i], pr.Refr[i], pr.Off[i])
	}
	row["strategies"] = stratRows

	// 在线 rollout 策略 (planLive 等价: MultiRollout + shiyi-first)
	if hc.liveMaxPool > 0 && sc.Pool <= hc.liveMaxPool {
		opt := jiju.DefaultRolloutOptions()
		opt.Base = jiju.EvolvedStrategy()
		opt.Seed = c.seed
		if hc.liveSamples > 0 {
			opt.Samples = hc.liveSamples
			opt.MaxSamples = hc.liveSamples
		}
		opt.PolicySamples = hc.livePolicySamples
		opt.Budget = hc.liveBudget
		liveMean, liveSE, turns := evalLive(sc, opt, hc.liveEpisodes, c.seed)
		row["live"] = map[string]any{"samples": opt.Samples, "max_samples": opt.MaxSamples,
			"policy_samples": opt.PolicySamples, "budget": opt.Budget.String(), "episodes": hc.liveEpisodes,
			"mean_shiyi": liveMean, "se": liveSE,
			"gap_vs_optimal": exact.Value.Shiyi - liveMean, "mean_turns": turns}
		fmt.Printf("     %-36s 诗 %8.2f ±%.2f  gap %7.2f (句%.1f, samples=%d psc=%d)\n",
			"LIVE rollout(planLive 等价)", liveMean, liveSE, exact.Value.Shiyi-liveMean, turns, opt.Samples, opt.PolicySamples)
	}

	if hc.deltaEpisodes > 0 && sc.Pool <= hc.deltaMaxPool {
		d := deltaDecompose(c, sc, hc.deltaEpisodes)
		row["delta"] = d
		fmt.Printf("     δ分解: episodes=%d 决策=%d incomplete=%d | gap=%.2f ±%.2f (SE)\n",
			d["episodes"], d["decisions"], d["incomplete"], d["mean_gap"], d["se"])
		total := math.Max(1, float64(d["decisions"].(int)))
		for _, cat := range d["by_decision"].([]map[string]any) {
			fmt.Printf("        %-34s n=%5.0f (%5.1f%%) Σδ=%8.2f\n", cat["class"], cat["count"],
				100*cat["count"].(float64)/total, cat["sum_delta"])
		}
	}
	*rows = append(*rows, row)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '/', '\\', ':', ' ', '(', ')', ',':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

func addPoetAnchors(m *libMeta, libIDs, seenPoets []int) []int {
	have := map[int]bool{}
	for _, id := range libIDs {
		if p := m.poet(id); p != 0 {
			have[p] = true
		}
	}
	for _, p := range seenPoets {
		if have[p] {
			continue
		}
		for _, mm := range m.metas {
			if len(mm.BelongPoet) > 0 && mm.BelongPoet[0] == p {
				libIDs = append(libIDs, mm.ID)
				have[p] = true
				break
			}
		}
	}
	return libIDs
}

// evalLive 以在线战术求解器 (MultiRollout, shiyi-first) 作为策略做 MC 评估。
func evalLive(sc *scenario, opt jiju.RolloutOptions, n int, seed int64) (float64, float64, float64) {
	var sum, sq, turns float64
	for i := 0; i < n; i++ {
		rng := rand.New(rand.NewSource(seed + int64(i)*1000003))
		st, err := sc.World.Start(sc.Spec, rng)
		if err != nil {
			panic(err)
		}
		s0, t0 := st.Shiyi, st.Turns
		for !st.Done {
			r, err := sc.World.MultiRollout(context.Background(), st.Clone(), opt, shiyiObjective())
			if err != nil {
				panic(err)
			}
			a := r.Action
			if a < 0 || !sc.World.Legal(st, a) {
				a = sc.World.BaselineAction(st, opt.Base)
			}
			if a < 0 {
				break
			}
			if err := sc.World.Step(st, a, rng); err != nil {
				panic(err)
			}
		}
		x := float64(st.Shiyi - s0)
		sum += x
		sq += x * x
		turns += float64(st.Turns - t0)
	}
	fn := float64(n)
	mean := sum / fn
	se := 0.0
	if n > 1 {
		se = math.Sqrt(math.Max(0, (sq-sum*sum/fn)/(fn-1)) / fn)
	}
	return mean, se, turns / fn
}

// deltaDecompose 沿 current 策略轨迹的精确一步遗憾分解。
// 恒等式: V*(s0) - V^π(s0) = E_π[ Σ_t ( V*(s_t) - Q*(s_t, a_π(s_t)) ) ]
func deltaDecompose(c cfg, sc *scenario, episodes int) map[string]any {
	cur := jiju.DefaultStrategy()
	rc := newRepCtx(sc, sc.Rules)
	type cat struct {
		count int
		sum   float64
	}
	cats := map[string]*cat{}
	get := func(k string) *cat {
		if cc, ok := cats[k]; ok {
			return cc
		}
		cc := &cat{}
		cats[k] = cc
		return cc
	}
	var sumDelta, sqDelta float64
	totalDecisions, incomplete, used := 0, 0, 0
	for i := 0; i < episodes; i++ {
		rng := rand.New(rand.NewSource(c.seed + int64(i)*1000003))
		st, err := sc.World.Start(sc.Spec, rng)
		if err != nil {
			panic(err)
		}
		rs := newRepState(rc, sc.Spec, sc.World, st)
		ep := 0.0
		bad := false
		for !st.Done {
			if len(sc.World.CandidateIDs(st)) == 0 {
				break
			}
			a, why := repDecide(rs, cur, repAblate{}, rc)
			if a < 0 {
				break
			}
			if real := sc.World.BaselineAction(st, cur); real != a {
				panic(fmt.Sprintf("replica parity broken at %s: rep=%d real=%d", sc.Name, a, real))
			}
			r := sc.World.ExactMulti(context.Background(), st.Clone(), sc.Pool, c.maxNodes, shiyiObjective())
			if !r.Complete {
				incomplete++
				bad = true
				break
			}
			q, ok := valueOf(r, a)
			if !ok {
				bad = true
				break
			}
			d := r.Value.Shiyi - q
			ep += d
			totalDecisions++
			class := classifyRep(sc, rs, rc, st, a, why, r.Action)
			cc := get(class)
			cc.count++
			cc.sum += d
			if err := sc.World.Step(st, a, rng); err != nil {
				panic(err)
			}
			rs.advance(rc, sc.World, st, a)
		}
		if bad {
			continue
		}
		used++
		sumDelta += ep
		sqDelta += ep * ep
	}
	fn := float64(used)
	mean, se := 0.0, 0.0
	if used > 0 {
		mean = sumDelta / fn
		if used > 1 {
			se = math.Sqrt(math.Max(0, (sqDelta-sumDelta*sumDelta/fn)/(fn-1)) / fn)
		}
	}
	var by []map[string]any
	keys := make([]string, 0, len(cats))
	for k := range cats {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return cats[keys[i]].sum > cats[keys[j]].sum })
	for _, k := range keys {
		by = append(by, map[string]any{"class": k, "count": float64(cats[k].count), "sum_delta": cats[k].sum,
			"share": float64(cats[k].count) / math.Max(1, float64(totalDecisions))})
	}
	return map[string]any{
		"episodes": used, "incomplete": incomplete, "decisions": totalDecisions,
		"mean_gap": mean, "se": se, "by_decision": by,
	}
}

// classifyRep 按"当前策略在该状态下用掉的规则"给遗憾分类。
func classifyRep(sc *scenario, rs *repState, rc *repCtx, st *jiju.WorldState, a int, why string, optA int) string {
	optRefresh := optA == jiju.RefreshAction
	if a == jiju.RefreshAction {
		tag := "刷"
		switch {
		case why != "":
			tag += "(" + why + ")"
		case rs.free > 0:
			tag += "(免费-闸后)"
		default:
			tag += "(付费-闸后)"
		}
		if optRefresh {
			return tag + " / 最优也刷"
		}
		return tag + " / 最优应选句"
	}
	// 选句
	prevV := rc.vb[rs.prev]
	v := rc.vb[rs.cands[a]]
	kind, xz, _ := jiju.Judge(prevV, v)
	newPoet := v.Poet != 0 && !rs.seen[v.Poet]
	tag := "选句:" + kind.String()
	if newPoet {
		tag += "+新诗人"
	}
	if !xz && kind != jiju.KindOffRhyme {
		tag += "(不粘-5)"
	}
	if optRefresh {
		return tag + " / 最优应刷"
	}
	if a != optA {
		return tag + " / 最优他句"
	}
	return tag + " / 已最优"
}
