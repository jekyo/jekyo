// Package vpn drives wg-easy's REST API (v14) so peers are managed from the
// CLI instead of the web UI: session login, list/add/remove clients, and
// config download.
package vpn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

type Client struct {
	BaseURL string // e.g. https://vpn.example.com or http://<clusterip>:51821
	HTTP    *http.Client
}

// New builds a client; transport may be nil (default) or an SSH-dialing
// transport when the admin UI is cluster-internal.
func New(baseURL string, transport http.RoundTripper) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second, Jar: jar, Transport: transport},
	}, nil
}

// Login establishes the session cookie.
func (c *Client) Login(password string) error {
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := c.HTTP.Post(c.BaseURL+"/api/session", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vpn admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vpn login failed (status %d) — check the password in 'jekyo context show'", resp.StatusCode)
	}
	return nil
}

// Peer is one WireGuard client as reported by wg-easy.
type Peer struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Enabled           bool       `json:"enabled"`
	Address           string     `json:"address"`
	CreatedAt         time.Time  `json:"createdAt"`
	LatestHandshakeAt *time.Time `json:"latestHandshakeAt"`
	TransferRx        int64      `json:"transferRx"`
	TransferTx        int64      `json:"transferTx"`
}

func (c *Client) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vpn api %s %s: status %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Peers() ([]Peer, error) {
	var peers []Peer
	err := c.do(http.MethodGet, "/api/wireguard/client", nil, &peers)
	return peers, err
}

func (c *Client) AddPeer(name string) error {
	return c.do(http.MethodPost, "/api/wireguard/client", map[string]string{"name": name}, nil)
}

func (c *Client) RemovePeer(id string) error {
	return c.do(http.MethodDelete, "/api/wireguard/client/"+id, nil, nil)
}

// Config downloads a peer's WireGuard configuration file.
func (c *Client) Config(id string) ([]byte, error) {
	resp, err := c.HTTP.Get(c.BaseURL + "/api/wireguard/client/" + id + "/configuration")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("config download: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// FindPeer resolves a peer by name.
func FindPeer(peers []Peer, name string) (Peer, error) {
	for _, p := range peers {
		if p.Name == name {
			return p, nil
		}
	}
	return Peer{}, fmt.Errorf("no peer named %q (see 'jekyo vpn peers')", name)
}
