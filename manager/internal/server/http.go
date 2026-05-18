package server

import (
	"context"
	"net/http"
	"strings"

	iv1 "manager/api/image/v1"
	uv1 "manager/api/user/v1"
	"manager/internal/auth"
	"manager/internal/conf"
	"manager/internal/service"

	validate "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/selector"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

// NewHTTPServer new an HTTP server.
func NewHTTPServer(
	c *conf.Server,
	jwtAuth auth.JwtAuthenticator,
	user *service.UserService,
	image *service.ImageService,
	logger log.Logger,
) *khttp.Server {
	opts := []khttp.ServerOption{
		khttp.Logger(logger),
		khttp.Middleware(
			recovery.Recovery(),
			selector.Server(jwtAuth.Validator()).
				Path("/v1/user/password").
				Prefix("/v1/image").
				Build(),
			validate.ProtoValidate(),
			iv1.ImageValidate(),
		),
	}
	if c.Http.Endpoint != "" {
		opts = append(opts, khttp.Address(c.Http.Endpoint))
	}
	if c.Http.RequestTimeout != nil {
		opts = append(opts, khttp.Timeout(0))
		timeout := c.Http.RequestTimeout.AsDuration()
		opts = append(opts, khttp.Filter(func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v1/image/ws") {
					ctx, cancel := context.WithTimeout(r.Context(), timeout)
					defer cancel()
					r = r.WithContext(ctx)
				}

				h.ServeHTTP(w, r)
			})
		}))
	}
	srv := khttp.NewServer(opts...)
	uv1.RegisterUserHTTPServer(srv, user)
	iv1.RegisterImageHTTPServer(c, srv, image, logger)
	return srv
}
