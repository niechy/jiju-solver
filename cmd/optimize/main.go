// Command optimize 针对**当前这一局的具体状态**搜索一组更好的策略权重。
//
// 它做的是"短程进化搜索 + 独立验证": 每一代在若干个局上评估一批候选权重,
// 取表现好的作为下一代的父本; 最后把冠军放在**另一批独立种子**上验证,
// 只有配对优势超过 2.4 倍标准误才判定采纳 —— 避免把噪声当成提升。
//
// 例:
//
//	go run ./cmd/optimize -poolsize 70 -lingxi 66 -cost 8 -free 2 -seconds 60
//	go run ./cmd/optimize -out tuned.json -generations 24 -episodes 256
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

func main() {
	libPath := flag.String("lib", "data/verses.json", "诗句库 JSON")
	unlockPath := flag.String("unlock", "", "账号解锁快照 JSON(推荐, 据此算出真实可抽池)")
	poolSize := flag.Int("poolsize", 70, "可抽池大小(未给 -unlock 时从词库随机切); <=0 用整库")
	poolSeed := flag.Int64("poolseed", 20260910, "切池子的随机种子")
	cost := flag.Int("cost", 8, "入场费")
	lingxi := flag.Int("lingxi", 25, "初始灵犀")
	free := flag.Int("free", 1, "初始免费刷新次数")
	first := flag.Int("first", 0, "起始上一句 ID; 0 = 随机")
	seconds := flag.Duration("seconds", 60*time.Second, "搜索时间预算")
	generations := flag.Int("generations", 24, "最大代数")
	population := flag.Int("population", 24, "每代候选数")
	episodes := flag.Int("episodes", 256, "每代评估局数")
	validation := flag.Int("validation", 4096, "最终验证局数")
	seed := flag.Int64("seed", 42, "抽样种子")
	out := flag.String("out", "", "把采纳后的权重写到该 JSON")
	baseJSON := flag.String("base", "", "起始权重 JSON(空 = 默认冠军权重)")
	flag.Parse()

	lib, err := jiju.LoadLibrary(*libPath)
	cliutil.Must(err)
	verses := lib.Verses()
	eligible, poolDesc, err := cliutil.Pool(lib, *libPath, *unlockPath, *poolSize, *poolSeed)
	cliutil.Must(err)

	base := jiju.DefaultStrategy()
	cliutil.Must(cliutil.DecodeStrategy(*baseJSON, &base))

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

	opt := jiju.DefaultTrainingOptions()
	opt.Base = base
	opt.Generations = *generations
	opt.Population = *population
	opt.Episodes = *episodes
	opt.Validation = *validation
	opt.Budget = *seconds

	fmt.Printf("词库 %s | %s | 灵犀%d 费用%d 免费%d\n",
		*libPath, poolDesc, *lingxi, *cost, *free)
	fmt.Printf("搜索: 最多 %d 代 × %d 候选 × %d 局, 预算 %s; 验证 %d 局\n\n",
		*generations, *population, *episodes, *seconds, *validation)

	ctx := context.Background()
	if *seconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *seconds)
		defer cancel()
	}
	start := time.Now()
	res, err := w.TrainContinuation(ctx, spec, opt)
	if err != nil {
		cliutil.Must(err)
	}
	elapsed := time.Since(start).Round(time.Millisecond)

	fmt.Printf("完成 %d/%d 代 | 用时 %s\n", res.Generations, *generations, elapsed)
	fmt.Printf("验证: 台词意差 %+.2f ± %.2f (约2SE) | 采纳=%v\n", res.Delta, 1.96*res.SE, res.Accepted)
	if res.Generations < *generations {
		fmt.Println("注意: 搜索被时间预算截断, 这一代未必是收敛结果 —— 想要更快收敛就调大 -seconds 或调小 -generations。")
	}
	if !res.Accepted {
		fmt.Println("结论: 没找到显著优于起始权重的解, 保持原权重。这本身是有用的结果 —— 说明该状态附近已经比较接近局部最优。")
		return
	}
	if *out != "" {
		cliutil.Must(cliutil.SaveStrategy(*out, res.Strategy))
		fmt.Printf("已写入 %s\n", *out)
	}
}
