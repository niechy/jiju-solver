# 集句策略 headroom 量化报告（workstream B）

> **说明**：本文写成于原始工作区，文中出现的工具名（如 `jijiworld`、`jijicritic`）、
> 路径（如 `screenshots/...`）与脚本名都是那次实验当时的样子，本仓库未包含它们。
> 保留原样是为了让数字可追溯；要复现，请用本目录 `README.md` 里给出的 `cmd/research/` 工具。

> 产出物：本文件 + `_research_b/headroom-main.json`（27 个状态的精确解与消融表）+ `_research_b/headroom-live.json`（在线 rollout 口径）+ `_research_b/nested-{low,rich}.json`+ `_research_b/delta-conv.json`（400 局 δ 校验）。原始日志：`_research_b/log-*.txt`。

## 0. 结论摘要

1. **当前在线策略（`planLive` = `MultiRollout` + 诗意优先）在能精确求解的小池上已经接近最优**：池 7–9、`samples=1024 / policy_samples=32` 口径下 27 个状态里 **18 个 gap 恰好为 0**（其中 3 个是灵犀 2 < 成本 8 的已终局残局；剩下 15 个是真正可决策的状态，每个 60 局评估全部取到精确最优值、SE=0.00），**中位 gap = 0.00，最大 gap = 5.94 诗意**；换成线上真实参数（`samples=4096/max 8192/budget 1500ms/policy_samples=128/exact_limit=6`）后，嵌套池 8/9/10 的 gap 分别为 **2.88 / 4.75 / 7.35 诗意（占 V\* 的 4.4% / 4.5% / 7.0%）**。
2. **损失几乎全部落在后续启发式（`BaselineAction(EvolvedStrategy)`）身上**：同一批 24 个非终局状态上它的 gap 均值 **15.40 诗意（区间 0.70–41.42）**，占 V\* 的 1%–74%；而部署口径的 rollout 把它压在 0–7 诗意。也就是说：**在线求解器已经把可测的 headroom 花掉了**，剩余 headroom 在 rollout 的采样精度与后续策略的尾部偏差上。
3. **损失归因（δ 恒等式，400 局/状态，1600 局）**：**68.9% 来自「最优候选出韵 → 必刷」这条硬规则**（当前策略为躲一个出韵候选而刷新，精确最优是直接把这一句选下来）；另外约 **17.1%** 是反向时机错误（该刷时没刷，主要是把中间态接了下来）；选句质量类（含「押韵但不粘 −5」的偏好）合计约 **14%**。
4. **换组逻辑（b）与诗人首收加分（d）的贡献是 0**：关闭换组/目标大组权重平均 **−0.83 诗意**（24 个状态里 14 个显著变差、1 个显著变好）——它不是损失来源而是净收益来源；关闭诗人首收加分平均 **−0.01 ± 0.00**，24 个状态里 0 个有显著变化。出韵的**评分惩罚**（c）反而略有过重：置 0 平均 **+0.42 诗意**，16 个状态显著变好、0 个变差。
5. **精确在线求解在真实池规模上不可行**：真实池 20（残局）/68（标准协议态）/最多 572（全库）。实测精确求解 nodes 随池每加 1 句 ×5.6–8.1；池 10 已需 178 万节点 / 82 s，池 13 在 300 s 内未完成（772 万节点），池 16 即使把灵犀压到 9（可见回合≈1）也在 120 s 内未完成（107 万节点，被抽候选组合数 C(16,3)=560 主导）。→ 现状 `exact_limit=6`（892 节点 / 8 ms）是安全阈值；抬到 8 仍在 1.5 s 决策预算内（5.4 万节点 / 0.73 s），再往上不划算。

## 1. 方法与口径

### 1.1 精确最优

* `World.ExactMulti(ctx, st, limit, maxNodes, {Priority:"shiyi-first"})`：在 `DrawRound` 下穷举所有等概率候选集（`C(pool,3)`，本文件中候选数固定 3），状态记忆化求解精确期望。返回 `Value.Shiyi`＝**V\*（诗意最优期望）**，`Value.Count`＝同一条最优线上的句数，`Values[]`＝每个合法根动作的精确 Q（后续打最优）。
* 目标即项目口径 `liveObjective()` = `{Priority:"shiyi-first"}`：首要最大化诗意，诗意打平时比句数（`betterTie`）。
* 精确值只统计**从根状态起的增量**（`after.Shiyi - st.Shiyi`），与项目 `jijiworld`/评估脚本口径一致。

### 1.2 缩减世界（为什么可以这样做）

`ExactMulti` 的 `limit` 是 `len(st.left)` 的硬门槛，真实池远超它，所以本报告把状态**限制到真实库的子池**：从 `data/inscribe/inscribe_VerseConfig.json`（573 句）里按主韵组比例确定性抽样，写成临时 verse-config，用 `LoadLibrary` + `NewWorld` 重建世界。

**规则（`DefaultWorldRules`：DrawRound / PayOnEntry / MinCandidates=1 / RefreshCost=1）、诗句属性、灵犀账本、判定 `Judge` 全部来自真实代码**，只有可选池被缩小。任何既有文件都没有改动。

### 1.3 两类"当前策略"口径

| 口径 | 定义 | 用途 |
|---|---|---|
| 部署口径（rollout） | `planLive` = `MultiRollout(st, liveRolloutOptions(), liveObjective())`；`DefaultRolloutOptions()` → `Samples=4096, MaxSamples=8192, PolicySamples=128, ExactLimit=6, ExactNodes=1e5`，线上再叠 `Budget=1500ms`（`liveRolloutOptions()`） | 回答"当前策略离最优多远" |
| 后续启发式口径 | `World.BaselineAction(st, EvolvedStrategy())` = `DefaultStrategy()`；它同时是 rollout 的兜底动作与 rollout 内的 continuation | 回答"损失长在哪个决策上" |

注意两者的关系：`MultiRollout` 只在**根动作**上做前瞻，之后每一轮由线上主循环重新规划；但每次根动作比较都用一个固定的后续启发式把局面走到底，所以后续启发式的偏差会部分抵消、部分残留。

### 1.4 损失归因：精确一步遗憾（δ 恒等式，无偏）

