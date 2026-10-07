# 集句调参流水线 测量审计报告

> **说明**：本文写成于原始工作区，文中出现的工具名（如 `jijiworld`、`jijicritic`）、
> 路径（如 `screenshots/...`）与脚本名都是那次实验当时的样子，本仓库未包含它们。
> 保留原样是为了让数字可追溯；要复现，请用本目录 `README.md` 里给出的 `cmd/research/` 工具。

审计对象：`C:\Users\ncy\Desktop\whhx\wuhui-auto`（Go module `wuhui-auto`），
证据库：`screenshots\jiji-research-20260906\`（127 个文件，其中 44 个含完整逐局记录、6 个模型、6 份训练报告、6 份 parity、6 份 `.training.json`、6 个 `set-diagnose-*`）。

审计方式：只读源码 + 重算统计量 + 用仓库自带工具端到端复现。未修改任何既有文件；
新增文件仅 `_research_d\`（本报告、`se_audit.ps1`、`se_recompute.csv`、复现输出 JSON）。

---

## 0. 结论摘要（先给判决）

1. **测量本身是可信的，而且质量高于同类项目**：44/44 份 artefact 的逐局数据都能用标准配对公式精确重算出文件里写的 CI（0 处不符）；`go run` 复现的 3 个数字**逐位一致**（含 1000 局逐局 Turns）；6 个模型的 Python/Go parity 与文档声称的误差完全一致；连 PyTorch 训练的第 1 个 epoch 指标都能逐位复现。模拟器与种子是可复现的。
2. **"±2SE" 是诚实的配对区间，但它只有一个条件维度**。它只统计"同一个起始状态下重复抽样的噪声"，不含状态间方差。生产缓存里 3 个真实状态的 `预计诗意差` 分别是 **+104.83 / +102.01 / +18.83**，跨状态标准差 **48.86**，而每次报的是 ±1.52 ~ ±4.22。也就是说引用 ±2SE 会把它当成总不确定性，实际低估约 **12~32 倍**。
3. **集句 critic（set-critic）这条线的"提升"是真测量，但不是产品意义上的提升**：v6 相对 v2 的 **+1.755 ± 0.275**（n=1000，预留种子）是干净结论；但同一份 artefact 显示 v6 **输给一个 128 抽样的 rollout 规划器 −2.593 ± 0.597**（n=300），v2 同样输 −3.893 ± 0.559。文档自己也写明了"not over the production planner"。凡是把它讲成"调参让策略变好了"就会改变结论。
4. **同一批 artefact 里，两个"独立"评测协议对同一个改动（ShiyiWeight 0→16）给出的结论相差 23σ**（内部 +94.75 ± 2.04，外部 −2.66 ± 3.62）。代码层面可定位：`screenMultiContinuation` 在**两条臂内部**都重新筛选了含 `ShiyiWeight∈{0,4,16}` 的同一族策略，因此 `shiyi-*.json` 那批外部评测**结构上无法度量**被调策略的效应，不能当作独立验证。
5. **缓存指纹在"字段覆盖"上是完整的，我没有找到可达的假命中**；真正的风险是另一类：`trainingCacheVersion` 是手写常量（无代码哈希），且**被时间预算截断的搜索结果是可缓存且会被永久复用的**（已实测 8s 预算下只跑 6/24 代却 `accepted=true`、`validation=4096`、`delta=95.86 > 2.4*SE`，完全满足 `cacheableTraining`）。
6. 一个非统计事实，但直接影响"进化搜索取得提升"这句话：写进报告的两个自适应训练产物（`live-shiyi-policy.json`、`shiyi-trained-policy.json`）**逐位等于硬编码锚点** `CountStrategy()+ShiyiWeight∈{4,16}`；`live-count-policy.json` **7/7 字段逐位等于**由池大小确定性推出的 `mid` 锚点。即：这几份证据里的收益来自人工预置的锚点/单一超参，搜索的变异子代没有贡献。（生产缓存里的 3 条记录倒是变异子代，见 §3.3——所以搜索本身没坏，是**被写进报告的那几份 artefact 不能证明搜索有贡献**。）

---

## 1. 泄漏审计（哪些数据调参、哪些数据报分）

### 1.1 三条流水线的种子/状态来源

| 阶段 | 工具 | 种子 | 起始状态 | 关键参数 |
|---|---|---|---|---|
| 数据集 v1 | `jijicritic -mode data` | **202609081** | `snapshot/runtime-main.json` | 400 局 × 16 roots、每动作 64 个 MC 未来、`mixed=true`、teacher=`CountStrategy()` |
| 数据集 v2 | 同上 | **202609083** | 同上 | 40 局 × 8 roots × 4 samples（极小，v3 用） |
| 数据集 v4 | 同上 + `-coverage` | **202609101** | 同上 | 600 局 × 20 roots、**64** samples、workers 8 |
| 数据集 v6 | 同上 + `-coverage` | **202609101** | 同上 | 600 局 × 20 roots、**256** samples、workers 8 |
| 模型训练/选择 | `scripts/jiji-set-train.py` (torch 2.8.0+cu128) | 202609082 / 084 / **202609102** | — | 选择准则 = 数据集**内部 dev 分片**的 `weighted loss` 或 `label_regret`；dev 分片按整局切分，报告里完整列出 episode id 列表 |
| 策略评测 | `jijiworld` | **202609091 / 097 / 103 / 109** | 同一个 `runtime-main.json` | `mixed` 全为 **false** |
| 诗意 pilot | `jijiworld` | 202609201 ~ 202609211 | 同上 | 部分 `mixed=true` |

### 1.2 关键发现 A：数据集 v4 与 v6 的**状态集完全相同**

`set-data-v4.jsonl.gz` 与 `set-data-v6.jsonl.gz` 的 header：

```
v4 options = {"episodes":600,"roots_per_episode":20,"every":3,"samples":64, "seed":202609101,...,"whole_episode_coverage":true}
v6 options = {"episodes":600,"roots_per_episode":20,"every":3,"samples":256,"seed":202609101,...,"whole_episode_coverage":true}
```

只有 `samples` 不同。而轨迹 RNG 是 `rand.NewSource(opt.Seed + ep*1000003)`（`set_dataset.go:106`），
标签 MC 的种子来自另一条独立流（`set_dataset.go:189`），`opt.Samples` 只改变标签的抽样条数，
**不消耗轨迹 RNG**。⇒ v4 与 v6 访问的是**同一批 600 局的同一批状态**，v6 只是把标签噪声降低了 4 倍。

后果：`v4 / v5 / v6` 不是 3 个独立候选。v4 与 v5 更是**同一次训练**（同数据、同 `seed=202609102`、同 `init=v2`、同 lr/权重），只在 `selection` 上不同（`loss` → epoch 89，`label_regret` → epoch 112）。所以"v1..v6 共 6 个候选"实际只有约 **4 次独立尝试**（v1 scratch；v2←v1；v3←v2 换数据集；v4/v5/v6 家族←v2）。选择压力比"6 选 1"小一半。

### 1.3 关键发现 B：调参与报分的**状态分布没有隔离**

- 数据集的生成策略是 `CountStrategy()`（header `options.base` 已记录），`mixed=true` 且 v4/v6 `coverage=true`，故意覆盖合成经济状态与稀疏池。
- 所有 `set-eval-*` / `set-dev-*` / `set-final-*` 评测都是 `mixed=false` **且都从同一个 `WorldStart` 出发**（`prev_id=110, collected=[3,110], lingxi=41, eligible=70, cost=8, free=1`）。1000 局之间只有 RNG 不同。

⇒ **评测状态同时落在训练状态分布之内**（该快照本身就是数据集里 episode 的起始状态），
并且**没有任何状态级 holdout**。文档 `jiji-set-critic-experiment-20260907.md:119` 也自认：
"Eligibility pools themselves were NOT held out: this is a snapshot-pool study with synthetic thinnings, not generalization proof."
这一条是诚实披露的，但意味着 §2 的 CI 只对"这一池这一状态"有效。

### 1.4 关键发现 C：种子命名空间没有隔离（本次侥幸未撞）

`jijicritic` 与 `jijiworld` 用**同一个仿射族**生成每局种子：

```
数据集 : rng := rand.New(rand.NewSource(opt.Seed + int64(ep)*1000003))     // set_dataset.go:106
评测   : sd  := *seed + int64(episode)*1000003                             // jijiworld/main.go:172
        rng := rand.New(rand.NewSource(sd))                                // jijiworld/main.go:203
