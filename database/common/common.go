package common

import (
	"github.com/komari-monitor/komari/database/dbcore"
)

// Create 新增一条记录
func Create[T any](entity *T) error {
	db := dbcore.GetDBInstance()
	return db.Create(entity).Error
}

// GetByID 根据 ID 查询一条记录
func GetByID[T any](id any) (*T, error) {
	db := dbcore.GetDBInstance()
	var entity T
	if err := db.First(&entity, id).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// UpdateFields 根据主键更新指定字段，支持任意主键名称
func UpdateFields[T any](id any, fields map[string]interface{}) error {
	db := dbcore.GetDBInstance()
	var entity T
	if err := db.First(&entity, id).Error; err != nil {
		return err
	}
	return db.Model(&entity).Updates(fields).Error
}

// Delete 根据 ID 删除一条记录
func Delete[T any](id any) error {
	db := dbcore.GetDBInstance()
	var entity T
	if err := db.First(&entity, id).Error; err != nil {
		return err
	}
	return db.Delete(&entity).Error
}

// DeleteBatch 批量删除记录，支持任意主键名称
func DeleteBatch[T any](ids []any) error {
	if len(ids) == 0 {
		return nil
	}
	db := dbcore.GetDBInstance()
	// 使用 GORM Delete 方法并传入主键值列表，会基于模型主键字段执行删除
	var list []T
	return db.Delete(&list, ids).Error
}

// List 查询所有记录
func List[T any]() ([]T, error) {
	db := dbcore.GetDBInstance()
	var list []T
	if err := db.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
