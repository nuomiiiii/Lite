package metric

import "strconv"

// Pxx builds the Aggregation for an arbitrary percentile. The argument is a
// percentage in (0,100): Pxx(99.9) -> "p99.9", Pxx(50) -> "p50". The fixed
// AggP50/AggP95/AggP99 constants are just the common cases of this same string
// form, so they keep working unchanged.
//
// This is what turns the package from "p50/p95/p99 only" into "any percentile":
// callers can ask for p75, p90, p99.99, etc., and every path (in-memory,
// SQL pushdown, and rollup-via-t-digest) understands it.
//
// Pxx 根据任意百分位构造 Aggregation。参数是 (0,100) 内的百分比：
// Pxx(99.9) -> "p99.9"，Pxx(50) -> "p50"。固定的 AggP50/AggP95/AggP99
// 常量只是同一字符串形式的常用情况，因此会保持原有行为。
//
// 这让 package 从“只支持 p50/p95/p99”变成“支持任意百分位”：调用方可以请求
// p75、p90、p99.99 等，并且每条路径（内存、SQL 下推、基于 t-digest 的 rollup）
// 都能理解它。
func Pxx(p float64) Aggregation {
	// Trim trailing zeros so Pxx(95) == AggP95 ("p95"), not "p95.000000".
	s := strconv.FormatFloat(p, 'f', -1, 64)
	return Aggregation("p" + s)
}
