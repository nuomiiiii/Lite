package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ConfigItem struct {
	Key   string `gorm:"primaryKey;column:key;type:text"`
	Value string `gorm:"column:value;type:text"` // 存 JSON 字符串
}

func (ConfigItem) TableName() string {
	return "configs"
}

var (
	db          *gorm.DB
	cacheMu     sync.RWMutex
	cachedDB    *gorm.DB
	cacheGen    uint64
	configCache map[string]string
	cacheLoaded bool

	SetDb = func(gdb *gorm.DB) {
		cacheMu.Lock()
		db = gdb
		cachedDB = gdb
		cacheGen++
		configCache = make(map[string]string)
		cacheLoaded = false
		cacheMu.Unlock()
		if err := gdb.AutoMigrate(&ConfigItem{}); err != nil {
			panic("failed to migrate config item table: " + err.Error())
		}
	}
)

// InvalidateCache clears the in-memory configuration cache so subsequent reads
// reload directly from the database (e.g. after raw SQL/GORM migrations).
func InvalidateCache() {
	cacheMu.Lock()
	cacheGen++
	configCache = nil
	cacheLoaded = false
	cacheMu.Unlock()
}

// CacheGeneration returns the monotonic generation counter of the config cache.
func CacheGeneration() uint64 {
	cacheMu.RLock()
	gen := cacheGen
	cacheMu.RUnlock()
	return gen
}

func ensureConfigCacheLoaded() (*gorm.DB, error) {
	cacheMu.RLock()
	currentDB := db
	if currentDB == nil {
		cacheMu.RUnlock()
		return nil, fmt.Errorf("config database is not initialized")
	}
	if cachedDB == currentDB && cacheLoaded {
		cacheMu.RUnlock()
		return currentDB, nil
	}
	cacheMu.RUnlock()

	cacheMu.Lock()
	defer cacheMu.Unlock()
	currentDB = db
	if currentDB == nil {
		return nil, fmt.Errorf("config database is not initialized")
	}
	if cachedDB == currentDB && cacheLoaded {
		return currentDB, nil
	}
	var items []ConfigItem
	if err := currentDB.Find(&items).Error; err != nil {
		return nil, err
	}
	fresh := make(map[string]string, len(items))
	for _, item := range items {
		fresh[item.Key] = item.Value
	}
	cachedDB = currentDB
	configCache = fresh
	cacheLoaded = true
	return currentDB, nil
}

func getCachedRaw(key string) (string, bool, error) {
	if _, err := ensureConfigCacheLoaded(); err != nil {
		return "", false, err
	}
	cacheMu.RLock()
	val, ok := configCache[key]
	cacheMu.RUnlock()
	return val, ok, nil
}

func getCachedManyRaw(keys []string) (map[string]string, *gorm.DB, uint64, error) {
	currentDB, err := ensureConfigCacheLoaded()
	if err != nil {
		return nil, nil, 0, err
	}
	cacheMu.RLock()
	gen := cacheGen
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := configCache[k]; ok {
			out[k] = v
		}
	}
	cacheMu.RUnlock()
	return out, currentDB, gen, nil
}

func getCachedAllRaw() (map[string]string, error) {
	if _, err := ensureConfigCacheLoaded(); err != nil {
		return nil, err
	}
	cacheMu.RLock()
	out := make(map[string]string, len(configCache))
	for k, v := range configCache {
		out[k] = v
	}
	cacheMu.RUnlock()
	return out, nil
}

func putCachedDefaultsIfGen(targetDB *gorm.DB, expectedGen uint64, items []ConfigItem) {
	cacheMu.Lock()
	if cachedDB == targetDB && cacheGen == expectedGen && cacheLoaded && configCache != nil {
		cacheGen++
		for _, item := range items {
			if _, exists := configCache[item.Key]; !exists {
				configCache[item.Key] = item.Value
			}
		}
	}
	cacheMu.Unlock()
}

func putCachedWrite(targetDB *gorm.DB, items []ConfigItem) {
	cacheMu.Lock()
	cacheGen++
	if cachedDB == targetDB && cacheLoaded && configCache != nil {
		for _, item := range items {
			configCache[item.Key] = item.Value
		}
	}
	cacheMu.Unlock()
}

