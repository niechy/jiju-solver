# 集句求解算法

本文档描述 `jiju` 包里的规则模型与求解器。所有内容以代码为准（`jiju/*.go`）。
凡标注「实测」的数字都来自离线模拟器内的配对实验，并给出 n / 种子 / 口径；凡标注「假设」或「未验证」的，就是没有被测量过的。

**范围**：本仓库是纯离线规则模拟 + 策略规划/评估库。输入是「玩家当前可集句诗句池与资源状态」的 JSON（`WorldStart`），输出是动作与期望评估。无网络、无随机之外的 IO、结果只由种子决定。

---

## 1. `World`：确定性规则模拟

`World` 持有不可变数据（诗句库、每对诗句的判定码、诗人首收奖励、规则），策略与随机性都在它之外。这是刻意的设计：`World` 不做任何决策，只回答「这个动作合法吗」「执行后状态是什么」。

### 1.1 判定（`verse.go`）

每句诗有三个属性组：

| 属性组 | 含义 |
|---|---|
| `Group1` | 主韵组（末字韵）。一句可属多个组（斜字句） |
| `Group2` | 平仄类型。李贺句用 `Group2 ≥ 101` 独立编号 |
| `Group3` | 副韵组 |

`Judge(prev, cand)` 的判定顺序是「先看 Group1，再退回 Group3，否则出韵」：

| 判定 | 条件 | 灵犀 | 诗意 |
|---|---|---|---|
| **押韵** `KindRhyme` | `prev.Group1 ∩ cand.Group1 ≠ ∅` | **+7** | — |
| **中间态** `KindMiddle` | Group1 无交集，且 `prev.Group3 ∩ cand.Group3 ≠ ∅` | 0 | 0 |
| **出韵** `KindOffRhyme` | Group1 与 Group3 都没有交集 | 0 | 0 |
| **相粘**（独立维度） | `Group2` 不同；任一方为李贺句则直接视为相粘 | **+2** | — |
| **不相粘** | 同上不成立 | — | **−5** |

注意三点，它们容易被读错：

1. **相粘与判定种类正交**。相粘在押韵/中间态/出韵三种情形下都会计算：+2 灵犀或 −5 诗意照常结算。
2. **中间态与出韵的即时收益都是 0**，区别只在「是否推进连错标记」（见 §1.3）。这也是为什么中间态的价值完全由后续影响决定。
3. `Group3` 只在 Group1 无交集时才被检查。Group1 有交集就是押韵，即使 Group3 也有交集。

诗句自带效果从 `EffectTypeList`/`EffectParamList` 提取：`32502` → 灵犀（+2 / +5），`32501` → 诗意（+10 / +20）。其余效果（民心、战斗力、军心、腐化、威望、发展年数、国家状态）被解析并保留在 `Verse` 上，但**不进入任何决策打分**。

诗人首收奖励（`PoetFirstRewards`，9 位有续航价值，其余记 0）：

| 诗人 | PoetConfig.ID | 首收奖励 |
|---|---|---|
| 温庭筠 | 16 | 免费刷新 +1 |
| 鱼玄机 | 41 | 免费刷新 +1 |
| 孟浩然 / 常建 / 贾岛 / 刘长卿 / 孟郊 | 9 / 101 / 106 / 110 / 112 | 灵犀 +10 |
| 韦应物 | 117 | 灵犀 +5 |
| 李贺 | 13 | 诗意 +30（且李贺句恒相粘） |

### 1.2 规则张量 `WorldRules`

```go
DefaultWorldRules() == WorldRules{DrawMode: DrawRound, PayOnEntry: true,
                                 MinCandidates: 1, RefreshCost: 1, MiddleClearsRun: false}
```

| 字段 | 默认 | 含义 |
|---|---|---|
| `DrawMode` | `round` | 刷新语义，见 §1.5。另有 `replace` / `exclude-current` 两个对照口径 |
| `PayOnEntry` | `true` | 进入新一轮时扣 `cost` |
| `MinCandidates` | `1` | 一次抽签至少要能抽到几句才算合法；=1 表示尾批可以只有 1~2 句 |
| `RefreshCost` | `1` | 免费次数用完后，每次刷新花 1 灵犀 |
| `MiddleClearsRun` | `false` | 中间态是否清空连错标记。默认**不清空**，见 §1.3 |

`MiddleClearsRun=false` 是默认值，也是敏感性分析开关。其规则依据是属性组配置：Group1 成功移除涨价效果、Group3 失败添加涨价效果、**Group3 成功没有移除效果** ⇒ 出韵→中间态→出韵会涨价。这一条是**按配置推断的暂定结论**，没有做逐项运行时核对，所以保留成开关而不是写死。

### 1.3 回合结算与费用

`World.Step(s, action)` 的动作空间是 `{0..n-1}`（选第 i 个候选）∪ `{3}`（`RefreshAction` = 刷新）。

