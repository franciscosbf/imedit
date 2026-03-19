package auth

import (
	"github.com/google/wire"
)

// ProviderSet is auth providers.
var ProviderSet = wire.NewSet(NewJwtAuthenticator, NewPasswordGenerator)