```

在 `mixed=false` + 有 snapshot 时，评测第 `i` 局的 `spec` 恒等于快照，世界 RNG 恒等于 `NewSource(base+i*1000003)`。
若 `base` 与数据种子相同，则**评测的偶数号局就是训练局的逐位重放**（数据侧 `ep%2==1` 才做合成扰动，见 `set_dataset.go:108`）。

本次审计逐条核对：数据种子 ∈ {202609081, 202609083, 202609101}，评测种子 ∈ {202609091, 097, 103, 109, 201..211}，
且 `Δbase` 必须等于 `k*1000003` 才可能撞（本处 |Δ| ≤ 28 < 1000003），**所以没有发生碰撞，泄漏没有实际发生**。
但这是"靠人没写错一个整数"，工具链不提供任何保护。建议：把用途标签并入种子（如 `seed = hash(purpose, raw)`），
或在 `jijiworld` 里拒绝 dataset 用过的种子段。

### 1.5 关键发现 D：报分基线变过三次，"提升"的口径不是同一条基线

逐份 artefact 的参考臂（由 `critic` / `reference_critic` 字段唯一确定）：

| 种子 | 文件 | 候选臂 | **参考臂** |
|---|---|---|---|
| 202609091 | `set-eval-v1-count/stop`、`set-eval-v2-count`、`set-eval-v3-count` | v1/v2/v3 | **无规划器的裸启发式**（`reference_samples=0` 且无 `reference_critic` ⇒ `changed=0`） |
| 202609091 | `set-eval-v1-all` | v1 all-action | 裸启发式 |
| 202609091 | `set-eval-v2-all-rollout128` | v2 all-action | CountStrategy + 128 抽样 rollout |
| 202609097（预留） | `set-final-v2-count` | v2 | 裸启发式 |
| 202609097（预留） | `set-final-v2-all-rollout128` | v2 all-action | CountStrategy + 128 rollout |
| 202609103（调参用） | `set-dev-v4/v5/v6-*` | v4/v5/v6 | **critic v2**（`reference_critic=set-model-v2.json`, sha `e01452fa..`） |
| 202609109（预留） | `set-final-v6-v2-direct` | v6 | **critic v2** |
| 202609109（预留） | `set-final-v6-all-rollout128` | v6 all-action | CountStrategy + 128 rollout |

我按这条映射重跑，**逐位复现**了 `set-final-v6-v2-direct.json`（§4）。这也说明：
`+1.755` 的分母是**上一个实验模型 v2**，不是发布基线。
注意 `set-eval-*` 那批文件连 `reference_critic` 字段都没有（旧工具版本），
单看文件名无法知道参考臂是什么——**口径只能从字段反推**。

---

## 2. 统计审计（重算 SE / 选择偏差）

### 2.1 ±2SE 是不是配对的？是。

- `jijiworld`：同一种子跑两条臂，`d = r[1].Turns - r[0].Turns`，`se` 用差值的样本标准差（`main.go:304-318`）；Shiyi 同理（`main.go:369-379`）。
- `TrainContinuation`：`trainingBatch` 对每一局用**同一个 `sd`** 评估所有策略（`training.go:211-217`），
  `compare()` 用逐局配对差 `objective.Value(a)-objective.Value(b)`（`training.go:172-180`）。**是配对，且 `pairedSE` 公式正确**（`rollout.go:56-62`）。

### 2.2 CI 可重算性：44/44 精确一致

用 `_research_d\se_audit.ps1` 从每份 artefact 的 `episodes` 数组重算 `1.96×pairedSE`，
与文件里 `ci95_normal_approx` / `shiyi_ci95_normal_approx` 对比：**44 份全部相符（0 处不符）**。

### 2.3 跨种子独立性：预留种子确实独立

存在两组"同配置、仅种子相差 6"的 artefact，可直接检验：

| 对比 | n | corr(逐局 Δ) | corr(基线 Turns) | 均值 Δ |
|---|---|---|---|---|
| v6/v2 direct，202609103 vs 202609109 | 1000 | **−0.0147** | **+0.0406** | 1.704 vs 1.755 |
| v2/CountStrategy direct，202609091 vs 202609097 | 500 | **+0.0553** | −0.0129 | 3.376 vs 3.333 |

n=1000 时相关系数标准误 ≈ 0.032、n=500 时 ≈ 0.045，全部落在 0 附近 1.3σ 内。
⇒ Go `math/rand` 相邻种子之间**没有可利用的相关性**，"预留种子"是真 holdout。这是本审计对流水线的一个正面确认。

### 2.4 真正的方差缺口：状态维度完全没测

- 全部 `set-*` 评测 `mixed=false`、单一快照 ⇒ **1000 "局"其实是同一个状态的 1000 次抽样**。
  报出的 ±0.275 是"同状态重复抽样"的标准误，**不含状态间方差**。
- 可以从生产缓存直接估计状态间方差（3 条真实记录，同一流程、同一默认种子 20260907、不同 live 状态）：

| 缓存指纹 | generations | validation | `预计诗意差` delta | 报告出的 ±2SE |
|---|---|---|---|---|
| `ece63aae…` | 24 | 4096 | **+104.83** | ±4.22 |
| `729c16cd…` | 24 | 4096 | **+102.01** | ±2.81 |
| `0079ce3f…` | 24 | 4096 | **+18.83** | ±1.52 |

均值 75.22，**跨状态 sd(n−1) = 48.86**，极差 85.99。
即：同一个流程报出的效应量在状态间相差 ~86 诗意，而每次附带的 ±2SE 只有 1.5~4.2，**低估约 12~32 倍**。
（这不是说 ±2SE 算错了——它算的确实是它自己那个量；问题是 `auto.go:77` 把它印成"预计诗意差 ±…（约2SE）"，
读者会把它当作该效应量的不确定性，而它只是"这一个状态下重复抽样的不确定性"。）

### 2.5 多重比较与 winner's curse 的量化

**(a) 单次 `TrainContinuation` 调用内部**：搜索流 24 代 × 24 策略 = 576 次策略评估（每代 256 局，策略间共用同批种子），
最终只把 3 个候选（base / incumbent / winner）放到**独立的 4096 局验证流**（`opt.Seed^0x6813abc910`，与搜索流 `opt.Seed^0x239417ace` 不同源）上比较，
门槛 `delta > 2.4*SE`，而 `z_{0.025/3} = 2.394` —— **这正好是 3 次比较的 Bonferroni 门槛，写法是规范的**（`training.go:189-201`）。
因为"选择在搜索流、报分在验证流"，验证流上的 delta **对该被选项是无偏的**。这条设计是对的。

**(b) 但我复现出的实际被选策略是锚点**（§0.6 / §3.3），所以 `+94.75 ± 2.04`（z=46）这个数字实质上是
"固定对比 CountStrategy(ShiyiWeight 0) vs CountStrategy(ShiyiWeight=4/16)" 在验证流上的测量，
选择代价 ≤ ~1 SE ≈ 2.0。⇒ 在**该状态、该模拟器**下，`预计诗意差 ≈ 94.75 ± 2.04` 站得住。

**(c) 整场实验层面（真正的问题）**：整个 trove 共 **58 个可重算的检验端点**（44 个句数 + 14 个诗意）。
`p<0.05` 的：句数 28 个、诗意 2 个。Bonferroni 门槛 α = 0.05/58 = **0.00086**（|z| > 3.33）。结果：

| 端点 | n | Δ | SE | z | p |
|---|---|---|---|---|---|
| `shiyi-exact-independent-pilot` 诗意 | 200 | **+20.65** | 7.49 | **2.757** | 0.0058 |
| `shiyi-action-policies-pilot` 诗意 | 200 | −11.13 | 5.45 | −2.04 | 0.041 |

**唯一为正且显著的诗意结果 z=2.757 通不过 Bonferroni**（连只看 14 个诗意端点的 α=0.0036 → |z|>2.91 也通不过）。
而 artefact 里 `shiyi-priority / shiyi-screened / shiyi-screened-tiebreak` **三份用的是同一个种子 202609201、
同一批 200 局**，互相之间的差异是配对的、但"挑最好的那个报 CI"是选择——这三个变体的诗意差分别是 +0.53 / +7.10 / +7.68。

**(d) set-critic 冠军的 winner's curse：很小，可以定量**

- 在调参种子 202609103 上比较了 v4 / v5 / v6 三个候选（有效独立候选见 §1.2）：
  Δ = **−1.777 ± 0.279 / −1.433 ± 0.292 / +1.704 ± 0.286**。候选之间相距 ~12 SE，说明差异是真实的信号而非噪声，取 max 不属于噪声驱动的 winner's curse。
- 冠军 v6 随后在**预留种子 202609109** 上重测（未用于任何选择）：**+1.755，95% CI [1.217, 2.293]**。
  dev→预留的收缩只有 **1.755 − 1.704 = 0.051 局（占效应 2.9%）**，远小于 SE(≈0.40)。
  ⇒ 对**该快照**，v6 相对 v2 的期望真实增益下界 ≈ 1.755 − 0.05 ≈ **1.70 局**（约 +5.0%），没有可测的选择偏差。
- 但这条对比**不能外推**：参考臂是模型 v2，不是产品基线。见 §2.6。

### 2.6 把三/四个基线放在同一把尺子上（全部来自同一状态、同一预留种子）

| 臂 | 状态/种子 | 平均句数 | 相对最弱者 |
|---|---|---|---|
| 裸进化启发式（无规划器） | 202609109, n=1000 | **21.486** | — |
| v2 critic 规划 | 202609109, n=1000 | **34.136** | +12.65 |
| v6 critic 规划（direct） | 202609109, n=1000 | **35.891** | +14.41（我的复现，95% CI [13.777, 15.033]） |
| CountStrategy + 128 抽样 rollout | 202609109, n=300 | **44.980** | +23.49 |

- v6 vs 裸启发式：**+14.405 ± 0.320**（z=45，我实测）
- v6 vs v2：**+1.755 ± 0.275**（z=6.4，作者报的）
- v6(all-action) vs 128 抽样 rollout：**−2.593 ± 0.597**（z=−4.34）
- v2(all-action) vs 128 抽样 rollout：**−3.893 ± 0.559**（z=−6.97，另一预留种子 202609097）

⇒ 收益几乎全部来自"**加了规划器**"，learning 部分只贡献 +1.755；而整个 learned-critic 家族**仍然打不过**已有的 rollout 规划器。
文档 `jiji-model-diagnosis-20260907.md:132-140` 也是这么写的（"not over the production planner"、"do not replace the live planner"）。
**只要把 +1.755 讲成"调参带来的提升"而不加限定，结论就会反转。**

### 2.7 两个协议对同一改动的结论相差 23σ（最严重的统计问题）

同一个改动（`CountStrategy` 的 `ShiyiWeight 0→16`），两套协议给出：

| 协议 | 出处 | n | Δ诗意 | SE | z |
|---|---|---|---|---|---|
| A. 训练内验证（`trainingBatch` + `BaselineAction`，无筛选） | `shiyi-trained-policy.json.training.json` | 2048 | **+98.66** | 2.94 | 33.5 |
| A′. 同协议（live 状态） | 我复现的 `tune_b0` / log | 4096 | **+94.75** | 2.04 | 46.5 |
| B. `jijiworld -objective shiyi-first` | `shiyi-trained-vs-screened-pilot.json`（202609204） | 200 | **+9.55** | 6.44 | 1.48 |
| B′. 同协议 | `shiyi-trained-mixed-pilot.json`（202609206） | 400 | **−2.66** | 3.62 | −0.74 |

A 与 B′ 相差 **97.4，SE_diff = 4.15，z ≈ 23.5**。两个数不可能都是同一因果量的估计。

代码层面的根因（`multi_objective.go:219-275`）：
`screenMultiContinuation` 的策略族固定为
`{opt.Base} ∪ {CountStrategy, EvolvedStrategy} × ShiyiWeight{0,4,16}`，
**两条臂各自都会在这 7 个候选里重新筛选一次**。当参考臂的 `opt.Base = CountStrategy(0)` 时，
它的族里本来就含 `CountStrategy(16)` —— 与候选臂的"训练成果"是同一个策略。
⇒ B 类评测**天然把两条臂筛选成同一个策略**，观测差只能来自筛选种子/根动作的残余差异，因此 ≈0 是结构性的，不是"训练无效"的证据。
同理，A 类数字也不能用来宣称泛化。

**结论：`shiyi-*.json` 这一族不能作为训练成果的独立验证；A 类数字也只是"单一状态下、模拟器内"的效应量。**

---

## 3. 缓存审计

### 3.1 指纹字段覆盖表

`trainingFingerprint`（`training_cache.go:42-55`）实际哈希的内容：

| 对象 | 计入指纹的字段 | 覆盖度 | 是否影响对局 |
|---|---|---|---|
| `trainingCacheVersion` | 字符串常量 `"continuation-v1"` | 手动 | —（**唯一**的代码语义守卫） |
| `WorldRules` | `DrawMode, PayOnEntry, MinCandidates, RefreshCost, MiddleClearsRun` | **5/5** | 是 |
| `Library []Verse` | `ID, Pre, Next, Poet, Group1/2/3, Lingxi, Shiyi, MinXin, ZhanLi, JunXin, FuHua, WeiWang, DevYears, StateID` | **16/16 导出字段** | 是 |
| `Rewards []PoetFirstReward` | 全部导出字段 | 全 | 是（间接覆盖全局 `PoetFirstRewards`） |
| `Pairs []uint8` | n² 判定码（含相粘位） | 全 | 是 |
| `RefreshPoetIDs`（包级 map） | 全部键值 | 全 | 是 |
| `WorldStart` | `PrevID, Collected, SeenPoets, Eligible, Lingxi, Cost, FreeLimit, FreeRemaining, OffRun, Candidates, Displayed` | **11/11** | 是 |
| `TrainingOptions` | `Objective{Priority,Count,Shiyi}, Seed, Generations, Population, Episodes, Validation, Budget, Base(Strategy)` | 全 | 是 |
| `Strategy` | 全部字段均为导出（已核对 `strategy.go:21-57`） | 全 | 是 |

**未计入指纹但需要单独论证的对象**（我逐一读了构造代码）：

| 对象 | 结论 |
|---|---|
| `World.ids / poets / poetBits` | **安全**。`NewWorld` 中是 `w.verses` / `w.rewards` 的纯函数，循环顺序确定（`world.go:86-105`）。 |
| `PoetFirstRewards` 全局 | **间接覆盖**：`w.rewards[i] = PoetFirstRewards[v.Poet]`（`world.go:104`），库内每个诗人都在 `w.rewards` 里出现，故改表即改指纹。 |
| `TrainContinuation / mutateContinuation / BaselineAction / Step / draw / Judge` 的代码语义 | **不计入**。只靠手写常量 `continuation-v1`。 |
| 实际完成的搜索量 vs 请求量 | **不计入**（见 §3.2）。 |
| Go 工具链 / `math/rand` 实现 | 不计入（`rand.NewSource` 实为稳定；本次 go1.25.6 复现 go1.26.0 产物逐位一致，已验证）。 |
| 缓存条目对应的 `WorldStart` / `TrainingOptions` 原文 | **不存**（文件里只有 sha256 key + result），人无法审计一次命中的合法性。 |

### 3.2 是否存在"假命中"？分两类回答

**(a) 状态/数据/选项的假命中：我没有找到可达路径。**
指纹把 `WorldStart` 全部 11 个字段、`WorldRules` 全部 5 个字段、整个 `Verse`/`Reward` 表、
两两判定码、刷新诗人集合、整个 `TrainingOptions`（含 `Base` 策略的全部字段）都纳入了 sha256；
nil 与空切片不会混淆（`training_cache_test.go:167-170` 有专门的回归测试，
且 `training_cache_test.go:120-166` 用反射遍历所有字段，逐个改动后断言 key 必须变——这是很强的护栏）。
JSON 序列化 map 时键有序，浮点用最短往返表示，故 key 稳定。**结论：不存在"状态不同却命中同一 key"的可达路径。**

**(b) 行为/版本维度的命中：存在，且是真实风险。**

1. **代码语义无哈希。** `trainingCacheVersion` 是手写字面量，最后一次改动与 `training.go` 在同一个 squash commit（`428f8cf`，2026-10-06），
   git 历史里只有这一个 commit，**无法核验"改语义必须 bump 版本号"这条纪律是否被遵守**。
   一旦 `TrainContinuation` / 打分函数改动而忘记 bump，旧策略会被静默复用。
2. **被时间预算截断的搜索结果可以被永久缓存（已实测）。** 论证链：
   `TrainContinuation` 把预算的 2/3 给搜索（`training.go:79-83`）；搜索被 `searchCtx` 截断时只是 `break`（`training.go:133-135`），
   随后用**还没过期**的外层 ctx 跑完整验证（`training.go:168`），于是 `err == nil`；
   而 `cacheableTraining`（`training_cache.go:57-66`）接受 `r.Generations > 0 && <= opt.Generations`，
   **只要求"完成了至少 1 代"，不要求跑满**；`readTrainingCache` 也从不拿 `r.Generations` 和 `opt.Generations` 比对。
   实测（`jijitune -adaptive -objective shiyi-first -generations 24 -population 24 -sims 256 -validation 4096`）：

   | budget | 实际完成代数 | validation | accepted | delta | se | 满足 `cacheableTraining`？ |
   |---|---|---|---|---|---|---|
   | 0（固定工作量） | **24 / 24** | 4096 | true | 94.746 | 2.040 | 是 |
   | 8s | **6 / 24** | 4096 | **true** | **95.863** | 2.038 | **是** |
   | 4s / 2s | 未产出（搜索+验证都超时） | — | — | — | — | 否 |

   ⇒ 8s 那次（只跑了 25% 的搜索）完全满足缓存写入条件，会被写进
   `%LOCALAPPDATA%\wuhui-auto\jiji-training\<key>.json` 并在之后**每一个相同指纹的调用上返回**，
   而 key 里只有 `Budget`（请求值，恒为 live 的 60s），**没有任何字段区分"跑了 6 代"和"跑了 24 代"**。
   唯一可见的痕迹是 `r.Generations` 与 `auto.go:78` 日志里的"完成N代"。
   本机 24 代固定工作量 19.07s，live 预算 60s ⇒ 有约 3 倍余量；**要触发截断需要机器慢 3 倍以上或把 Generations/Population 调大**
   （`adaptive-trained-long.json.training.json` 就是 47 代）。属"可触发但需要条件"的缺陷，不是必然发生。

3. **缓存条目不可审计。** 只存 `version / fingerprint / result / checksum`，
   不存对应的 `WorldStart`、`TrainingOptions`、`library_sha256`，也没有时间戳字段。
   事后无法回答"这条命中对应的状态是什么"。**这正是本次审计想验证却做不到的地方。**

### 3.3 生产缓存实物（一个意外收获）

本机 `%LOCALAPPDATA%\wuhui-auto\jiji-training\` 实际存在 3 条记录（只读查看），全部 `objective=shiyi-first`、`generations=24`（完整）：

| 指纹前缀 | ShiyiWeight | RhymeBonus | DryGroupFloor | delta | se |
|---|---|---|---|---|---|
| `ece63aae` | 4.647 | 309.02 | 30 | +104.83 | 2.110 |
| `729c16cd` | 0.825 | 298.58 | 2 | +102.01 | 1.404 |
| `0079ce3f` | 22.773 | 395.32 | 28 | +18.83 | 0.758 |

三个策略**都不是锚点**（锚点的 ShiyiWeight ∈ {0,4,16}），说明**在真实运行中搜索确实接受了变异子代**。
同时它们的 delta 跨越 18.8~104.8，是 §2.4 状态间方差估计的来源。
另：3 条记录对应 3 个不同指纹，说明 key 太细（含 Lingxi/Cost/Collected 等），**实际命中率极低**。

---

## 4. 复现（命令 + 结果）

工作目录 `C:\Users\ncy\Desktop\whhx\wuhui-auto`，前置：
`$env:Path = "<mingw64>\bin;" + $env:Path`（cgo）。go1.25.6 windows/amd64；产物由 go1.26.0 生成。

### 4.1 `set-reference-self-check.json`（自校验：同模型 vs 自己）

```powershell
go run ./cmd/tools/jijiworld -sims 10 -seed 202609103 `
  -critic screenshots/jiji-research-20260906/set-model-v2.json `
  -reference-critic screenshots/jiji-research-20260906/set-model-v2.json `
  -snapshot screenshots/jiji-research-20260906/runtime-main.json `
  -workers 2 -samples 0 -stop-samples 8 -z 1 -out _research_d/repro-selfcheck.json
