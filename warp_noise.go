// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// The WARP engine's handshake probe: one WireGuard handshake initiation (Noise_IKpsk2) made with a device's own key,
// and the wait for the answer. A Cloudflare WARP endpoint answers only a key it has registered, so an answer proves
// the endpoint is reachable and speaks for the account; the round trip is the endpoint's latency from this machine.

package main

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"hash"
	"time"

	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

const (
	wgInitiationSize = 148
	wgResponseSize   = 92
	wgCookieSize     = 64

	wgConstruction = "Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s"
	wgIdentifier   = "WireGuard v1 zx2c4 Jason@zx2c4.com"
	wgLabelMAC1    = "mac1----"
)

func blake2sSum(parts ...[]byte) [32]byte {
	h, _ := blake2s.New256(nil)
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func hmacBlake2s(key []byte, parts ...[]byte) [32]byte {
	mac := hmac.New(func() hash.Hash { h, _ := blake2s.New256(nil); return h }, key)
	for _, p := range parts {
		mac.Write(p)
	}
	var out [32]byte
	mac.Sum(out[:0])
	return out
}

// kdf1 and kdf2 are the HKDF-style derivations of the WireGuard paper (section 5.4).
func kdf1(key [32]byte, input []byte) (t1 [32]byte) {
	t0 := hmacBlake2s(key[:], input)
	return hmacBlake2s(t0[:], []byte{1})
}

func kdf2(key [32]byte, input []byte) (t1, t2 [32]byte) {
	t0 := hmacBlake2s(key[:], input)
	t1 = hmacBlake2s(t0[:], []byte{1})
	t2 = hmacBlake2s(t0[:], t1[:], []byte{2})
	return t1, t2
}

func seal(key [32]byte, plain, ad []byte) []byte {
	var aead cipher.AEAD
	aead, _ = chacha20poly1305.New(key[:])
	return aead.Seal(nil, make([]byte, chacha20poly1305.NonceSize), plain, ad)
}

// tai64n is the timestamp a handshake carries: seconds since the TAI epoch (2^62 + 10 + Unix time) and nanoseconds.
func tai64n(t time.Time) []byte {
	out := make([]byte, 12)
	binary.BigEndian.PutUint64(out[:8], uint64(1<<62)+10+uint64(t.Unix()))
	binary.BigEndian.PutUint32(out[8:], uint32(t.Nanosecond()))
	return out
}

// buildInitiation makes a handshake initiation from the device key priv to the peer public key peerPub.
// sender is the index the answer will name.
func buildInitiation(priv, peerPub [32]byte, sender uint32, now time.Time) ([]byte, error) {
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	var ephPriv [32]byte
	if _, err := rand.Read(ephPriv[:]); err != nil {
		return nil, err
	}
	ephPub, err := curve25519.X25519(ephPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	c := blake2sSum([]byte(wgConstruction))
	h := blake2sSum(c[:], []byte(wgIdentifier))
	h = blake2sSum(h[:], peerPub[:])
	c = kdf1(c, ephPub)
	h = blake2sSum(h[:], ephPub)

	dh, err := curve25519.X25519(ephPriv[:], peerPub[:])
	if err != nil {
		return nil, err
	}
	var k [32]byte
	c, k = kdf2(c, dh)
	encStatic := seal(k, pub, h[:])
	h = blake2sSum(h[:], encStatic)

	dh, err = curve25519.X25519(priv[:], peerPub[:])
	if err != nil {
		return nil, err
	}
	c, k = kdf2(c, dh)
	encTime := seal(k, tai64n(now), h[:])

	msg := make([]byte, wgInitiationSize)
	binary.LittleEndian.PutUint32(msg[0:4], 1)
	binary.LittleEndian.PutUint32(msg[4:8], sender)
	copy(msg[8:40], ephPub)
	copy(msg[40:88], encStatic)
	copy(msg[88:116], encTime)

	macKey := blake2sSum([]byte(wgLabelMAC1), peerPub[:])
	mac, _ := blake2s.New128(macKey[:])
	mac.Write(msg[:116])
	mac.Sum(msg[116:116])
	return msg, nil
}

var errNotAnAnswer = errors.New("the packet is not an answer to the handshake")

// isAnswer reports whether pkt is the response (or a cookie reply) to the initiation that named sender.
func isAnswer(pkt []byte, sender uint32) error {
	if len(pkt) < 8 {
		return errNotAnAnswer
	}
	switch binary.LittleEndian.Uint32(pkt[0:4]) {
	case 2:
		if len(pkt) == wgResponseSize && binary.LittleEndian.Uint32(pkt[8:12]) == sender {
			return nil
		}
	case 3: // an endpoint under load asks for the cookie instead; it still proves the key is known
		if len(pkt) == wgCookieSize && binary.LittleEndian.Uint32(pkt[4:8]) == sender {
			return nil
		}
	}
	return errNotAnAnswer
}
