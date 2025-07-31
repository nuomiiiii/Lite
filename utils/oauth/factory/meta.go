package factory

import "context"

type IOidcProvider interface {
	GetName() string
	GetConfiguration() Configuration
	GetAuthorizationURL() string
	OnCallback(ctx context.Context, query map[string]string) (OidcCallback, error)
	Init() error
	Destroy() error
}

type OidcCallback struct {
	UserId string
}

type Configuration interface{}

type OidcConstructor func() IOidcProvider

type IOidcProviderAdditionalItem struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     string `json:"type"`
	Options  string `json:"options"`
}
