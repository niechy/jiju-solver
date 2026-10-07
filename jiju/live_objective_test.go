package jiju

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestLiveObjectiveAndOptions(t *testing.T) {
	if liveObjective().Priority != "shiyi-first" || liveTrainingOptions().Objective != liveObjective() {
		t.Fatal("live training and planning objectives drifted")
	}
	o := liveRolloutOptions()
	if o.Samples != 4096 || o.MaxSamples != 8192 || o.Budget != 1500*time.Millisecond || o.ActionPolicies || o.TerminalPolicy {
		t.Fatalf("unexpected live settings: %+v", o)
	}
	w := tinyWorld(t, 3, DrawRound)
	w.verses[1].Shiyi = 20
	w.verses[2].Lingxi = 10
	st := startWorld(t, w, WorldStart{PrevID: 1, Cost: 10, Candidates: []int{2, 3}})
	o.Samples, o.PolicySamples, o.MaxSamples, o.Budget = 8, 0, 0, 0
	for _, limit := range []int{0, 6} {
		o.ExactLimit = limit
		r, err := w.planLive(context.Background(), st, o)
		if err != nil || r.Action != 0 || r.Exact != (limit == 6) {
			t.Fatalf("live ignored Shiyi: %+v %v", r, err)
		}
		if limit == 0 && (len(r.Comparisons) != 1 || r.Comparisons[0].Delta != 5 || r.Comparisons[0].SE != 0) {
			t.Fatalf("diagnostic is not Shiyi gain: %+v", r.Comparisons)
		}
	}
	o.ActionPolicies = true
	if _, err := w.planLive(context.Background(), st, o); err == nil {
		t.Fatal("experimental refinement entered live play")
	}
	o.ActionPolicies, o.TerminalPolicy = false, true
	if _, err := w.planLive(context.Background(), st, o); err == nil {
		t.Fatal("experimental terminal repair entered live play")
	}
}

// Optional local artifact: no device, game state, or save writes are involved.
func TestLiveBudgetRecordedSnapshot(t *testing.T) {
	raw, err := os.ReadFile("../../screenshots/jiju-research-20260906/runtime-main.json")
	if os.IsNotExist(err) {
		t.Skip("recorded account snapshot not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	var spec WorldStart
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	lib, err := LoadLibrary("../data/verses.json")
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWorld(lib, DefaultWorldRules())
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.Start(spec, rand.New(rand.NewSource(202609212)))
	if err != nil {
		t.Fatal(err)
	}
	before := st.Clone()
	r, err := w.planLive(context.Background(), st, liveRolloutOptions())
	if err != nil || !w.Legal(st, r.Action) || r.Samples < 1 || !reflect.DeepEqual(st, before) {
		t.Fatalf("budgeted live planning: %+v %v", r, err)
	}
	t.Logf("snapshot: samples=%d elapsed=%s action=%d exact=%v values=%+v", r.Samples, r.Elapsed, r.Action, r.Exact, r.Values)
}
