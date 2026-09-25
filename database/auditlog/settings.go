package auditlog

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// SettingChange is one field the user actually changed.
type SettingChange struct {
	Key    string
	Params map[string]string
}

var secretSettingKeys = map[string]struct{}{
	"api_key":                 {},
	"cloudflare_tunnel_token": {},
	"metric_db_dsn":           {},
	"tempory_share_token":     {},
}

var longSettingKeys = map[string]struct{}{
	"custom_head":           {},
	"custom_body":           {},
	"notification_template": {},
}

// Companion values are written by the server next to a field the user edits.
// They are not separate controls, so they do not get their own log line.
var ignoredSettingKeys = map[string]struct{}{
	"metric_db_driver":              {},
	"metric_migration_target":       {},
	"metric_store_enabled":          {},
	"metric_downsampling_enabled":   {},
	"session_ttl_capped_v1":         {},
	"tempory_share_token_expire_at": {},
}

// SettingChanges compares the saved patch with the previous settings.
// Unchanged fields are skipped. Secret values are never copied into the result.
func SettingChanges(before, next map[string]any) []SettingChange {
	if len(next) == 0 {
		return nil
	}
	keys := make([]string, 0, len(next))
	for key := range next {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var changes []SettingChange
	for _, key := range keys {
		if _, skip := ignoredSettingKeys[key]; skip {
			continue
		}
		oldValue, hadOld := before[key]
		newValue := next[key]
		if hadOld && canonical(oldValue) == canonical(newValue) {
			continue
		}
		changes = append(changes, describeSetting(key, oldValue, newValue, hadOld))
	}
	return changes
}

func describeSetting(key string, oldValue, newValue any, hadOld bool) SettingChange {
	params := map[string]string{"setting": key}
	if _, secret := secretSettingKeys[key]; secret {
		return SettingChange{Key: secretAction(oldValue, newValue, hadOld), Params: params}
	}
	if _, long := longSettingKeys[key]; long {
		return SettingChange{Key: "audit.text_update", Params: params}
	}
	if nextOn, ok := boolValue(newValue); ok {
		action := "audit.bool_off"
		if nextOn {
			action = "audit.bool_on"
		}
		return SettingChange{Key: action, Params: params}
	}
	params["from"] = clip(canonical(oldValue))
	params["to"] = clip(canonical(newValue))
	return SettingChange{Key: "audit.value_change", Params: params}
}

func secretAction(oldValue, newValue any, hadOld bool) string {
	oldEmpty := !hadOld || strings.TrimSpace(canonical(oldValue)) == ""
	newEmpty := strings.TrimSpace(canonical(newValue)) == ""
	switch {
	case newEmpty:
		return "audit.secret_clear"
	case oldEmpty:
		return "audit.secret_set"
	default:
		return "audit.secret_replace"
	}
}

func boolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func canonical(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case string:
		return typed
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) && typed == math.Trunc(typed) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return canonical(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		return strings.TrimSpace(strings.Trim(stringify(typed), `"`))
	}
}

func stringify(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func clip(value string) string {
	chars := []rune(value)
	if len(chars) <= 80 {
		return value
	}
	return string(chars[:80]) + "…"
}