对任意策略 π 有恒等式

```
V*(s0) - V^π(s0) = E_π[ Σ_t ( V*(s_t) - Q*(s_t, a_π(s_t)) ) ] = E_π[ Σ_t δ_t ]
```

沿 π 的真实轨迹逐步用 `ExactMulti` 精确算 δ_t，并按"当前策略在该步用掉的是哪条规则 / 选了什么样的句子"分类，即可把总 gap **精确分解**到决策类型上（不是启发式近似）。

### 1.5 规则级消融需要复刻

`BaselineAction` 的三条硬刷新规则（`world.go:475-488`）无法用 `Strategy` 字段关闭，因此本工具在 scratch CLI 内逐行复刻了它（`cmd/tools/jijiresearch_b/replica.go`），并**逐状态**与真实 `BaselineAction` 比对后才使用：

```
PARITY TOTAL: episodes=11200 decisions=85534 mismatch=0
```

覆盖：新开局 / 低灵犀 / 一局中途（`Candidates`+`Displayed`）/ 残局快照型状态 × 5 组权重（Evolved、Baseline、Count、LowLingxiFloor=30、SwitchThreshPct=0）× 池 4–10，**0 失配**。（复刻过程中修掉的真实差异：中间态在 `MiddleClearsRun=false` 时**不清零**连错计数。）

### 1.6 确定性

* 评估 MC 种子 `seed=20260906`，第 i 局种子 `seed + i*1000003`；同一 seed 在各策略间共用（公共随机数配对）。
* 精确求解无随机性（全枚举 + float64）。
* 环境 Windows / Go 1.26.0，单进程；除注明外均串行运行。

## 2. 状态空间规模与"精确在线求解"可行性

### 2.1 真实状态规模

| 状态 | 池（≈剩余可选句） | 候选 | 成本 | 免费刷新 | 备注 |
|---|---|---|---|---|---|
| 标准评估协议态（`runtime-main.json` = `set-data-v6` 头部 state） | **68** | 3 | 8 | 1/1 | lingxi 41，collected 2，seen 2；所有 `set-eval-*`/`set-final-*` 评估用的就是它 |
| 真实残局快照（`live-shiyi-authorized-final-state-20260908.json`） | **20** | 0（终局） | 8 | 3/3 | lingxi=2 < cost=8 → `World.Start` 直接 `Done`，无可决策空间 |
| 全库可选（`Eligible=nil`） | **572** | 3 | 8 | 1/1 | `WorldStart.Eligible=nil` 的合成压力场景 |

精确状态 = (剩余子集 ⊆ 池, 本轮 blocked 子集, prev, lingxi, cost, Free, FreeLimit, OffRun, 已见诗人掩码)。仅"剩余子集"一项就有 ≥ 2^P 个：P=20 → 1.05e6，P=68 → 2.95e20，P=572 → 1.3e172；再乘 blocked 子集(≤2^3)、prev(P)、cost(8→~30)、Free(0→5)、OffRun(2) 与已见诗人掩码(41 位)。

### 2.2 实测精确求解规模（`-mode scale`，lingxi=41 cost=8 free=1，prev 不在池内）

| 池 | 完成 | 节点数 | 墙钟 | V\*诗意 | V\*句 |
|---|---|---|---|---|---|
| 4 | 是 | 37 | 0.3 ms | 30.0 | 4.0 |
| 5 | 是 | 202 | 0.5 ms | 40.0 | 5.0 |
| 6 | 是 | 892 | 8.1 ms | 45.0 | 6.0 |
| 7 | 是 | 6,631 | 66.9 ms | 60.0 | 7.0 |
| 8 | 是 | 54,099 | 734.7 ms | 105.0 | 8.0 |
| 9 | 是 | 318,637 | 6.90 s | 115.0 | 9.0 |
| 10 | 是 | 1,781,446 | 82.0 s | 125.0 | 10.0 |
| 11 | **否**（>600 s 未完成） | — | >600 s | — | — |
| 13 | **否**（300 s 超时） | 7,723,009 | >300 s | — | — |

每加 1 句池，节点数 ×5.6（10→11 量级）、×6.1（9→10）、×5.9（8→9）、×8.1（7→8）。**注意增长不是被"可见回合数"主导，而是被 `C(P,3)` 抽候选组合数主导**：

### 2.3 池 × 灵犀（`-mode feas`）

| 池 | 灵犀 | 完成 | 节点数 | 墙钟 | V\*诗意 |
|---|---|---|---|---|---|
| 10 | 9 | 是 | 175 | 1.2 ms | 40.0 |
| 10 | 10 | 是 | 182 | 3.5 ms | 40.0 |
| 10 | 12 | 是 | 182 | 0.6 ms | 40.0 |
| 10 | 16 | 是 | 47,390 | 721 ms | 60.0 |
| 10 | 25 | 是 | 327,313 | 12.2 s | 95.0 |
| 10 | 41 | 是 | 1,781,446 | 73.8 s | 125.0 |
| 16 | 9 | **否**（120 s 超时） | 1,074,894 | >120 s | — |
| 16 | 10 | **否**（120 s 超时） | 1,354,257 | >120 s | — |
| 16 | 12 | **否**（120 s 超时） | 1,171,356 | >120 s | — |
| 16 | 16 | **否**（120 s 超时） | 1,473,145 | >120 s | — |

灵犀低会把树迅速压小（池 10：灵犀 41 → 178 万节点，灵犀 9 → 175 节点，差 1 万倍），**但池一旦变大，抽候选的组合数就把这个收益吃光**：池 16 + 灵犀 9（几乎只能走 1 回合）仍需 >100 万节点、120 s 未完。

### 2.4 判定

