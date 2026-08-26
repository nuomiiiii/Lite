package accounts

import (
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openPreferenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.User{}))
	require.NoError(t, db.Create(&models.User{UUID: "user-1", Username: "admin", Passwd: "hash"}).Error)
	return db
}

func TestUpdateUserPreferencesWithDBSupportsPartialUpdates(t *testing.T) {
	db := openPreferenceTestDB(t)
	language := "zh-CN"
	color := "jade"
	require.NoError(t, UpdateUserPreferencesWithDB(db, "user-1", &language, nil))
	require.NoError(t, UpdateUserPreferencesWithDB(db, "user-1", nil, &color))

	var user models.User
	require.NoError(t, db.First(&user, "uuid = ?", "user-1").Error)
	require.Equal(t, language, user.Language)
	require.Equal(t, color, user.Color)
}

func TestUpdateUserPreferencesWithDBRejectsInvalidValues(t *testing.T) {
	db := openPreferenceTestDB(t)
	badLanguage := "not-a-language"
	badColor := "not-a-color"

	require.Error(t, UpdateUserPreferencesWithDB(db, "user-1", &badLanguage, nil))
	require.Error(t, UpdateUserPreferencesWithDB(db, "user-1", nil, &badColor))
	require.Error(t, UpdateUserPreferencesWithDB(db, "user-1", nil, nil))
	color := "iris"
	require.Error(t, UpdateUserPreferencesWithDB(db, "missing", nil, &color))
}
