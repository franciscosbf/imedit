package biz

import (
	"context"
	"sync"

	uv1 "manager/api/user/v1"
	"manager/internal/auth"
)

type User struct {
	Username string
	Password string
}

type PasswordUpdate struct {
	Username        string
	CurrentPassword string
	NewPassword     string
}

type AuthToken struct {
	Token string
}

type UserRepo interface {
	CreateUser(ctx context.Context, u *User) error
	GetUserPassword(ctx context.Context, username string) (string, error)
	UpdateUserPassword(
		ctx context.Context,
		username string,
		matches func(currentPassword string) bool,
		newPassword func() (string, error),
	) error
}

type UserUsecase struct {
	jwtAuth auth.JwtAuthenticator
	pwdGen  auth.PasswordGenerator
	repo    UserRepo
}

func (uu *UserUsecase) Create(ctx context.Context, u *User) error {
	password, err := uu.pwdGen.Hash(u.Password)
	if err != nil {
		return err
	}
	u.Password = string(password)

	return uu.repo.CreateUser(ctx, u)
}

func (uu *UserUsecase) Authenticate(ctx context.Context, u *User) (*AuthToken, error) {
	password, err := uu.repo.GetUserPassword(ctx, u.Username)
	if err != nil {
		return nil, err
	}

	if !uu.pwdGen.Equivalent(password, u.Password) {
		return nil, uv1.ErrorInvalidUsernameOrPassword("username or password is invalid")
	}

	token, err := uu.jwtAuth.Sign(u.Username)
	if err != nil {
		return nil, err
	}

	return &AuthToken{Token: token}, nil
}

func (uu *UserUsecase) UpdatePassword(ctx context.Context, pwdUp *PasswordUpdate) error {
	var (
		wg          sync.WaitGroup
		newPassword string
		err         error
	)

	wg.Go(func() {
		newPassword, err = uu.pwdGen.Hash(pwdUp.NewPassword)
	})

	return uu.repo.UpdateUserPassword(
		ctx,
		pwdUp.Username,
		func(password string) bool {
			return uu.pwdGen.Equivalent(password, pwdUp.CurrentPassword)
		},
		func() (string, error) {
			wg.Wait()

			return newPassword, err
		})
}

func NewUserUsecase(jwtAuth auth.JwtAuthenticator, pwdGen auth.PasswordGenerator, repo UserRepo) *UserUsecase {
	return &UserUsecase{jwtAuth, pwdGen, repo}
}
