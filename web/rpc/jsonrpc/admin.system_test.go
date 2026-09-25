package jsonrpc

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	gormtests "gorm.io/gorm/utils/tests"
)

func TestLogSchemaDefinesQueryIndexes(t *testing.T) {
	parsed, err := schema.Parse(&models.Log{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse log schema: %v", err)
	}
	indexes := make(map[string]*schema.Index)
	for _, index := range parsed.ParseIndexes() {
		indexes[index.Name] = index
	}
	for _, name := range []string{"idx_logs_time", "idx_logs_msg_type_time"} {
		if indexes[name] == nil {
			t.Fatalf("expected log index %q to be defined", name)
		}
	}
	composite := indexes["idx_logs_msg_type_time"]
	if len(composite.Fields) != 2 || composite.Fields[0].Field.Name != "MsgType" || composite.Fields[1].Field.Name != "Time" {
		t.Fatalf("unexpected composite log index fields: %#v", composite.Fields)
	}
}

func TestFilterAdminLogsByMessageType(t *testing.T) {
	db, err := gorm.Open(gormtests.DummyDialector{}, &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}

	var logs []models.Log
	statement := filterAdminLogsByValues(db.Model(&models.Log{}), "msg_type", parseAdminLogCSV(" visitor ")).Find(&logs).Statement
	if sql := statement.SQL.String(); !strings.Contains(sql, "WHERE msg_type IN (?)") {
		t.Fatalf("filtered SQL missing message type predicate: %s", sql)
	}
	if len(statement.Vars) != 1 || statement.Vars[0] != "visitor" {
		t.Fatalf("unexpected filter variables: %#v", statement.Vars)
	}
}

func TestQueryAdminLogsSearchAndFilters(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Log{}); err != nil {
		t.Fatalf("migrate logs: %v", err)
	}
	first := time.Date(2026, 9, 21, 3, 43, 22, 0, time.UTC)
	rows := []models.Log{
		{IP: "38.65.93.115", UUID: "client-a", Message: "remote file operation requested", MsgType: "warn", Time: first},
		{IP: "10.0.0.1", UUID: "client-b", Message: "logged in (passkey)", MsgType: "login", Time: first.Add(time.Hour)},
		{IP: "10.0.0.1", UUID: "client-c", Message: "logged out", MsgType: "logout", Time: first.Add(26 * time.Hour)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("insert logs: %v", err)
	}

	mustHit := func(q adminLogQuery, wantMessage string) {
		t.Helper()
		q.Limit = 20
		q.Page = 1
		logs, total, _, err := queryAdminLogs(db, q)
		if err != nil {
			t.Fatalf("query %#v: %v", q, err)
		}
		if total != 1 || len(logs) != 1 || logs[0].Message != wantMessage {
			t.Fatalf("query %#v: total=%d logs=%#v", q, total, logs)
		}
	}
	mustHit(adminLogQuery{Search: "38.65.93.115"}, "remote file operation requested")
	mustHit(adminLogQuery{Search: "remote file"}, "remote file operation requested")
	mustHit(adminLogQuery{Types: []string{"login"}}, "logged in (passkey)")
	mustHit(adminLogQuery{Days: []string{"2026-09-22"}}, "logged out")

	logs, total, facets, err := queryAdminLogs(db, adminLogQuery{Limit: 20, Page: 1, Search: "warn"})
	if err != nil {
		t.Fatalf("type text should not match message search: %v", err)
	}
	if total != 0 || len(logs) != 0 {
		t.Fatalf("searching type in the message box should miss: total=%d logs=%#v", total, logs)
	}
	if len(facets.Types) != 3 || len(facets.Days) != 2 {
		t.Fatalf("unexpected facets: %#v", facets)
	}

	logs, total, _, err = queryAdminLogs(db, adminLogQuery{Limit: 20, Page: 1, Search: "no-such-log"})
	if err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if total != 0 || len(logs) != 0 {
		t.Fatalf("empty search should return nothing: total=%d logs=%#v", total, logs)
	}
}

func TestFilterAdminLogsBySearch(t *testing.T) {
	db, err := gorm.Open(gormtests.DummyDialector{}, &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}

	var logs []models.Log
	statement := filterAdminLogsBySearch(db.Model(&models.Log{}), `warn_%`).Find(&logs).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{
		`ip LIKE ? ESCAPE '\'`,
		`message LIKE ? ESCAPE '\'`,
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("search SQL missing %q: %s", fragment, sql)
		}
	}
	if strings.Contains(sql, "msg_type LIKE") || strings.Contains(sql, "strftime") {
		t.Fatalf("search SQL should only match IP and message: %s", sql)
	}
	if len(statement.Vars) != 2 {
		t.Fatalf("unexpected search variables: %#v", statement.Vars)
	}
	for _, value := range statement.Vars {
		if value != `%warn\_\%%` {
			t.Fatalf("LIKE pattern was not escaped: %#v", statement.Vars)
		}
	}
}

func TestEscapeAdminLogLike(t *testing.T) {
	if got := escapeAdminLogLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("unexpected escaped LIKE pattern: %q", got)
	}
}