```

**逐位一致**：`delta=0`，`baseline.mean=38.9`，`rollout.mean=38.9`，`ci95=[0,0]`；
10/10 局的 `Turns` 与 `Decisions` 两条臂全部相同（41/41, 39/39, 37/37, 37/37, 48/48, 44/44, 38/38, 43/43, 25/25, 37/37）。

### 4.2 头条数字 `set-final-v6-v2-direct.json`（n=1000）

```powershell
go run ./cmd/tools/jijiworld -sims 1000 -seed 202609109 `
  -critic screenshots/jiji-research-20260906/set-model-v6.json `
  -reference-critic screenshots/jiji-research-20260906/set-model-v2.json `
  -snapshot screenshots/jiji-research-20260906/runtime-main.json `
  -workers 8 -samples 0 -stop-samples 8 -z 1 -out _research_d/repro-final-v6-vs-v2.json
```

**逐位一致**：
`delta = 1.755`（对 1.755）、`ci95 = [1.21675820731999, 2.29324179268001]`（完全相同）、
`baseline.mean = 34.1360000000001`、`rollout.mean = 35.891`、**1000/1000 局 Turns 完全相同**。

**附带发现**：如果漏掉 `-reference-critic`（把参考臂当成裸启发式），同一个种子会得到
`delta = +14.405`，`ci95 = [13.777, 15.033]`，`baseline.mean = 21.486`（`baseline.changed = 0`，即参考臂没跑任何规划器），
而候选臂 `rollout.mean = 35.891` 一模一样。这条对照正是 §2.6 里"裸启发式 21.486"那一行的来源，
也说明**同一份命令换掉参考臂就能让"提升"从 +1.755 变成 +14.405**——引用时必须写清参考臂。

### 4.3 Python/Go parity（6/6，与文档声称完全一致）

```powershell
foreach($v in "v1".."v6"){ go run ./cmd/tools/jijicritic -mode verify `
  -model "screenshots/jiji-research-20260906/set-model-$v.json" `
  -parity "screenshots/jiji-research-20260906/set-model-$v.parity.json" }
