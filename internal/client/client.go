package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"github.com/schollz/progressbar/v3"
	"github.com/yiitz/sshw/internal/config"
	"github.com/yiitz/sshw/pkg/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

var (
	DefaultCiphers = []string{
		"aes128-ctr",
		"aes192-ctr",
		"aes256-ctr",
		"aes128-gcm@openssh.com",
		"chacha20-poly1305@openssh.com",
		"arcfour256",
		"arcfour128",
		"arcfour",
		"aes128-cbc",
		"3des-cbc",
		"blowfish-cbc",
		"cast128-cbc",
		"aes192-cbc",
		"aes256-cbc",
	}
)

const (
	// transferRetryMinWait is the delay before the first reconnect after a
	// transfer is interrupted. The delay doubles after every failed attempt,
	// up to transferRetryMaxWait.
	transferRetryMinWait = 3 * time.Second
	transferRetryMaxWait = 30 * time.Second
)

type Client interface {
	Shell(cmd string)
	SendFile(srcPath string, destPath string, useTemp bool)
	GetFile(srcPath string, destPath string)
}

type defaultClient struct {
	clientConfig *ssh.ClientConfig
	node         *config.Node
}

func genSSHConfig(node *config.Node) *defaultClient {
	u, err := user.Current()
	if err != nil {
		log.GetLogger().Error(err)
		return nil
	}

	var authMethods []ssh.AuthMethod

	var pemBytes []byte
	if node.KeyBytes != "" {
		pemBytes = []byte(node.KeyBytes)
	} else if node.KeyPath == "" {
		pemBytes, err = os.ReadFile(path.Join(u.HomeDir, ".ssh/id_rsa"))
		if err != nil {
			pemBytes, err = os.ReadFile(path.Join(u.HomeDir, ".ssh/id_ed25519"))
		}
	} else {
		pemBytes, err = os.ReadFile(node.KeyPath)
	}
	if err != nil {
		log.GetLogger().Error(err)
	} else {
		var signer ssh.Signer
		if node.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(node.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pemBytes)
		}
		if err != nil {
			log.GetLogger().Error(err)
		} else {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	password := node.GetPassword()

	if password != nil {
		authMethods = append(authMethods, password)
	}

	authMethods = append(authMethods, ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, 0, len(questions))
		for i, q := range questions {
			fmt.Print(q)
			if echos[i] {
				scan := bufio.NewScanner(os.Stdin)
				if scan.Scan() {
					answers = append(answers, scan.Text())
				}
				err := scan.Err()
				if err != nil {
					return nil, err
				}
			} else {
				b, err := term.ReadPassword(int(syscall.Stdin))
				if err != nil {
					return nil, err
				}
				fmt.Println()
				answers = append(answers, string(b))
			}
		}
		return answers, nil
	}))

	config := &ssh.ClientConfig{
		User:            node.GetUser(),
		Auth:            authMethods,
		HostKeyCallback: newHostKeyCallback(),
		Timeout:         time.Second * 10,
	}

	config.SetDefaults()
	config.Ciphers = append(config.Ciphers, DefaultCiphers...)

	return &defaultClient{
		clientConfig: config,
		node:         node,
	}
}

func NewClient(node *config.Node) Client {
	return genSSHConfig(node)
}

// isPermanentTransferError reports errors that reconnecting will not fix,
// such as a missing or inaccessible remote path.
func isPermanentTransferError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission)
}

// transferRetryDelay returns the exponential backoff before the given retry
// attempt (1-based): transferRetryMinWait, doubled on every attempt, capped at
// transferRetryMaxWait.
func transferRetryDelay(attempt int) time.Duration {
	delay := transferRetryMinWait
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= transferRetryMaxWait {
			return transferRetryMaxWait
		}
	}
	return delay
}

// newTransferBar returns a byte based progress bar using IEC units (KiB/MiB).
// The option must be passed at construction time: progressbar formats the
// total size once, when the bar is created, so applying OptionUseIECUnits
// afterwards leaves the total in SI units (MB/GB).
//
// A nil bar is returned for an empty file: progressbar rejects a max of 0.
func newTransferBar(total int64, description string) *progressbar.ProgressBar {
	if total <= 0 {
		return nil
	}
	return progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionSetWidth(10),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionUseIECUnits(true),
	)
}

