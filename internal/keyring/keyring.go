package keyring

import (
	"github.com/chrischapin/discord-cli/internal/consts"
	"github.com/zalando/go-keyring"
)

const (
	keyringService = consts.Name
	keyringUser    = "token"
)

func GetToken() (string, error) {
	return keyring.Get(keyringService, keyringUser)
}

func SetToken(s string) error {
	return keyring.Set(keyringService, keyringUser, s)
}

func DeleteToken() error {
	return keyring.Delete(keyringService, keyringUser)
}
