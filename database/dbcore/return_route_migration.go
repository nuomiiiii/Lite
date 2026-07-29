package dbcore

import (
	"fmt"

	"gorm.io/gorm"
)

const (
	legacyReturnRouteLine10099 = "10099"
	returnRouteLineCUGVIP      = "CUG VIP"
)

func migrateLegacyReturnRouteLines(db *gorm.DB) error {
	columns := []struct {
		table  string
		column string
	}{
		{table: "return_route_tasks", column: "expected_line"},
		{table: "return_route_statuses", column: "current_line"},
		{table: "return_route_statuses", column: "candidate_line"},
		{table: "return_route_events", column: "expected_line"},
		{table: "return_route_events", column: "from_line"},
		{table: "return_route_events", column: "to_line"},
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, item := range columns {
			if !tx.Migrator().HasTable(item.table) || !tx.Migrator().HasColumn(item.table, item.column) {
				continue
			}
			result := tx.Table(item.table).
				Where(item.column+" = ?", legacyReturnRouteLine10099).
				Update(item.column, returnRouteLineCUGVIP)
			if result.Error != nil {
				return fmt.Errorf("update %s.%s: %w", item.table, item.column, result.Error)
			}
		}
		return nil
	})
}
