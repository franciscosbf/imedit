//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.

package main

import (
	"manager/internal/auth"
	"manager/internal/biz"
	"manager/internal/conf"
	"manager/internal/data"
	"manager/internal/server"
	"manager/internal/service"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
)

// wireApp init kratos application.
func wireApp(*conf.Server, *conf.Auth, *conf.Data, log.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(auth.ProviderSet, server.ProviderSet, data.ProviderSet, biz.ProviderSet, service.ProviderSet, newApp))
}