* **精确在线求解在真实池上不可行。** 池 16 已经不可解，池 20/68/572 更不可能；按 ×6/句外推，池 68 相对池 10 还要乘约 6^58。
* 可行的精确区间：**池 ≤ 6 → <10 ms**（现状 `ExactLimit=6`，892 节点）；**池 7 → 67 ms**；**池 8 → 0.73 s**（仍在线上 1500 ms/决策预算内）；**池 9 → 6.9 s**（超预算）；池 10 → 82 s（超预算）。
* 结论：`exact_limit=6` 可安全抬到 **8**（多覆盖 2 句的残局，代价 ≤0.73 s/决策，最坏与 1500 ms 预算同量级）；抬到 9 需要 ≥7 s/决策，除非只在"残局 + 低灵犀"才启用（灵犀 12 时池 9 只要 22 ms，池 10 灵犀 12 只要 0.6 ms）。→ **可行的工程做法是按 `(池, 灵犀)` 二维门槛触发精确解，而不是只按池大小**。

## 3. 状态 → 精确最优 → 当前策略 → gap

三张主表。gap 的 ±SE 是 MC 配对标准误（2000 局）；V\* 无 SE（精确）。

### 主表 A：均衡分层池（按 573 句库的韵组比例确定性抽样，池内每韵组 2–3 句），池 7/8/9，共 27 个状态

| 状态 | 池 | 候选 | 成本 | 免费 | 精确节点 | 精确耗时ms | V*(诗意) | V*(句) | 后续启发式诗意 | ±SE | gap | gap% |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| midrun-snap-pool7(lingxi41,free1) | 7 | 3 | 8 | 1/1 | 6700 | 98.5 | 80.00 | 7.00 | 67.53 | 0.12 | 12.47 | 16% |
| lowlingxi-snap-pool7(lingxi12,free1) | 7 | 3 | 8 | 1/1 | 128 | 6.6 | 40.00 | 3.00 | 12.05 | 0.25 | 27.95 | 70% |
| highfree-snap-pool7(lingxi41,free4) | 7 | 3 | 8 | 4/4 | 4127 | 74.5 | 80.00 | 7.00 | 67.53 | 0.12 | 12.47 | 16% |
| midrun-snap-pool8(lingxi41,free1) | 8 | 3 | 8 | 1/1 | 54092 | 1032.7 | 85.00 | 8.00 | 79.87 | 0.10 | 5.13 | 6% |
| lowlingxi-snap-pool8(lingxi12,free1) | 8 | 3 | 8 | 1/1 | 282 | 3.9 | 40.00 | 3.00 | 10.85 | 0.13 | 29.15 | 73% |
| highfree-snap-pool8(lingxi41,free4) | 8 | 3 | 8 | 4/4 | 22855 | 385.3 | 85.00 | 8.00 | 79.87 | 0.10 | 5.13 | 6% |
| midrun-snap-pool9(lingxi41,free1) | 9 | 3 | 8 | 1/1 | 336354 | 7490.6 | 100.00 | 9.00 | 87.31 | 0.12 | 12.69 | 13% |
| lowlingxi-snap-pool9(lingxi12,free1) | 9 | 3 | 8 | 1/1 | 1048 | 22.5 | 40.00 | 3.00 | 10.38 | 0.14 | 29.62 | 74% |
| highfree-snap-pool9(lingxi41,free4) | 9 | 3 | 8 | 4/4 | 117905 | 2918.5 | 100.00 | 9.00 | 87.93 | 0.11 | 12.07 | 12% |
| fresh-pool7(cost8,lingxi25,free1) | 7 | 3 | 8 | 1/1 | 2617 | 35.9 | 55.00 | 6.00 | 48.95 | 0.18 | 6.05 | 11% |
| lowlingxi-fresh-pool7(lingxi14,free1) | 7 | 3 | 8 | 1/1 | 328 | 2.0 | 30.00 | 3.00 | 15.62 | 0.07 | 14.38 | 48% |
| rich-fresh-pool7(lingxi41,free1) | 7 | 3 | 8 | 1/1 | 6631 | 65.1 | 60.00 | 7.00 | 59.30 | 0.05 | 0.70 | 1% |
| highfree-fresh-pool7(lingxi25,free4) | 7 | 3 | 8 | 4/4 | 1677 | 22.3 | 55.00 | 6.00 | 49.16 | 0.17 | 5.84 | 11% |
| fresh-pool8(cost8,lingxi25,free1) | 8 | 3 | 8 | 1/1 | 14477 | 334.5 | 95.00 | 7.00 | 72.13 | 0.54 | 22.87 | 24% |
| lowlingxi-fresh-pool8(lingxi14,free1) | 8 | 3 | 8 | 1/1 | 1580 | 15.5 | 56.43 | 2.77 | 15.01 | 0.17 | 41.42 | 73% |
| rich-fresh-pool8(lingxi41,free1) | 8 | 3 | 8 | 1/1 | 54099 | 778.9 | 105.00 | 8.00 | 97.54 | 0.07 | 7.46 | 7% |
| highfree-fresh-pool8(lingxi25,free4) | 8 | 3 | 8 | 4/4 | 6610 | 194.3 | 95.00 | 7.00 | 73.50 | 0.54 | 21.50 | 23% |
| fresh-pool9(cost8,lingxi25,free1) | 9 | 3 | 8 | 1/1 | 63819 | 2168.2 | 95.00 | 7.00 | 70.94 | 0.55 | 24.06 | 25% |
| lowlingxi-fresh-pool9(lingxi14,free1) | 9 | 3 | 8 | 1/1 | 5696 | 84.2 | 54.78 | 2.64 | 16.83 | 0.23 | 37.94 | 69% |
| rich-fresh-pool9(lingxi41,free1) | 9 | 3 | 8 | 1/1 | 318637 | 7076.9 | 115.00 | 9.00 | 107.35 | 0.13 | 7.65 | 7% |
| highfree-fresh-pool9(lingxi25,free4) | 9 | 3 | 8 | 4/4 | 26610 | 1343.9 | 95.00 | 7.00 | 74.20 | 0.52 | 20.80 | 22% |
| depleted-pool7(lingxi2,free3) | 7 | 3 | 8 | 3/3 | 0 | 0.0 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0% |
| depleted-pool7(lingxi20,free3) | 7 | 3 | 8 | 3/3 | 796 | 12.4 | 35.00 | 5.00 | 33.48 | 0.08 | 1.52 | 4% |
| depleted-pool8(lingxi2,free3) | 8 | 3 | 8 | 3/3 | 0 | 0.0 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0% |
| depleted-pool8(lingxi20,free3) | 8 | 3 | 8 | 3/3 | 3152 | 81.6 | 40.00 | 5.00 | 35.58 | 0.17 | 4.42 | 11% |
| depleted-pool9(lingxi2,free3) | 9 | 3 | 8 | 3/3 | 0 | 0.0 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0% |
| depleted-pool9(lingxi20,free3) | 9 | 3 | 8 | 3/3 | 11880 | 486.4 | 40.00 | 5.00 | 33.67 | 0.18 | 6.33 | 16% |

