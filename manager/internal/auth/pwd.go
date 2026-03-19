package auth

import "golang.org/x/crypto/bcrypt"

type PasswordGenerator interface {
	Hash(password string) (string, error)
	Equivalent(hash, password string) bool
}

type BcryptPasswordGenerator struct{}

func (BcryptPasswordGenerator) Hash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return "", err
	}

	return string(hash), nil
}

func (BcryptPasswordGenerator) Equivalent(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func NewPasswordGenerator() PasswordGenerator {
	return BcryptPasswordGenerator{}
}
