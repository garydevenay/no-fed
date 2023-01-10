package main

import (
	"crypto/rsa"
	"github.com/fiatjaf/litepub"
)

type Keys struct {
	PrivateKey *rsa.PrivateKey
	PublicKey  *rsa.PublicKey
}

func GenerateKeys(secret string) (*Keys, error) {
	var seed [4]byte
	copy(seed[:], secret)
	privateKey, err := litepub.GeneratePrivateKey(seed)

	keys := Keys{
		PrivateKey: privateKey,
		PublicKey:  &privateKey.PublicKey,
	}

	return &keys, err
}

func (k *Keys) GetPublicKeyPEM() (string, error) {
	return litepub.PublicKeyToPEM(k.PublicKey)
}