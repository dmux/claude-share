// Package noise implements the end-to-end secure channel using the Noise
// Protocol Framework, pattern Noise_NNpsk0_25519_ChaChaPoly_BLAKE2s.
//
// Both peers use ephemeral X25519 keys (forward secrecy) and mix a pre-shared
// key derived from the operator's shared token at position 0. Mixing the PSK
// authenticates both peers: a party without the token cannot complete the
// handshake, so a passive or active man-in-the-middle is rejected.
//
// See the package README/threat model: because Claude runs on the server and
// must read the project files, the server is a legitimate endpoint that handles
// plaintext. This channel secures the wire between the two real endpoints; it is
// not a design where the server is blind.
package noise

import (
	"crypto/rand"
	"errors"
	"hash"
	"io"
	"sync"

	"github.com/flynn/noise"
	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/hkdf"
)

// PSKLen is the required pre-shared-key length for Noise.
const PSKLen = 32

// hkdfInfo domain-separates our PSK derivation.
var hkdfInfo = []byte("claude-share/psk/v1")

// DerivePSK turns an operator token of any length into a 32-byte Noise PSK using
// HKDF-BLAKE2s. An empty token is rejected so a channel is never established
// without a shared secret.
func DerivePSK(token string) ([]byte, error) {
	if token == "" {
		return nil, errors.New("noise: empty token")
	}
	newHash := func() hash.Hash {
		h, err := blake2s.New256(nil)
		if err != nil {
			panic(err) // blake2s.New256(nil) never errors
		}
		return h
	}
	r := hkdf.New(newHash, []byte(token), nil /*salt*/, hkdfInfo)
	psk := make([]byte, PSKLen)
	if _, err := io.ReadFull(r, psk); err != nil {
		return nil, err
	}
	return psk, nil
}

// Config parameterizes a handshake.
type Config struct {
	PSK       []byte // must be PSKLen bytes
	Initiator bool
}

// Channel is an established secure channel. Seal and Open are each serialized
// internally, but callers must still Seal in the same order records are written
// to the wire (and Open in receive order) because AEAD nonces are sequential.
type Channel struct {
	sendMu sync.Mutex
	recvMu sync.Mutex
	send   *noise.CipherState
	recv   *noise.CipherState
}

var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// Handshake runs the NNpsk0 handshake to completion using the provided message
// transport callbacks, returning an established Channel. send transmits one
// handshake message; recv returns the next one.
func Handshake(cfg Config, send func([]byte) error, recv func() ([]byte, error)) (*Channel, error) {
	if len(cfg.PSK) != PSKLen {
		return nil, errors.New("noise: PSK must be 32 bytes")
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           cipherSuite,
		Random:                rand.Reader,
		Pattern:               noise.HandshakeNN,
		Initiator:             cfg.Initiator,
		PresharedKey:          cfg.PSK,
		PresharedKeyPlacement: 0, // psk0
	})
	if err != nil {
		return nil, err
	}

	var csEnc, csDec *noise.CipherState
	if cfg.Initiator {
		// -> e
		msg, _, _, err := hs.WriteMessage(nil, nil)
		if err != nil {
			return nil, err
		}
		if err := send(msg); err != nil {
			return nil, err
		}
		// <- e, ee
		in, err := recv()
		if err != nil {
			return nil, err
		}
		if _, cs0, cs1, err := hs.ReadMessage(nil, in); err != nil {
			return nil, err
		} else {
			// cs0: initiator->responder (we encrypt), cs1: responder->initiator (we decrypt)
			csEnc, csDec = cs0, cs1
		}
	} else {
		// -> e
		in, err := recv()
		if err != nil {
			return nil, err
		}
		if _, _, _, err := hs.ReadMessage(nil, in); err != nil {
			return nil, err
		}
		// <- e, ee
		msg, cs0, cs1, err := hs.WriteMessage(nil, nil)
		if err != nil {
			return nil, err
		}
		if err := send(msg); err != nil {
			return nil, err
		}
		// cs0: initiator->responder (we decrypt), cs1: responder->initiator (we encrypt)
		csEnc, csDec = cs1, cs0
	}
	if csEnc == nil || csDec == nil {
		return nil, errors.New("noise: handshake did not yield cipher states")
	}
	return &Channel{send: csEnc, recv: csDec}, nil
}

// Seal encrypts one plaintext record. The plaintext must not exceed
// protocol.MaxRecordPlaintext; callers chunk larger payloads.
func (c *Channel) Seal(plaintext []byte) ([]byte, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.send.Encrypt(nil, nil, plaintext)
}

// Open decrypts one record. It fails if the ciphertext was tampered with or if
// records arrive out of order.
func (c *Channel) Open(ciphertext []byte) ([]byte, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	return c.recv.Decrypt(nil, nil, ciphertext)
}