| 状态 | 在线rollout诗意 | ±SE | rollout gap | rollout gap% | 总刷新/局 | 出韵/局 | 句/局 | 精确机会均值 gap (δ 恒等式) |
|---|---|---|---|---|---|---|---|---|
| midrun-snap-pool7(lingxi41,free1) | 80.00 | 0.00 | 0.00 | 0.0% | 3.8 | 3.4 | 7.0 | 10.00±0.99 |
| lowlingxi-snap-pool7(lingxi12,free1) | 40.00 | 0.00 | 0.00 | 0.0% | 2.2 | 1.1 | 3.0 | 27.33±2.31 |
| highfree-snap-pool7(lingxi41,free4) | 80.00 | 0.00 | 0.00 | 0.0% | 3.8 | 3.4 | 7.0 | 10.00±0.99 |
| midrun-snap-pool8(lingxi41,free1) | 85.00 | 0.00 | 0.00 | 0.0% | 3.7 | 2.7 | 8.0 | 4.33±0.75 |
| lowlingxi-snap-pool8(lingxi12,free1) | 40.00 | 0.00 | 0.00 | 0.0% | 1.3 | 0.3 | 3.0 | 29.17±1.02 |
| highfree-snap-pool8(lingxi41,free4) | 85.00 | 0.00 | 0.00 | 0.0% | 3.7 | 2.7 | 8.0 | 4.33±0.75 |
| midrun-snap-pool9(lingxi41,free1) | 100.00 | 0.00 | 0.00 | 0.0% | 5.0 | 3.4 | 9.0 | — |
| lowlingxi-snap-pool9(lingxi12,free1) | 40.00 | 0.00 | 0.00 | 0.0% | 1.3 | 0.1 | 3.0 | — |
| highfree-snap-pool9(lingxi41,free4) | 100.00 | 0.00 | 0.00 | 0.0% | 5.1 | 3.5 | 9.0 | — |
| fresh-pool7(cost8,lingxi25,free1) | 55.00 | 0.00 | 0.00 | 0.0% | 1.6 | 1.6 | 6.0 | 7.50±1.43 |
| lowlingxi-fresh-pool7(lingxi14,free1) | 25.92 | 0.51 | 4.08 | 13.6% | 0.8 | 0.0 | 2.8 | 12.46±0.73 |
| rich-fresh-pool7(lingxi41,free1) | 60.00 | 0.00 | 0.00 | 0.0% | 1.6 | 2.3 | 7.0 | 1.33±0.41 |
| highfree-fresh-pool7(lingxi25,free4) | 55.00 | 0.00 | 0.00 | 0.0% | 1.6 | 1.6 | 6.0 | 7.50±1.43 |
| fresh-pool8(cost8,lingxi25,free1) | 92.83 | 0.43 | 2.17 | 2.3% | 3.7 | 2.0 | 6.8 | 11.83±3.39 |
| lowlingxi-fresh-pool8(lingxi14,free1) | 55.00 | 0.90 | 1.43 | 2.5% | 1.4 | 0.3 | 2.8 | 43.24±1.71 |
| rich-fresh-pool8(lingxi41,free1) | 104.67 | 0.16 | 0.33 | 0.3% | 3.7 | 2.6 | 8.0 | 6.69±0.66 |
| highfree-fresh-pool8(lingxi25,free4) | 92.83 | 0.36 | 2.17 | 2.3% | 3.7 | 2.1 | 6.8 | 10.83±3.36 |
| fresh-pool9(cost8,lingxi25,free1) | 92.00 | 0.32 | 3.00 | 3.2% | 4.6 | 1.8 | 6.5 | — |
| lowlingxi-fresh-pool9(lingxi14,free1) | 48.83 | 2.01 | 5.94 | 10.9% | 1.5 | 0.3 | 2.6 | — |
| rich-fresh-pool9(lingxi41,free1) | 114.58 | 0.18 | 0.42 | 0.4% | 4.9 | 3.3 | 9.0 | — |
| highfree-fresh-pool9(lingxi25,free4) | 93.50 | 0.30 | 1.50 | 1.6% | 4.7 | 1.9 | 6.9 | — |
| depleted-pool7(lingxi2,free3) | 0.00 | 0.00 | 0.00 | 0.0% | 0.0 | 0.0 | 0.0 | 0.00±0.00 |
| depleted-pool7(lingxi20,free3) | 35.00 | 0.00 | 0.00 | 0.0% | 2.2 | 1.8 | 5.0 | 1.67±0.69 |
| depleted-pool8(lingxi2,free3) | 0.00 | 0.00 | 0.00 | 0.0% | 0.0 | 0.0 | 0.0 | 0.00±0.00 |
| depleted-pool8(lingxi20,free3) | 40.00 | 0.00 | 0.00 | 0.0% | 2.4 | 1.3 | 5.0 | 6.00±1.39 |
| depleted-pool9(lingxi2,free3) | 0.00 | 0.00 | 0.00 | 0.0% | 0.0 | 0.0 | 0.0 | — |
| depleted-pool9(lingxi20,free3) | 40.00 | 0.00 | 0.00 | 0.0% | 3.2 | 1.2 | 5.0 | — |

（主表 A 的 `在线rollout` 列来自 `-live-samples 1024 -live-policysamples 32 -live-episodes 60` 的独立运行，状态与种子完全相同；`后续启发式` 列来自 2000 局。真实线上参数口径见主表 C 的 as-wired 行与 §6 第 4 点。）

### 主表 B：嵌套池·低灵犀（lingxi=14, free=1, cost=8；池 7 ⊂ 8 ⊂ 9 ⊂ 10，韵组构成固定）