// GetAs 获取并转换为指定类型 (泛型)，支持数值类型自动转换
func GetAs[T any](key string, defaul ...any) (T, error) {
	var t T
	rawValue, found, err := getCachedRaw(key)
	if err != nil {
		return t, err
	}
	if !found {
		if len(defaul) > 0 {
			// 尝试直接类型断言
			if v, ok := defaul[0].(T); ok {
				err = Set(key, v)
				return v, err
			}
			// 尝试类型转换
			val := reflect.ValueOf(&t).Elem()
			if err := convertAndSet(defaul[0], val); err != nil {
				return t, fmt.Errorf("default value type mismatch: expected %T, got %T", t, defaul[0])
			}
			err = Set(key, t)
			return t, err
		}
		return t, gorm.ErrRecordNotFound
	}

	// 先尝试直接反序列化
	if err = json.Unmarshal([]byte(rawValue), &t); err != nil {
		// 尝试通用解析后转换
		var generic any
		if err := json.Unmarshal([]byte(rawValue), &generic); err != nil {
			return t, err
		}
		val := reflect.ValueOf(&t).Elem()
		if err := convertAndSet(generic, val); err != nil {
			return t, err
		}
	}
	return t, nil
}

// GetMany 获取多个配置项，keys 为 map[key]defaultValue
// 如果 defaultValue 为 nil，则数据库不存在时不写入
// 如果 defaultValue 不为 nil，则数据库不存在时写入默认值
func GetMany(keys map[string]any) (map[string]any, error) {
	result := make(map[string]any)
	keyList := make([]string, 0, len(keys))
	for k := range keys {
		keyList = append(keyList, k)
	}
	if len(keyList) == 0 {
		return result, nil
	}
	rawItems, currentDB, gen, err := getCachedManyRaw(keyList)
	if err != nil {
		return nil, err
	}

	foundKeys := make(map[string]bool, len(rawItems))
	for k, rawVal := range rawItems {
		var parsed any
		if err := json.Unmarshal([]byte(rawVal), &parsed); err == nil {
			result[k] = parsed
			foundKeys[k] = true
		}
	}

	// 收集需要写入数据库的默认值
	var toInsert []ConfigItem
	for k, def := range keys {
		if _, found := foundKeys[k]; !found {
			if def != nil {
				result[k] = def
				// 序列化后加入待写入列表
				jsonBytes, err := json.Marshal(def)
				if err != nil {
					logger.Warn("config", "marshal default value failed", "key", k, "error", err)
					continue
				}
				toInsert = append(toInsert, ConfigItem{
					Key:   k,
					Value: string(jsonBytes),
				})
			}
		}
	}

	// 批量写入默认值到数据库（若并发 Set 已写入则不覆盖）
	if len(toInsert) > 0 {
		if err := currentDB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&toInsert).Error; err != nil {
			logger.Warn("config", "batch insert default config failed", "error", err)
		} else {
			putCachedDefaultsIfGen(currentDB, gen, toInsert)
		}
	}

	return result, nil
}

// GetManyAs 将多个配置项映射到一个结构体中，json tag 作为 Key
// 支持 default tag 作为默认值，如果数据库中不存在且有 default tag 则写入数据库
// 没有 default tag 的字段使用零值，不写入数据库
func GetManyAs[T any]() (*T, error) {
	var t T
	val := reflect.ValueOf(&t).Elem()
	typ := val.Type()

	type fieldInfo struct {
		index      int
		key        string
		hasDefault bool
		defaultVal string
	}

	fields := make([]fieldInfo, 0)
	keys := make([]string, 0)

	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		// 解析 json tag，处理 "key,omitempty" 格式
		key := strings.Split(jsonTag, ",")[0]
		if key == "" || key == "-" {
			continue
		}

		defaultTag := field.Tag.Get("default")
		// 检查是否显式定义了 default tag (即使值为空)
		_, hasDefault := field.Tag.Lookup("default")

		fields = append(fields, fieldInfo{
			index:      i,
			key:        key,
			hasDefault: hasDefault,
			defaultVal: defaultTag,
		})
		keys = append(keys, key)
	}

	if len(keys) == 0 {
		return &t, nil
	}

	foundItems, currentDB, gen, err := getCachedManyRaw(keys)
	if err != nil {
		return nil, err
	}

	// 需要写入数据库的新配置项
	var toInsert []ConfigItem

	for _, fi := range fields {
		fieldVal := val.Field(fi.index)
		if !fieldVal.CanSet() {
			continue
		}

		if dbValue, found := foundItems[fi.key]; found {
			// 数据库中存在，使用数据库值
			if err := unmarshalToField(dbValue, fieldVal); err != nil {
				logger.Warn("config", "unmarshal config failed", "key", fi.key, "error", err)
			}
		} else if fi.hasDefault {
			// 数据库中不存在，但有 default tag，解析默认值并写入数据库
			if err := parseDefaultToField(fi.defaultVal, fieldVal); err != nil {
				logger.Warn("config", "parse default value failed", "key", fi.key, "error", err)
				continue
			}
			// 序列化后写入数据库
			jsonBytes, err := json.Marshal(fieldVal.Interface())
			if err != nil {
				logger.Warn("config", "marshal default value failed", "key", fi.key, "error", err)
				continue
			}
			toInsert = append(toInsert, ConfigItem{
				Key:   fi.key,
				Value: string(jsonBytes),
			})
		}
		// 没有 default tag 且数据库中不存在，保持零值，不写入数据库
	}

	// 批量写入默认值到数据库（若并发 Set 已写入则不覆盖）
	if len(toInsert) > 0 {
		if err := currentDB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&toInsert).Error; err != nil {
			logger.Warn("config", "batch insert default config failed", "error", err)
		} else {
			putCachedDefaultsIfGen(currentDB, gen, toInsert)
		}
	}

	return &t, nil
}

