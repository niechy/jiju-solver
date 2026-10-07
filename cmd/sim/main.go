// Command sim 用蒙特卡洛模拟评测一个策略: 在同一批初始条件下跑若干局, 报告
// 最终诗意与集句数的分布。
//
// 关键点(与常见做法的主要差异): **池子必须是玩家真实可抽的那一批**, 而不是整本词库。
// 用 -poolsize 从词库里切一个指定大小的子集来近似"已解锁且未收集"的真实池。
//
// 例:
//
//	go run ./cmd/sim -poolsize 70 -lingxi 66 -cost 8 -free 2 -episodes 2000
//	go run ./cmd/sim -rollout -samples 128 -episodes 200
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"sort"

	"jiju-solver/internal/cliutil"
	"jiju-solver/jiju"
)

func main() {
	libPath := flag.String("lib", "data/verses.json", "诗句库 JSON")
	unlockPath := flag.String("unlock", "", "账号解锁快照 JSON(推荐, 据此算出真实可抽池)")
	poolSize := flag.Int("poolsize", 70, "可抽池大小(未给 -unlock 时从词库随机切); <=0 用整库")
	poolSeed := flag.Int64("poolseed", 20260910, "切池子的随机种子")
	episodes := flag.Int("episodes", 2000, "模拟局数")
	seed := flag.Int64("seed", 42, "模拟随机种子")
	cost := flag.Int("cost", 8, "每轮入场费(灵犀)")
	lingxi := flag.Int("lingxi", 25, "初始灵犀")
	free := flag.Int("free", 1, "初始免费刷新次数")
	first := flag.Int("first", 0, "起始的上一句 ID; 0 = 随机")
	strategyJSON := flag.String("strategy", "", "策略权重 JSON(空 = 默认冠军权重)")
	useRollout := flag.Bool("rollout", false, "用蒙特卡洛 rollout 决策(更慢更强); 默认直接用线性续接策略")
	samples := flag.Int("samples", 128, "rollout 每动作采样数")
	flag.Parse()

	lib, err := jiju.LoadLibrary(*libPath)
	cliutil.Must(err)
	verses := lib.Verses()
	eligible, poolDesc, err := cliutil.Pool(lib, *libPath, *unlockPath, *poolSize, *poolSeed)
	cliutil.Must(err)

	strategy := jiju.DefaultStrategy()
	cliutil.Must(cliutil.DecodeStrategy(*strategyJSON, &strategy))

	w, err := jiju.NewWorld(lib, jiju.DefaultWorldRules())
	cliutil.Must(err)

	opt := jiju.DefaultRolloutOptions()
	opt.Samples = *samples
	objective := jiju.VerseObjective{Priority: "shiyi-first"}

	shiyi := make([]int, 0, *episodes)
	counts := make([]int, 0, *episodes)
	for ep := 0; ep < *episodes; ep++ {
		rng := rand.New(rand.NewSource(*seed + int64(ep)*1000003))
		prev := *first
		if prev == 0 {
			prev = verses[rng.Intn(len(verses))].ID
		}
		spec := jiju.WorldStart{
			PrevID: prev, Cost: *cost, Lingxi: *lingxi,
			FreeLimit: *free, FreeRemaining: *free, Eligible: eligible,
		}
		st, err := w.Start(spec, rng)
		cliutil.Must(err)
		for !st.Done {
			action := w.BaselineAction(st, strategy)
			if *useRollout {
				if m, e := w.MultiRollout(context.Background(), st, opt, objective); e == nil && m.Action >= 0 {
					action = m.Action
				}
			}
			if action < 0 || !w.Legal(st, action) {
				break
			}
			cliutil.Must(w.Step(st, action, rng))
		}
		shiyi = append(shiyi, st.Shiyi)
		counts = append(counts, st.Turns)
	}

	fmt.Printf("词库 %s | %s | 初始 灵犀%d 费用%d 免费%d | %d 局\n",
		*libPath, poolDesc, *lingxi, *cost, *free, *episodes)
	if *useRollout {
		fmt.Printf("决策: 蒙特卡洛 rollout(%d 采样) + 诗意优先\n", *samples)
	} else {
		fmt.Printf("决策: 线性续接策略(刷新走精确最优停止)\n")
	}
	report("诗意", shiyi)
	report("集句数", counts)
}

func report(name string, v []int) {
	sort.Ints(v)
	sum := 0
	for _, x := range v {
		sum += x
	}
	n := len(v)
	fmt.Printf("%s: 均值 %.2f | p5 %d | 中位 %d | p95 %d | 最小 %d | 最大 %d\n",
		name, float64(sum)/float64(n),
		v[n*5/100], v[n/2], v[min(n-1, n*95/100)], v[0], v[n-1])
}
