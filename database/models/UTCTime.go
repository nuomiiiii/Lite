package models

import (
	"database/sql/driver"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// UTCTime 是一个自定义类型，用于在 GORM 中处理 time.Time。
// 它强制所有时间都以指定时区（或 UTC）的字符串格式存取。
type UTCTime time.Time

// Value 实现 driver.Valuer 接口，将 UTCTime 转换为数据库可以识别的 TEXT 类型。
// 所有时间在存储到数据库前，都转换为 UTC 并以标准格式存储。
func (t UTCTime) Value() (driver.Value, error) {
	if time.Time(t).IsZero() {
		return nil, nil // GORM 默认会将零值时间存储为 NULL，这里保持一致
	}
	// 将时间转换为应用程序指定的时区，并格式化为不带时区信息的字符串。
	// 这样数据库中存储的是 'YYYY-MM-DD HH:MM:SS.NNNNNNN' 格式。
	// 注意：之前你遇到的格式是 "2025-06-14 23:51:17.3022328+08:00"
	// 这种格式带有微秒和时区偏移。为了兼容旧数据读取，我们这里也用精确格式。
	// 但存储时，为了避免时区混乱，建议统一为某个特定时区的纯时间戳字符串。
	// 这里我建议统一存储为 UTC 时间的精确字符串。
	// "2006-01-02 15:04:05.0000000" 这个布局对应不带时区后缀的纳秒精度。
	return time.Time(t).In(GetAppLocation()).Format("2006-01-02 15:04:05.0000000"), nil
}

// Scan 实现 sql.Scanner 接口，将数据库中的 TEXT 值扫描到 UTCTime。
// 所有时间字符串在读取时，都会被尝试解析为指定时区（或 UTC）的 time.Time 对象。
func (t *UTCTime) Scan(v interface{}) error {
	if v == nil {
		*t = UTCTime(time.Time{})
		return nil
	}

	switch val := v.(type) {
	case time.Time:
		// If the driver already parsed it to time.Time, use it directly.
		// Then convert to the application's desired location.
		*t = UTCTime(val.In(GetAppLocation()))
		return nil
	case []byte:
		// If it's still a byte slice (string), parse it.
		timeStr := strings.TrimSpace(string(val))
		if timeStr == "" {
			*t = UTCTime(time.Time{})
			return nil
		}

		var parsedTime time.Time
		var err error

		layouts := []string{
			"2006-01-02 15:04:05.0000000-07:00", // Your observed precise format with timezone
			"2006-01-02 15:04:05-07:00",         // With seconds and timezone
			time.RFC3339Nano,                    // RFC3339 with nanoseconds
			time.RFC3339,                        // RFC3339
			"2006-01-02 15:04:05.0000000",       // Nanoseconds, no timezone
			"2006-01-02 15:04:05",               // Standard, no timezone
			"2006-01-02",                        // Date only
		}

		for _, layout := range layouts {
			// time.Parse handles strings that include timezone info correctly.
			parsedTime, err = time.Parse(layout, timeStr)
			if err == nil {
				break
			}
		}

		if err != nil {
			return fmt.Errorf("无法解析时间字符串 '%s' 为 UTCTime: %w", timeStr, err)
		}

		*t = UTCTime(parsedTime.In(GetAppLocation()))
		return nil
	default:
		return fmt.Errorf("UTCTime scan source was not []byte or time.Time: %T (%v)", v, v)
	}
}

// MarshalJSON implements json.Marshaler interface.
// This method defines how UTCTime should be serialized to JSON.
func (t UTCTime) MarshalJSON() ([]byte, error) {
	if time.Time(t).IsZero() {
		// 如果是零值时间，返回 JSON null。
		// 这样 API 返回的就是 "field": null，而不是 "field": {}。
		return []byte("null"), nil
	}
	// 将时间转换为应用程序指定的时区，并格式化为 JSON 字符串。
	// 使用双引号包裹，符合 JSON 字符串规范。
	// 同样使用之前定义的精确格式，便于客户端解析。
	formattedTime := time.Time(t).In(GetAppLocation()).Format("2006-01-02 15:04:05.0000000-07:00")
	return []byte(fmt.Sprintf(`"%s"`, formattedTime)), nil
}

// ToTime 将 UTCTime 转换为 Go 的 time.Time 类型。
func (t UTCTime) ToTime() time.Time {
	return time.Time(t)
}

// FromTime 将 Go 的 time.Time 类型转换为 UTCTime。
func FromTime(t time.Time) UTCTime {
	return UTCTime(t)
}

// Now returns the current time in the application's configured location.
func Now() UTCTime {
	return UTCTime(time.Now().In(GetAppLocation()))
}

// AppLocation 用于存储应用程序全局使用的时区。
// 首次获取后会缓存，避免重复加载。
var (
	appLocation     *time.Location
	locationOnce    sync.Once
	defaultLocation = time.UTC // 默认时区设为 UTC
)

// GetAppLocation 获取应用程序的全局时区。
// 优先从环境变量 "TZ" 获取，否则使用默认的 UTC。
func GetAppLocation() *time.Location {
	locationOnce.Do(func() {
		tz := os.Getenv("TZ")
		if tz != "" {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				log.Printf("Warning: Failed to load timezone from environment variable TZ='%s', using default UTC. Error: %v\n", tz, err)
				appLocation = defaultLocation
			} else {
				appLocation = loc
				log.Printf("Info: Application timezone set from environment variable TZ='%s' (%s).\n", tz, appLocation.String())
			}
		} else {
			appLocation = defaultLocation
			log.Printf("Info: Environment variable TZ not set, using default UTC timezone (%s).\n", appLocation.String())
		}
		time.Local = appLocation // 设置全局时区
	})
	return appLocation
}
