package metric

import "context"

const (
	queryStateBlockSize    = 64
	queryStateCachedBlocks = 64
)

type queryArenaContextKey struct{}

type queryBucketSlot struct {
	bucket rollupBucket
	digest *TDigest
}

type queryBucketBlock struct {
	slots [queryStateBlockSize]queryBucketSlot
	used  int
	next  *queryBucketBlock
}

var queryBucketBlocks = make(chan *queryBucketBlock, queryStateCachedBlocks)

// queryBucketArena owns the rollupBucket objects for one historical query.
// AggregatePoint output must not retain pointers into this arena.
type queryBucketArena struct {
	head *queryBucketBlock
}

func withQueryBucketArena(ctx context.Context) (context.Context, func()) {
	if queryArenaFrom(ctx) != nil {
		return ctx, func() {}
	}
	arena := &queryBucketArena{}
	return context.WithValue(ctx, queryArenaContextKey{}, arena), arena.release
}

func queryArenaFrom(ctx context.Context) *queryBucketArena {
	arena, _ := ctx.Value(queryArenaContextKey{}).(*queryBucketArena)
	return arena
}

func newQueryRollupBucket(ctx context.Context, compression float64, needDigest bool) *rollupBucket {
	if arena := queryArenaFrom(ctx); arena != nil {
		return arena.bucket(compression, needDigest)
	}
	return newRollupBucketWithDigest(compression, needDigest)
}

func newQueryRollupBucketFrom(arena *queryBucketArena, compression float64, needDigest bool) *rollupBucket {
	if arena != nil {
		return arena.bucket(compression, needDigest)
	}
	return newRollupBucketWithDigest(compression, needDigest)
}

func (a *queryBucketArena) bucket(compression float64, needDigest bool) *rollupBucket {
	block := a.head
	if block == nil || block.used == len(block.slots) {
		select {
		case block = <-queryBucketBlocks:
		default:
			block = new(queryBucketBlock)
		}
		block.next = a.head
		a.head = block
	}
	slot := &block.slots[block.used]
	block.used++
	digest := slot.digest
	slot.bucket = rollupBucket{}
	if needDigest {
		if digest == nil {
			digest = NewTDigest(compression)
		} else {
			digest.reset(compression)
		}
		slot.digest = digest
		slot.bucket.digest = digest
	}
	return &slot.bucket
}

func (a *queryBucketArena) release() {
	block := a.head
	a.head = nil
	for block != nil {
		next := block.next
		for i := 0; i < block.used; i++ {
			slot := &block.slots[i]
			digest := slot.digest
			slot.bucket = rollupBucket{}
			slot.digest = digest
			if digest != nil {
				digest.reset(defaultTDigestCompression)
			}
		}
		block.used = 0
		block.next = nil
		select {
		case queryBucketBlocks <- block:
		default:
		}
		block = next
	}
}