// progressWriter reports transferred bytes to the progress bar. Reporting is
// best-effort: progressbar returns an error once the bar is full, and a file
// that grew after its size was measured (a busy log file, for example) must
// not abort the transfer.
type progressWriter struct {
	bar *progressbar.ProgressBar
}

func (w *progressWriter) Write(p []byte) (int, error) {
	if w.bar != nil {
		_ = w.bar.Add(len(p))
	}
	return len(p), nil
}

// connectSFTP opens an ssh connection plus an sftp session on top of it.
func (c *defaultClient) connectSFTP() (*ssh.Client, *sftp.Client, error) {
	client := c.Connect()
	if client == nil {
		return nil, nil, fmt.Errorf("ssh connect to %s:%d failed", c.node.Host, c.node.GetPort())
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return client, sftpClient, nil
}

func (c *defaultClient) GetFile(srcPath string, destPath string) {
	if destPath == "" {
		_, fn := path.Split(srcPath)
		destPath = fn
	} else {
		fi, err := os.Stat(destPath)
		if err == nil && fi.IsDir() {
			_, fn := path.Split(srcPath)
			destPath = path.Join(destPath, fn)
		}
	}

	log.GetLogger().Infof("get file remote:%s -> local:%s", srcPath, destPath)

	// Create the destination file. On a retry we keep using this handle and
	// continue from its current size instead of truncating it again.
	dstFile, err := os.Create(destPath)
	if err != nil {
		log.GetLogger().Fatal("os create error", err)
		return
	}
	defer dstFile.Close()

	var (
		sshClient  *ssh.Client
		sftpClient *sftp.Client
		srcFile    *sftp.File
		bar        *progressbar.ProgressBar
		srcSize    int64
		lastErr    error
	)

	// dropConnection releases the current session so the next attempt starts
	// from a fresh connection. The client is closed first: that unblocks any
	// pending request on a dead connection instead of waiting for a timeout.
	dropConnection := func() {
		if sftpClient != nil {
			_ = sftpClient.Close()
			sftpClient = nil
		}
		if srcFile != nil {
			_ = srcFile.Close()
			srcFile = nil
		}
		if sshClient != nil {
			_ = sshClient.Close()
			sshClient = nil
		}
	}
	defer dropConnection()

	fail := func(err error) {
		dropConnection()
		if bar != nil {
			_ = bar.Clear()
		}
		log.GetLogger().Fatal("download failed: ", err)
	}

	for attempt := 0; ; attempt++ {
		// The local file size is the authoritative resume point.
		offset, err := dstFile.Seek(0, io.SeekEnd)
		if err != nil {
			log.GetLogger().Fatal("os seek error", err)
			return
		}

		if attempt > 0 {
			if bar != nil {
				_ = bar.Clear()
			}
			delay := transferRetryDelay(attempt)
			log.GetLogger().Errorf("download interrupted at %d/%d bytes: %v, reconnecting in %s (press Enter to reconnect now)",
				offset, srcSize, lastErr, delay)
			waitEnterOrTimeout(delay)
		}

		if sftpClient == nil {
			sshClient, sftpClient, lastErr = c.connectSFTP()
			if lastErr != nil {
				continue
			}
		}

		if srcFile == nil {
			srcFile, lastErr = sftpClient.Open(srcPath)
			if lastErr != nil {
				if isPermanentTransferError(lastErr) {
					fail(lastErr)
				}
				dropConnection()
				continue
			}
		}

		// Stat on every attempt rather than once on open: a growing file (a
		// live log, for example) may be larger now than on the previous
		// attempt, and the size decides both the resume point and the
		// progress bar total.
		fi, statErr := srcFile.Stat()
		if statErr != nil {
			if isPermanentTransferError(statErr) {
				fail(statErr)
			}
			dropConnection()
			lastErr = statErr
			continue
		}
		srcSize = fi.Size()

		if offset > srcSize {
			// The local file is longer than the remote one, so the remote file
			// was truncated or replaced: start over.
			if err = dstFile.Truncate(0); err != nil {
				log.GetLogger().Fatal("os truncate error", err)
				return
			}
			// Truncate leaves the current offset alone; without this seek the
			// next write would land at the old end and recreate a sparse file.
			if _, err = dstFile.Seek(0, io.SeekStart); err != nil {
				log.GetLogger().Fatal("os seek error", err)
				return
			}
			offset = 0
		}

		// Recreate the bar when the remote size changed: the previous bar may
		// already be full and cannot follow a larger file.
		if bar == nil || bar.GetMax64() != srcSize {
			if bar != nil {
				_ = bar.Clear()
			}
			bar = newTransferBar(srcSize, "downloading")
		}
		if bar != nil {
			_ = bar.Set64(offset)
		}

		if _, err = srcFile.Seek(offset, io.SeekStart); err != nil {
			dropConnection()
			lastErr = err
			continue
		}

		n, err := io.Copy(io.MultiWriter(dstFile, &progressWriter{bar: bar}), srcFile)
		if err != nil {
			dropConnection()
			lastErr = err
			continue
		}
		offset += n
		// Everything the remote file had when the attempt started must have
		// been read; going past that is fine, the file grew meanwhile.
		if offset < srcSize {
			dropConnection()
			lastErr = fmt.Errorf("transfer file size not correct: %d < %d", offset, srcSize)
			continue
		}
		if bar != nil {
			_ = bar.Finish()
		}
		return
	}
}

func (c *defaultClient) SendFile(srcPath string, destPath string, useTemp bool) {
	var realDestPath string

	if useTemp {
		defer func() {
			c.Shell(fmt.Sprintf(`mv "%s" "%s"`, destPath, realDestPath))
		}()
	}

	fi, err := os.Stat(srcPath)
	if err != nil {
		log.GetLogger().Fatal("os stat error", err)
		return
	}
	total := fi.Size()

	// Open the source file
	srcFile, err := os.Open(srcPath)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer srcFile.Close()

	bar := newTransferBar(total, "uploading")

	var (
		sshClient  *ssh.Client
		sftpClient *sftp.Client
		dstFile    *sftp.File
		resolved   bool
		opened     bool
		sent       int64
		lastErr    error
	)

	// dropConnection releases the current session so the next attempt starts
	// from a fresh connection. The client is closed first: that unblocks any
	// pending request on a dead connection instead of waiting for a timeout.
	dropConnection := func() {
		if sftpClient != nil {
			_ = sftpClient.Close()
			sftpClient = nil
		}
		if dstFile != nil {
			_ = dstFile.Close()
			dstFile = nil
		}
		if sshClient != nil {
			_ = sshClient.Close()
			sshClient = nil
		}
	}
	defer dropConnection()

	fail := func(err error) {
		dropConnection()
		if bar != nil {
			_ = bar.Clear()
		}
		log.GetLogger().Fatal("upload failed: ", err)
	}

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if bar != nil {
				_ = bar.Clear()
			}
			delay := transferRetryDelay(attempt)
			log.GetLogger().Errorf("upload interrupted at %d/%d bytes: %v, reconnecting in %s (press Enter to reconnect now)",
				sent, total, lastErr, delay)
			waitEnterOrTimeout(delay)
		}

		if sftpClient == nil {
			sshClient, sftpClient, lastErr = c.connectSFTP()
			if lastErr != nil {
				continue
			}
		}

		if !resolved {
			if destPath == "" {
				_, destPath = path.Split(srcPath)
			} else if st, statErr := sftpClient.Stat(destPath); statErr == nil && st.IsDir() {
				_, fn := path.Split(srcPath)
				destPath = path.Join(destPath, fn)
			} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				if isPermanentTransferError(statErr) {
					fail(statErr)
				}
				dropConnection()
				lastErr = statErr
				continue
			}
			if useTemp {
				realDestPath = destPath
				destPath += ".sshwtmp"
			}
			log.GetLogger().Infof("send file use-temp:%v local:%s -> remote:%s", useTemp, srcPath, destPath)
			resolved = true
		}

		if dstFile == nil {
			if !opened {
				// First attempt: truncate any existing file, like before.
				dstFile, lastErr = sftpClient.Create(destPath)
			} else {
				// Retry: keep what was already written and resume from there.
				dstFile, lastErr = sftpClient.OpenFile(destPath, os.O_WRONLY|os.O_CREATE)
			}
			if lastErr != nil {
				if isPermanentTransferError(lastErr) {
					fail(lastErr)
				}
				dropConnection()
				continue
			}
			opened = true
		}

		// The remote file size is the authoritative resume point.
		offset, err := dstFile.Seek(0, io.SeekEnd)
		if err != nil {
			dropConnection()
			lastErr = err
			continue
		}
		if offset > total {
			if err = dstFile.Truncate(0); err != nil {
				dropConnection()
				lastErr = err
				continue
			}
			// sftp.File.Truncate keeps its write offset; without this seek the
			// next write would land at the old end and make the remote file
			// sparse.
			if _, err = dstFile.Seek(0, io.SeekStart); err != nil {
				dropConnection()
				lastErr = err
				continue
			}
			offset = 0
		}
		sent = offset

		if _, err = srcFile.Seek(offset, io.SeekStart); err != nil {
			log.GetLogger().Fatal("os seek error", err)
			return
		}
		if bar != nil {
			_ = bar.Set64(offset)
		}

		n, err := io.Copy(io.MultiWriter(dstFile, &progressWriter{bar: bar}), srcFile)
		if err != nil {
			dropConnection()
			lastErr = err
			continue
		}
		offset += n
		sent = offset
		// Everything the local file had when the transfer started must have
		// been sent; going past that is fine, the file grew meanwhile.
		if offset < total {
			dropConnection()
			lastErr = fmt.Errorf("transfer file size not correct: %d < %d", offset, total)
			continue
		}
		if bar != nil {
			_ = bar.Finish()
		}
		return
	}
}

