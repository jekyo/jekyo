// Package sshx is JEKYO's SSH transport: command execution with streaming
// output, file read/write, and sudo handling. Host keys are pinned
// trust-on-first-use into ~/.jekyo/known_hosts.
package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client is an SSH connection to one server.
type Client struct {
	conn *ssh.Client
	user string
	mu   sync.Mutex
}

// Target is a parsed user@host[:port] spec.
type Target struct {
	User string
	Host string
	Port string
}

func ParseTarget(s string) (Target, error) {
	t := Target{Port: "22"}
	rest := s
	if i := strings.Index(rest, "@"); i >= 0 {
		t.User, rest = rest[:i], rest[i+1:]
	}
	if t.User == "" {
		return Target{}, fmt.Errorf("ssh target %q must be user@host[:port]", s)
	}
	if h, p, err := net.SplitHostPort(rest); err == nil {
		t.Host, t.Port = h, p
	} else {
		t.Host = rest
	}
	if t.Host == "" {
		return Target{}, fmt.Errorf("ssh target %q has no host", s)
	}
	return t, nil
}

// Options configures Dial.
type Options struct {
	KeyPath        string // explicit private key; otherwise ssh-agent
	KnownHostsPath string // default ~/.jekyo/known_hosts
}

func Dial(target string, opts Options) (*Client, error) {
	t, err := ParseTarget(target)
	if err != nil {
		return nil, err
	}

	var auths []ssh.AuthMethod
	if opts.KeyPath != "" {
		key, err := os.ReadFile(opts.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("reading ssh key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parsing ssh key %s: %w", opts.KeyPath, err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}
	if len(auths) == 0 {
		return nil, errors.New("no SSH auth available: start ssh-agent or pass --ssh-key")
	}

	hostKeyCallback, err := tofuCallback(opts.KnownHostsPath)
	if err != nil {
		return nil, err
	}

	conn, err := ssh.Dial("tcp", net.JoinHostPort(t.Host, t.Port), &ssh.ClientConfig{
		User:            t.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", target, err)
	}
	return &Client{conn: conn, user: t.User}, nil
}

// tofuCallback verifies against known_hosts and appends unknown hosts on
// first use (matching OpenSSH's accept-new); a changed key is still fatal.
func tofuCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".jekyo", "known_hosts")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return nil, err
		}
	}
	check, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			// Unknown host: record and accept.
			f, ferr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			_, ferr = f.WriteString(line + "\n")
			return ferr
		}
		return fmt.Errorf("host key verification failed for %s (remove the entry from %s if the server was reinstalled): %w", hostname, path, err)
	}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// DialRemote opens a TCP connection to addr as seen from the server (e.g. a
// ClusterIP) through the SSH connection. This is how the laptop talks to the
// in-cluster registry API without any public exposure.
func (c *Client) DialRemote(addr string) (net.Conn, error) {
	return c.conn.Dial("tcp", addr)
}

// RunWithStdin executes cmd as root with r streamed to its stdin. Used to
// pipe image tars into `ctr images import` without a server-side temp file.
func (c *Client) RunWithStdin(cmd string, r io.Reader) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	sess.Stdin = r
	out, err := sess.CombinedOutput(c.sudo(cmd))
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("remote command failed: %s: %s", cmd, firstLines(s, 12))
	}
	return s, nil
}

// sudo prefixes cmd so it runs as root regardless of the login user.
func (c *Client) sudo(cmd string) string {
	if c.user == "root" {
		return cmd
	}
	return "sudo -n bash -c " + shellQuote(cmd)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Run executes cmd as root and returns combined output. A non-zero exit
// returns an error that includes the output.
func (c *Client) Run(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(c.sudo(cmd))
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("remote command failed: %s: %s", cmd, firstLines(s, 12))
	}
	return s, nil
}

// Stream executes cmd as root, streaming combined output to w.
func (c *Client) Stream(cmd string, w io.Writer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.conn.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdout = w
	sess.Stderr = w
	if err := sess.Run(c.sudo(cmd)); err != nil {
		return fmt.Errorf("remote command failed: %s: %w", cmd, err)
	}
	return nil
}

// WriteFile writes data to path on the server (as root), creating parent
// directories, with the given mode (e.g. "0600").
func (c *Client) WriteFile(path string, data []byte, mode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.conn.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = bytes.NewReader(data)
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %s %s",
		shellQuote(filepath.Dir(path)), shellQuote(path), mode, shellQuote(path))
	out, err := sess.CombinedOutput(c.sudo(cmd))
	if err != nil {
		return fmt.Errorf("writing %s: %s: %w", path, firstLines(string(out), 4), err)
	}
	return nil
}

// ReadFile reads a file from the server as root.
func (c *Client) ReadFile(path string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if err := sess.Run(c.sudo("cat " + shellQuote(path))); err != nil {
		return nil, fmt.Errorf("reading %s: %s: %w", path, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, "\n")
}
