package jiju

import (
	"context"
	"fmt"
	"time"
)

func liveObjective() VerseObjective { return VerseObjective{Priority: "shiyi-first"} }

func liveRolloutOptions() RolloutOptions {
	opt := DefaultRolloutOptions()
	opt.MaxSamples = 8192
	opt.Budget = 1500 * time.Millisecond
	return opt
}

func liveTrainingOptions() TrainingOptions {
	opt := DefaultTrainingOptions()
	opt.Objective = liveObjective()
	opt.Budget = 60 * time.Second
	return opt
}

func (w *World) planLive(ctx context.Context, st *WorldState, opt RolloutOptions) (MultiResult, error) {
	if opt.ActionPolicies || opt.TerminalPolicy {
		return MultiResult{}, fmt.Errorf("experimental continuations are not enabled for live play")
	}
	return w.MultiRollout(ctx, st, opt, liveObjective())
}
