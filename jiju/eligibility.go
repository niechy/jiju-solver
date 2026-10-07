package jiju

import (
	"encoding/json"
	"fmt"
	"os"
)

// UnlockState 是玩家账号的显式快照: 已拥有的账号大臣与各自星级、所在国家的在任
// 大臣与星级、当前已激活的政策。
//
// 为什么必须显式传入: 诗句库里每句诗都带着解锁条件(需要哪些大臣到几星、或需要
// 哪些政策之一)。**只有已解锁且尚未收集的诗句才会出现在抽取池里**, 所以池子不是
// 整本词库, 而是随账号进度变化的子集。若省略这一步直接用整库模拟, 韵组分布、
// 押韵命中率、以及"池子何时枯竭"都会失真 —— 这是本仓库与早期实现的主要区别。
//
// 国家在任大臣可以满足一条"账号里没有"的大臣要求。
type UnlockState struct {
	AccountMinisters map[int]int `json:"account_ministers"`
	CountryMinisters map[int]int `json:"country_ministers"`
	ActivePolicies   []int       `json:"active_policies"`
}

type verseUnlock struct {
	ID        int   `json:"ID"`
	Ministers []int `json:"UnlockNeedMinister"`
	Stars     []int `json:"UnlockNeedMinisterStar"`
	Policies  []int `json:"UnlockNeedPolicyId"`
}

// EligibleVerseIDs 按解锁条件算出"当前可抽"的诗句 ID。
//
// 规则: 大臣要求必须**全部**满足(账号星级足够, 或国家在任且星级足够);
// 政策要求是"任一满足"即可, 没有政策要求则视为通过。
// 它只判解锁, 不模拟政策带来的经济效果 —— 那部分需要另行提供。
func EligibleVerseIDs(path string, state UnlockState) ([]int, error) {
	if state.AccountMinisters == nil || state.CountryMinisters == nil || state.ActivePolicies == nil {
		return nil, fmt.Errorf("解锁快照的三个字段都必须显式给出(可以是空的 map / 空切片)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []verseUnlock
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	active := make(map[int]bool, len(state.ActivePolicies))
	for _, id := range state.ActivePolicies {
		active[id] = true
	}
	ids := []int{}
	for _, v := range entries {
		if len(v.Ministers) == 0 && len(v.Policies) == 0 {
			ids = append(ids, v.ID)
			continue
		}
		if len(v.Ministers) != len(v.Stars) {
			return nil, fmt.Errorf("诗句 %d 的大臣/星级要求数量不一致", v.ID)
		}
		unlocked := true
		for i, id := range v.Ministers {
			account, hasAccount := state.AccountMinisters[id]
			country, hasCountry := state.CountryMinisters[id]
			if !(hasAccount && account >= v.Stars[i] || hasCountry && country >= v.Stars[i]) {
				unlocked = false
				break
			}
		}
		policy := len(v.Policies) == 0
		for _, id := range v.Policies {
			policy = policy || active[id]
		}
		if unlocked && policy {
			ids = append(ids, v.ID)
		}
	}
	return ids, nil
}

// RemainingPool 在可抽集合的基础上再排除已收集的诗句, 得到真正能抽到的池子。
func RemainingPool(eligible []int, collected []int) []int {
	has := make(map[int]bool, len(collected))
	for _, id := range collected {
		has[id] = true
	}
	out := make([]int, 0, len(eligible))
	for _, id := range eligible {
		if !has[id] {
			out = append(out, id)
		}
	}
	return out
}
