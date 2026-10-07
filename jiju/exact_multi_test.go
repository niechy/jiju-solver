package jiju

import (
	"context"
	"reflect"
	"testing"
)

func TestExactMultiPriorities(t *testing.T) {
	w := tinyWorld(t, 3, DrawRound)
	w.verses[1].Shiyi = 20
	w.verses[2].Lingxi = 10
	st := startWorld(t, w, WorldStart{PrevID: 1, Cost: 10, Candidates: []int{2, 3}})
	before := st.Clone()
	for _, tc := range []struct {
		priority string
		action   int
		value    VerseReturn
	}{{"shiyi-first", 0, VerseReturn{1, 20}}, {"count-first", 1, VerseReturn{2, 15}}} {
		r := w.ExactMulti(context.Background(), st, 6, 100000, VerseObjective{Priority: tc.priority})
		if !r.Complete || r.Action != tc.action || r.Value != tc.value {
			t.Fatalf("%s: %+v", tc.priority, r)
		}
		opt := DefaultRolloutOptions()
		p, err := w.MultiRollout(context.Background(), st, opt, VerseObjective{Priority: tc.priority})
		if err != nil || !p.Exact || p.Action != r.Action || p.Samples != 0 {
			t.Fatalf("exact integration: %+v %v", p, err)
		}
	}
	if !reflect.DeepEqual(st, before) {
		t.Fatal("root mutated")
	}
}

func TestExactMultiNegativeAndHistoricalTotals(t *testing.T) {
	w := tinyWorld(t, 3, DrawRound)
	w.verses[1].Shiyi, w.verses[2].Shiyi = -20, -10
	st := startWorld(t, w, WorldStart{PrevID: 1, Cost: 100, Candidates: []int{2, 3}})
	o := VerseObjective{Priority: "shiyi-first"}
	r := w.ExactMulti(context.Background(), st, 6, 100000, o)
	if !r.Complete || r.Action != 1 || r.Value != (VerseReturn{1, -15}) {
		t.Fatalf("negative return: %+v", r)
	}
	st.Shiyi, st.Turns = 500, 20
	again := w.ExactMulti(context.Background(), st, 6, 100000, o)
	if !reflect.DeepEqual(r, again) {
		t.Fatalf("history changes future: %+v %+v", r, again)
	}
}

func TestExactMultiRefreshAndLimits(t *testing.T) {
	w := tinyWorld(t, 5, DrawRound)
	st := startWorld(t, w, WorldStart{PrevID: 1, Cost: 8, FreeLimit: 1, FreeRemaining: 1, Candidates: []int{3, 5}})
	o := VerseObjective{Priority: "count-first"}
	r := w.ExactMulti(context.Background(), st, 6, 100000, o)
	count := w.Exact(context.Background(), st, 6, 100000)
	if !r.Complete || r.Action != RefreshAction || r.Value.Count != count.Value {
		t.Fatalf("refresh: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, r := range []ExactMultiResult{
		w.ExactMulti(ctx, st, 6, 100000, o),
		w.ExactMulti(context.Background(), st, 6, 1, o),
		w.ExactMulti(context.Background(), st, 1, 100000, o),
		w.ExactMulti(context.Background(), st, 6, 100000, VerseObjective{}),
	} {
		if r.Complete || r.Action != -1 || len(r.Values) != 0 {
			t.Fatalf("partial recommendation: %+v", r)
		}
	}
}

func TestExactMultiChanceExpectation(t *testing.T) {
	w := tinyWorld(t, 6, DrawRound)
	w.rules.RefreshCost = 100
	// Isolate chance averaging: all transitions rhyme and stick, so rewards
	// below need no pair-specific deduction. Only one future pick is affordable.
	for i := range w.pairs {
		w.pairs[i] |= 4
	}
	for i, reward := range []int{0, 0, 1, 2, 4, 8} {
		w.verses[i].Shiyi = reward
	}
	st := startWorld(t, w, WorldStart{PrevID: 1, Cost: 100, Lingxi: 100, Candidates: []int{2}})
	before := st.Clone()
	r := w.ExactMulti(context.Background(), st, 6, 100000, VerseObjective{Priority: "shiyi-first"})
	// Three of four equally likely triples contain the 8-reward verse;
	// the remaining triple has best reward 4: (8+8+8+4)/4 = 7.
	if !r.Complete || r.Action != 0 || r.Value != (VerseReturn{2, 7}) {
		t.Fatalf("chance expectation: %+v", r)
	}
	if !reflect.DeepEqual(st, before) {
		t.Fatal("chance enumeration mutated root")
	}
}
