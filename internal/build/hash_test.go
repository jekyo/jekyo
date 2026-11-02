package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jekyo/jekyo/internal/dsl"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Issue #34: a build secret's VALUE must not reach the content hash.
// Rotating a credential would otherwise re-tag and redeploy an image whose
// contents are identical.
func TestContentHashIgnoresSecretValues(t *testing.T) {
	dir := fixture(t)
	withValue := func(v string) string {
		b := &dsl.Build{Context: ".", Secrets: map[string]dsl.FileMount{
			"netrc": {From: "${NETRC}", Content: v},
		}}
		h, err := contentHash(dir, b, "linux/amd64")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if withValue("token-one") != withValue("token-two") {
		t.Fatal("rotating a secret changed the image tag")
	}
}

// Adding or removing a secret IS a different build: the Dockerfile
// consumes a different set of mounts.
func TestContentHashTracksSecretIDs(t *testing.T) {
	dir := fixture(t)
	hash := func(b *dsl.Build) string {
		h, err := contentHash(dir, b, "linux/amd64")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	none := hash(&dsl.Build{Context: "."})
	one := hash(&dsl.Build{Context: ".", Secrets: map[string]dsl.FileMount{"netrc": {Content: "x"}}})
	two := hash(&dsl.Build{Context: ".", Secrets: map[string]dsl.FileMount{"netrc": {Content: "x"}, "npmrc": {Content: "y"}}})
	renamed := hash(&dsl.Build{Context: ".", Secrets: map[string]dsl.FileMount{"other": {Content: "x"}}})
	if none == one || one == two || one == renamed {
		t.Fatalf("secret ids must affect the hash: none=%s one=%s two=%s renamed=%s", none, one, two, renamed)
	}
}

func TestContentHashTracksSSH(t *testing.T) {
	dir := fixture(t)
	hash := func(b *dsl.Build) string {
		h, err := contentHash(dir, b, "linux/amd64")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if hash(&dsl.Build{Context: "."}) == hash(&dsl.Build{Context: ".", SSH: true}) {
		t.Fatal("build.ssh must affect the hash")
	}
}
