// Package jiju 实现"集句"(唐诗集句, InscribePoem)自动化。
//
// 判定规则(数据驱动, 配置来自 inscribe_VerseConfig.json + inscribe_VerseGroupConfig.json):
//   - 押韵(Group1): 上一联与候选联的主韵组(末字韵)有交集 → 灵犀+7
//   - 中间态: 主韵组无交集, 但副韵组(Group3)有交集 → 无奖惩
//   - 出韵(Group3): 主韵组无交集 且 副韵组也无交集 → 集句消耗提升(惩罚, 连续2次永久+1)
//   - 相粘(Group2): 平仄类型不同 → 灵犀+2; 相同 → 诗意-5
// 决策优先级: 押韵 > 中间态 > 出韵(避免惩罚); 全出韵时优先用免费刷新/花灵犀刷新。
package jiju

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// richtextRe 匹配词库里的富文本标签如 <size=30>　</size>。
var richtextRe = regexp.MustCompile(`<[^>]*>`)

// stripRichtext 去掉词库文本里的富文本标签与全角空格, 得到纯诗句。
func stripRichtext(s string) string {
	s = richtextRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "　", "")
	return strings.TrimSpace(s)
}

// verseRaw 对应 inscribe_VerseConfig.json 单条(仅字段足够用)。
type verseRaw struct {
	ID            int      `json:"ID"`
	PreSentense   string   `json:"PreSentense"`   // 出句
	NextSentense  string   `json:"NextSentense"`  // 对句
	BelongPoet    []int    `json:"BelongPoet"`
	EffectTypeList  []int  `json:"EffectTypeList"`
	EffectParamList []float64 `json:"EffectParamList"`
	Group1        []int    `json:"Group1"` // 主韵组(末字韵)
	Group2        []int    `json:"Group2"` // 平仄类型
	Group3        []int    `json:"Group3"` // 副韵组
}

// Verse 一条诗句数据(整理后)。
type Verse struct {
	ID     int
	Pre    string // 出句
	Next   string // 对句
	Poet   int    // 首个诗人
	Group1 []int
	Group2 []int
	Group3 []int

	// 效果(从 EffectTypeList+EffectParamList 提取, 供策略打分)
	Lingxi int // 32502 灵犀增减(如 +2/+5)
	Shiyi  int // 32501 诗意增减(普通+10/稀有+20)

	// 诗句携带的其他效果(对"集句续航"目标无直接影响, 但对战力/资源目标有意义;
	// 效果类型表: 51民心 204战斗力 209军心 351腐化 553威望 757发展年数 881国家状态)
	MinXin    int // 51 民心
	ZhanLi    int // 204 战斗力
	JunXin    int // 209 军心
	FuHua     int // 351 腐化(负为好)
	WeiWang   int // 553 威望
	DevYears  int // 757 发展年数
	StateID   int // 881 国家状态变更(出韵惩罚/温庭筠特供句 21526~21529)
}

// PoetFirstReward 诗人首次收集的真实奖励(来自 PolicyConfig 218xx, 非 GitHub 统一 lingxi)。
// 按"集句续航"价值分类:
//   - FreeRefresh: 免费更换次数+N (温庭筠21815/鱼玄机21836) —— 战略价值最高
//   - Lingxi:      首收回灵犀 (孟浩然/常建/贾岛/刘长卿/孟郊 ×10, 韦应物 ×5)
//   - Shiyi:       首收加诗意 (李贺21812 +30, 且李贺句恒相粘)
//   其余诗人首收不给灵犀/刷新, 对续航无直接帮助(战力/科技/乐感/好感等见 PoetSide)。
type PoetFirstReward struct {
	FreeRefresh int // 免费更换次数+N
	Lingxi      int // 灵犀+N
	Shiyi       int // 诗意+N
}

// PoetFirstRewards 41 位诗人首收奖励表(以游戏内效果说明为准)。
// 键 = PoetConfig.ID(与诗句库 BelongPoet 一致)。
// 注意: PolicyConfig 218xx 的顺序≠PoetConfig.ID(21800 李白=ID1, 21801 杜甫=ID2...),
//   已按真实 PoetConfig.ID 重新映射。未列出 = 首收不回灵犀/刷新/诗意(对续航记 0)。
var PoetFirstRewards = map[int]PoetFirstReward{
	16:  {FreeRefresh: 1}, // 温庭筠 21815 免费更换+1
	41:  {FreeRefresh: 1}, // 鱼玄机 21836 免费更换+1
	9:   {Lingxi: 10},     // 孟浩然 21808 灵犀×10
	101: {Lingxi: 10},     // 常建 21816 灵犀×10 (军心-5)
	106: {Lingxi: 10},     // 贾岛 21821 灵犀×10 (民心-10)
	110: {Lingxi: 10},     // 刘长卿 21825 灵犀×10 (腐化+10)
	112: {Lingxi: 10},     // 孟郊 21827 灵犀×10 (民心-10)
	117: {Lingxi: 5},      // 韦应物 21832 灵犀×5 (民心+5)
	13:  {Shiyi: 30},      // 李贺 21812 诗意+30, 李贺句恒相粘
}