// unmarshalToField 将 JSON 字符串反序列化到字段，支持数值类型转换
func unmarshalToField(jsonStr string, fieldVal reflect.Value) error {
	target := reflect.New(fieldVal.Type()).Interface()
	if err := json.Unmarshal([]byte(jsonStr), target); err != nil {
		// 尝试通用解析后转换
		var generic any
		if err := json.Unmarshal([]byte(jsonStr), &generic); err != nil {
			return err
		}
		return convertAndSet(generic, fieldVal)
	}
	fieldVal.Set(reflect.ValueOf(target).Elem())
	return nil
}

// parseDefaultToField 解析 default tag 值到字段
func parseDefaultToField(defaultVal string, fieldVal reflect.Value) error {
	kind := fieldVal.Kind()

	switch kind {
	case reflect.String:
		fieldVal.SetString(defaultVal)
	case reflect.Bool:
		fieldVal.SetBool(defaultVal == "true" || defaultVal == "1")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var v int64
		if defaultVal != "" {
			if _, err := fmt.Sscanf(defaultVal, "%d", &v); err != nil {
				// 尝试解析浮点数后转换
				var f float64
				if _, err := fmt.Sscanf(defaultVal, "%f", &f); err != nil {
					return err
				}
				v = int64(f)
			}
		}
		fieldVal.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var v uint64
		if defaultVal != "" {
			if _, err := fmt.Sscanf(defaultVal, "%d", &v); err != nil {
				var f float64
				if _, err := fmt.Sscanf(defaultVal, "%f", &f); err != nil {
					return err
				}
				v = uint64(f)
			}
		}
		fieldVal.SetUint(v)
	case reflect.Float32, reflect.Float64:
		var v float64
		if defaultVal != "" {
			if _, err := fmt.Sscanf(defaultVal, "%f", &v); err != nil {
				return err
			}
		}
		fieldVal.SetFloat(v)
	default:
		// 对于复杂类型，尝试 JSON 解析
		if defaultVal == "" {
			return nil // 保持零值
		}
		target := reflect.New(fieldVal.Type()).Interface()
		if err := json.Unmarshal([]byte(defaultVal), target); err != nil {
			return err
		}
		fieldVal.Set(reflect.ValueOf(target).Elem())
	}
	return nil
}

// convertAndSet 通用类型转换并设置字段值
func convertAndSet(val any, fieldVal reflect.Value) error {
	if val == nil {
		return nil
	}

	targetType := fieldVal.Type()
	v := reflect.ValueOf(val)

	// 直接类型匹配
	if v.Type().AssignableTo(targetType) {
		fieldVal.Set(v)
		return nil
	}

	// 类型可转换
	if v.Type().ConvertibleTo(targetType) {
		fieldVal.Set(v.Convert(targetType))
		return nil
	}

	// 数值类型特殊处理 (JSON 数字默认解析为 float64)
	if f, ok := val.(float64); ok {
		switch fieldVal.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fieldVal.SetInt(int64(f))
			return nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fieldVal.SetUint(uint64(f))
			return nil
		case reflect.Float32, reflect.Float64:
			fieldVal.SetFloat(f)
			return nil
		}
	}

	// JSON 回环转换
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	target := reflect.New(targetType).Interface()
	if err := json.Unmarshal(b, target); err != nil {
		return err
	}
	fieldVal.Set(reflect.ValueOf(target).Elem())
	return nil
}

