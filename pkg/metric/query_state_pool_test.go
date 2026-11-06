package metric

import "testing"

func TestQueryBucketArenaReleaseClearsBucketState(t *testing.T) {
	drainQueryBucketBlocks()
	var arena queryBucketArena
	first := arena.bucket(defaultTDigestCompression, true)
	first.count = 12
	first.sum = 40
	first.digest.Add(1, 1)
	second := arena.bucket(defaultTDigestCompression, false)
	second.count = 7
	arena.release()

	var reused queryBucketArena
	digestBucket := reused.bucket(defaultTDigestCompression, true)
	if digestBucket.count != 0 || digestBucket.sum != 0 || digestBucket.digest == nil || digestBucket.digest.count != 0 {
		t.Fatalf("digest slot leaked query state: %+v", digestBucket)
	}
	plain := reused.bucket(defaultTDigestCompression, false)
	if plain.count != 0 || plain.digest != nil {
		t.Fatalf("plain slot leaked query state: %+v", plain)
	}
	reused.release()
}

func TestQueryBucketArenaReusesBlocks(t *testing.T) {
	drainQueryBucketBlocks()
	var arena queryBucketArena
	for i := 0; i < queryStateBlockSize+2; i++ {
		bucket := arena.bucket(defaultTDigestCompression, i%2 == 0)
		if bucket == nil {
			t.Fatal("arena returned a nil bucket")
		}
		if i%2 == 0 && bucket.digest == nil {
			t.Fatal("digest bucket missing t-digest")
		}
		if i%2 != 0 && bucket.digest != nil {
			t.Fatal("non-digest bucket kept a t-digest")
		}
		bucket.count = int64(i + 1)
	}
	arena.release()
	if len(queryBucketBlocks) == 0 {
		t.Fatal("released blocks were not cached")
	}
	var reused queryBucketArena
	bucket := reused.bucket(defaultTDigestCompression, false)
	if bucket.count != 0 || bucket.digest != nil {
		t.Fatalf("reused bucket was not reset: %#v", bucket)
	}
	reused.release()
}

func drainQueryBucketBlocks() {
	for {
		select {
		case <-queryBucketBlocks:
		default:
			return
		}
	}
}
