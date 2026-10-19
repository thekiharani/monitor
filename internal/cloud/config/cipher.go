package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

const cipherVersion = "v1:"

var ErrCiphertext = errors.New("value was not sealed by this application")

func Seal(key []byte, value string) (string, error) {
	gcm, err := sealer(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return cipherVersion + base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(value), nil)), nil
}

func Open(key []byte, value string) (string, error) {
	if len(value) < len(cipherVersion) || value[:len(cipherVersion)] != cipherVersion {
		return "", ErrCiphertext
	}
	raw, err := base64.RawStdEncoding.DecodeString(value[len(cipherVersion):])
	if err != nil {
		return "", ErrCiphertext
	}
	gcm, err := sealer(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrCiphertext
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", ErrCiphertext
	}
	return string(plain), nil
}

func sealer(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
