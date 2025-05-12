package dbcore

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	instance *gorm.DB
	once     sync.Once
)

// true if database file exists
//
// false if database file does not exist
func InitDatabase() bool {
	if _, err := os.Stat(flags.DatabaseFile); os.IsNotExist(err) {
		log.Printf("Database file %q does not exist, creating...", flags.DatabaseFile)
		dbDir := filepath.Dir(flags.DatabaseFile)
		if dbDir != "" {
			if err := os.MkdirAll(dbDir, 0755); err != nil {
				log.Fatalf("Failed to create directory %q for database file: %v", dbDir, err)
			}
		}
		file, err := os.Create(flags.DatabaseFile)
		if err != nil {
			log.Fatalf("Failed to create SQLite3 database file %q: %v", flags.DatabaseFile, err)
		}
		if err := file.Close(); err != nil {
			log.Fatalf("Failed to close database file %q: %v", flags.DatabaseFile, err)
		}
		return false
	} else if err != nil {
		log.Fatalf("Failed to check database file %q: %v", flags.DatabaseFile, err)
	}
	return true
}

func GetDBInstance() *gorm.DB {
	once.Do(func() {
		var err error

		logConfig := &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		}

		instance, err = gorm.Open(sqlite.Open(flags.DatabaseFile), logConfig)
		if err != nil {
			log.Fatalf("Failed to connect to SQLite3 database: %v", err)
		}
		err = instance.AutoMigrate(
			&models.User{},
			&models.Client{},
			&models.Session{},
			&models.Record{},
			&common.ClientInfo{},
			&models.Config{},
		)
		if err != nil {
			log.Fatalf("Failed to create tables: %v", err)
		}
	})
	return instance
}
