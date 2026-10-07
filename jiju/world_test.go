package jiju

import (
	"context"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"testing"
)

func tinyWorld(t testing.TB, n int, mode DrawMode) *World {
	t.Helper()
	lib := &Library{}
	for id := 1; id <= n; id++ {
		lib.verses = append(lib.verses, Verse{ID: id, Group1: []int{1}, Group2: []int{id % 2}, Group3: []int{1}})
	}
	rules := DefaultWorldRules()
	rules.DrawMode = mode
	w, err := NewWorld(lib, rules)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func startWorld(t *testing.T, w *World, spec WorldStart) *WorldState {
	t.Helper()
	s, err := w.Start(spec, rand.New(rand.NewSource(42)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWorldEntryAndZeroBalanceSelection(t *testing.T) {
	w := tinyWorld(t, 2, DrawRound)
	s := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 8, Cost: 8, FreeLimit: 1})
	if s.Lingxi != 0 || !w.Legal(s, 0) || w.Legal(s, RefreshAction) || s.n != 1 {
		t.Fatalf("paid final single-candidate round: %+v", s)
	}
	if err := w.Step(s, 0, rand.New(rand.NewSource(1))); err != nil {
		t.Fatal(err)
	}
	if s.Lingxi != 9 || s.Turns != 1 || !s.Done {
		t.Fatalf("entry was charged twice or reward wrong: %+v", s)
	}
}

func TestWorldRoundExclusionAndReset(t *testing.T) {
	w := tinyWorld(t, 6, DrawRound)
	s := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 50, Cost: 8, FreeLimit: 2})
	first := w.CandidateIDs(s)
	if err := w.Step(s, RefreshAction, rand.New(rand.NewSource(2))); err != nil {
		t.Fatal(err)
	}
	if s.n != 2 || w.Legal(s, RefreshAction) {
		t.Fatalf("tail should contain two verses and disable further refresh: %+v", s)
	}
	for _, id := range w.CandidateIDs(s) {
		if slices.Contains(first, id) {
			t.Fatal("round repeated candidate")
		}
	}
	chosen := w.CandidateIDs(s)[0]
	if err := w.Step(s, 0, rand.New(rand.NewSource(3))); err != nil {
		t.Fatal(err)
	}
	if s.blockedN != 0 || s.Free != 2 || len(s.left) != 4 {
		t.Fatalf("next round did not reset: %+v", s)
	}
	for _, id := range first {
		if s.where[w.ids[id]] < 0 {
			t.Fatal("unselected verse removed permanently")
		}
	}
	if s.where[w.ids[chosen]] >= 0 {
		t.Fatal("collected verse still in pool")
	}
}

func TestWorldDrawModesAndHistory(t *testing.T) {
	for _, mode := range []DrawMode{DrawRound, DrawReplace, DrawExcludeCurrent} {
		t.Run(string(mode), func(t *testing.T) {
			w := tinyWorld(t, 5, mode)
			s := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 1, Cost: 8, FreeLimit: 1,
				Candidates: []int{2, 3, 4}, Displayed: []int{5}})
			if got := w.Legal(s, RefreshAction); got != (mode != DrawRound) {
				t.Fatalf("refresh legal=%v", got)
			}
			if mode == DrawExcludeCurrent {
				if err := w.Step(s, RefreshAction, rand.New(rand.NewSource(0))); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(w.CandidateIDs(s), []int{5}) || s.Lingxi != 0 || !w.Legal(s, 0) {
					t.Fatalf("paid refresh should allow selecting at zero: %+v", s)
				}
			}
		})
	}
}

func TestWorldEligibilityValidationAndClone(t *testing.T) {
	w := tinyWorld(t, 5, DrawRound)
	s := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 50, Cost: 8, Eligible: []int{2, 4}})
	if len(s.left) != 2 || s.n != 2 {
		t.Fatal("eligibility was ignored")
	}
	c := s.Clone()
	c.left[0], c.where[0], c.blocked[0] = -1, 123, true
	if s.left[0] == -1 || s.where[0] == 123 || s.blocked[0] {
		t.Fatal("clone aliases mutable state")
	}
	empty := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 50, Cost: 8, Eligible: []int{}})
	if !empty.Done || empty.Lingxi != 50 {
		t.Fatal("empty eligibility must terminate before charging")
	}
	for _, spec := range []WorldStart{
		{PrevID: 999, Cost: 8},
		{PrevID: 1, Cost: 8, Candidates: []int{2, 2}},
		{PrevID: 1, Cost: 8, Candidates: []int{1}},
		{PrevID: 1, Cost: 8, Candidates: []int{3}, Eligible: []int{2}},
		{PrevID: 1, Cost: 8, FreeRemaining: 2, FreeLimit: 1},
	} {
		if _, err := w.Start(spec, rand.New(rand.NewSource(0))); err == nil {
			t.Fatalf("accepted invalid snapshot %+v", spec)
		}
	}
}