选句（`selectVerse`）：

1. 若 `PayOnEntry=false` 则此时扣 `cost`；
2. 灵犀 `+= v.Lingxi`，诗意 `+= v.Shiyi`；
3. 押韵 `+7`；相粘 `+2`，否则诗意 `−5`；
4. 出韵：`OffRun++`，若 `OffRun ≥ 2` 则 **`Cost += 1`（本局永久）且 `OffRun = 0`**；押韵则 `OffRun = 0`；中间态在默认规则下**不动** `OffRun`；
5. 若该句诗人尚未见过，结算首收奖励（灵犀 / 诗意 / `FreeLimit += 1`）；
6. 该句从 `left`（未收集池）中永久移除，`Turns++`，`prev = v`，候选清空。

进入下一轮（`begin`）：

1. **终止检查**：`Lingxi < Cost` 或 `len(left) < MinCandidates` → `Done`，本局结束；
2. `Lingxi −= Cost`；
3. `Free = FreeLimit`（免费刷新**每轮重置**，诗人首收抬高的是上限，不是余额）；
4. **清空本轮屏蔽集**（`blocked`）——上一轮没被选中的句子回到池中；
5. 抽 3 句。

### 1.4 `Legal` / `Step` / `AfterState` 的分工

这三个接口合起来才完整描述一步，用途各不相同：

| 接口 | 做什么 | 不做什么 |
|---|---|---|
| `Legal(st, a)` | 判定动作当下是否合法。刷新的条件是 `(Free > 0 或 Lingxi ≥ 下限) 且 canDraw`；选句的条件是下标在 `[0, n)` 且余额够 | 不改变状态 |
| `Step(st, a, rng)` | 结算动作 + 抽下一批候选（含进入下一轮的入场费） | 不暴露未来抽签给策略 |
| `AfterState(st, a)` | 只应用**可观测的确定性效果**，返回 `(*WorldState, 新增句数, error)`。选句返回 1，刷新返回 0 | **不抽任何新候选**。刷新时只扣费并把当前候选计入屏蔽集；选句时结算并进入下一轮（扣费、重置免费额度、清空屏蔽集、做终止检查） |

`AfterState` 是 Q 分解 `Q(s,a) = 即时新增句数 + E[抽签后的价值]` 的载体：它给了「已知效果之后、随机抽签之前」的半状态。`Exact` / `ExactMulti`（§8）就架在它上面。注意 `AfterState` 只在 `DrawRound` 下支持。

`Legal` 的刷新余额下限有个细节：`PayOnEntry=true` 时刷新只需 `Lingxi ≥ RefreshCost`；否则还要多留一个 `cost`。

### 1.5 刷新语义：`DrawRound`

这是本仓库与早期复刻实现差异最大的一条规则，也是所有「精确期望」的地基：

- 刷新时，**当前这一批候选被加入本轮屏蔽集**，然后从 `left \ blocked` 中抽 `k = min(3, available)` 句；
- 已展示过的句子**在本轮内不会再出现**；
- 但新一轮开始时屏蔽集被清空，**未选中的句子下一轮回归池中**（不是永久消耗）；
- 因此单轮内的可用池是单调缩小的：`available = len(left) − blockedN − (未被屏蔽的当前候选数)`。当 `available < MinCandidates` 时刷新变成非法（`canDraw` 返回 false），这一轮只能选。
- 抽签用无放回抽样（小池走有限扫描，大池走拒绝采样 + 去重），所以一批 3 句互不相同。

这一条把刷新从「重新随机抽 3 句」（有放回近似）变成了单轮内的无放回搜索问题——§4 的次序统计量正是它的精确解。

### 1.6 输入 JSON：`WorldStart`

调用方（CLI 或上层服务）把当前局面的可观测量交进来，`World.Start` 只做合法性校验：

| 字段 | JSON | 说明 |
|---|---|---|
| `PrevID` | `prev_id` | 上一联的诗句 ID，必填 |
| `Collected` | `collected` | 已收集诗句 ID |
| `SeenPoets` | `seen_poets` | 已经收过诗的诗人 ID（用于首收奖励判定） |
| `Eligible` | `eligible` | **合格池**。`nil` = 全库；非 nil 空切片 = 无合格句 |
| `Lingxi` / `Cost` | `lingxi` / `cost` | 当前灵犀**余额**（不会补发历史首收奖励）与当前入场费 |
| `FreeLimit` / `FreeRemaining` | `free_limit` / `free_remaining` | 免费刷新上限与余额 |
| `OffRun` | `off_run` | 连错标记，只允许 0 或 1 |
| `Candidates` / `Displayed` | `candidates` / `displayed` | 非空 `candidates` 表示「玩家已经在一个付费回合内」；`displayed` 是本轮已展示过的句子，用于重建屏蔽集 |