func (c *defaultClient) Shell(cmd string) {
	client := c.Connect()
	if client == nil {
		log.GetLogger().Fatal("ssh new client nil")
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer session.Close()

	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer term.Restore(fd, state)

	w, h, err := term.GetSize(fd)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	err = session.RequestPty("xterm", h, w, modes)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}

	err = session.Shell()
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}

	// then callback
	for i := range c.node.CallbackShells {
		shell := c.node.CallbackShells[i]
		time.Sleep(shell.Delay * time.Millisecond)
		stdinPipe.Write([]byte(shell.Cmd + "\r"))
	}

	if cmd != "" {
		stdinPipe.Write([]byte(cmd + "\r" + "exit\r"))
	}

	// change stdin to user
	go func() {
		_, err = io.Copy(stdinPipe, os.Stdin)
		log.GetLogger().Error(err)
		session.Close()
	}()

	// interval get terminal size
	// fix resize issue
	go func() {
		var (
			ow = w
			oh = h
		)
		for {
			cw, ch, err := term.GetSize(fd)
			if err != nil {
				break
			}

			if cw != ow || ch != oh {
				err = session.WindowChange(ch, cw)
				if err != nil {
					break
				}
				ow = cw
				oh = ch
			}
			time.Sleep(time.Second)
		}
	}()

	// send keepalive
	go func() {
		for {
			time.Sleep(time.Second * 10)
			client.SendRequest("keepalive@openssh.com", false, nil)
		}
	}()

	session.Wait()
}

