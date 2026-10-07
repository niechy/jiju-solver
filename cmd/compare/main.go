// Command compare 做**配对**对照实验: 两个策略跑完全相同的初始条件与随机种子,
// 逐局求差, 报告均值差与 95% 置信区间。
//
// 为什么要配对: 单看两组的平均分会被初始条件差异淹没。配对后组内差异被消掉,
// 同样的局数能分辨小得多的真实差距。
//
// 例:
//
//	go run ./cmd/compare -strategy-a a.json -strategy-b b.json -episodes 400 -seed 42
//	go run ./cmd/compare -rollout-b -samples 128 -episodes 200     # A=线性策略, B=rollout
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"

	"jiju-solver/internal/cliutil"
	"jiju-solver/jiju"
)

func main() {
	libPath := flag.String("lib", "data/verses.json", "诗句库 JSON")
	unlockPath := flag.String("unlock", "", "账号解锁快照 JSON(推荐, 据此算出真实可抽池)")
	poolSize := flag.Int("poolsize", 70, "可抽池大小(未给 -unlock 时从词库随机切); <=0 用整库")
	poolSeed := flag.Int64("poolseed", 20260910, "切池子的随机种子")
	episodes := flag.Int("episodes", 400, "配对局数")
	seed := flag.Int64("seed", 42, "配对种子基数(两侧完全相同)")
	cost := flag.Int("cost", 8, "每轮入场费")
	lingxi := flag.Int("lingxi", 25, "初始灵犀")
	free := flag.Int("free", 1, "初始免费刷新次数")
	first := flag.Int("first", 0, "起始上一句 ID; 0 = 随机")
	aJSON := flag.String("strategy-a", "", "A 侧策略 JSON(空 = 默认)")
	bJSON := flag.String("strategy-b", "", "B 侧策略 JSON(空 = 默认)")
	useRolloutB := flag.Bool("rollout-b", false, "B 侧改用蒙特卡洛 rollout")
	samples := flag.Int("samples", 128, "rollout 采样数")
	flag.Parse()

	lib, err := jiju.LoadLibrary(*libPath)
	cliutil.Must(err)
	verses := lib.Verses()
	eligible, poolDesc, err := cliutil.Pool(lib, *libPath, *unlockPath, *poolSize, *poolSeed)
	cliutil.Must(err)

	a := jiju.DefaultStrategy()
	b := jiju.DefaultStrategy()
	cliutil.Must(cliutil.DecodeStrategy(*aJSON, &a))
	cliutil.Must(cliutil.DecodeStrategy(*bJSON, &b))

	w, err := jiju.NewWorld(lib, jiju.DefaultWorldRules())
	cliutil.Must(err)

	opt := jiju.DefaultRolloutOptions()
	opt.Samples = *samples
	objective := jiju.VerseObjective{Priority: "shiyi-first"}

	var sumD, sumD2 float64
	var sumC, sumC2 float64
	wins := 0
	flag.Parse()

	for ep := 0; ep < *episodes; ep++ {
		dShiyi, dCount := 0.0, 0.0
		for side := 0; side < 2; side++ {
			// 同一局两侧共用 seed 与初始条件 —— 配对的关键。
			rng := rand.New(rand.NewSource(*seed + int64(ep)*1000003))
			prev := *first
			if prev == 0 {
				prev = verses[rng.Intn(len(verses))].ID
			}
			spec := jiju.WorldStart{
				PrevID: prev, Cost: *cost, Lingxi: *lingxi,
				FreeLimit: *free, FreeRemaining: *free, Eligible: eligible,
			}
			// Start 会推进 rng, 所以两侧必须从同一个 rng 状态出发: 上面已用固定 seed 重建。
			st, err := w.Start(spec, rng)
			cliutil.Must(err)
			strategy := a
			if side == 1 {
				strategy = b
			}
			for !st.Done {
				action := w.BaselineAction(st, strategy)
				if side == 1 && *useRolloutB {
					if m, e := w.MultiRollout(context.Background(), st, opt, objective); e == nil && m.Action >= 0 {
						action = m.Action
					}
				}
				if action < 0 || !w.Legal(st, action) {
					break
				}
				cliutil.Must(w.Step(st, action, rng))
			}
			if side == 0 {
				dShiyi, dCount = -float64(st.Shiyi), -float64(st.Turns)
			} else {
				dShiyi += float64(st.Shiyi)
				dCount += float64(st.Turns)
				if dShiyi > 0 {
					wins++
				}
			}
		}
		sumD += dShiyi
		sumD2 += dShiyi * dShiyi
		sumC += dCount
		sumC2 += dCount * dCount
	}

	n := float64(*episodes)
	meanD := sumD / n
	seD := pairedSE(sumD, sumD2, *episodes)
	meanC := sumC / n
	seC := pairedSE(sumC, sumC2, *episodes)

	fmt.Printf("词库 %s | %s | %d 配对局 | 种子 %d\n",
		*libPath, poolDesc, *episodes, *seed)
	line("诗意差  (B-A)", meanD, seD, wins, *episodes)
	line("集句数差(B-A)", meanC, seC, wins, *episodes)
	if seD > 0 {
		fmt.Printf("\nt = %.2f   注意: 多轮比较后单看 t≈2 不足以判定, 需要独立种子复现。\n", meanD/seD)
	}
}

func line(name string, mean, se float64, wins, n int) {
	fmt.Printf("%s: %+.3f  ± %.3f (95%%CI [%+.2f, %+.2f])  B 胜 %d/%d\n",
		name, mean, 1.96*se, mean-1.96*se, mean+1.96*se, wins, n)
}

// pairedSE 由逐局差的均值与平方和给出标准误。
func pairedSE(sum, sumSq float64, n int) float64 {
	if n < 2 {
		return 0
	}
	fn := float64(n)
	mean := sum / fn
	variance := (sumSq - fn*mean*mean) / (fn - 1)
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance / fn)
}
