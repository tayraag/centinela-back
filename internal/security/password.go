package security

import "golang.org/x/crypto/bcrypt"

type BcryptHasher struct {
	Cost int
}

func (hasher BcryptHasher) Compare(password, encodedHash string) error {
	return bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(password))
}

func (hasher BcryptHasher) Hash(password string) (string, error) {
	cost := hasher.Cost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	return string(hash), err
}