```

```
v1 verified 8 samples, max_abs_error=4.8923069595474544e-06
v2 verified 8 samples, max_abs_error=2.244494609726644e-06
v3 verified 8 samples, max_abs_error=2.209174859046925e-06
v4 verified 8 samples, max_abs_error=6.70822866055687e-06
v5 verified 8 samples, max_abs_error=3.0443551928271972e-06
v6 verified 8 samples, max_abs_error=4.839140867574088e-06
```

与 `jiji-set-critic-experiment-20260907.md:116-117`、`jiji-model-diagnosis-20260907.md:164` 的数字**完全一致**。
`parity.json` 只含 `samples`（每条含 `tokens`/输入与两侧输出），它验证的是**同一个模型在 Go 推理与 PyTorch 推理下数值一致**，
不是"模型正确"。`set-reference-self-check.json` 验证的是**同一配置两次运行逐位一致**（delta 恒 0）。

### 4.4 训练侧端到端（第 1 个 epoch 逐位一致）

```powershell
.\.venv-jiji-critic\Scripts\python.exe scripts\jiji-set-train.py `
  --data screenshots/jiji-research-20260906/set-data-v6.jsonl.gz --out _research_d/v6_e1.json `
  --init screenshots/jiji-research-20260906/set-model-v2.json `
  --epochs 1 --advantage-weight 4 --seed 202609102 --selection label_regret --device cuda
```

