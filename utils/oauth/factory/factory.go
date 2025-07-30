package factory

var (
	providers = make(map[string]IOidcProvider)
)

func RegisterProvider(constructor OidcConstructor) {
	provider := constructor()
	if provider == nil {
		panic("OIDC provider constructor returned nil")
	}
	if _, exists := providers[provider.GetName()]; exists {
		panic("OIDC provider already registered: " + provider.GetName())
	}
	providers[provider.GetName()] = provider
}
