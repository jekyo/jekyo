// Package registry is a minimal Docker Registry v2 API client, used to skip
// already-pushed builds and to power `jekyo images`.
package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	// BaseURL like http://127.0.0.1:41539 (tunnel) or https://registry.example.com
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
}

func New(baseURL, user, pass string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Username: user,
		Password: pass,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(method, path string, accept string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return c.HTTP.Do(req)
}

const manifestAccept = "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"

// HasTag reports whether repo:tag already exists.
func (c *Client) HasTag(repo, tag string) (bool, error) {
	resp, err := c.do(http.MethodHead, fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), manifestAccept)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case http.StatusUnauthorized:
		return false, fmt.Errorf("registry auth failed (check 'jekyo context show' credentials)")
	default:
		return false, fmt.Errorf("registry: unexpected status %d for %s:%s", resp.StatusCode, repo, tag)
	}
}

// Repositories lists all repos in the registry.
func (c *Client) Repositories() ([]string, error) {
	resp, err := c.do(http.MethodGet, "/v2/_catalog?n=1000", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry catalog: status %d", resp.StatusCode)
	}
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Repositories, nil
}

// Tags lists tags for one repository.
func (c *Client) Tags(repo string) ([]string, error) {
	resp, err := c.do(http.MethodGet, fmt.Sprintf("/v2/%s/tags/list", repo), "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry tags %s: status %d", repo, resp.StatusCode)
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Tags, nil
}