对比 `set-model-v6.report.json` 的 `history[0]`：

| 字段 | artefact | 复现 |
|---|---|---|
| dev.loss | 9.90004203014576 | **9.90004203014576** |
| dev.label_regret | 0.23110699747332958 | **0.23110699747332958** |
| dev.label_best_rate | 0.7703537338573835 | **0.7703537338573835** |
| train_online.loss | 12.393589095910558 | **12.393589095910558** |
| dev 分片 episode id 列表 | — | 完全一致 |

### 4.5 其它已核验项

- `data_sha256`：`set-model-v1.report.json` → `333a8799…`、`set-model-v6.report.json` → `7df614d9…`，
  与磁盘上 `set-data-v1/v6.jsonl.gz` 的实际 sha256 **相符**。
- `library_sha256 = 8f6ea254…` 与当前 `data/inscribe/inscribe_VerseConfig.json` **相符**；
  `set-model-v2.json` 的 sha256 `e01452fa…` 与 `reference_critic_sha256` **相符**。
- 58 个端点的 SE/z/p：全部由 `_research_d\se_audit.ps1` 从逐局数据重算，结果见 `_research_d\se_recompute.csv`。

**未能复现的部分（明确列出）**：完整 120 epoch 的 torch 训练没有重跑（只重跑了 epoch 1 以验证确定性；
CUDA 下多 epoch 的收敛轨迹未验证）。这不影响本报告的任何统计结论，因为
(a) 模型文件 sha256 已核验、(b) 模型选择指标可重算、(c) 择优后的评测已逐位复现。

