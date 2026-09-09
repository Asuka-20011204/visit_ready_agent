package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"visitready/internal/domain"
)

var encryptedPayloadMagic = []byte("VRSE1")

type sessionCodec struct {
	aead cipher.AEAD
}

func newSessionCodec(key []byte) (*sessionCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("session encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize session AEAD: %w", err)
	}
	return &sessionCodec{aead: aead}, nil
}

func (c *sessionCodec) encode(item domain.Session) ([]byte, error) {
	plaintext, err := encodeSession(item)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate session nonce: %w", err)
	}
	result := make([]byte, 0, len(encryptedPayloadMagic)+len(nonce)+len(plaintext)+c.aead.Overhead())
	result = append(result, encryptedPayloadMagic...)
	result = append(result, nonce...)
	result = c.aead.Seal(result, nonce, plaintext, encryptedPayloadMagic)
	return result, nil
}

func (c *sessionCodec) decode(payload []byte) (domain.Session, bool, error) {
	if !bytes.HasPrefix(payload, encryptedPayloadMagic) {
		item, err := decodeSession(payload)
		return item, true, err
	}
	offset := len(encryptedPayloadMagic)
	if len(payload) < offset+c.aead.NonceSize()+c.aead.Overhead() {
		return domain.Session{}, false, errors.New("encrypted session payload is truncated")
	}
	nonce := payload[offset : offset+c.aead.NonceSize()]
	ciphertext := payload[offset+c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, encryptedPayloadMagic)
	if err != nil {
		return domain.Session{}, false, errors.New("decrypt session payload: authentication failed")
	}
	item, err := decodeSession(plaintext)
	return item, false, err
}
