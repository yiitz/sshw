package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// startTestSSHServer starts a loopback SSH server that accepts any client and
// returns its address plus host key.
func startTestSSHServer(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					_ = conn.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for newChan := range chans {
					_ = newChan.Reject(ssh.Prohibited, "not supported")
				}
			}()
		}
	}()

	return ln.Addr().String(), signer.PublicKey()
}

func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func testRemote() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 22}
}

func testVerifier(path, input string, interactive bool) (*hostKeyVerifier, *bytes.Buffer) {
	out := new(bytes.Buffer)
	return &hostKeyVerifier{
		path:        path,
		in:          strings.NewReader(input),
		out:         out,
		interactive: interactive,
	}, out
}

func TestHostKeyKnownHostAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testPublicKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"example.com"}, key)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	v, out := testVerifier(path, "", false)
	if err := v.check("example.com:22", testRemote(), key); err != nil {
		t.Fatalf("known host rejected: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected prompt: %q", out.String())
	}
}

func TestHostKeyUnknownHostAcceptedAndSaved(t *testing.T) {
	// The known_hosts file and its directory do not exist yet.
	path := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	key := testPublicKey(t)
	const host = "example.com:2222"
	remote := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 2222}

	v, out := testVerifier(path, "yes\n", true)
	if err := v.check(host, remote, key); err != nil {
		t.Fatalf("confirming an unknown key failed: %v", err)
	}
	if !strings.Contains(out.String(), ssh.FingerprintSHA256(key)) {
		t.Fatalf("prompt does not show the key fingerprint: %q", out.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := knownhosts.Line([]string{host}, key)
	if !strings.Contains(string(data), entry) {
		t.Fatalf("known_hosts = %q, want a line with %q", data, entry)
	}
	if !strings.HasPrefix(entry, "[example.com]:2222 ") {
		t.Fatalf("entry for a non-default port was not normalized: %q", entry)
	}

	// A later connection accepts the saved key without prompting.
	v2, out2 := testVerifier(path, "", false)
	if err := v2.check(host, remote, key); err != nil {
		t.Fatalf("saved host rejected on reconnect: %v", err)
	}
	if out2.Len() != 0 {
		t.Fatalf("unexpected prompt: %q", out2.String())
	}
}

func TestHostKeyUnknownHostRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testPublicKey(t)

	v, _ := testVerifier(path, "no\n", true)
	err := v.check("example.com:22", testRemote(), key)
	if err == nil || !strings.Contains(err.Error(), "was not accepted") {
		t.Fatalf("error = %v, want a host key rejection", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("known_hosts was written after a rejection: %v", statErr)
	}
}

func TestHostKeyMismatchRejectedWithoutPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	known := testPublicKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"example.com"}, known)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	other := testPublicKey(t)

	// Even a "yes" on stdin must not override a changed host key.
	v, out := testVerifier(path, "yes\n", true)
	err := v.check("example.com:22", testRemote(), other)
	if err == nil || !strings.Contains(err.Error(), "remote host identification has changed") {
		t.Fatalf("error = %v, want a host key mismatch", err)
	}
	if out.Len() != 0 {
		t.Fatalf("prompted for a changed host key: %q", out.String())
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(data), knownhosts.Line([]string{"example.com"}, other)) {
		t.Fatal("mismatched key was appended to known_hosts")
	}
}

func TestHostKeyUnknownHostWithoutTTY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testPublicKey(t)

	v, _ := testVerifier(path, "yes\n", false)
	err := v.check("example.com:22", testRemote(), key)
	if err == nil || !strings.Contains(err.Error(), "host key verification failed") {
		t.Fatalf("error = %v, want a host key verification failure", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("known_hosts was written without confirmation: %v", statErr)
	}
}

// TestHostKeyCallbackDuringHandshake wires the verifier into a real SSH
// handshake: the first connection prompts and saves the key, the next one
// verifies it without prompting.
func TestHostKeyCallbackDuringHandshake(t *testing.T) {
	addr, hostKey := startTestSSHServer(t)
	path := filepath.Join(t.TempDir(), "known_hosts")
	dial := func(v *hostKeyVerifier) *ssh.Client {
		t.Helper()
		client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User:            "tester",
			HostKeyCallback: v.check,
			Timeout:         5 * time.Second,
		})
		if err != nil {
			t.Fatalf("dial %s failed: %v", addr, err)
		}
		return client
	}

	v, out := testVerifier(path, "yes\n", true)
	dial(v).Close()
	if !strings.Contains(out.String(), ssh.FingerprintSHA256(hostKey)) {
		t.Fatalf("prompt does not show the server key fingerprint: %q", out.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := knownhosts.Line([]string{addr}, hostKey)
	if !strings.Contains(string(data), entry) {
		t.Fatalf("known_hosts = %q, want a line with %q", data, entry)
	}

	v2, out2 := testVerifier(path, "", false)
	dial(v2).Close()
	if out2.Len() != 0 {
		t.Fatalf("unexpected prompt after the key was saved: %q", out2.String())
	}
}
