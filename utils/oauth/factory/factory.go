package factory

import (
	"log"
	"reflect"

	"github.com/komari-monitor/komari/utils/item"
)

var (
	providers                = make(map[string]IOidcProvider)
	providerConstructor      = make(map[string]OidcConstructor)
	providersAdditionalItems = make(map[string][]item.Item)
)

func RegisterOidcProvider(constructor OidcConstructor) {
	provider := constructor()
	providerConstructor[provider.GetName()] = constructor
	if provider == nil {
		panic("OIDC provider constructor returned nil")
	}
	if _, exists := providers[provider.GetName()]; exists {
		log.Println("OIDC provider already registered: " + provider.GetName())
	}
	providers[provider.GetName()] = provider

	// 使用反射来提取提供程序的配置字段
	config := provider.GetConfiguration()
	val := reflect.ValueOf(config)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	var items []item.Item
	for i := 0; i < val.NumField(); i++ {
		field := val.Type().Field(i)
		item := item.Item{
			Name:     field.Tag.Get("json"),
			Required: field.Tag.Get("required") == "true",
			Type:     field.Type.Name(),
			Options:  field.Tag.Get("options"),
		}
		if item.Type == "" {
			item.Type = "string"
		}
		items = append(items, item)
	}
	providersAdditionalItems[provider.GetName()] = items
}

func GetProviderConfigs() map[string][]item.Item {
	return providersAdditionalItems
}

func GetAllOidcProviders() map[string]IOidcProvider {
	return providers
}

func GetConstructor(name string) (OidcConstructor, bool) {
	constructor, exists := providerConstructor[name]
	return constructor, exists
}

func GetAllOidcProviderNames() []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	return names
}

func Initialize() {
	for _, provider := range providers {
		if err := provider.Init(); err != nil {
			log.Printf("Failed to initialize OIDC provider %s: %v", provider.GetName(), err)
		}
	}
}