| 状态 | 池 | 候选 | 成本 | 免费 | 精确节点 | 精确耗时ms | V*(诗意) | V*(句) | 后续启发式诗意 | ±SE | gap | gap% |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| nested-lowlingxi-fresh-pool7(lingxi14,free1) | 7 | 3 | 8 | 1/1 | 972 | 3.8 | 38.75 | 4.00 | 22.40 | 0.18 | 16.35 | 42% |
| nested-lowlingxi-fresh-pool8(lingxi14,free1) | 8 | 3 | 8 | 1/1 | 3779 | 35.1 | 38.00 | 4.00 | 18.57 | 0.14 | 19.43 | 51% |
| nested-lowlingxi-fresh-pool9(lingxi14,free1) | 9 | 3 | 8 | 1/1 | 12592 | 115.8 | 70.00 | 4.00 | 32.01 | 0.37 | 37.99 | 54% |
| nested-lowlingxi-fresh-pool10(lingxi14,free1) | 10 | 3 | 8 | 1/1 | 73843 | 899.5 | 66.04 | 3.94 | 27.66 | 0.36 | 38.38 | 58% |

| 状态 | 在线rollout诗意 | ±SE | rollout gap | rollout gap% | 总刷新/局 | 出韵/局 | 句/局 | 精确机会均值 gap (δ 恒等式) |
|---|---|---|---|---|---|---|---|---|
| nested-lowlingxi-fresh-pool7(lingxi14,free1) | 37.67 | 0.44 | 1.08 | 2.8% | 3.2 | 1.8 | 3.9 | 16.49±0.59 |
| nested-lowlingxi-fresh-pool8(lingxi14,free1) | 31.75 | 0.96 | 6.25 | 16.4% | 3.3 | 1.1 | 3.5 | 19.66±0.52 |
| nested-lowlingxi-fresh-pool9(lingxi14,free1) | 58.00 | 1.17 | 12.00 | 17.1% | 3.8 | 1.3 | 3.0 | 36.48±1.10 |
| nested-lowlingxi-fresh-pool10(lingxi14,free1) | 57.58 | 1.28 | 8.45 | 12.8% | 4.1 | 1.2 | 2.9 | — |

### 主表 C：嵌套池·富灵犀（lingxi=41, free=1, cost=8；池 7 ⊂ 8 ⊂ 9 ⊂ 10，韵组构成固定）

| 状态 | 池 | 候选 | 成本 | 免费 | 精确节点 | 精确耗时ms | V*(诗意) | V*(句) | 后续启发式诗意 | ±SE | gap | gap% |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| nested-rich-fresh-pool7(lingxi41,free1) | 7 | 3 | 8 | 1/1 | 6969 | 29.3 | 60.00 | 7.00 | 51.25 | 0.09 | 8.75 | 15% |
| nested-rich-fresh-pool8(lingxi41,free1) | 8 | 3 | 8 | 1/1 | 47342 | 276.4 | 65.00 | 8.00 | 51.33 | 0.13 | 13.67 | 21% |
| nested-rich-fresh-pool9(lingxi41,free1) | 9 | 3 | 8 | 1/1 | 282060 | 4528.0 | 105.00 | 8.00 | 84.28 | 0.21 | 20.72 | 20% |
| nested-rich-fresh-pool10(lingxi41,free1) | 10 | 3 | 8 | 1/1 | 1603171 | 39951.8 | 104.98 | 8.00 | 77.34 | 0.32 | 27.63 | 26% |

| 状态 | 在线rollout诗意 | ±SE | rollout gap | rollout gap% | 总刷新/局 | 出韵/局 | 句/局 | 精确机会均值 gap (δ 恒等式) |
|---|---|---|---|---|---|---|---|---|
| nested-rich-fresh-pool7(lingxi41,free1) | 60.00 | 0.00 | 0.00 | 0.0% | 4.1 | 4.7 | 7.0 | 8.40±0.29 |
| nested-rich-fresh-pool8(lingxi41,free1) | 62.08 | 0.32 | 2.92 | 4.5% | 5.9 | 5.0 | 7.4 | 13.47±0.41 |
| nested-rich-fresh-pool9(lingxi41,free1) | 97.83 | 0.57 | 7.17 | 6.8% | 7.9 | 5.0 | 8.0 | — |
| nested-rich-fresh-pool10(lingxi41,free1) | 97.33 | 0.55 | 7.65 | 7.3% | 9.7 | 4.5 | 7.2 | — |

（主表 C 的池 8/9/10 rollout 列另有 as-wired 复测：`samples=8192, max_samples=8192, policy_samples=128, budget=1500ms, exact_limit=6, 40 局` → gap 2.88 / 4.75 / 7.35 诗意，见 `_research_b/live-aswired.json`。）

### 3.1 线上真实参数口径（as-wired）的 rollout gap

`planLive` 线上真实参数 = `samples=4096, max_samples=8192, policy_samples=128, budget=1500ms, exact_limit=6`；下面用同一组嵌套池·富灵犀状态、40 局评估（`_research_b/live-aswired.json`）：

| 状态 | V*诗意 | 后续启发式 | gap | 后续启发式 gap% | **as-wired rollout** | ±SE | **rollout gap** | rollout gap% |
|---|---|---|---|---|---|---|---|---|
| nested-rich-fresh-pool8(lingxi41,free1) | 65.00 | 51.52 | 13.48 | 21% | **62.12** | 0.40 | **2.88** | **4.4%** |
| nested-rich-fresh-pool9(lingxi41,free1) | 105.00 | 84.45 | 20.55 | 20% | **100.25** | 0.69 | **4.75** | **4.5%** |
| nested-rich-fresh-pool10(lingxi41,free1) | 104.98 | 77.43 | 27.55 | 26% | **97.62** | 0.67 | **7.35** | **7.0%** |

对照同一组状态在 `samples=1024, policy_samples=32, 40–60 局` 下的 gap（主表 C）：

| 池 | rollout gap @1024/32 | rollout gap @4096~8192/128（as-wired） | 差 |
|---|---|---|---|
| 8 | 2.92 | 2.88 | -0.04 |
| 9 | 7.17 | 4.75 | -2.42 |
| 10 | 7.65 | 7.35 | -0.30 |