---

## 5. 问题分级

### A. 会影响结论（必须限定口径或补数据）

| # | 问题 | 量化 | 影响 |
|---|---|---|---|
| A1 | `+1.755` 的参考臂是**上一个实验模型 v2**，不是发布基线；而整个 learned-critic 家族**输给 128 抽样 rollout 规划器** | v6 vs v2 **+1.755 ± 0.275**；v6 vs rollout128 **−2.593 ± 0.597**（n=300）；v2 vs rollout128 **−3.893 ± 0.559** | 把它讲成"调参使策略变好"→ 结论反转。收益主要来自"加规划器"（裸启发式 21.486 → v2 34.136），不是来自 learning |
| A2 | 报出的 ±2SE **只含状态内抽样噪声**，缺状态间方差；全部 `set-*` 评测 `mixed=false` 且同一快照 | 生产缓存 3 个真实状态：+104.83 ± 4.22 / +102.01 ± 2.81 / **+18.83 ± 1.52**，跨状态 **sd = 48.86**，极差 85.99 | 引用 ±2SE 作为"预计诗意差"的不确定性 → 低估 12~32 倍 |
| A3 | 两个"独立"评测协议对同一改动（ShiyiWeight 0→16）结论矛盾，且 B 类评测在代码上被筛选族污染，**结构上无法度量该改动** | A: +94.75 ± 2.04（z=46）；B: −2.66 ± 3.62（z=−0.74）；差异 **z ≈ 23.5**。根因 `multi_objective.go:219-225` 在两条臂内都筛 `{Count,Evolved}×{0,4,16}` | `shiyi-*.json` 不能当独立验证；A 类也不能当泛化结论 |
| A4 | 整场实验 **58 个端点**，唯一为正且显著的诗意结果通不过多重比较修正 | z=2.757（p=0.0058）；Bonferroni 58 端点 α=0.00086 → \|z\|>3.33；仅诗意 14 端点 → \|z\|>2.91 | "诗意提升"作为整场结论不成立；单次内部测量成立 |
| A5 | 写进报告的自适应训练产物**逐位等于硬编码锚点**，搜索变异零贡献 | `live-shiyi-policy.json` = `CountStrategy`+`ShiyiWeight 4`（仅 1 个字段不同）；`shiyi-trained-policy.json` = +`ShiyiWeight 16`；`live-count-policy.json` 与确定性 `mid` 锚点 **7/7 字段逐位相同**（f=0.65=n68） | "进化搜索取得提升"在这几份证据上不成立。但生产缓存 3 条记录均为变异子代 → 结论应改为"这几份 artefact 不能证明搜索有贡献" |
| A6 | **被预算截断的搜索结果可被永久缓存**（实测 8s → 6/24 代，`accepted=true`、`validation=4096`、`delta=95.86>2.4SE`，满足 `cacheableTraining`） | 固定工作量 24 代 = 19.07s vs live 预算 60s（≈3× 余量） | 机器慢 3 倍或调大 gens/pop 后，一次残缺搜索会永久决定"预计诗意差"。`readTrainingCache` 从不校验 `r.Generations == opt.Generations` |
| A7 | 缓存指纹对**代码语义**只有手写常量 `continuation-v1`，无代码哈希；git 只有一个 squash commit，无法核验 bump 纪律 | — | 语义改动若忘记 bump ⇒ 静默复用旧策略 |
| A8 | 训练集 v4 与 v6 **状态集完全相同**（仅 `samples` 64→256）；v4/v5 是同一次训练的两个 checkpoint | seed 均 202609101、episodes 600、roots 20、every 3、coverage 均 true | 独立候选数从"6"降到约"4"，选择压力被高估 |

