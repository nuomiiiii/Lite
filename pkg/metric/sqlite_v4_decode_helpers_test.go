package metric

func decodeSQLiteV4EncodedRollupBlock(encoded sqliteV4EncodedRollupBlock, needDigest bool) ([]sqliteV4RollupRecord, error) {
	return decodeSQLiteV4StoredRollupBlock(
		encoded.codec, encoded.count, encoded.checksum, encoded.payload,
		encoded.axisCodec, encoded.axisChecksum, encoded.axisPayload,
		encoded.digestCodec, encoded.digestChecksum, encoded.digestPayload, needDigest,
	)
}
