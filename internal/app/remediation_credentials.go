package app

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

const integrationTokenLimit = 16384

var integrationChannel = regexp.MustCompile(`^[CG][A-Z0-9]{2,127}$`)

func newIntegrationCipher(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, nil
	}
	if len(key) != 32 {
		return nil, errors.New("integration encryption requires an explicit 32-byte key")
	}
	owned := bytes.Clone(key)
	defer clear(owned)
	block, err := aes.NewCipher(owned)
	if err != nil {
		return nil, errors.New("initialize integration encryption failed")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize integration encryption failed")
	}
	return aead, nil
}

func validIntegrationToken(token string) bool {
	return token != "" && len(token) <= integrationTokenLimit && strings.TrimSpace(token) == token &&
		!strings.ContainsFunc(token, unicode.IsControl)
}

func integrationCredentialAAD(workspace, connection string) []byte {
	return []byte("aspm/slack-credential/v1\x00" + workspace + "\x00" + connection)
}

func sealIntegrationToken(aead cipher.AEAD, workspace, connection, token string) ([]byte, error) {
	return sealCredential(aead, integrationCredentialAAD(workspace, connection), token)
}

func sealCredential(aead cipher.AEAD, aad []byte, token string) ([]byte, error) {
	if aead == nil {
		return nil, errUnavailable
	}
	envelope := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(token)+aead.Overhead())
	envelope[0] = 1
	if _, err := rand.Read(envelope[1:]); err != nil {
		return nil, errUnavailable
	}
	plain := []byte(token)
	defer clear(plain)
	return aead.Seal(envelope, envelope[1:], plain, aad), nil
}

func openIntegrationToken(aead cipher.AEAD, workspace, connection string, envelope []byte) ([]byte, error) {
	return openCredential(aead, integrationCredentialAAD(workspace, connection), envelope)
}

func openCredential(aead cipher.AEAD, aad []byte, envelope []byte) ([]byte, error) {
	if aead == nil || len(envelope) < 1+aead.NonceSize()+aead.Overhead()+1 ||
		len(envelope) > 1+aead.NonceSize()+aead.Overhead()+integrationTokenLimit || envelope[0] != 1 {
		return nil, errUnavailable
	}
	end := 1 + aead.NonceSize()
	plain, err := aead.Open(nil, envelope[1:end], envelope[end:], aad)
	if err != nil {
		return nil, errUnavailable
	}
	if !validIntegrationToken(string(plain)) {
		clear(plain)
		return nil, errUnavailable
	}
	return plain, nil
}