### B. 只是记录不规范（不影响结论，但建议修）

| # | 问题 | 说明 |
|---|---|---|
| B1 | `set-model-v*.report.json` 里的 `fingerprint`（全为 `bb3ea04a…`）是**编码器**指纹，不是数据指纹；6 份报告数据不同却同指纹 | 真正的数据标识是 `data_sha256`。字段名容易让人误用于去重 |
| B2 | `auto.go:77` 用 `1.96*trained.SE` 却标注"约2SE" | 数值上 1.96 vs 2.0，但标签应写"95% 正态近似" |
| B3 | `set-reference-self-check.json` / `shiyi-reference-identity.json` 的 `ci95_normal_approx = [0,0]` | 那是"两次同样运行之差"的区间，[0,0] 正确；但容易被误读成**均值**的 CI |
| B4 | 部分旧 artefact 的 CI 只印 2 位小数（如 `count-repro-worker1.json` 的 `[8.86, 19.64]`），新文件印 15 位有效数字 | 只是格式代际差异；逐局数据仍可精确重算（已验证） |
| B5 | `count-repro-worker1/4.json`（n=12）用 1.96 正态近似而非 t 分位 | n=12 时 t=2.201，区间应宽约 12%；该文件用途是 worker 数确定性检查，不作置信陈述 |
| B6 | `set-eval-*` 这一批没有 `reference_critic` 字段，参考臂口径只能反推 | 建议所有 artefact 统一写全 `reference_*` 字段 |
| B7 | 缓存条目不保存对应的 `WorldStart` / `TrainingOptions`，也不带时间戳字段 | 事后无法审计一次命中的合法性（本次审计受此限制） |
| B8 | `set-data-v2` 极小（40 局 × 8 roots × 4 samples），v3 在其上训练后回退 | 已在文档说明"pilot"，但该候选与 v1/v2 的数据规模不可比 |

