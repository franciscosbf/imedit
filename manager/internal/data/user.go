package data

import (
	"context"
	"fmt"

	"manager/ent"
	"manager/ent/user"
	"manager/internal/biz"

	uv1 "manager/api/user/v1"

	"github.com/go-kratos/kratos/v2/log"
)

type userRepo struct {
	data *Data
	log  *log.Helper
}

func (ur *userRepo) withTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	tx, err := ur.data.db.Tx(ctx)
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			err = fmt.Errorf("%w: rolling back transaction: %v", err, rerr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

func (ur *userRepo) CreateUser(ctx context.Context, u *biz.User) error {
	_, err := ur.data.db.User.
		Create().
		SetUsername(u.Username).
		SetPassword(u.Password).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return uv1.ErrorUsernameAlreadyExists("username %s is already registered", u.Username)
	}

	return err
}

func (ur *userRepo) GetUserPassword(ctx context.Context, username string) (string, error) {
	u, err := ur.data.db.User.
		Query().
		Where(user.Username(username)).
		Select(user.FieldPassword).
		Only(ctx)

	if err != nil && ent.IsNotFound(err) {
		return "", nil
	}

	return u.Password, err
}

func (ur *userRepo) UpdateUserPassword(
	ctx context.Context,
	username string,
	matches func(currentPassword string) bool,
	newPassword func() (string, error),
) error {
	return ur.withTx(ctx, func(tx *ent.Tx) error {
		u, err := tx.User.
			Query().
			Where(user.Username(username)).
			Select(user.FieldPassword).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return uv1.ErrorUserNotFound("username %s was not found", username)
			} else {
				return err
			}
		}

		if !matches(u.Password) {
			return uv1.ErrorUserNotFound("username %s was not found", username)
		}

		newPassword, err := newPassword()
		if err != nil {
			return err
		}

		_, err = tx.User.
			Update().
			Where(user.Username(username)).
			SetPassword(newPassword).
			Save(ctx)

		return err
	})
}

func NewUserRepo(data *Data, logger log.Logger) biz.UserRepo {
	return &userRepo{
		data: data,
		log:  log.NewHelper(logger),
	}
}
