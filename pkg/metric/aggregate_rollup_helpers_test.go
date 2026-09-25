package metric

import (
	"context"
	"fmt"
	"time"
)

// AggregateRollup answers an AggregateQuery from a stored rollup tier instead of
// raw points. resolution names which tier to read (it must match a tier
// Interval that Compact has materialized). The query Interval must be a positive
// integer multiple of resolution, so each output bucket is composed of whole
// rollup buckets.
//
// query.Tags is honored: only rollup series whose stored tag set matches the
// filter are folded in, so a tag filter selects the same data it would over raw
// points. By default, matched series are merged into each output bucket; callers
// that need entity/tag identities preserved can set PreserveSeries.
//
// Every aggregation works except AggRate, which needs the ordered raw series and
// is therefore raw-only. Percentiles (p50, p95, p99, and arbitrary pXX) are
// answered by merging the per-bucket t-digests, so they survive downsampling
// with bounded error.
//
// AggregateRollup 从已存储的 rollup 层回答 AggregateQuery，而不是读取原始点。
// resolution 指定要读取的层级（必须匹配 Compact 已物化的某个层级 Interval）。
// 查询的 Interval 必须是 resolution 的正整数倍，因此每个输出桶都由完整的
// rollup 桶组成。
//
// query.Tags 会被遵守：只有存储标签集合匹配过滤条件的 rollup 序列会被合入，
// 因此标签过滤会选中与原始点查询相同的数据。默认会把匹配序列合并进每个
// 输出桶；需要保留 entity/tag 身份的调用方可以设置 PreserveSeries。
//
// 除 AggRate 外，每种聚合都可用；AggRate 需要有序原始序列，因此只能基于原始点。
// 百分位（p50、p95、p99 和任意 pXX）通过合并每桶 t-digest 回答，因此能在
// 降采样后以有界误差保留下来。
func (s *Store) AggregateRollup(ctx context.Context, query AggregateQuery, resolution time.Duration) ([]AggregatePoint, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if resolution <= 0 {
		return nil, fmt.Errorf("%w: rollup resolution must be positive", ErrInvalidArgument)
	}
	if query.Interval < resolution || query.Interval%resolution != 0 {
		return nil, fmt.Errorf("%w: query interval must be a positive multiple of the rollup resolution", ErrInvalidArgument)
	}
	if query.Aggregation == AggRate {
		return nil, fmt.Errorf("%w: rate is not derivable from rollups (raw only)", ErrInvalidArgument)
	}

	q := query.Query.normalized()
	comp := s.cfg.RollupPolicy.compression()
	needDigest := isPercentile(query.Aggregation)

	// Read rollup buckets at this resolution that are FULLY contained in the
	// inclusive window [Start, End] (entity and tag filters pushed into SQL),
	// then fold them into query.Interval-wide output buckets. Full containment
	// (rather than mere overlap) is what keeps a partially-overlapping bucket's
	// out-of-window samples from leaking into the result: a rollup bucket is an
	// indivisible summary, so a bucket straddling a window edge cannot be
	// trimmed to the window and is therefore excluded. Callers that need every
	// sample in a sub-bucket window must align the window to resolution
	// boundaries (or query raw points).
	rows, err := s.scanRollupRowsContained(ctx, q.MetricName, q.EntityID, q.Tags, resolution, q.Start, q.End, needDigest)
	if err != nil {
		return nil, err
	}
	groups := foldRollupRows(nil, rows, query.Interval, comp, query.PreserveSeries, needDigest)

	out, err := rollupGroupsToPoints(groups, query)
	if err != nil {
		return nil, err
	}
	return pageBuckets(out, query.BucketLimit, query.BucketOffset), nil
}