`Eligible` 的语义是关键：**合格池 = 已解锁且尚未收集的句子**，随收集单调缩小。把 `Eligible` 留成 `nil` 会让求解器假定全库可抽，这与真实局面不符——这是历史研究中代价最大的一个建模错误，见 [failures.md](failures.md#1-假定整库都可抽)。

### 1.7 合格池是怎么算出来的（`eligibility.go`）

合格池不是调用方随手给的：诗句库里**每句诗都带着解锁条件**（需要哪些大臣到几星，或需要哪些政策之一）。`EligibleVerseIDs` 把它算出来：

```go
state := jiju.UnlockState{
    AccountMinisters: map[int]int{13: 3, 16: 2, 41: 1},  // 账号拥有的大臣 -> 星级
    CountryMinisters: map[int]int{217: 2},               // 所在国家的在任大臣 -> 星级
    ActivePolicies:   []int{2467, 2468, 2469},           // 已激活的政策 ID
}
eligible, err := jiju.EligibleVerseIDs("data/verses.json", state)
pool := jiju.RemainingPool(eligible, collected)          // 再排除已收集的
```

判定规则：

- **大臣要求必须全部满足**：账号里该大臣星级足够，**或**国家在任且星级足够——国家在任大臣可以满足一条账号里没有的要求；
- **政策要求是「任一满足」**即可；没有政策要求的诗句直接通过；
- `RemainingPool(eligible, collected)` 在可抽集合上再排除已收集的句子，得到真正能抽到的池；
- `WorldState.Remaining()` 返回当前**尚未收集**的句数（不排除本轮屏蔽的——那些本轮结束就重新可抽）。

`UnlockState` 的三个字段都必须显式给出（可以是空 map / 空切片），否则直接报错——这是刻意的，因为「池子到底是什么」正是本仓库与早期实现的主要分界（§1.6 与 [failures.md §1](failures.md#1-假定整库都可抽)）。`data/unlock-example.json` 是一份可直接用的示例快照。

它**只判解锁，不模拟政策带来的经济效果**（如入场费、初始灵犀的变化）——那部分要调用方另行提供。

---

## 2. 候选打分：一个线性价值函数

选句的分数是**特征的线性组合**，实现在 `World.actionScore`。它被两处共用：`BaselineAction` 选最优候选，以及 §4 的停止规则估计「重抽之后的期望最优分」——两条路径共用同一个函数，保证打分口径完全一致。

```
net   = v.Lingxi − st.Cost
      + 7                      // 押韵
      + 2                      // 相粘
      + pr.Lingxi              // 新诗人首收回灵犀
shiyi = v.Shiyi
      − 5                      // 不相粘
      + pr.Shiyi               // 新诗人首收加诗意

score = net   × NetGainWeight
      + shiyi × ShiyiWeight
      + RhymeBonus                            if 押韵
      + XiangZhanBonus                        if 押韵 且 相粘
      + MiddleBonus                           if 中间态
      − OffRhymePenalty                       if 出韵
      − OffRhymePenalty                       if 出韵 且 st.OffRun ≥ 1   // 第二次连错双倍罚
      + NewPoetBonus + pr.Lingxi×NetGainWeight×0.5    if 新诗人
      + RefreshPoetBonus                      if 新诗人 且 pr.FreeRefresh > 0
      + RichBonus                             if net ≥ RichThreshold
      + pool×TargetPoolWeight                 if cur ≤ threshold 且 中间态
      + TargetNewPoetWeight                   if 上式成立 且 新诗人
```

其中 `cur = st.groups[prev.Group1[0]]`（**只取 prev 的第一个主韵组**），`threshold = int(len(st.left) × SwitchThreshPct)`，`pool = max over v.Group1 of st.groups[g]`（下家**最大**组剩余，不是并集）。

### 2.1 冠军权重与逐项解读

`EvolvedStrategy()` 是默认权重（`DefaultStrategy()` 返回它），`BaselineStrategy()` 是手调基线，保留作对照：

| 字段 | 冠军 | 手调基线 | 含义与解读 |
|---|---:|---:|---|
| `NetGainWeight` | 31.84 | 50.8 | 净灵犀收益倍率。灵犀是本局唯一的续航资源 |
| `RhymeBonus` | 234.84 | 178 | 押韵加分。**进化把它推到压倒一切的量级**：押韵链断了就没有 +7 净流入 |
| `XiangZhanBonus` | 4.36 | 17 | 押韵且相粘的额外加分。注意 +2/−5 已经进入 `net`/`shiyi`，这一项只是「完美连击」溢价 |
| `NewPoetBonus` | **0** | 60 | 新诗人加分。**被进化压到 0**：真实首收奖励已计入 `net`/`shiyi`，再加一次是重复计分 |
| `RefreshPoetBonus` | 246.83 | 232 | 首收送免费刷新的诗人（温庭筠 16 / 鱼玄机 41）抢夺分。这两个人是唯一能抬高免费额度上限的来源 |
| `RichThreshold` / `RichBonus` | 0 / 20.74 | 3 / 33 | 「净收益 ≥ 阈值就加分」。阈值为 0 时这一项退化成「净收益非负就 +20.74」 |
| `SwitchThreshPct` | 0.1947 | 0.22 | 换组阈值比例：当前组剩余 ≤ `len(left)×此值` 才算枯竭 |
| `TargetPoolWeight` | 0.829 | 1.13 | 换组时下家最大组剩余 × 此值 |
| `TargetNewPoetWeight` | 8 | 8 | 换组且下家是新诗人时**一次性** +8（不乘句数） |
| `MinTargetPool` | 2 | 3 | 下家池过小线。只在旧门控路径生效，见 §2.2 |
| `MiddleBonus` | **0** | 64 | 中间态加分。**被进化彻底清零**：宁可花 1 灵犀刷找押韵，也不硬接一个净 0 的中间态 |
| `RefreshGainFloor` | 0.8752 | 0.56 | 期望收益闸。只在旧门控路径生效 |
| `LowLingxiFloor` | 5 | 12 | 保命线。只在旧门控路径生效 |
| `ShiyiWeight` | 5.09 | 4.5 | 诗意折算权重 |
| `OffRhymePenalty` | 50 | 243 | 出韵扣分。连错标记 ≥1 时再扣一次 |
| `RefreshCost` | 1 | 1 | 一次付费刷新的灵犀成本 |
| `DryGroupFloor` | 9 | 10 | 干涸线。只在旧门控路径生效 |
| `OffRhymeRespectsGate` | false | false | 最优候选是出韵时是否仍受两道预算闸约束 |
| `ExactStopRefresh` | **true** | false | 用精确一步最优停止取代三段硬门控（§4） |
| `StopMargin` | 0 → `DefaultStopMargin` = 1.0 | 0 | 停止规则要求的领先余量（打分单位） |
| `StopDepth` | 0（即一步） | 0 | ≥2 时展开两步（§5） |

### 2.2 反直觉的几项，以及「惰性字段」

进化搜索找到的几条规律与直觉相反，它们是本策略的核心机理：

1. **中间态彻底放弃（`MiddleBonus: 64 → 0`）**。中间态的即时收益是 0，唯一价值是「换组通道」和「不清连错标记」。进化发现花 1 灵犀刷找押韵的期望回报更高，于是把它清零。
2. **新诗人分是噪声（`NewPoetBonus: 60 → 0`）**。首收灵犀/诗意/免费刷新已经在 `net` / `shiyi` / `RefreshPoetBonus` 里各计一次，再加一个与奖励无关的常数分只会干扰排序。
3. **`RichThreshold = 0`** 使「高收益抢夺」退化为「净收益非负加 20.74」。这不是笔误，是搜索的解。
4. **押韵加分只在押韵时给，且 `XiangZhanBonus` 只在「押韵且相粘」时给**；但相粘的 +2/−5 在三种判定下都通过 `net`/`shiyi` 结算。也就是说相粘本身**不是**被忽略的，只是它没有独立于判定的额外溢价。
5. **换组项只在中间态触发**（`cur ≤ threshold 且 kind == KindMiddle`）。在旧版 `Library.scoreCandidate` 里这一项的条件是「不押韵且不出韵」，在 `World.actionScore` 里收窄为中间态。
6. **`cur` 只看 prev 的第一个主韵组，下家 `pool` 只看各组最大值**。这是两个已知近似：真实押韵供给是组集合的并集，这里退化成单组与最大值。历史研究把这个近似列为旧门控的四个可测缺陷之一。
7. **冠军在 `ExactStopRefresh=true` 下的惰性字段**：`DryGroupFloor`、`RefreshGainFloor`、`LowLingxiFloor`、`MinTargetPool`、`OffRhymeRespectsGate` 只出现在 `BaselineAction` 的旧门控分支里。冠军走的是精确停止分支，所以这五个字段**对冠军的决策没有任何影响**——它们仍留在结构体里，是为了让旧门控与消融实验能用同一套权重对照。看到冠军注释里提到这些数值时，注意它们描述的是历史机理，不是当前生效的路径。

另外三个预置权重也值得记住：`CountStrategy()`（只优化句数、诗意权重为 0）与 `AdaptiveStrategies(st)` 生成的状态相关策略族（当池 ≤120 且集中度 ≥0.18 或非空组 ≤8 时加入 `CountStrategy`，池在 40~180 时再加入一个 `mid` 插值策略）。它们主要作为续跑筛选的候选，不是默认值。

---

## 3. `BaselineAction`：续跑策略

`World.BaselineAction(st, s)` 是所有规划器的**续跑策略**（rollout continuation），也是默认策略本身。它的结构是：

1. 对每个候选算 `actionScore`，取最高分者（并列时取小下标）；
2. `ExactStopRefresh=true` 时调用 `stopRefresh` 决定是否刷新（§4）；否则走三段硬门控 + 两道预算闸（下）；
3. 返回刷新（`RefreshAction`）或最优候选下标；`Done` 时返回 −1。

**旧门控路径**（`ExactStopRefresh=false`，即 `BaselineStrategy()` 的行为）保留下来是为了可对照。它的三段门控是：

- 最优候选是**出韵** → 必刷（出韵会永久推高消耗，设计意图是「不惜代价规避」）；
- 最优是**中间态**且 `cur > DryGroupFloor` → 刷，找押韵保住 +7；
- 当前组跌破 `SwitchThreshPct` 且下家最大组 `pool < MinTargetPool` → 刷，找大组。

再加两道预算闸（只在要花灵犀刷时生效，且 `OffRhymeRespectsGate=false` 时出韵被豁免）：

- 闸 A：`balance < LowLingxiFloor` → 撤销刷新；
- 闸 B：`p×8 < RefreshCost + RefreshGainFloor` → 撤销刷新，其中 `p = 1 − (1 − cur/len(left))³`；
- 硬底线：还要留够本轮入场费，绝不透支。

旧门控的 `p` 是**有放回**近似，分母 `len(left)` 还包含了本轮已屏蔽的句子。这两点正是 §4 要修掉的东西。

---

## 4. 精确一步最优停止（当前默认刷新规则）

这是本仓库相对历史实现最主要的算法改进：**用同一个线性价值函数做一步最优停止，取代三段硬门控 + 两道收益闸**。权重一个都没动，只换刷新判据。

### 4.1 判据

```
R          = 本轮真正的未屏蔽池（未收集 ∩ 未被本轮屏蔽 ∩ 不在当前候选中）
k          = min(3, |R|)
S(·)       = actionScore（§2 的线性打分）
bestScore  = 当前候选里的最高分

EV         = E[ 从 R 中无放回抽 k 句，其最大值 ]        ← 精确次序统计量，不是抽样
刷新 ⟺ EV − cost(第 1 次刷新) > bestScore + StopMargin
```

`stopRefresh` 的池定义（`refreshPoolScores`）严格是 `st.left` 里排除 `st.blocked` 与 `st.cands` 的那些句子，逐句算 `actionScore`。这正是旧门控用近似概率 `p = 1 − (1 − cur/left)³` 想表达的量，这里换成精确值。池为空或刷新非法时直接返回「不刷」。

### 4.2 精确次序统计量

把 `S` 在 `R` 上排序为升序 `s₍₁₎ ≤ … ≤ s₍ᵣ₎`，`R = |R|`。从 `R` 个里无放回抽 `k` 个，**排名第 i 的句子是这一批最大值**当且仅当其余 `k−1` 句都比它小：

```
P(排名 i 者为最大) = C(i−1, k−1) / C(R, k)

E[max] = Σ_{i=k}^{R} s₍ᵢ₎ · C(i−1, k−1) / C(R, k)
```

`orderStatMax` 按 `k = 1 / 2 / 3` 分别展开为闭式（`k=3` 时权重是 `C(i−1,2)/C(R,3)`），复杂度 O(R)，不需要任何采样。这与「用蒙特卡洛估计刷新后的期望」是同一目标的两个成本量级。`TestOrderStatMaxMatchesBruteForce` 用暴力枚举校验了这个公式。

### 4.3 成本换算、余量与两次刷新的记账

1 灵犀按 `NetGainWeight` 换算成分数单位，与 `actionScore` 的量纲一致：

```
refreshScoreCost(st, s, nth) = 0                                  if st.Free > nth
                             = RefreshCost × NetGainWeight        otherwise
```

免费刷新次数是顺序消耗的，所以 `nth=0` 表示「紧接着这次刷新」用掉第 1 次免费额度，`nth=1` 是它之后的下一次。判据里外层减 `cost(0)`。

`StopMargin` 是要求的领先余量：`s.StopMargin == 0` 时取 `DefaultStopMargin = 1.0`（由留出种子评测确定）。余量存在的原因是打分是启发式的：期望只比眼前高一点点时，不值得承担模型误差。

### 4.4 实测

同一份冠军权重、同一快照（合格池 70 句、已集 `{3,110}`、灵犀 41、入场费 8、免费 1、`prev=110`）、目标 `shiyi-first`、两侧均为 128 根采样 / 32 续跑筛选采样、成对整局、种子 42、n=200。唯一改动是 `ExactStopRefresh: false → true`（已核对该 A/B 的两条臂其余字段逐位相同）：

| 指标 | 三段硬门控 | 精确一步最优停止 | 配对差 / 约 95% 区间 | t |
|---|---:|---:|---|---:|
| 诗意 | 473.675 | 528.400 | **+54.725 [38.561, 70.889]** | +6.64 |
| 句数 | 43.330 | 49.155 | **+5.825 [4.179, 7.471]** | +6.94 |
| 平均付费刷新 | 28.775 | 28.170 | 略降 | — |

**独立留出集复刻**（两个从未用于训练/调参的种子，线性权重一个都没动、未做 60 秒调参）：种子 202609401（n=400）**+59.95 ± 11.86 诗意 / +6.30 句数**（t=+10.1）；种子 202609402（n=200）**+49.83 ± 16.52 诗意 / +5.28 句数**（t=+6.0）；合并 **n=600：+56.58 ± 9.64 诗意 / +5.96 句数，t=+11.7**。三条独立测量的量级一致。

一个重要的负向对照：**单独把刷新换成最优停止、不套 rollout，反而远不如原策略**（−143.25 ± 19.25 与 −87.17 ± 21.09 诗意）。规则改进必须嵌在「rollout + 续跑筛选」这套搜索结构里才生效——增益来自决策规则，但载体是搜索结构。

---

## 5. 两步最优停止（`StopDepth=2`，已实现，**非默认**）

`StopDepth ≥ 2` 时，刷新判据再多展开一步：刷完之后手里还握着「再刷一次」的期权。

### 5.1 数学

记 `maxS` 为「这一批 k 句的最高分」，它的期望就是一步值 `Emax(P)`。设

```
K  = Emax(P) − cost(第 2 次刷新)        // 刷完之后再刷一次所能拿到的分数
V₂ = E[ max(maxS, K) ]
```

因为 `E[maxS] = Emax(P)`，可以把 `max` 拆成一个恒等式：

```
V₂ = E[maxS] + E[ (K − maxS)⁺ ]
   = Emax(P) + E[ (K − maxS)⁺ ]
```

即 **两步值 = 一步值 + 一个非负的期权价值**，是严格推广（`optionValue` 用与 `orderStatMax` 完全相同的次序统计量权重算 `E[(K − maxS)⁺]`，仍是 O(R)）。判据变成 `V₂ − cost(0) > bestScore + StopMargin`。

### 5.2 已知近似

唯一的近似在 `K` 里的池：**再刷时的池被当成 `P` 本身，而它实际是 `P` 去掉刚展示的 k 句**。这会对 `R` 造成小扰动，但方向是**高估**期权价值（实际可抽的池更小 ⇒ 期望最大值更低），池越小时越粗。这是 `StopDepth=2` 没有被设为默认的直接原因之一。

### 5.3 实测

参考臂 `StopDepth=0`（一步），候选臂 `StopDepth=2`，其余完全相同（同一份冠军权重、128 根采样 / 32 续跑筛选、`shiyi-first`、成对整局）：

| 种子 | n | 诗意（一步 → 两步） | 配对差 | 2SE | t | 句数差 | 付费刷新 |
|---|---:|---|---:|---:|---:|---:|---|
| 42 | 200 | 533.100 → 550.525 | +17.425 | 16.810 | +2.07 | +1.710 | 28.460 → 35.040（**+23.1%**） |
| 202609401 | 200 | 550.350 → 558.525 | +8.175 | 15.890 | +1.03 | +0.695 | 29.900 → 34.975（**+17.0%**） |
| 两种子合并 | 400 | — | **+12.80** | 11.56 | **+2.21** | +1.202 | 约 +20% |

（合并口径为按逐局配对差池化重算；按两个种子效应量做反方差加权得到 **+12.54，t ≈ 2.17**，与池化口径同向。）

**诚实结论**：方向一致（两个种子都是正的），但 **t ≈ 2.17 只有两个种子，不足以称为已确立的改进**。而且它把付费刷新抬高了约 20%，其中一部分来自 §5.2 那个已知近似——期权被高估，于是策略更爱刷。因此代码里保留 `StopDepth`、默认置 0、把权重注释写清「实测 +54.7 的那一版 = `StopDepth` 默认」，不默认开启。

---

## 6. `MultiRollout`：用续跑策略做蒙特卡洛动作评估

`World.MultiRollout(ctx, st, opt, objective)` 是本仓库的主规划器（`planLive` 就是它 + `shiyi-first`）。它回答的问题是：**在这个状态、这个目标下，哪个合法根动作最好**。

做法：

1. **残局精确解**：若 `len(left) ≤ opt.ExactLimit`（默认 6）且节点预算够，直接调 `ExactMulti`（§8）。只有 `Complete` 的结果才会被采用，不完整搜索不暴露任何部分推荐。
2. **续跑策略筛选**（`screenMultiContinuation`，`PolicySamples>0` 时）：在一个小策略族上做独立配对筛选——`{opt.Base} ∪ {CountStrategy, EvolvedStrategy} × ShiyiWeight{0,4,16}`——用**独立的随机数流**（`opt.Seed ^ 0x732916b`），并且**每条完整轨迹固定使用同一种策略**：绝不按每条随机未来事后挑最好的策略。只有超过 `ConfidenceZ × SE` 才替换基准续跑策略。
3. **根动作配对评估**：对每个合法动作，用**同一个局面种子**把这一局走到底（先走该根动作，之后由续跑策略接管），得到 `VerseReturn{新增句数, 新增诗意}`。配对差分、配对 SE、以及相对所选动作的 `Comparisons` 都由同一批逐局数据算出。
4. **两阶段采样**：先跑 `Samples` 批；只有在 `closeRootActions` 判定「最佳动作与某个竞争动作的差 ≤1.96×SE」时才继续跑到 `MaxSamples`。只有**完整的配对批次**才会被计入。
5. **目标与并列**：`VerseObjective` 支持 `shiyi-first` / `count-first` 字典序（并列时用次要分量 `betterTie` 比较）或显式加权。`markMultiDominance` 标注均值被支配的动作，作为诊断（不是选择依据）。

几个刻意的设计约束，写在这里以免被误读：

- `Samples` / `DeltaSE` / `Comparisons` 的注释明确写着这是**诊断性的配对误差，不是覆盖率保证**：自适应采样 + 事后选择动作，使得固定的置信覆盖说法不成立。
- `RolloutOptions.ActionPolicies`（为每个根动作单独精炼续跑策略）与 `TerminalPolicy`（终局最后一步按目标修选）都是**实验开关，默认关闭**，且 `planLive` 会拒绝它们。见 [failures.md](failures.md#7-动作条件策略筛选--终局修复--两步搜索)。
- 默认参数：`Samples=4096, Seed=42, ConfidenceZ=1.96, PolicySamples=128, ExactLimit=6, ExactNodes=100000`；线上口径再叠 `MaxSamples=8192` 与 `Budget=1500ms/决策`。

---

## 7. `TrainContinuation`：权重向量的短程进化搜索

`World.TrainContinuation(ctx, spec, opt)` 在给定合格池与经济状态上进化搜索续跑权重，并**只在独立验证流上通过才采纳**。

- **锚点**：`opt.Base` + `AdaptiveStrategies(root)`；当目标是诗意相关时再补 `{opt.Base, CountStrategy, EvolvedStrategy} × ShiyiWeight{4,16}`。锚点数被截到 `Population−2`，留两个变异槽位。
- **变异**：`mutateContinuation` 用高斯扰动缩放 `0.22/(1+gen/5)`，逐个改动 `RhymeBonus / RefreshPoetBonus / TargetPoolWeight / MiddleBonus / OffRhymePenalty / RefreshGainFloor / SwitchThreshPct / DryGroupFloor / MinTargetPool / LowLingxiFloor`，并把 `ShiyiWeight` 置 0（诗意权重由外层按需单独变异，避免「顺带变了一点诗意权重」这种不可归因的改动）。
- **精英**：每代取目标最优的前 `max(2, Population/4)` 作为下一代父本。
- **随机数流分离**：变异用 `opt.Seed`，训练局用 `opt.Seed ^ 0x239417ace`，验证局用 `opt.Seed ^ 0x6813abc910`。搜索与验证不共用流。
- **采纳门槛**：验证流上做三次固定比较（anchor/base、winner/base、winner/anchor），只有 `delta > 2.4 × SE` 才采纳。2.4 对应 3 次比较的 Bonferroni 门槛（`z_{0.025/3} ≈ 2.394`），写法是规范的。
- **时间预算**：`Budget` 的 **2/3 给搜索**，剩下给验证。默认 `Seed=20260907, Generations=24, Population=24, Episodes=256, Validation=4096, Budget=30s`；线上 `liveTrainingOptions()` 用 60s。
- 超时/取消时 `err != nil`，结果被显式回退为 `opt.Base` 且 `Accepted=false`。

**已知的诚实缺口**：搜索被 `searchCtx` 截断时循环只是 `break`，随后外层（还没过期的）`ctx` 仍会跑完整验证，于是 `err == nil`、`out.Generations < opt.Generations`、`Accepted` 依然可能为 true。也就是说一次**残缺的搜索**可以产出被标记为「已验证通过」的策略。本包不含缓存层，所以不会像历史流水线那样把它**永久固化**；但任何在此之上加缓存/落盘的调用方，必须校验 `Generations == 请求代数`。详见 [failures.md](failures.md#9-被预算截断的搜索结果被永久缓存)。

---

## 8. `ExactMulti`：小残局的精确期望解

`World.ExactMulti(ctx, st, limit, maxNodes, objective)` 在 `DrawRound` 下**穷举所有等概率候选集**并记忆化求解精确期望：

- 状态键 = 排序后的 `left` + 每个句子的 `blocked` 位 + `prev` + `Lingxi` + `Cost` + `Free` + `FreeLimit` + `OffRun` + 候选数 + 候选集合 + 已见诗人掩码；
- `draw` 枚举 `C(pool, 3)` 个无序组合（等概率），对每个组合递归求值后取平均；
- 返回向量回报 `VerseReturn{句数, 诗意}`，按请求的字典序目标比较；`Values[]` 给出每个根动作的精确 Q（后续走最优）；
- 期望用 float64，不是有理数运算；
- **不完整的搜索不暴露任何部分推荐**：超时、节点超限、`len(left) > limit`、目标非法时一律返回 `Action = -1`。

`World.Exact` 是它的标量（句数）前身，逻辑相同、返回 float64。

### 8.1 可解规模的实测上限

在缩小池上实测（`lingxi=41, cost=8, free=1, prev` 不在池内，串行执行）：

| 池 | 完成 | 节点数 | 墙钟 | V\*（诗意） | V\*（句） |
|---:|---|---:|---:|---:|---:|
| 4 | 是 | 37 | 0.3 ms | 30.0 | 4.0 |
| 5 | 是 | 202 | 0.5 ms | 40.0 | 5.0 |
| 6 | 是 | **892** | **8.1 ms** | 45.0 | 6.0 |
| 7 | 是 | **6,631** | **66.9 ms** | 60.0 | 7.0 |
| 8 | 是 | **54,099** | **734.7 ms** | 105.0 | 8.0 |
| 9 | 是 | **318,637** | **6.90 s** | 115.0 | 9.0 |
| 10 | 是 | **1,781,446** | **82.0 s** | 125.0 | 10.0 |
| 11 | 否（>600 s） | — | >600 s | — | — |
| 13 | 否（300 s 超时） | 7,723,009 | >300 s | — | — |

每加一句池，节点数约 ×5.6~8.1。**增长由 `C(P,3)` 的候选组合数主导，不是被可见回合数主导**：把灵犀压到 9（几乎只能走一回合）时池 10 只要 175 节点，但池 16 + 灵犀 9 仍在 120 s 内跑不完（107 万节点）。

**结论：池 6（`ExactLimit` 默认值，892 节点 / 8 ms）是安全阈值。** 抬到 8（5.4 万节点 / 0.73 s）仍在 1.5 s/决策的预算内；抬到 9 需要 ≥7 s/决策，不划算。

**精确在线求解在真实池规模上不可行。** 真实局面里，标准评估态剩 68 句、残局快照剩 20 句、全库最多 572 句——按 ×6/句外推，池 68 相对池 10 还要再乘约 6⁵⁸。所以 `ExactMulti` 的定位是**残局求解器与建模校验工具**，不是通用规划器；日常规划靠在线的 `MultiRollout`。一个可行的工程做法是按 `(池, 灵犀)` 二维门槛触发精确解，而不是只按池大小。

---

## 9. 口径与复现

- **指标**：`WorldState` 累计 `Turns`（本局新增句数）、`Shiyi`（本局诗意增量）、`Refreshes` / `PaidRefreshes`（总刷新 / 付费刷新）、`OffRhymes`、`Cost`（结束时入场费）。所有「增量」都不重发历史累计值。
- **配对**：同一初始状态、同一局面种子跑两条臂，逐局做差后算均值与配对 SE（`pairedSE`）。这消掉了状态内的抽样噪声，但**不含状态间方差**——引用任何 ±2SE 时必须同时说明它只覆盖一个状态。这一点在 [failures.md](failures.md#10-方法论警告) 里有量化。
- **种子**：Go `math/rand` 相邻种子之间没有可利用的相关性（实测 corr ≈ −0.01 / +0.06），所以「留出种子」是真 holdout。有 `Budget` 的并发测试受 CPU 竞争影响，不保证跨机器逐动作复现；去掉 `Budget` 即为固定种子的确定性实验。
- **可核验性**：`TestWorldBaselineParity` 逐决策比对新实现与旧门控，`TestWorldEntryAndZeroBalanceSelection`、`TestWorldRoundExclusionAndReset`、`TestWorldDrawModesAndHistory`、`TestWorldFirstRewardsNotReissued`、`TestWorldMiddleAndDualRhyme`、`TestExactStopRefreshTakesEffect`、`TestOrderStatMaxMatchesBruteForce`、`TestRolloutDeterministicCancelledAndImmutable`、`TestExactMulti*` 覆盖了上面每一条规则主张。
- **未验证的假设**（不要当成已证）：刷新语义 `DrawRound` 是按规则配置推断并显式建模的；`MiddleClearsRun=false` 同上；候选均匀抽样是模型假设，不是对真实抽签分布的实测；所有数字都在规则模型内，不是真实客户端表现。
