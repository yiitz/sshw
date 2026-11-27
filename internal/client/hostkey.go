package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/yiitz/sshw/pkg/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// newHostKeyCallback returns a callback that verifies server host keys against
// the user's OpenSSH known_hosts file (`~/.ssh/known_hosts`).
func newHostKeyCallback() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err != nil {
		log.GetLogger().Error(err)
		return func(string, net.Addr, ssh.PublicKey) error {
			return fmt.Errorf("cannot locate known_hosts to verify the host key: %w", err)
		}
	}

	return (&hostKeyVerifier{
		path:        filepath.Join(home, ".ssh", "known_hosts"),
		in:          os.Stdin,
		out:         os.Stderr,
		interactive: term.IsTerminal(int(os.Stdin.Fd())),
	}).check
}

// hostKeyVerifier follows the same trust model as OpenSSH:
//
//   - a key that is already recorded for the host must match exactly;
//   - a host that is not recorded yet is accepted after an interactive
//     confirmation and appended to known_hosts (trust on first use);
//   - a changed key is always rejected, even if the user would confirm it.
type hostKeyVerifier struct {
	path        string
	in          io.Reader
	out         io.Writer
	interactive bool
}

func (v *hostKeyVerifier) check(hostname string, remote net.Addr, key ssh.PublicKey) error {
	verify, err := knownhosts.New(v.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No known_hosts file yet: every host is unknown, not invalid.
			return v.confirmUnknown(hostname, key)
		}
		return fmt.Errorf("cannot load %s: %w", v.path, err)
	}

	if err = verify(hostname, remote, key); err != nil {
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) == 0 {
				return v.confirmUnknown(hostname, key)
			}
			return hostKeyMismatchError(hostname, key, keyErr.Want, v.path)
		}
		return err
	}
	return nil
}

// confirmUnknown asks the user to trust an unknown host key and stores it.
// Without a terminal there is no way to ask, so the connection fails closed.
func (v *hostKeyVerifier) confirmUnknown(hostname string, key ssh.PublicKey) error {
	fingerprint := ssh.FingerprintSHA256(key)
	if !v.interactive {
		return fmt.Errorf("host key verification failed for %s: %s key %s is not in %s and there is no terminal to confirm it; connect once with ssh to trust the host",
			hostname, key.Type(), fingerprint, v.path)
	}

	fmt.Fprintf(v.out, "The authenticity of host '%s' can't be established.\n%s key fingerprint is %s.\nAre you sure you want to continue connecting (yes/no)? ",
		hostname, key.Type(), fingerprint)

	scanner := bufio.NewScanner(v.in)
	for scanner.Scan() {
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "yes", "y":
			if err := appendKnownHost(v.path, hostname, key); err != nil {
				return fmt.Errorf("host key accepted but could not be saved to %s: %w", v.path, err)
			}
			return nil
		case "no", "n", "":
			return fmt.Errorf("host key verification failed for %s: the host key was not accepted", hostname)
		default:
			fmt.Fprint(v.out, "Please type 'yes' or 'no': ")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("host key verification failed for %s: no confirmation received", hostname)
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	line := knownhosts.Line([]string{hostname}, key)
	if _, err = io.WriteString(f, line+"\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// hostKeyMismatchError explains why a known host was rejected, mirroring the
// warning OpenSSH prints when a server's key changes.
func hostKeyMismatchError(hostname string, key ssh.PublicKey, want []knownhosts.KnownKey, path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "host key verification failed for %s: remote host identification has changed", hostname)
	fmt.Fprintf(&b, "\n  remote host sent %s %s", key.Type(), ssh.FingerprintSHA256(key))
	for _, k := range want {
		fmt.Fprintf(&b, "\n  %s:%d has %s %s", k.Filename, k.Line, k.Key.Type(), ssh.FingerprintSHA256(k.Key))
	}
	fmt.Fprintf(&b, "\nremove the outdated entry from %s if the change is expected", path)
	return errors.New(b.String())
}