即：**采样从 1024 提到线上真实水平只在池 9 上带来明显收益（−2.42 诗意）**，池 8/10 几乎不变（−0.04 / −0.30）。所以这些状态上的残留 gap **主要不是采样不足**，而是"同一后续启发式给所有根动作打了同向偏差 + `policy_samples` 后续策略预筛噪声"造成的系统性选择误差。

### 3.2 怎样读这三张表

* **gap% 随池增大而上升**（嵌套池·富灵犀：池 7→10 依次 15% / 21% / 20% / 26%；嵌套池·低灵犀：42% / 51% / 54% / 58%）。机制是确定的：随机 3 句里出现"不能押韵"候选的概率随池内可用韵组变多而上升，于是硬刷新规则被触发得更多（§4.3 实测每局刷新 3.2–9.7 次）。真实 70 句池的韵组比这些小池更分散，**这一项**会让真实 loss 不小于小池；但单次刷新的封盘成本（blocked 3 句占池比）在真实池上更小，方向相反——见 §6 第 1 点。
* **灵犀越低，相对 gap 越大**（40%–74%）：此时"保住链子"本身就是全部价值，一次错误的付费刷新直接结束这一局；而灵犀充足时同一策略只损失 1%–16%。
* **免费刷新多并不自动变好**：`highfree`（4/4）与 `free1` 在这些池里几乎同值，说明瓶颈不在免费次数，而在"什么时候该刷"。
* 真实残局快照（灵犀 2 < 成本 8）在三种池大小下 `World.Start` 直接 `Done`，V\*=0、gap=0：**这一局已经没有任何可回收的 headroom**。

## 4. 损失归因

### 4.1 δ 恒等式分解（400 局/状态，4 个状态，共 1600 局）

| 决策类别 | 归类 | 每状态平均贡献（诗意） | 占比 |
|---|---|---|---|
| `刷(规则1:最优出韵) / 最优应选句` | 刷新/出韵规避 | 64.94 | 68.9% |
| `选句:押韵+新诗人(不粘-5) / 最优应刷` | 选句/时机 | 4.79 | 5.1% |
| `选句:押韵+新诗人(不粘-5) / 最优他句` | 选句/他句 | 4.46 | 4.7% |
| `选句:中间态+新诗人 / 最优应刷` | 选句/时机 | 3.87 | 4.1% |
| `选句:中间态+新诗人(不粘-5) / 最优应刷` | 选句/时机 | 3.71 | 3.9% |
| `选句:中间态 / 最优应刷` | 选句/时机 | 2.76 | 2.9% |
| `选句:中间态 / 最优他句` | 选句/他句 | 2.47 | 2.6% |
| `选句:押韵(不粘-5) / 最优他句` | 选句/他句 | 2.36 | 2.5% |
| `选句:押韵+新诗人 / 最优他句` | 选句/他句 | 1.89 | 2.0% |
| `选句:押韵 / 最优他句` | 选句/他句 | 0.95 | 1.0% |
| `选句:出韵+新诗人 / 最优他句` | 选句/他句 | 0.84 | 0.9% |
| `选句:押韵(不粘-5) / 最优应刷` | 选句/时机 | 0.79 | 0.8% |
| `选句:出韵 / 最优他句` | 选句/他句 | 0.26 | 0.3% |
| `选句:中间态(不粘-5) / 最优应刷` | 选句/时机 | 0.19 | 0.2% |
| **合计** |  | **94.29** | **100%** |

（恒等式校验：Σδ = 94.29；同 4 个状态按策略表 MC（4000 局）算的 gap 之和 = 93.87，两者差 0.42 诗意（0.4%），即 δ 恒等式在 400 局/状态下已经收敛。）

（未列入上表的小类（>0.005 诗意）：`选句:中间态(不粘-5) / 最优他句` 0.01；其余 15 类为 0 或可忽略。）

### 4.2 按问题清单逐项回答

| 问题 | 结论 | 量化证据 |
|---|---|---|
| (a) 刷新时机 | **是主因，占 86.0%** | δ 分解中"刷/最优应选句" 68.9% + "选句/最优应刷" 17.1%；消融「完全不刷新」在 24 个非终局状态平均 **+0.66 诗意**（8 个显著变好、13 个显著变差，符号随灵犀/免费次数翻转）|
| (b) 换组切换 | **不是损失来源，是净收益来源** | 关闭 `SwitchThreshPct/TargetPoolWeight/TargetNewPoetWeight/MinTargetPool` 平均 **−0.83 诗意**（14 个状态显著变差、1 个显著变好）|
| (c) 出韵规避 | **硬规则是最大单点损失；评分惩罚略偏重** | 关规则 1（`NoOffRefresh`）平均 **+1.94 诗意**（11 好 / 11 坏，取决于状态）；`OffRhymePenalty=0`（只改评分权重）平均 **+0.42 诗意**，16 个状态显著变好、**0 个变差**；而"真的选出韵句"本身几乎不亏（`选句:出韵/最优他句` 仅 0.3%）|
| (d) 诗人首收加分 | **0 损失，可删** | 关闭 `NewPoetBonus/RefreshPoetBonus/TargetNewPoetWeight` 平均 **−0.01 ± 0.00 诗意**，24 个状态 0 个显著（与 `EvolvedStrategy` 注释里"NewPoetBonus 是噪声"一致）|
| (e) 别处 | **"押韵但不粘"的偏好** 是第二主题（14.0%） | `选句:押韵+新诗人(不粘-5)` 类 9.8% + `选句:押韵(不粘-5)` 类 3.3%（后者再拆 "最优应刷" 0.8% / "最优他句" 2.5%）；其余为零散的中间态选择/时机错误 |

### 4.3 三条硬规则的实际触发频次（`-mode rules`，300 局）

