package session

import (
	"bytes"
	"testing"
	"time"

	"visitready/internal/domain"
)

func TestEncryptedSessionCodecDoesNotExposeHealthData(t *testing.T) {
	codec, err := newSessionCodec(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	original := domain.Session{
		ID: "encrypted", OwnerHash: "owner-digest", RawInput: "最近反复心悸和夜间出汗",
		Status: domain.StatusWaitingClarification, Revision: 4, ExpiresAt: time.Now().Add(time.Hour),
	}
	payload, err := codec.encode(original)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("心悸")) || bytes.Contains(payload, []byte(original.OwnerHash)) || bytes.HasPrefix(payload, []byte("{")) {
		t.Fatalf("encrypted payload exposes session content: %q", payload)
	}
	restored, legacy, err := codec.decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if legacy || restored.RawInput != original.RawInput || restored.OwnerHash != original.OwnerHash || restored.Revision != 4 {
		t.Fatalf("restored session = %#v, legacy = %t", restored, legacy)
	}
}

func TestEncryptedSessionCodecRejectsTampering(t *testing.T) {
	codec, err := newSessionCodec(bytes.Repeat([]byte{0x24}, 32))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := codec.encode(domain.Session{ID: "tamper", RawInput: "敏感健康内容"})
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0xff
	if _, _, err := codec.decode(payload); err == nil {
		t.Fatal("decode() error = nil after ciphertext tampering")
	}
}

func TestEncryptedSessionCodecRecognizesLegacyJSON(t *testing.T) {
	codec, err := newSessionCodec(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := encodeSession(domain.Session{ID: "legacy", RawInput: "旧格式健康内容", Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	restored, isLegacy, err := codec.decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !isLegacy || restored.ID != "legacy" || restored.RawInput != "旧格式健康内容" {
		t.Fatalf("legacy decode = %#v, legacy = %t", restored, isLegacy)
	}
}
