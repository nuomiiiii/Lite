package dbcore

import (
	"fmt"
	"log"
	"sync"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// mergeClientInfo 将旧版ClientInfo数据迁移到新版Client表
func mergeClientInfo(db *gorm.DB) {
	var clientInfos []common.ClientInfo
	if err := db.Find(&clientInfos).Error; err != nil {
		log.Printf("Failed to read ClientInfo table: %v", err)
		return
	}

	for _, info := range clientInfos {
		var client models.Client
		if err := db.Where("uuid = ?", info.UUID).First(&client).Error; err != nil {
			log.Printf("Could not find Client record with UUID %s: %v", info.UUID, err)
			continue
		}

		// 更新Client记录
		client.Name = info.Name
		client.CpuName = info.CpuName
		client.Virtualization = info.Virtualization
		client.Arch = info.Arch
		client.CpuCores = info.CpuCores
		client.OS = info.OS
		client.GpuName = info.GpuName
		client.IPv4 = info.IPv4
		client.IPv6 = info.IPv6
		client.Region = info.Region
		client.Remark = info.Remark
		client.PublicRemark = info.PublicRemark
		client.MemTotal = info.MemTotal
		client.SwapTotal = info.SwapTotal
		client.DiskTotal = info.DiskTotal
		client.Version = info.Version
		client.Weight = info.Weight
		client.Price = info.Price
		client.BillingCycle = info.BillingCycle
		client.ExpiredAt = models.FromTime(info.ExpiredAt)
		// Save updated Client record
		if err := db.Save(&client).Error; err != nil {
			log.Printf("Failed to update Client record: %v", err)
			continue
		}
	}

	// Backup and rename old table after migration
	if err := db.Migrator().RenameTable("client_infos", "client_infos_backup"); err != nil {
		log.Printf("Failed to backup ClientInfo table: %v", err)
		return
	}
	log.Println("Data migration completed, old table has been backed up as client_infos_backup")
}

func MergeDatabase(db *gorm.DB) {
	if db.Migrator().HasTable("client_infos") {
		log.Println("[>0.0.5] Legacy ClientInfo table detected, starting data migration...")
		mergeClientInfo(db)
	}
	if db.Migrator().HasColumn(&models.Config{}, "allow_cros") {
		log.Println("[>0.0.5a] Renaming column 'allow_cros' to 'allow_cors' in config table...")
		db.Migrator().RenameColumn(&models.Config{}, "allow_cros", "allow_cors")
	}
	if db.Migrator().HasColumn(&models.LoadNotification{}, "client") {
		log.Println("[>0.1.4] Rebuilding LoadNotification table....")
		db.Migrator().DropTable(&models.LoadNotification{})
	}
}

var (
	instance *gorm.DB
	once     sync.Once
)

func GetDBInstance() *gorm.DB {
	once.Do(func() {
		var err error

		logConfig := &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		}

		if utils.VersionHash == "unknown" {
			logConfig = &gorm.Config{
				Logger: logger.Default.LogMode(logger.Info),
			}
		}

		// 根据数据库类型选择不同的连接方式
		switch flags.DatabaseType {
		case "sqlite", "":
			// SQLite 连接
			instance, err = gorm.Open(sqlite.Open(flags.DatabaseFile), logConfig)
			if err != nil {
				log.Fatalf("Failed to connect to SQLite3 database: %v", err)
			}
			log.Printf("Using SQLite database file: %s", flags.DatabaseFile)

		case "mysql":
			// MySQL 连接
			dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local",
				flags.DatabaseUser,
				flags.DatabasePass,
				flags.DatabaseHost,
				flags.DatabasePort,
				flags.DatabaseName)
			instance, err = gorm.Open(mysql.Open(dsn), logConfig)
			if err != nil {
				log.Fatalf("Failed to connect to MySQL database: %v", err)
			}
			log.Printf("Using MySQL database: %s@%s:%s/%s", flags.DatabaseUser, flags.DatabaseHost, flags.DatabasePort, flags.DatabaseName)

		default:
			log.Fatalf("Unsupported database type: %s", flags.DatabaseType)
		}
		MergeDatabase(instance)
		// 自动迁移模型
		err = instance.AutoMigrate(
			&models.User{},
			&models.Client{},
			&models.Record{},
			&models.Config{},
			&models.Log{},
			&models.Clipboard{},
			&models.LoadNotification{},
			&models.OfflineNotification{},
			&models.PingRecord{},
			&models.PingTask{},
		)
		if err != nil {
			log.Fatalf("Failed to create tables: %v", err)
		}
		err = instance.Table("records_long_term").AutoMigrate(
			&models.Record{},
		)
		if err != nil {
			log.Printf("Failed to create records_long_term table, it may already exist: %v", err)
		}
		err = instance.AutoMigrate(
			&models.Session{},
		)
		if err != nil {
			log.Printf("Failed to create Session table, it may already exist: %v", err)
		}
		err = instance.AutoMigrate(
			&models.Task{},
			&models.TaskResult{},
		)
		if err != nil {
			log.Printf("Failed to create Task and TaskResult table, it may already exist: %v", err)
		}

	})
	return instance
}
