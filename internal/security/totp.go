package security

import "github.com/pquerna/otp/totp"

type TOTPProvider struct {
	Issuer string
}

func (provider TOTPProvider) NewSecret(email string) (string, string, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: provider.Issuer, AccountName: email})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func (provider TOTPProvider) Verify(secret, code string) bool {
	return totp.Validate(code, secret)
}