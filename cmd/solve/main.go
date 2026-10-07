// Command solve 在**小池**上求精确最优: 把"每次抽三句"的全部可能性精确展开,
// 得到该状态下真正的最优动作与精确期望值。
//
// 用途不是在线决策(池子一大就算不动), 而是当"标准答案": 拿它量一个策略离最优
// 还有多远, 也用它检查规则实现有没有写错。
//
// 可算规模(实测, 见 docs/algorithm.md): 池 8 约 5 万节点/0.7 秒, 池 9 约 32 万/7 秒,
// 池 10 约 178 万/82 秒; 再大就跑不动了。
//
// 例:
//
//	go run ./cmd/solve -poolsize 8 -lingxi 25 -cost 8 -first 110
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"time"

	"jiju-solver/internal/cliutil"
	"jiju-solver/jiju"
)

// leftOf 取当前剩余池大小, 仅用于把"算不动"的原因说清楚。
func leftOf(_ *jiju.World, st *jiju.WorldState) int { return st.Remaining() }

func main() {
	libPath := flag.String("lib", "data/verses.json", "诗句库 JSON")
	unlockPath := flag.String("unlock", "", "账号解锁快照 JSON(推荐, 据此算出真实可抽池)")
	poolSize := flag.Int("poolsize", 8, "可抽池大小(精确解只适合小池)")
	poolSeed := flag.Int64("poolseed", 20260910, "切池子的随机种子")
	seed := flag.Int64("seed", 42, "抽样种子")
	cost := flag.Int("cost", 8, "入场费")
	lingxi := flag.Int("lingxi", 25, "初始灵犀")
	free := flag.Int("free", 1, "初始免费刷新次数")
	first := flag.Int("first", 0, "起始上一句 ID; 0 = 随机")
	limit := flag.Int("limit", 8, "精确解允许的最大剩余池(超过就判定为算不动, 直接返回未完成)")
	maxNodes := flag.Int("maxnodes", 200000000, "节点预算")
	flag.Parse()

	lib, err := jiju.LoadLibrary(*libPath)
	cliutil.Must(err)
	verses := lib.Verses()
	eligible, poolDesc, err := cliutil.Pool(lib, *libPath, *unlockPath, *poolSize, *poolSeed)
	cliutil.Must(err)

	w, err := jiju.NewWorld(lib, jiju.DefaultWorldRules())
	cliutil.Must(err)

	rng := rand.New(rand.NewSource(*seed))
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

	fmt.Printf("词库 %s | %s | 灵犀%d 费用%d 免费%d\n",
		*libPath, poolDesc, *lingxi, *cost, *free)
	fmt.Printf("当前候选: %v   允许动作: ", w.CandidateIDs(st))
	for a := 0; a <= jiju.RefreshAction; a++ {
		if w.Legal(st, a) {
			fmt.Printf("%d ", a)
		}
	}
	fmt.Println("(0..2=选第 n 句, 3=刷新)")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	start := time.Now()
	res := w.ExactMulti(ctx, st, *limit, *maxNodes, jiju.VerseObjective{Priority: "shiyi-first"})
	elapsed := time.Since(start)

	fmt.Printf("\n精确解: 动作 %d | 完成=%v | 用时 %s\n", res.Action, res.Complete, elapsed.Round(time.Millisecond))
	if !res.Complete {
		fmt.Printf("未完成: 当前剩余池 %d 句, 而 -limit=%d。精确解的规模随池子增长极快\n", leftOf(w, st), *limit)
		fmt.Println("(池 8 约 0.7 秒 / 池 9 约 7 秒 / 池 10 约 82 秒), 调大 -limit 或调小 -poolsize 再试。")
	}

	// 对照: 默认策略在该状态会怎么走。
	strategy := jiju.DefaultStrategy()
	got := w.BaselineAction(st, strategy)
	fmt.Printf("默认策略: 动作 %d", got)
	if res.Complete && got != res.Action {
		fmt.Printf("   <-- 与精确最优不一致, 这就是这一状态上损失的位置")
	} else if res.Complete {
		fmt.Printf("   <-- 一致")
	}
	fmt.Println()
}
