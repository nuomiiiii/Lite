package factory

import "log"

var (
	providers                = make(map[string]IOidcProvider)
	ProvidersAdditionalItems = make(map[string][]IOidcProviderAdditionalItem)
)

func RegisterProvider(constructor OidcConstructor) {
	provider := constructor()
	if provider == nil {
		panic("OIDC provider constructor returned nil")
	}
	if _, exists := providers[provider.GetName()]; exists {
		log.Println("OIDC provider already registered: " + provider.GetName())
	}
	providers[provider.GetName()] = provider

}
