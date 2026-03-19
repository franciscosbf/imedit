package test

import (
	"context"
	"net/http"

	pb "manager/api/user/v1"
	"manager/ent/user"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/assert"
)

func (s *IntegrationSuite) TestUserCreation() {
	request := pb.RegisterUserRequest{
		Username: "username",
		Password: "password",
	}
	response := pb.RegisterUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/register", &request, &response),
		"failed to send request",
	)

	u, err := s.db.User.
		Query().
		Where(user.Username("username")).
		Only(context.Background())
	assert.NoError(s.T(), err, "user not found")

	assert.Equal(s.T(), "username", u.Username)
	assert.True(s.T(), s.pwdGen.Equivalent(u.Password, request.Password), "passwords don't match")
}

func (s *IntegrationSuite) TestUserLogin() {
	tu := &testUser{
		username: "username",
		password: "password",
	}

	s.registerUser(tu)

	request := pb.LoginUserRequest{
		Username: tu.username,
		Password: tu.password,
	}
	response := pb.LoginUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/login", &request, &response),
		"failed to send request",
	)

	signedJwt, err := s.jwtAuth.Sign(tu.username)
	assert.NoError(s.T(), err, "failed to sign expected token")

	assert.NotEmpty(s.T(), signedJwt, response.Token, "JWT token is empty")
}

func (s *IntegrationSuite) TestUserPasswordChanged() {
	tu := &testUser{
		username: "username",
		password: "password",
	}

	s.registerUser(tu)
	bearerToken := s.loginUser(tu)

	request := pb.UpdateUserPasswordRequest{
		Username:        tu.username,
		CurrentPassword: tu.password,
		NewPassword:     "password2",
	}
	response := pb.UpdateUserPasswordReply{}

	header := http.Header(map[string][]string{
		"Authorization": {bearerToken},
	})

	assert.NoError(
		s.T(),
		s.sendJsonRequest("PUT", "/v1/user/password", &request, &response, khttp.Header(&header)),
		"failed to send request",
	)

	u, err := s.db.User.
		Query().
		Where(user.Username("username")).
		Only(context.Background())
	assert.NoError(s.T(), err, "user not found")

	assert.True(s.T(), s.pwdGen.Equivalent(u.Password, request.NewPassword), "passwords don't match")
}
