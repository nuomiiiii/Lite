package dbcore

func sqliteFileSetSize(dsn string) (int64, error) {
	files, err := sqliteFileSetSizes(dsn)
	if err != nil {
		return 0, err
	}
	return files.Total(), nil
}
