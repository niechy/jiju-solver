// Package cliutil 放各命令行工具共用的小工具: 参数解析、策略权重读写、池子构造。
package cliutil

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"

	"jiju-solver/jiju"
)

// Must 在出错时直接终止并打印原因 —— 命令行工具里足够。
func Must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

// DecodeStrategy 把 JSON(逐字段覆盖)读进一个已初始化的策略, 因此只想改一两个
// 字段时, 其余权重保持默认, 不必把整份权重写全。
func DecodeStrategy(path string, s *jiju.Strategy) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, s)
}

// SaveStrategy 把策略写成 JSON。
func SaveStrategy(path string, s jiju.Strategy) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Pool 描述"玩家当前可抽的那一批诗句"。
//
// 三种来源, 优先级从高到低:
//  1. unlockPath 非空 —— 按账号解锁快照算出真实可抽集合(推荐, 这是本仓库的核心差异)
//  2. size > 0      —— 从词库随机切一个指定大小的子集(只想快速试算时用)
//  3. 否则          —— 整库。方便对照, 但**不代表真实游戏**。
func Pool(lib *jiju.Library, libPath string, unlockPath string, size int, seed int64) ([]int, string, error) {
	verses := lib.Verses()
	if unlockPath != "" {
		raw, err := os.ReadFile(unlockPath)
		if err != nil {
			return nil, "", err
		}
		var state jiju.UnlockState
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, "", err
		}
		ids, err := jiju.EligibleVerseIDs(libPath, state)
		if err != nil {
			return nil, "", err
		}
		return ids, fmt.Sprintf("按解锁快照算出的可抽池 %d 句(词库共 %d 句)", len(ids), len(verses)), nil
	}
	if size > 0 && size < len(verses) {
		idx := rand.New(rand.NewSource(seed)).Perm(len(verses))[:size]
		out := make([]int, 0, size)
		for _, i := range idx {
			out = append(out, verses[i].ID)
		}
		sort.Ints(out)
		return out, fmt.Sprintf("随机切出的可抽池 %d 句(词库共 %d 句)", size, len(verses)), nil
	}
	return nil, fmt.Sprintf("整库 %d 句 —— 注意真实游戏只抽已解锁且未收集的子集, 整库会高估池子", len(verses)), nil
}
