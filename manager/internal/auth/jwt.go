package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"manager/internal/conf"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/auth/jwt"
	"github.com/go-kratos/kratos/v2/transport"
	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const (
	bearerWord       string = "Bearer"
	authorizationKey string = "Authorization"
)

type JwtAuthenticator interface {
	Validator() middleware.Middleware
	Sign(sub string) (string, error)
}

type keyType int

func (kt keyType) isPublic() bool {
	return kt == publicKey
}

const (
	publicKey keyType = iota
	privateKey
)

func getSigningMethod(alg string) (jwtv5.SigningMethod, error) {
	method := jwtv5.GetSigningMethod(alg)
	if method == nil {
		return nil, fmt.Errorf("could not find JWT signing method for algorithm %s", alg)
	}

	return method, nil
}

func loadRawKey(path string) ([]byte, error) {
	rawKey, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load key %s", path)
	}

	return rawKey, nil
}

func parseKey(alg string, rawKey []byte, kt keyType) (any, error) {
	switch alg {
	case "ES256", "ES384", "ES512":
		if kt.isPublic() {
			return jwtv5.ParseECPublicKeyFromPEM(rawKey)
		} else {
			return jwtv5.ParseECPrivateKeyFromPEM(rawKey)
		}
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		if kt.isPublic() {
			return jwtv5.ParseRSAPublicKeyFromPEM(rawKey)
		} else {
			return jwtv5.ParseRSAPrivateKeyFromPEM(rawKey)
		}
	case "EdDSA":
		if kt.isPublic() {
			return jwtv5.ParseEdPublicKeyFromPEM(rawKey)
		} else {
			return jwtv5.ParseEdPrivateKeyFromPEM(rawKey)
		}
	default:
		keyType := ""
		if kt.isPublic() {
			keyType = "public"
		} else {
			keyType = "private"
		}

		return nil, fmt.Errorf("could not parse %s key due to unrecognized algorithm %s", keyType, alg)
	}
}

func testTokenAuth(method jwtv5.SigningMethod, privateKey, publicKey any) error {
	signedToken, err := jwtv5.New(method).SignedString(privateKey)
	if err != nil {
		return fmt.Errorf("token authentication test failed during "+
			"signing with algorithm %s and key pair: %v", method.Alg(), err)
	}

	keyFunc := func(*jwtv5.Token) (any, error) {
		return publicKey, nil
	}
	options := jwtv5.WithValidMethods([]string{method.Alg()})
	if _, err := jwtv5.Parse(signedToken, keyFunc, options); err != nil {
		return fmt.Errorf("token authentication test failed during "+
			"parsing with algorithm %s and key pair: %v", method.Alg(), err)
	}

	return nil
}

type claimsMeta struct {
	issuer     string
	expiration time.Duration
}

type JwtKeyAuthenticator struct {
	method          jwtv5.SigningMethod
	privKey, pubKey any
	claims          claimsMeta
}

func (ja *JwtKeyAuthenticator) Validator() middleware.Middleware {
	keyFunc := func(token *jwtv5.Token) (any, error) {
		return ja.pubKey, nil
	}

	options := []jwtv5.ParserOption{
		jwtv5.WithIssuer(ja.claims.issuer),
		jwtv5.WithExpirationRequired(),
		jwtv5.WithValidMethods([]string{ja.method.Alg()}),
	}

	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if header, ok := transport.FromServerContext(ctx); ok {
				auths := strings.SplitN(header.RequestHeader().Get(authorizationKey), " ", 2)
				if len(auths) != 2 || !strings.EqualFold(auths[0], bearerWord) {
					return nil, jwt.ErrMissingJwtToken
				}
				jwtToken := auths[1]
				var (
					tokenInfo *jwtv5.Token
					err       error
				)
				tokenInfo, err = jwtv5.Parse(jwtToken, keyFunc, options...)
				if err != nil {
					if errors.Is(err, jwtv5.ErrTokenMalformed) || errors.Is(err, jwtv5.ErrTokenUnverifiable) {
						return nil, jwt.ErrTokenInvalid
					}
					if errors.Is(err, jwtv5.ErrTokenNotValidYet) || errors.Is(err, jwtv5.ErrTokenExpired) {
						return nil, jwt.ErrTokenExpired
					}
					return nil, jwt.ErrTokenParseFail
				}

				if !tokenInfo.Valid {
					return nil, jwt.ErrTokenInvalid
				}
				if tokenInfo.Method != ja.method {
					return nil, jwt.ErrUnSupportSigningMethod
				}
				ctx = jwt.NewContext(ctx, tokenInfo.Claims)
				return handler(ctx, req)
			}
			return nil, jwt.ErrWrongContext
		}
	}
}

func (ja *JwtKeyAuthenticator) Sign(sub string) (string, error) {
	claims := jwtv5.MapClaims{
		"sub": sub,
		"iss": ja.claims.issuer,
		"exp": time.Now().Add(ja.claims.expiration).Unix(),
	}
	token := jwtv5.NewWithClaims(ja.method, claims)

	return token.SignedString(ja.privKey)
}

func NewJwtAuthenticator(c *conf.Auth) (JwtAuthenticator, error) {
	method, err := getSigningMethod(c.Algorithm)
	if method == nil {
		return nil, err
	}

	rawPrivateKey, err := loadRawKey(c.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	privKey, err := parseKey(c.Algorithm, rawPrivateKey, privateKey)
	if err != nil {
		return nil, err
	}

	rawPublicKey, err := loadRawKey(c.PublicKeyFile)
	if err != nil {
		return nil, err
	}
	pubKey, err := parseKey(c.Algorithm, rawPublicKey, publicKey)
	if err != nil {
		return nil, err
	}

	if err := testTokenAuth(method, privKey, pubKey); err != nil {
		return nil, err
	}

	issuer := c.Issuer
	expiration := time.Second * time.Duration(c.ExpirationTime.AsDuration())
	claims := claimsMeta{issuer, expiration}

	return &JwtKeyAuthenticator{method, privKey, pubKey, claims}, nil
}

func HasSub(ctx context.Context, sub string) bool {
	claims, has := jwt.FromContext(ctx)
	if !has {
		return false
	}

	csub, err := claims.GetSubject()
	return sub == csub && err == nil
}
