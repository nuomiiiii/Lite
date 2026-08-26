package oauth

import (
	_ "github.com/nuomiiiii/lite/web/oauth/factory"
	_ "github.com/nuomiiiii/lite/web/oauth/generic"
	_ "github.com/nuomiiiii/lite/web/oauth/github"
	_ "github.com/nuomiiiii/lite/web/oauth/qq"
)

func All() {
	//empty function to ensure all OIDC providers are registered
}