| 池类型 | 灵犀 | 决策数 | 刷新数 | 仅规则1 | 仅规则2 | 仅规则3 | 规则1∩规则3 | 闸后刷 |
|---|---|---|---|---|---|---|---|---|
| balanced 池7（分层） | 25 | 2,727 | 1,217 | 0 | 0 | 323 | 894 | 0 |
| balanced 池8（分层） | 25 | 3,047 | 1,620 | 0 | 0 | 340 | 1,280 | 0 |
| balanced 池9（分层） | 25 | 3,322 | 1,952 | 0 | 0 | 199 | 1,753 | 0 |
| balanced 池8（**非嵌套** stratified） | 25 | 2,872 | 1,083 | 1,064 | 0 | 19 | 0 | 0 |
| 单一韵组池 mix 11:10 | 25 | 3,363 | 363 | 363 | 0 | 0 | 0 | 0 |
| mix 11:10,9:1（大组 10 句） | 25 | 3,838 | 538 | 472 | 0 | 66 | 0 | 0 |
| mix 11:10,9:3（大组 10 句） | 25 | 4,517 | 617 | 617 | 0 | 0 | 0 | 0 |

读出三点：

1. **规则 1（`bestKind==出韵 → 刷`）在均衡池里从不单独生效**：它 100% 与规则 3 联合触发（因为均衡池里常有韵组只剩 1 句，`bestPool(=1) < MinTargetPool(=2)`）。这解释了嵌套池里 `NoOffRefresh` 与 `NoMiddleRefresh` 的消融结果恰好为 **Δ=0.00**——规则 1 被关掉后，同一状态仍被规则 3 强制刷新，轨迹逐字不变。**要真正去掉这些刷新必须同时关规则 1 和 3**（= `完全不刷新` 那一行）。
2. **规则 2（`中间态 且 当前组剩余 > DryGroupFloor=9`）在本报告所有小池里触发次数为 0**，原因是它要求"当前韵组还剩 >9 句"而池本身只有 7–13 句（且非嵌套分层池每组恰好 2 句 → `bestPool=2 ≥ MinTargetPool` 又让规则 3 失效）。真实 70 句池里 `cur` 可达 10–80，同时 3 抽里没有押韵候选的概率约 40%–55%，**规则 2 在线上很可能是最高频的刷新触发**。本报告无法在缩减池里测量它的代价——这是最重要的外推缺口（见 §6）。
3. 「闸后刷」（走到 `LowLingxiFloor`/`RefreshGainFloor` 两道收益闸后仍然刷）在所有测试里都是 0 次：刷新的决定 100% 由三条硬规则做出，收益闸只用来否决，从不新增刷新。**这直接解释了低灵犀灾难**：闸门 `if refresh && free<=0 && bestKind != 出韵` 显式排除出韵情形，所以当 3 个候选全出韵时，规则 1 会绕过保命闸花掉最后 1 点灵犀 → 本轮结束、链子断掉。

## 5. 校验（这些数字为什么可信）

1. **复刻等价性**：11,200 局 / 85,534 次决策 / 0 失配（§1.5）。
2. **δ 恒等式 vs 策略表 gap**：

| 状态 | 策略表 gap（2000/4000 局 MC） | 30 局 δ 估计 | 400 局 δ 估计 |
|---|---|---|---|
| fresh-pool8(lingxi25,free1) | 22.87 | 11.83 ± 3.39 | **22.56 ± 1.23** |
| lowlingxi-fresh-pool8(lingxi14,free1) | 41.42 | 43.24 ± 1.71 | **43.06 ± 0.47** |
| rich-fresh-pool8(lingxi41,free1) | 7.46 | 6.69 ± 0.66 | **7.38 ± 0.16** |
| highfree-fresh-pool8(lingxi25,free4) | 21.50 | 10.83 ± 3.36 | **21.29 ± 1.22** |

30 局时 δ 估计对两个高方差状态偏低 10–11 诗意（SE 3.4）；**400 局后全部回到策略表值 ±0.5 内**。所以本报告的归因只用 ≥200 局的 δ 运行。
3. **两条独立代码路径互证**：`δ` 是在每个中间状态重新调用 `ExactMulti` 得到的（400 局 × 最多 12 状态 = 数万次精确求解），而策略表的 gap 来自"完整 MC 模拟 + 无前瞻的后续策略"。两者在 4 个状态上相差 0.4%–1%，说明精确求解器与模拟器对同一规则集的理解一致（也说明 `World` 的两条实现路径没有分叉）。
4. **精确值交叉核对**：`-mode real` 在全库（572）与真实快照池（70，`Eligible` 全量）上尝试精确求解，均返回 `Complete=false`（节点/时间预算耗尽），与 §2 的可行性判定一致；而同样规模的缩减池（7–10）在同一预算内全部 `Complete=true`。

## 6. 局限与外推缺口（必须一起读）

1. **gap 的绝对值不可直接搬到真实池**：本报告的 gap 是"缩减世界里的最优"与"同一世界的当前策略"之差。池越大，抽到非押韵候选越频繁、硬刷新触发越多（§3.1、§4.3），所以**真实池上的启发式 loss 不应小于这里的数值**；反过来，单次刷新的"封盘成本"（blocked 3 句占池比例）在真实池上更小（3/68 vs 3/8），这一项使真实 loss 更小。两个方向都真实存在，本报告没有把它们分离——这是最重要的量化缺口。
2. **规则 2 未被测量**（§4.3 第 2 点）：它在真实 70 句池上可能是最高频刷新触发，但需要 `DryGroupFloor` 同量级（≥10 句/组）的池才能激活，而那样的池精确不可解。**能测的部分（规则 1/3 + 收益闸 + 评分权重 + 诗人加分）指向同一个方向：刷新过多。**
3. **换组机制未被公平测量**：`SwitchThreshPct` 按池大小取整（池 8 → 阈值 1，真实池 → 13），`DryGroupFloor/MinTargetPool` 是绝对句数。小池里这套机制大部分时间不活跃，所以"关换组平均 −0.83"只能说明"在这些池里它没造成损失"，不能证明它在真实池上无害。
4. **部署口径的 rollout 用 1024 采样做代理**（as-wired 的 8192 采样另测了嵌套池 8/9/10）。真实池上无法取得 V\*，只能用项目既有评估做"相对量"：`screenshots/jiji-research-20260906/live-budget-full-episode-smoke.json`（seed 202609213，4 局，70 句池，objective=shiyi-first，samples 4096/max 8192/budget 1500ms/policy_samples 128/exact_limit 6）：诗意 478.75 → 520.0，**Δ=+41.25，95% CI [−15.52, +98.02]**；句数 45.25 → 48.25；`PlanningSamples=548,864` / 123 次决策 = **4,463 采样/决策**，`MaxPlanningNS=636 ms`（预算内），4 局耗时 250 s。即：**在真实池上，部署口径比后续启发式高约 41 诗意（样本太小，CI 跨 0）**，这与"启发式 gap 随池增大而增大"的方向一致；但"部署口径离最优还有多远"在真实池上**没有**可测答案。
5. **诗句抽样偏差**：同一池大小下不同抽样会带来 V\* 的跳变（嵌套池 8→9 时 V\* 从 38 跳到 70，因为新增了一句稀有/李贺句）。因此跨池比较只看**相对 gap%**，不看绝对诗意。

