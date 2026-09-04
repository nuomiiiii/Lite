package public

import (
	"os"
	"testing"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/utils/instancekey"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:web_api_public_test?mode=memory&cache=shared"

	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}
