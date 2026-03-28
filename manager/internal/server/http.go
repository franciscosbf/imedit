package server

import (
	iv1 "manager/api/image/v1"
	uv1 "manager/api/user/v1"
	"manager/internal/auth"
	"manager/internal/conf"
	"manager/internal/service"

	validate "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/selector"
	"github.com/go-kratos/kratos/v2/transport/http"
)

// NewHTTPServer new an HTTP server.
func NewHTTPServer(c *conf.Server, jwtAuth auth.JwtAuthenticator, user *service.UserService, image *service.ImageService, logger log.Logger) *http.Server {
	opts := []http.ServerOption{
		http.Logger(logger),
		http.Middleware(
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
		opts = append(opts, http.Address(c.Http.Endpoint))
	}
	if c.Http.RequestTimeout != nil {
		opts = append(opts, http.Timeout(c.Http.RequestTimeout.AsDuration()))
	}
	srv := http.NewServer(opts...)
	uv1.RegisterUserHTTPServer(srv, user)
	iv1.RegisterImageHTTPServer(c, srv, image, logger)
	return srv
}
