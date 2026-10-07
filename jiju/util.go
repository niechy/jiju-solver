package jiju

import (
	"math"
	"regexp"
	"strings"
)

// cleanText 去掉标点与空白等噪声字符, 只保留中文与数字, 便于诗句文本比对。
var cleanRe = regexp.MustCompile(`[^\p{Han}\p{N}]`)

func cleanText(s string) string {
	return cleanRe.ReplaceAllString(s, "")
}

// contains 判断 a 是否包含 b。
func contains(a, b string) bool {
	return strings.Contains(a, b)
}
// finite 报告 x 是否为有限数(既非 NaN 也非 ±Inf)。
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
