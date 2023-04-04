package client

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mitchellh/ioprogress"
	"github.com/pkg/sftp"
	"github.com/yiitz/sshw/internal/config"
	"github.com/yiitz/sshw/pkg/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
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

type Client interface {
	Shell(cmd string)
	SendFile(srcPath string, destPath string)
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
		pemBytes, err = ioutil.ReadFile(path.Join(u.HomeDir, ".ssh/id_rsa"))
	} else {
		pemBytes, err = ioutil.ReadFile(node.KeyPath)
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
				b, err := terminal.ReadPassword(int(syscall.Stdin))
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
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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

func (c *defaultClient) GetFile(srcPath string, destPath string) {
	client := c.Connect()
	if client == nil {
		return
	}
	defer client.Close()

	wg := sync.WaitGroup{}
	wg.Add(1)

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

	fmt.Printf("get file %s to %s\n", srcPath, destPath)

	// open an SFTP session over an existing ssh connection.
	sftp, err := sftp.NewClient(client)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer sftp.Close()

	// Open the source file
	dstFile, err := os.Create(destPath)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer dstFile.Close()

	// Create the destination file
	srcFile, err := sftp.Open(srcPath)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer srcFile.Close()
	fi, _ := srcFile.Stat()

	progressR := &ioprogress.Reader{
		Reader:       srcFile,
		Size:         fi.Size(),
		DrawFunc:     ioprogress.DrawTerminalf(os.Stdout, ioprogress.DrawTextFormatBytes),
		DrawInterval: time.Second,
	}

	// Copy all of the reader to some local file f. As it copies, the
	// progressR will write progress to the terminal on os.Stdout. This is
	// customizable.
	io.Copy(dstFile, progressR)
}

func (c *defaultClient) SendFile(srcPath string, destPath string) {
	client := c.Connect()
	if client == nil {
		return
	}
	defer client.Close()

	wg := sync.WaitGroup{}
	wg.Add(1)

	// open an SFTP session over an existing ssh connection.
	sftp, err := sftp.NewClient(client)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer sftp.Close()

	if destPath == "" {
		_, fn := path.Split(srcPath)
		destPath = fn
	} else {
		fi, err := sftp.Stat(destPath)
		if err == nil && fi.IsDir() {
			_, fn := path.Split(srcPath)
			destPath = path.Join(destPath, fn)
		}
	}

	fmt.Printf("send file %s to %s\n", srcPath, destPath)

	// Open the source file
	srcFile, err := os.Open(srcPath)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer srcFile.Close()

	// Create the destination file
	dstFile, err := sftp.Create(destPath)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer dstFile.Close()

	fi, err := os.Stat(srcPath)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	progressR := &ioprogress.Reader{
		Reader:       srcFile,
		Size:         fi.Size(),
		DrawFunc:     ioprogress.DrawTerminalf(os.Stdout, ioprogress.DrawTextFormatBytes),
		DrawInterval: time.Second,
	}

	// Copy all of the reader to some local file f. As it copies, the
	// progressR will write progress to the terminal on os.Stdout. This is
	// customizable.
	io.Copy(dstFile, progressR)
}

func (c *defaultClient) Shell(cmd string) {
	client := c.Connect()
	if client == nil {
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
	state, err := terminal.MakeRaw(fd)
	if err != nil {
		log.GetLogger().Fatal(err)
		return
	}
	defer terminal.Restore(fd, state)

	w, h, err := terminal.GetSize(fd)
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
			cw, ch, err := terminal.GetSize(fd)
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
				b, err = terminal.ReadPassword(int(syscall.Stdin))
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