func (c *defaultClient) Connect() *ssh.Client {
	host := c.node.Host
	port := strconv.Itoa(c.node.GetPort())
	jNodes := c.node.Jump

	var client *ssh.Client

	if len(jNodes) > 0 {
		jNode := jNodes[0]
		jc := genSSHConfig(jNode)
		proxyClient, err := ssh.Dial("tcp", net.JoinHostPort(jNode.Host, strconv.Itoa(jNode.GetPort())), jc.clientConfig)
		if err != nil {
			log.GetLogger().Error(err)
			return nil
		}
		conn, err := proxyClient.Dial("tcp", net.JoinHostPort(host, port))
		if err != nil {
			log.GetLogger().Error(err)
			return nil
		}
		ncc, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(host, port), c.clientConfig)
		if err != nil {
			log.GetLogger().Error(err)
			return nil
		}
		client = ssh.NewClient(ncc, chans, reqs)
	} else {
		client1, err := ssh.Dial("tcp", net.JoinHostPort(host, port), c.clientConfig)
		client = client1
		if err != nil {
			msg := err.Error()
			// use terminal password retry
			if strings.Contains(msg, "no supported methods remain") && !strings.Contains(msg, "password") {
				fmt.Printf("%s@%s's password:", c.clientConfig.User, host)
				var b []byte
				b, err = term.ReadPassword(int(syscall.Stdin))
				if err == nil {
					p := string(b)
					if p != "" {
						c.clientConfig.Auth = append(c.clientConfig.Auth, ssh.Password(p))
					}
					fmt.Println()
					client, err = ssh.Dial("tcp", net.JoinHostPort(host, port), c.clientConfig)
				}
			}
		}
		if err != nil {
			log.GetLogger().Error(err)
			return nil
		}
	}
	log.GetLogger().Infof("connect server ssh -p %d %s@%s version: %s\n", c.node.GetPort(), c.node.GetUser(), host, string(client.ServerVersion()))
	return client
}
