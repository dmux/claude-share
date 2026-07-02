package noise

import (
	"bytes"
	"io"
	"testing"
)

// handshakePair drives an initiator and responder over in-memory channels. Each
// side closes its outbound channel when it returns, so a handshake failure on
// one side unblocks the other's recv with io.EOF instead of deadlocking (this
// mirrors the real transport, where a failed handshake closes the connection).
func handshakePair(t *testing.T, pskA, pskB []byte) (*Channel, *Channel, error) {
	t.Helper()
	a2b := make(chan []byte, 4)
	b2a := make(chan []byte, 4)

	sendA := func(b []byte) error { a2b <- append([]byte(nil), b...); return nil }
	sendB := func(b []byte) error { b2a <- append([]byte(nil), b...); return nil }
	recvA := func() ([]byte, error) {
		b, ok := <-b2a
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	}
	recvB := func() ([]byte, error) {
		b, ok := <-a2b
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	}

	type res struct {
		ch  *Channel
		err error
	}
	respCh := make(chan res, 1)
	go func() {
		ch, err := Handshake(Config{PSK: pskB, Initiator: false}, sendB, recvB)
		close(b2a)
		respCh <- res{ch, err}
	}()
	initCh, initErr := Handshake(Config{PSK: pskA, Initiator: true}, sendA, recvA)
	close(a2b)
	r := <-respCh
	if initErr != nil {
		return nil, nil, initErr
	}
	if r.err != nil {
		return nil, nil, r.err
	}
	return initCh, r.ch, nil
}

func TestRoundTrip(t *testing.T) {
	psk, err := DerivePSK("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := handshakePair(t, psk, psk)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// client -> server
	msg := []byte("create HELLO.md please")
	sealed, err := client.Seal(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := server.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("got %q want %q", got, msg)
	}

	// server -> client
	reply := []byte("done")
	sealed, err = server.Seal(reply)
	if err != nil {
		t.Fatal(err)
	}
	got, err = client.Open(sealed)
	if err != nil {
		t.Fatalf("open reply: %v", err)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("reply got %q want %q", got, reply)
	}
}

func TestTamperDetected(t *testing.T) {
	psk, _ := DerivePSK("hunter2")
	client, server, err := handshakePair(t, psk, psk)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	sealed, err := client.Seal([]byte("secret payload"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0xFF // flip a byte in the auth tag
	if _, err := server.Open(sealed); err == nil {
		t.Fatal("expected auth failure on tampered ciphertext")
	}
}

func TestWrongTokenRejected(t *testing.T) {
	pskA, _ := DerivePSK("correct-token")
	pskB, _ := DerivePSK("attacker-token")
	_, _, err := handshakePair(t, pskA, pskB)
	if err == nil {
		t.Fatal("expected handshake to fail with mismatched PSK")
	}
}

func TestEmptyTokenRejected(t *testing.T) {
	if _, err := DerivePSK(""); err == nil {
		t.Fatal("expected error for empty token")
	}
}