// PoetNameToID 诗人姓名 → 诗人 ID 映射(41 位, 与游戏内诗人表一致)。
// 用于由已解锁诗人名初始化 GameState.SeenPoets。
var PoetNameToID = map[string]int{
	"李白": 1, "杜甫": 2, "王维": 3, "白居易": 4, "李商隐": 5, "韩愈": 6,
	"杜牧": 7, "刘禹锡": 8, "孟浩然": 9, "王勃": 10, "王昌龄": 11, "柳宗元": 12,
	"李贺": 13, "岑参": 14, "元稹": 15, "温庭筠": 16, "上官婉儿": 35, "鱼玄机": 41,
	"常建": 101, "陈子昂": 102, "崔颢": 103, "贺知章": 104, "胡令能": 105,
	"贾岛": 106, "李峤": 107, "李绅": 108, "李益": 109, "刘长卿": 110,
	"骆宾王": 111, "孟郊": 112, "王翰": 113, "王绩": 114, "王湾": 115,
	"王之涣": 116, "韦应物": 117, "许浑": 118, "张继": 119, "张志和": 120,
	"高适": 121, "张若虚": 122, "张九龄": 123,
}

// Library 集句词库。
type Library struct {
	verses []Verse
	byText map[string]*Verse // 出句/对句 → Verse 反查(完整匹配)
}

// LoadLibrary 加载集句词库。
func LoadLibrary(path string) (*Library, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取词库失败: %w", err)
	}
	var raws []verseRaw
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("解析词库失败: %w", err)
	}
	lib := &Library{
		verses: make([]Verse, 0, len(raws)),
		byText: make(map[string]*Verse, len(raws)*2),
	}
	for _, r := range raws {
		pre := stripRichtext(r.PreSentense)
		next := stripRichtext(r.NextSentense)
		v := Verse{
			ID:      r.ID,
			Pre:     pre,
			Next:    next,
			Group1:  r.Group1,
			Group2:  r.Group2,
			Group3:  r.Group3,
		}
		if len(r.BelongPoet) > 0 {
			v.Poet = r.BelongPoet[0]
		}
		// 提取效果: 32501 诗意 / 32502 灵犀 / 51民心 / 204战斗力 / 209军心 /
		// 351腐化 / 553威望 / 757发展年数 / 881国家状态
		for i, t := range r.EffectTypeList {
			if i >= len(r.EffectParamList) {
				break
			}
			p := int(r.EffectParamList[i])
			switch t {
			case 32501:
				v.Shiyi = p
			case 32502:
				v.Lingxi = p
			case 51:
				v.MinXin = p
			case 204:
				v.ZhanLi = p
			case 209:
				v.JunXin = p
			case 351:
				v.FuHua = p
			case 553:
				v.WeiWang = p
			case 757:
				v.DevYears = p
			case 881:
				v.StateID = p
			}
		}
		lib.verses = append(lib.verses, v)
		vv := &lib.verses[len(lib.verses)-1]
		// 出句/对句完整文本反查(集句界面显示整联, 可能合成"出句对句")
		lib.byText[pre] = vv
		lib.byText[next] = vv
		lib.byText[pre+next] = vv
	}
	return lib, nil
}

// Verses 返回全部诗句(测试/调试用)。
func (l *Library) Verses() []Verse { return l.verses }

// FindByText 按诗句文本反查 Verse。
// 匹配顺序:
//  1. 精确匹配(整联/出句/对句)
//  2. 子串匹配(整联可能连成一行)
//  3. 编辑距离模糊匹配(容忍错 1~2 字, 取距离最小且不超过阈值的)
func (l *Library) FindByText(text string) *Verse {
	clean := cleanText(text)
	if clean == "" {
		return nil
	}
	if v, ok := l.byText[clean]; ok {
		return v
	}
	// 子串: 整联可能连成一行且中间无标点
	for i := range l.verses {
		v := &l.verses[i]
		if contains(clean, v.Pre) && contains(clean, v.Next) ||
			contains(v.Pre+v.Next, clean) {
			return v
		}
	}
	// 编辑距离: 容忍错字/多字/少字
	bestDist := int(^uint(0) >> 1)
	var best *Verse
	for i := range l.verses {
		v := &l.verses[i]
		full := v.Pre + v.Next
		d := levenshtein(clean, full)
		// 阈值: 整联长度的 1/5(10字联容忍2字), 至少容忍 1 字
		threshold := len([]rune(full)) / 5
		if threshold < 1 {
			threshold = 1
		}
		if d <= threshold && d < bestDist {
			bestDist = d
			best = &l.verses[i]
		}
	}
	return best
}

