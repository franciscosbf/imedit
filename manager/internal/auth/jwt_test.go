package auth

import (
	"testing"
	"time"

	"manager/internal/conf"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"google.golang.org/protobuf/types/known/durationpb"
)

var authConfig = conf.Auth{
	Algorithm:      "ES256",
	PublicKeyFile:  "../../test/cert/ec256-public.pem",
	PrivateKeyFile: "../../test/cert/ec256-private.pem",
	Issuer:         "tester",
	ExpirationTime: durationpb.New(60 * time.Second),
}

func TestNewJwtAuthenticator(t *testing.T) {
	_, err := NewJwtAuthenticator(&authConfig)
	if err != nil {
		t.Fatalf("Failed to create JWT authenticator: %v", err)
	}
}

func TestJwtAuthenticatorSign(t *testing.T) {
	jwtAuth, err := NewJwtAuthenticator(&authConfig)
	if err != nil {
		t.Fatalf("Failed to create JWT authenticator: %v", err)
	}

	signedToken, err := jwtAuth.Sign("subject")
	if err != nil {
		t.Fatalf("Failed to sign JWT token: %v", err)
	}

	keyFunc := func(token *jwtv5.Token) (any, error) {
		return jwtAuth.(*JwtKeyAuthenticator).pubKey, nil
	}
	options := []jwtv5.ParserOption{
		jwtv5.WithSubject("subject"),
		jwtv5.WithIssuer("tester"),
		jwtv5.WithExpirationRequired(),
		jwtv5.WithValidMethods([]string{authConfig.Algorithm}),
	}
	parsedToken, err := jwtv5.Parse(signedToken, keyFunc, options...)
	if err != nil {
		t.Fatalf("Failed parse JWT token: %v", err)
	}
	if !parsedToken.Valid {
		t.Fatal("JWT token is invalid")
	}
	expectedSigningMethod := jwtv5.GetSigningMethod(authConfig.Algorithm)
	if parsedToken.Method != expectedSigningMethod {
		t.Fatalf("Unexpected JWT token method: got=%v, expected=%v",
			parsedToken.Method.Alg(), expectedSigningMethod.Alg())
	}
}
