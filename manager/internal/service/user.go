package service

import (
	"context"

	pb "manager/api/user/v1"
	"manager/internal/biz"

	"github.com/go-kratos/kratos/v2/log"
)

type UserService struct {
	pb.UnimplementedUserServer

	uc  *biz.UserUsecase
	log *log.Helper
}

func (s *UserService) RegisterUser(ctx context.Context, req *pb.RegisterUserRequest) (*pb.RegisterUserReply, error) {
	s.log.WithContext(ctx).Infof("RegisterUser for %s", req.Username)

	user := biz.User{
		Username: req.Username,
		Password: req.Password,
	}

	if err := s.uc.Create(ctx, &user); err != nil {
		return nil, err
	}

	return &pb.RegisterUserReply{}, nil
}

func (s *UserService) LoginUser(ctx context.Context, req *pb.LoginUserRequest) (*pb.LoginUserReply, error) {
	s.log.WithContext(ctx).Infof("LoginUser for %s", req.Username)

	user := biz.User{
		Username: req.Username,
		Password: req.Password,
	}

	authToken, err := s.uc.Authenticate(ctx, &user)
	if err != nil {
		return nil, err
	}

	return &pb.LoginUserReply{Token: authToken.Token}, nil
}

func (s *UserService) UpdateUserPassword(ctx context.Context, req *pb.UpdateUserPasswordRequest) (*pb.UpdateUserPasswordReply, error) {
	s.log.WithContext(ctx).Infof("UpdateUserPassword for %s", req.Username)

	pwdUp := biz.PasswordUpdate{
		Username:        req.Username,
		CurrentPassword: req.CurrentPassword,
		NewPassword:     req.NewPassword,
	}

	if err := s.uc.UpdatePassword(ctx, &pwdUp); err != nil {
		return nil, err
	}

	return &pb.UpdateUserPasswordReply{}, nil
}

func NewUserService(uc *biz.UserUsecase, logger log.Logger) *UserService {
	return &UserService{
		uc:  uc,
		log: log.NewHelper(logger),
	}
}