func GetAll() (map[string]any, error) {
	rawItems, err := getCachedAllRaw()
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(rawItems))
	for k, rawVal := range rawItems {
		var parsed any
		if err := json.Unmarshal([]byte(rawVal), &parsed); err == nil {
			result[k] = parsed
		}
	}
	return result, nil
}

// Set 设置单个配置
func Set(key string, value any) error {
	currentDB, err := ensureConfigCacheLoaded()
	if err != nil {
		return err
	}
	oldVal := map[string]any{}
	if oldRaw, ok, _ := getCachedRaw(key); ok {
		var parsed any
		if err := json.Unmarshal([]byte(oldRaw), &parsed); err == nil {
			oldVal[key] = parsed
		}
	}

	bytes, err := json.Marshal(value)
	if err != nil {
		return err
	}

	item := ConfigItem{
		Key:   key,
		Value: string(bytes),
	}

	err = currentDB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&item).Error
	if err != nil {
		return err
	}
	putCachedWrite(currentDB, []ConfigItem{item})

	newVal := map[string]any{key: value}
	publishEvent(oldVal, newVal)
	return nil
}

func SetMany(cst map[string]any) error {
	var items []ConfigItem
	for k, v := range cst {
		bytes, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal key %s failed: %w", k, err)
		}
		items = append(items, ConfigItem{
			Key:   k,
			Value: string(bytes),
		})
	}
	if len(items) == 0 {
		return nil
	}

	currentDB, err := ensureConfigCacheLoaded()
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(items))
	newVal := make(map[string]any, len(items))
	for _, it := range items {
		keys = append(keys, it.Key)
		var parsed any
		if err := json.Unmarshal([]byte(it.Value), &parsed); err == nil {
			newVal[it.Key] = parsed
		}
	}

	oldVal := map[string]any{}
	if oldRaw, _, _, rawErr := getCachedManyRaw(keys); rawErr == nil {
		for k, raw := range oldRaw {
			var parsed any
			if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
				oldVal[k] = parsed
			}
		}
	}

	err = currentDB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&items).Error
	if err != nil {
		return err
	}
	putCachedWrite(currentDB, items)

	publishEvent(oldVal, newVal)
	return nil
}

type ConfigEvent struct {
	Old map[string]any
	New map[string]any
}

func (e ConfigEvent) IsChanged(key string) bool {
	oldVal, oldOk := e.Old[key]
	newVal, newOk := e.New[key]
	if !oldOk && !newOk {
		return false
	}
	if oldOk != newOk {
		return true
	}
	return !reflect.DeepEqual(oldVal, newVal)
}

func IsChangedT[T any](e ConfigEvent, key string) (bool, T) {
	changed := e.IsChanged(key)
	var zero T

	val, ok := e.New[key]
	if !ok {
		val, ok = e.Old[key]
		if !ok {
			return changed, zero
		}
	}
	if val == nil {
		return changed, zero
	}

	// Fast path: direct assertion.
	if cast, ok := val.(T); ok {
		return changed, cast
	}

	// Try reflection-based conversion (covers numeric conversions, etc.).
	targetType := reflect.TypeOf((*T)(nil)).Elem()
	v := reflect.ValueOf(val)
	if v.IsValid() {
		if v.Type().AssignableTo(targetType) {
			return changed, v.Interface().(T)
		}
		if v.Type().ConvertibleTo(targetType) {
			converted := v.Convert(targetType)
			return changed, converted.Interface().(T)
		}
	}

	// Fallback: JSON roundtrip for map/struct and other loosely typed values.
	if b, err := json.Marshal(val); err == nil {
		var out T
		if err := json.Unmarshal(b, &out); err == nil {
			return changed, out
		}
	}

	return changed, zero
}

// ConfigSubscriber handles config events
type ConfigSubscriber func(event ConfigEvent)

var (
	subscribersMu sync.RWMutex
	subscribers   []ConfigSubscriber
)

// Subscribe registers a subscriber for all config events.
func Subscribe(subscriber ConfigSubscriber) {
	subscribersMu.Lock()
	defer subscribersMu.Unlock()
	subscribers = append(subscribers, subscriber)
}

// publishEvent notifies all subscribers of a config change.
func publishEvent(oldVal, newVal map[string]any) {
	subscribersMu.RLock()
	defer subscribersMu.RUnlock()
	for _, sub := range subscribers {
		event := ConfigEvent{Old: oldVal, New: newVal}
		go sub(event)
	}
}
