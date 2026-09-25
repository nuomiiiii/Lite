package metric

import (
	"strconv"
	"strings"
)

// parsePercentile reports whether agg names a percentile and, if so, returns
// the corresponding fraction in [0,1]. "p99.9" -> 0.999. Out-of-range
// percentages (<=0 or >=100) are rejected so validation can reject them.
//
// parsePercentile 判断 agg 是否命名了百分位；如果是，则返回对应的 [0,1] 小数。
// 例如 "p99.9" -> 0.999。越界百分比（<=0 或 >=100）会被拒绝，以便校验逻辑
// 能拒绝它们。
func parsePercentile(agg Aggregation) (float64, bool) {
	s := string(agg)
	if len(s) < 2 || (s[0] != 'p' && s[0] != 'P') {
		return 0, false
	}
	pct, err := strconv.ParseFloat(s[1:], 64)
	if err != nil {
		return 0, false
	}
	if pct <= 0 || pct >= 100 {
		return 0, false
	}
	return pct / 100, true
}

// isPercentile reports whether agg is any percentile aggregation.
//
// isPercentile 判断聚合类型是否为任意百分位聚合。
func isPercentile(agg Aggregation) bool {
	_, ok := parsePercentile(agg)
	return ok
}

// percentileFractionString renders the fraction for SQL percentile_cont, e.g.
// "p99.9" -> "0.999". Trailing zeros are trimmed for stable SQL text.
//
// percentileFractionString 把百分位聚合转换为 SQL percentile_cont 需要的
// 小数字符串，并去掉尾随零以保持 SQL 文本稳定。
func percentileFractionString(agg Aggregation) (string, bool) {
	f, ok := parsePercentile(agg)
	if !ok {
		return "", false
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		// f is in (0,1) so this should not happen, but guard anyway.
		s = strconv.FormatFloat(f, 'f', 1, 64)
	}
	return s, true
}
