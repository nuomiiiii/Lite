package factory

import "context"

type IOidcProvider interface {
	GetName() string
	// 请务必返回 &Configuration{} 的指针
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
