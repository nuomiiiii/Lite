package item

import "reflect"

type Item struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     string `json:"type"`
	Options  string `json:"options"`
	Default  string `json:"default"`
	Help     string `json:"help"`
}

func Parse(v any) []Item {
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	var items []Item
	for i := 0; i < val.NumField(); i++ {
		field := val.Type().Field(i)
		item := Item{
			Name:     field.Tag.Get("json"),
			Required: field.Tag.Get("required") == "true",
			Type:     field.Type.Name(),
			Options:  field.Tag.Get("options"),
			Default:  field.Tag.Get("default"),
			Help:     field.Tag.Get("help"),
		}
		if item.Type == "" {
			item.Type = "string"
		}
		items = append(items, item)
	}
	return items
}
