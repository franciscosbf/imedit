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
		"failed to request user registration",
	)

	u, err := s.edb.User.
		Query().
		Where(user.Username("username")).
		Only(context.Background())
	assert.NoError(s.T(), err, "user not found")

	assert.Equal(s.T(), "username", u.Username)
	assert.True(s.T(), s.pwdGen.Equivalent(u.Password, request.Password), "passwords don't match")
}

func (s *IntegrationSuite) TestRepeatedUserCreation() {
	request := pb.RegisterUserRequest{
		Username: "username",
		Password: "password",
	}
	response := pb.RegisterUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/register", &request, &response),
		"failed to request user registration",
	)

	err := s.sendJsonRequest("POST", "/v1/user/register", &request, &response)
	assert.Error(s.T(), err, "it was supposed to raise an error when a user attempt to register more than once")
	s.verifyResponseError(err, http.StatusBadRequest, "USERNAME_ALREADY_EXISTS", "username username is already registered")
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
		"failed to request user login",
	)

	signedJwt, err := s.jwtAuth.Sign(tu.username)
	assert.NoError(s.T(), err, "failed to sign expected token")

	assert.NotEmpty(s.T(), signedJwt, response.Token, "JWT token is empty")
}

func (s *IntegrationSuite) TestInvalidUserLogin() {
	tu := &testUser{
		username: "username",
		password: "password",
	}

	s.registerUser(tu)

	var (
		err     error
		request pb.LoginUserRequest
	)
	response := pb.LoginUserReply{}

	verifyResponseError := func(err error) {
		s.verifyResponseError(err, http.StatusBadRequest, "INVALID_USERNAME_OR_PASSWORD", "username or password is invalid")
	}

	request = pb.LoginUserRequest{
		Username: "gibberish",
		Password: tu.password,
	}

	err = s.sendJsonRequest("POST", "/v1/user/login", &request, &response)
	assert.Error(s.T(), err, "it was supposed to raise an error when a user attempt to login with an invalid username")
	verifyResponseError(err)

	request = pb.LoginUserRequest{
		Username: tu.username,
		Password: "gibberish",
	}

	err = s.sendJsonRequest("POST", "/v1/user/login", &request, &response)
	assert.Error(s.T(), err, "it was supposed to raise an error when a user attempt to login with an invalid password")
	verifyResponseError(err)
}

func (s *IntegrationSuite) TestUserPasswordChanged() {
	tu, bearerToken := s.registerAndLoginUser()

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
		"failed to request user password change",
	)

	u, err := s.edb.User.
		Query().
		Where(user.Username("username")).
		Only(context.Background())
	assert.NoError(s.T(), err, "user not found")

	assert.True(s.T(), s.pwdGen.Equivalent(u.Password, request.NewPassword), "passwords don't match")
}

func (s *IntegrationSuite) TestUserPasswordNotChanged() {
	tu, bearerToken := s.registerAndLoginUser()

	header := http.Header(map[string][]string{
		"Authorization": {bearerToken},
	})

	var (
		request pb.UpdateUserPasswordRequest
		err     error
	)
	response := pb.UpdateUserPasswordReply{}

	verifyResponseError := func(err error) {
		s.verifyResponseError(err, http.StatusNotFound, "USER_NOT_FOUND", "user was not found")
	}

	request = pb.UpdateUserPasswordRequest{
		Username:        "gibberish",
		CurrentPassword: tu.password,
		NewPassword:     "password2",
	}

	err = s.sendJsonRequest("PUT", "/v1/user/password", &request, &response, khttp.Header(&header))
	assert.Error(s.T(), err, "it was supposed to raise an error when a user attempt to insert new password with an invalid username")
	verifyResponseError(err)

	request = pb.UpdateUserPasswordRequest{
		Username:        tu.username,
		CurrentPassword: "gibberish",
		NewPassword:     "password2",
	}

	err = s.sendJsonRequest("PUT", "/v1/user/password", &request, &response, khttp.Header(&header))
	assert.Error(s.T(), err, "it was supposed to raise an error when a user attempt to insert new password with an invalid password")
	verifyResponseError(err)
}

func (s *IntegrationSuite) TestUserPasswordInvalidToken() {
	tu, bearerToken := s.registerAndLoginUser()

	request := pb.UpdateUserPasswordRequest{
		Username:        tu.username,
		CurrentPassword: tu.password,
		NewPassword:     "password2",
	}
	response := pb.UpdateUserPasswordReply{}

	var err error

	err = s.sendJsonRequest("PUT", "/v1/user/password", &request, &response)
	assert.Error(s.T(), err, "it was supposed to raise an error when authorization token isn't present")
	s.verifyResponseError(err, http.StatusUnauthorized, "UNAUTHORIZED", "JWT token is missing")

	header := http.Header(map[string][]string{
		"Authorization": {bearerToken + "gibberish"},
	})

	err = s.sendJsonRequest("PUT", "/v1/user/password", &request, &response, khttp.Header(&header))
	assert.Error(s.T(), err, "it was supposed to raise an error when authorization token is invalid ")
	s.verifyResponseError(err, http.StatusUnauthorized, "UNAUTHORIZED", "Fail to parse JWT token")
}