func TestWorldFirstRewardsNotReissued(t *testing.T) {
	lib := &Library{verses: []Verse{
		{ID: 1, Poet: 9, Group1: []int{1}},
		{ID: 2, Poet: 9, Group1: []int{1}},
		{ID: 3, Poet: 16, Group1: []int{1}},
		{ID: 4, Poet: 16, Group1: []int{1}},
	}}
	w, err := NewWorld(lib, DefaultWorldRules())
	if err != nil {
		t.Fatal(err)
	}
	s := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 0, Cost: 8, FreeLimit: 1, Candidates: []int{2, 3, 4}})
	w.selectVerse(s, 0)
	if s.Lingxi != 7 {
		t.Fatal("historical poet reward reissued")
	}
	s.cands[0], s.n = w.ids[3], 1
	w.selectVerse(s, 0)
	if s.FreeLimit != 2 {
		t.Fatal("first poet free allowance missing")
	}
	s.cands[0], s.n = w.ids[4], 1
	w.selectVerse(s, 0)
	if s.FreeLimit != 2 {
		t.Fatal("free allowance issued twice")
	}
}

func TestWorldMiddleAndDualRhyme(t *testing.T) {
	lib := &Library{verses: []Verse{
		{ID: 1, Group1: []int{1, 2}, Group3: []int{10}},
		{ID: 2, Group1: []int{3}, Group3: []int{10}},
		{ID: 3, Group1: []int{4}, Group3: []int{11}},
		{ID: 4, Group1: []int{2}, Group3: []int{12}},
	}}
	for _, clears := range []bool{false, true} {
		rules := DefaultWorldRules()
		rules.MiddleClearsRun = clears
		w, err := NewWorld(lib, rules)
		if err != nil {
			t.Fatal(err)
		}
		s := startWorld(t, w, WorldStart{PrevID: 1, Cost: 8, OffRun: 1, Candidates: []int{2, 3, 4}})
		if JudgeKind(w.pairs[w.ids[1]*len(w.verses)+w.ids[4]]&3) != KindRhyme {
			t.Fatal("second rhyme group ignored")
		}
		w.selectVerse(s, 0)
		if (s.OffRun == 0) != clears {
			t.Fatal("middle-clear rule ignored")
		}
		s.cands[0], s.n = w.ids[3], 1
		w.selectVerse(s, 0)
		expected := 9
		if clears {
			expected = 8
		}
		if s.Cost != expected {
			t.Fatalf("cost=%d expected=%d", s.Cost, expected)
		}
	}
}