---

## 6. 缺失的数据（要回答"真泛化"还差什么）

1. **多状态 holdout**：所有 `set-*` 对比都 `mixed=false` + 单一 `runtime-main.json`。工具已支持 `-mixed`
   （`count-mixed-holdout.json`、`count-adaptive-mixed-check.json` 就在用，n=5000/120），但**冠军对比从未用过**。
   需要的是一次 `set-final-v6-v2-direct` 的 `-mixed` 版本，或若干个不同 `eligible/lingxi/cost` 的快照，
   才能把 §2.4 的状态间方差从"用 3 条缓存记录间接估到 sd≈49"变成正式区间。
2. **`set-eval-*` 这一批（v1/v2/v3 的 7 次评测）的参考臂定义**：文件里没有 `reference_critic`，
   只能由 `changed=0` 推断是裸启发式。要复核 v1→v3 的口径，需要当时命令行或工具版本。
3. **缓存 key → (WorldStart, TrainingOptions) 的映射**：条目里只有 sha256。没有它，
   任何一次缓存命中都无法被第三方审计（这也是 §3.2 只能给出"没有可达的字段级假命中"而无法给出"实际未发生假命中"的原因）。
4. **`continuation-v1` 之后 `TrainContinuation` 语义是否变更过**：git 只有 `428f8cf` 一个 commit，无从比对。
   若要在事后确认历史缓存有效，需要代码哈希或版本变更日志。
5. **诗意目标的真实客户端校验**：`docs/jiji-shiyi-priority-20260907.md:34` 自认
   "does not verify actual Shiyi per settlement"、"Remaining validation: automatic per-settlement Shiyi reconciliation"。
   即：所有 +94/+99/+104 的数字都是**模拟器内**的，没有任何一局真机的逐次结算核对。
   `live-shiyi-authorized-full-20260908.log` 里只有最终 `Shiyi=545` 的**终值**，没有基线，因此不能作为增量证据。

---

## 7. 复现脚本与产物（本次审计新增，均在 `_research_d\`）

| 文件 | 内容 |
|---|---|
| `se_audit.ps1` | 从 44 份 artefact 的逐局数据重算配对 SE、z、p，做多重比较统计，核对 CI，并做锚点逐位比对 |
| `se_recompute.csv` | 上述 44 行的机器可读结果 |
| `repro-selfcheck.json` | §4.1 输出 |
| `repro-final-v6-vs-v2.json` | §4.2 输出（逐位一致） |
| `repro-final-v6.json` | §4.2 的漏参数对照（裸启发式参考臂，+14.405） |
| `v6_e1.json` / `v6_e1.report.json` | §4.4 输出（epoch 1 逐位一致） |
| `tune_b0.json.training.json` / `tune_b8s.json.training.json` | §3.2 截断实验（24/24 代 vs 6/24 代） |