## 7. 复现命令

```powershell
$env:Path = "C:\Users\ncy\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin;" + $env:Path
cd C:\Users\ncy\Desktop\whhx\wuhui-auto
go build -o _research_b/jrb.exe ./cmd/tools/jijiresearch_b

# 0) 复刻等价性 (必须为 0 失配)
_research_b/jrb.exe -mode parity -snapshot _research_b/snapshot-eval70.json -pools 4,5,6,7,8,9,10 -mc 80

# 1) 状态空间/精确规模
_research_b/jrb.exe -mode scale -minpool 4 -maxpool 10 -out _research_b/scale-raw.json
_research_b/jrb.exe -mode feas  -pools 10,16,24,40,70 -feas-lingxi 9,10,12,16,25,41 -solvetimeout 120s -maxnodes 20000000
_research_b/jrb.exe -mode real  -snapshot _research_b/snapshot-eval70.json

# 2) 主表 (27 状态: 快照池/新开局/残局 × 池 7/8/9 × 灵犀/免费次数变体)
_research_b/jrb.exe -mode headroom -snapshot _research_b/snapshot-eval70.json -snapshot2 _research_b/snapshot-live.json `
  -pools 7,8,9 -mc 2000 -delta-episodes 30 -delta-maxpool 8 -out _research_b/headroom-main.json

# 3) 部署口径 (在线 rollout)
_research_b/jrb.exe -mode headroom -snapshot _research_b/snapshot-eval70.json -snapshot2 _research_b/snapshot-live.json `
  -pools 7,8,9 -live-maxpool 9 -live-samples 1024 -live-policysamples 32 -live-episodes 60 -delta-episodes 0 -mc 300 -out _research_b/headroom-live.json
_research_b/jrb.exe -mode headroom -snapshot _research_b/snapshot-eval70.json -pools 8,9,10 -nested -only "rich-fresh" `
  -live-maxpool 10 -live-samples 8192 -live-policysamples 128 -live-episodes 40 -live-budget 1500ms -delta-episodes 0 -mc 1000 -out _research_b/live-aswired.json

# 4) 嵌套池趋势 (固定韵组构成) 与 400 局 δ 校验
_research_b/jrb.exe -mode headroom -snapshot _research_b/snapshot-eval70.json -pools 7,8,9,10 -nested -only "lowlingxi-fresh" `
  -mc 2000 -delta-episodes 200 -delta-maxpool 9 -live-maxpool 10 -live-samples 1024 -live-policysamples 32 -live-episodes 60 -out _research_b/nested-low.json
_research_b/jrb.exe -mode headroom -snapshot _research_b/snapshot-eval70.json -pools 7,8,9,10 -nested -only "rich-fresh" `
  -mc 2000 -delta-episodes 200 -delta-maxpool 8 -live-maxpool 10 -live-samples 1024 -live-policysamples 32 -live-episodes 60 -out _research_b/nested-rich.json
_research_b/jrb.exe -mode headroom -snapshot _research_b/snapshot-eval70.json -pools 8 -only "fresh-pool8" -delta-episodes 400 -delta-maxpool 8 -mc 4000 -out _research_b/delta-conv.json

# 5) 硬规则触发频次
_research_b/jrb.exe -mode rules -pools 7,8,9,10 -nested -mc 300
_research_b/jrb.exe -mode rules -pools 8 -mc 300
_research_b/jrb.exe -mode rules -pools 10 -mix "11:10" -mc 300
_research_b/jrb.exe -mode rules -pools 11 -mix "11:10,9:1" -mc 300
_research_b/jrb.exe -mode rules -pools 13 -mix "11:10,9:3" -mc 300
```

## 附：全部消融数值（24 个非终局状态，mc=2000，Δ 相对后续启发式）

| 消融 | 平均 Δ诗意 | 平均 gap | Δ>+2SE 的状态数 | Δ<−2SE 的状态数 |
|---|---|---|---|---|
| `P0 current(EvolvedStrategy)` | +0.00 | 15.40 | 0 | 0 |
| `a1 完全不刷新` | +0.66 | 14.74 | 8 | 13 |
| `a2 只允许免费刷新` | +2.43 | 12.97 | 11 | 3 |
| `a3 关规则1(出韵→刷)` | +1.94 | 13.46 | 11 | 11 |
| `a4 关规则2(中间态→刷)` | +0.00 | 15.40 | 0 | 0 |
| `a5 关规则3(下家池小→刷)` | -0.82 | 16.22 | 1 | 14 |
| `a6 刷新只保留规则1` | -0.82 | 16.22 | 1 | 14 |
| `b 关换组/目标大组权重` | -0.83 | 16.23 | 1 | 14 |
| `c 关出韵评分惩罚` | +0.42 | 14.98 | 16 | 0 |
| `d 关诗人首收加分` | -0.01 | 15.41 | 0 | 0 |
| `e 全部关闭` | +0.31 | 15.09 | 8 | 14 |
| `ref BaselineStrategy` | -1.95 | 17.35 | 5 | 13 |
| `ref CountStrategy` | -1.48 | 16.88 | 9 | 12 |

（`a4 关规则2` 在全部 24 个状态上 Δ 恰好 0.00：规则 2 未激活，见 §4.3；`d 关诗人首收加分` 同样 0.00/±0.00；`c 关出韵评分惩罚` 是唯一"0 个变差"的消融。）