// levenshtein 两字符串的编辑距离(按 rune 计)。
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// 滚动数组
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// JudgeKind 判定结果分类。
type JudgeKind int

const (
	KindRhyme     JudgeKind = iota // 押韵(主韵组交集) 灵犀+7
	KindMiddle                     // 中间态(副韵组交集, 无奖惩)
	KindOffRhyme                   // 出韵(主/副韵组都无交集, 惩罚)
)

func (k JudgeKind) String() string {
	switch k {
	case KindRhyme:
		return "押韵"
	case KindMiddle:
		return "中间态"
	case KindOffRhyme:
		return "出韵"
	}
	return "?"
}

// Judge 判定候选联 prev(上一联)与 cand(候选联)的关系。
// 返回 (判定分类, 是否相粘, 描述)。
func Judge(prev, cand *Verse) (JudgeKind, bool, string) {
	// 押韵: 主韵组 Group1 有交集
	g1Inter := intersect(prev.Group1, cand.Group1)
	if len(g1Inter) > 0 {
		return KindRhyme, xiangZhan(prev, cand),
			fmt.Sprintf("押韵(主韵%v∩%v=%v)", prev.Group1, cand.Group1, g1Inter)
	}
	// 中间态: 副韵组 Group3 有交集
	g3Inter := intersect(prev.Group3, cand.Group3)
	if len(g3Inter) > 0 {
		return KindMiddle, xiangZhan(prev, cand),
			fmt.Sprintf("中间态(副韵%v∩%v=%v)", prev.Group3, cand.Group3, g3Inter)
	}
	// 出韵
	return KindOffRhyme, xiangZhan(prev, cand),
		fmt.Sprintf("出韵(主%v副%v均无交集)", prev.Group1, cand.Group1)
}

// intersect 返回两 slice 的交集元素。
func intersect(a, b []int) []int {
	mb := map[int]bool{}
	for _, x := range b {
		mb[x] = true
	}
	var out []int
	for _, x := range a {
		if mb[x] {
			out = append(out, x)
		}
	}
	return out
}

// xiangZhan 相粘判定: 平仄类型(Group2)不同 = 相粘(成功)。
// 李贺句 Group2=101~130 独立编号, 官方效果始终相粘 → 直接视为相粘。
func xiangZhan(prev, cand *Verse) bool {
	// 李贺特判: 任一为李贺(G2>=101) → 相粘
	if hasLiHe(prev) || hasLiHe(cand) {
		return true
	}
	// 类型不同 → 相粘
	return !intSliceEqual(prev.Group2, cand.Group2)
}

func hasLiHe(v *Verse) bool {
	for _, g := range v.Group2 {
		if g >= 101 {
			return true
		}
	}
	return false
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Choose 从候选中选最优: 按 (押韵>中间态>出韵, 相粘优先, 出韵中选牺牲最小)。
// 返回 (选中索引, 判定 kind, 是否相粘, 描述)。若无可用候选返回 -1。
func (l *Library) Choose(prev *Verse, cands []*Verse) (int, JudgeKind, bool, string) {
	type scored struct {
		idx  int
		kind JudgeKind
		xi   bool
		desc string
	}
	var list []scored
	for i, c := range cands {
		if c == nil {
			continue
		}
		k, xi, desc := Judge(prev, c)
		list = append(list, scored{idx: i, kind: k, xi: xi, desc: desc})
	}
	if len(list) == 0 {
		return -1, KindOffRhyme, false, "无可用候选"
	}
	// 排序: 优先级 押韵(0) > 中间(1) > 出韵(2); 同 kind 相粘优先;
	// 同为出韵时, 后面可再按效果价值优化(暂不接入)。
	best := list[0]
	for _, s := range list[1:] {
		if betterScored(s, best) {
			best = s
		}
	}
	return best.idx, best.kind, best.xi, best.desc
}

func betterScored(a, b struct {
	idx  int
	kind JudgeKind
	xi   bool
	desc string
}) bool {
	if a.kind != b.kind {
		return a.kind < b.kind
	}
	if a.xi != b.xi {
		return a.xi
	}
	return false
}