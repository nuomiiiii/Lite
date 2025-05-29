package dbcore

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// migrateClientData 将旧版ClientInfo数据迁移到新版Client表
func migrateClientData(db *gorm.DB) {
	log.Println("正在迁移旧版ClientInfo数据到新版Client表...")

	// 读取所有ClientInfo记录
	var clientInfos []common.ClientInfo
	if err := db.Find(&clientInfos).Error; err != nil {
		log.Printf("读取ClientInfo表失败: %v", err)
		return
	}

	// 遍历每条记录并更新到Client表
	for _, info := range clientInfos {
		// 查找对应的Client记录
		var client models.Client
		if err := db.Where("uuid = ?", info.UUID).First(&client).Error; err != nil {
			log.Printf("找不到UUID为%s的Client记录: %v", info.UUID, err)
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
		client.ExpiredAt = info.ExpiredAt

		// 保存更新后的Client记录
		if err := db.Save(&client).Error; err != nil {
			log.Printf("更新Client记录失败: %v", err)
			continue
		}
	}

	// 数据迁移完成后，备份并删除旧表
	if err := db.Migrator().RenameTable("client_infos", "client_infos_backup"); err != nil {
		log.Printf("备份ClientInfo表失败: %v", err)
		return
	}

	log.Println("数据迁移完成，旧表已备份为client_infos_backup")
}

var (
	instance *gorm.DB
	once     sync.Once
)

// 初始化数据库
// 对于 SQLite：true 如果数据库文件存在，false 如果数据库文件不存在并被创建
// 对于 MySQL/其他数据库：总是返回 true
func InitDatabase() bool {
	// 默认使用 SQLite 如果未指定类型
	if flags.DatabaseType == "" || flags.DatabaseType == "sqlite" {
		if _, err := os.Stat(flags.DatabaseFile); os.IsNotExist(err) {
			log.Printf("SQLite database file %q does not exist, creating...", flags.DatabaseFile)
			dbDir := filepath.Dir(flags.DatabaseFile)
			if dbDir != "" {
				if err := os.MkdirAll(dbDir, 0755); err != nil {
					log.Fatalf("Failed to create database file directory %q: %v", dbDir, err)
				}
			}
			file, err := os.Create(flags.DatabaseFile)
			if err != nil {
				log.Fatalf("Failed to create SQLite database file %q: %v", flags.DatabaseFile, err)
			}
			if err := file.Close(); err != nil {
				log.Fatalf("Failed to close database file %q: %v", flags.DatabaseFile, err)
			}
			return false
		} else if err != nil {
			log.Fatalf("Failed to check database file %q: %v", flags.DatabaseFile, err)
		}
		return true
	} else if flags.DatabaseType == "mysql" {
		// 对于 MySQL，我们不需要创建文件，只需检查连接信息是否有效
		log.Printf("Using MySQL database: %s@%s:%s/%s",
			flags.DatabaseUser, flags.DatabaseHost, flags.DatabasePort, flags.DatabaseName)
		return true
	} else {
		log.Fatalf("Unsupported database type: %s", flags.DatabaseType)
		return false
	}
}

func GetDBInstance() *gorm.DB {
	once.Do(func() {
		var err error

		logConfig := &gorm.Config{
			//Logger: logger.Default.LogMode(logger.Silent),
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
		// 检查是否存在旧版ClientInfo表
		hasOldClientInfoTable := instance.Migrator().HasTable(&common.ClientInfo{})

		// 自动迁移模型
		err = instance.AutoMigrate(
			&models.User{},
			&models.Client{},
			&models.Record{},
			&models.Config{},
		)
		if err != nil {
			log.Fatalf("Failed to create tables: %v", err)
		}
		err = instance.AutoMigrate(
			&models.Session{},
		)
		if err != nil {
			log.Printf("Failed to create Session table, it may already exist: %v", err)
		}

		// 如果存在旧表，执行数据迁移
		if hasOldClientInfoTable {
			migrateClientData(instance)
		}
	})
	return instance
}
