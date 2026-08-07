package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const releaseBase = "https://github.com/jekyo/jekyo/releases"

// latestTag asks GitHub for the newest release tag ("v0.14.0").
func latestTag() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/jekyo/jekyo/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("release lookup failed: %s", resp.Status)
	}
	var v struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Tag, nil
}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update jekyo to the latest release in place",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tag, err := latestTag()
			if err != nil {
				return err
			}
			if tag == "v"+version {
				cmd.Printf("jekyo %s is already the latest release.\n", version)
				return nil
			}
			if version == "dev" {
				cmd.Printf("This is a dev build; latest release is %s. Rebuild from source or install the release:\n  curl -fsSL https://jekyo.com/install | sh\n", tag)
				return nil
			}

			self, err := os.Executable()
			if err != nil {
				return err
			}
			self, err = filepath.EvalSymlinks(self)
			if err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				cmd.Printf("Self-update is not supported on Windows. Download %s/download/%s/jekyo-windows-%s manually.\n", releaseBase, tag, runtime.GOARCH)
				return nil
			}

			url := fmt.Sprintf("%s/download/%s/jekyo-%s-%s", releaseBase, tag, runtime.GOOS, runtime.GOARCH)
			cmd.Printf("Updating jekyo v%s → %s ...\n", version, tag)
			client := &http.Client{Timeout: 5 * time.Minute}
			resp, err := client.Get(url)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("downloading %s: %s", url, resp.Status)
			}

			// write next to the current binary so the rename stays on one
			// filesystem and is atomic
			tmp, err := os.CreateTemp(filepath.Dir(self), ".jekyo-update-*")
			if err != nil {
				// no write permission next to the binary (e.g. /usr/local/bin)
				return fmt.Errorf("%w\nre-run with sudo: sudo jekyo update", err)
			}
			defer os.Remove(tmp.Name())
			hash := sha256.New()
			if _, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body); err != nil {
				tmp.Close()
				return err
			}
			if err := tmp.Close(); err != nil {
				return err
			}

			// verify against the release's checksums.txt before installing
			asset := fmt.Sprintf("jekyo-%s-%s", runtime.GOOS, runtime.GOARCH)
			want, err := releaseChecksum(client, tag, asset)
			if err != nil {
				return fmt.Errorf("fetching checksums: %w", err)
			}
			if got := hex.EncodeToString(hash.Sum(nil)); got != want {
				return fmt.Errorf("checksum mismatch for %s: got %s want %s (refusing to install)", asset, got, want)
			}
			if err := os.Chmod(tmp.Name(), 0o755); err != nil {
				return err
			}
			if err := os.Rename(tmp.Name(), self); err != nil {
				return fmt.Errorf("%w\nre-run with sudo: sudo jekyo update", err)
			}
			cmd.Printf("Updated. %s is now %s.\n", self, tag)

			// a stale skill misleads agents; refresh it when installed
			if home, err := os.UserHomeDir(); err == nil {
				if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "jekyo")); err == nil {
					refresh := exec.Command(self, "skill", "install", "--global")
					if out, err := refresh.CombinedOutput(); err == nil {
						cmd.Println("Agent skill refreshed for the new version.")
					} else {
						cmd.Printf("Skill refresh failed (%v): run jekyo skill install --global\n%s", err, out)
					}
				}
			}
			return nil
		},
	}
}

// releaseChecksum reads the sha256 for one asset from the release's
// checksums.txt.
func releaseChecksum(client *http.Client, tag, asset string) (string, error) {
	resp, err := client.Get(fmt.Sprintf("%s/download/%s/checksums.txt", releaseBase, tag))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("checksums.txt: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == asset {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("no checksum listed for %s", asset)
}