func TestRolloutDeterministicCancelledAndImmutable(t *testing.T) {
	w := tinyWorld(t, 20, DrawRound)
	st := startWorld(t, w, WorldStart{PrevID: 1, Lingxi: 20, Cost: 8, FreeLimit: 1})
	before := st.Clone()
	opt := DefaultRolloutOptions()
	a := w.Rollout(context.Background(), st, opt)
	b := w.Rollout(context.Background(), st, opt)
	a.Elapsed, b.Elapsed = 0, 0
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(before, st) || a.Samples != opt.Samples {
		t.Fatal("non-deterministic planning or mutated root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := w.Rollout(ctx, st, opt)
	if c.Samples != 0 || c.Action != c.Baseline || !w.Legal(st, c.Action) {
		t.Fatal("cancelled planner should use legal baseline")
	}
	for _, v := range a.Values {
		if !w.Legal(st, v.Action) {
			t.Fatal("evaluated illegal action")
		}
	}
}

func TestWorldBaselineParity(t *testing.T) {
	lib, err := LoadLibrary("../data/verses.json")
	if err != nil {
		t.Fatal(err)
	}
	rules := DefaultWorldRules()
	rules.DrawMode = DrawReplace
	w, err := NewWorld(lib, rules)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(315))
	for i := 0; i < 200; i++ {
		spec := WorldStart{PrevID: lib.verses[rng.Intn(len(lib.verses))].ID,
			Cost: 8, Lingxi: 8 + rng.Intn(50), FreeLimit: rng.Intn(3)}
		s, err := w.Start(spec, rng)
		if err != nil {
			t.Fatal(err)
		}
		legacy := NewGameState(s.Cost, s.Lingxi+s.Cost, s.Free)
		legacy.Collected[spec.PrevID] = true
		legacy.SeenPoets[lib.verses[s.prev].Poet] = true
		cs := make([]*Verse, s.n)
		for j := range cs {
			cs[j] = &lib.verses[s.cands[j]]
		}
		for _, strategy := range []Strategy{DefaultStrategy(), EvolvedStrategy373()} {
			// 本测试对拍的是"历史线性门控"这套语义。默认策略现在走
			// ExactStopRefresh(精确一步最优停止), 那是另一条判据, 不在对拍范围内,
			// 故此处显式关掉; 新判据由 TestExactStopRefreshTakesEffect 覆盖。
			strategy.ExactStopRefresh = false
			old := lib.Decide(&lib.verses[s.prev], cs, legacy, strategy, 0)
			want := old.PickIdx
			if old.Action == "refresh" {
				want = RefreshAction
			}
			if got := w.BaselineAction(s, strategy); got != want {
				t.Fatalf("case %d: baseline=%d legacy=%d", i, got, want)
			}
		}
	}
}

// TestExactStopRefreshTakesEffect 确认默认策略真的走了新判据, 且只走新判据:
// 开与关必须产生可观测的差异(否则说明开关没接上), 且两者给出的动作都必须合法。
func TestExactStopRefreshTakesEffect(t *testing.T) {
	lib, err := LoadLibrary("../data/verses.json")
	if err != nil {
		t.Fatal(err)
	}
	rules := DefaultWorldRules()
	w, err := NewWorld(lib, rules)
	if err != nil {
		t.Fatal(err)
	}
	if !DefaultStrategy().ExactStopRefresh {
		t.Fatal("默认策略应启用精确最优停止刷新")
	}
	on := DefaultStrategy()
	off := on
	off.ExactStopRefresh = false

	rng := rand.New(rand.NewSource(9091))
	diff, total := 0, 0
	for i := 0; i < 300; i++ {
		spec := WorldStart{PrevID: lib.verses[rng.Intn(len(lib.verses))].ID,
			Cost: 8, Lingxi: 8 + rng.Intn(60), FreeLimit: rng.Intn(3)}
		s, err := w.Start(spec, rng)
		if err != nil {
			t.Fatal(err)
		}
		a, b := w.BaselineAction(s, on), w.BaselineAction(s, off)
		if !w.Legal(s, a) || !w.Legal(s, b) {
			t.Fatalf("case %d: 非法动作 on=%d off=%d", i, a, b)
		}
		total++
		if a != b {
			diff++
		}
	}
	if diff == 0 {
		t.Fatal("开关未生效: 开与关 300 个状态给出完全相同的动作")
	}
	t.Logf("新判据改变了 %d/%d 个状态的动作", diff, total)
}

// bruteSubsetExpect 把"无放回抽 k 个"的所有组合摊开求 f(max) 的期望。
// 只用于小池上校验闭式次序统计量公式。
func bruteSubsetExpect(scores []float64, k int, f func(maxScore float64) float64) float64 {
	n, sum, cnt := len(scores), 0.0, 0
	var rec func(start int, chosen []int)
	rec = func(start int, chosen []int) {
		if len(chosen) == k {
			m := scores[chosen[0]]
			for _, c := range chosen[1:] {
				m = max(m, scores[c])
			}
			sum += f(m)
			cnt++
			return
		}
		for i := start; i < n; i++ {
			rec(i+1, append(chosen, i))
		}
	}
	rec(0, nil)
	return sum / float64(cnt)
}

// TestOrderStatMaxMatchesBruteForce 校验闭式次序统计量与暴力枚举逐位一致,
// 并校验两步恒等式 V2 = Emax + E[(K-maxS)+] 成立(这正是 V2 >= Emax 的来源)。
func TestOrderStatMaxMatchesBruteForce(t *testing.T) {
	sets := [][]float64{
		{5},
		{3, 9},
		{1, 4, 9},
		{-2.5, 0, 3.25, 11, 11.5, 40},
		{-100, -7, -7, 0.5, 1, 2, 3, 1000},
	}
	for _, raw := range sets {
		for _, k := range []int{1, 2, 3} {
			if k > len(raw) {
				continue
			}
			scores := append([]float64(nil), raw...)
			sort.Float64s(scores)
			got := orderStatMax(scores, k)
			want := bruteSubsetExpect(scores, k, func(m float64) float64 { return m })
			if math.Abs(got-want) > 1e-9 {
				t.Fatalf("R=%d k=%d Emax: 闭式 %v != 暴力 %v", len(raw), k, got, want)
			}
			// 期权价值: E[(K-maxS)+] 必须等于 E[max(maxS,K)] - E[maxS]
			for _, K := range []float64{-50, 0, got - 20, got, got + 20, 500} {
				gotOpt := optionValue(scores, k, K)
				wantOpt := bruteSubsetExpect(scores, k, func(m float64) float64 { return max(m, K) }) - want
				if math.Abs(gotOpt-wantOpt) > 1e-9 {
					t.Fatalf("R=%d k=%d K=%v 期权: 闭式 %v != 暴力 %v", len(raw), k, K, gotOpt, wantOpt)
				}
				if gotOpt < -1e-12 {
					t.Fatalf("期权价值必须非负, 得到 %v", gotOpt)
				}
			}
		}
	}
}
