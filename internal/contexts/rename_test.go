package contexts

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Issue #39: renaming keeps everything the context holds and follows the
// current-context setting, without touching the server.
func TestRenameKeepsEverythingAndFollowsCurrent(t *testing.T) {
	s := newTestStore(t)
	m := Meta{
		Name: "193-168-140-88", SSH: "warning@193.168.140.88", IP: "193.168.140.88",
		Storage: "/storage", Arch: "x86_64", CreatedAt: time.Now(),
		Registry: &Registry{Host: "registry.example.com", Username: "jekyo", Password: "pw"},
		Logins:   map[string]Login{"ghcr.io": {Username: "u", Password: "p"}},
	}
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	// a kubeconfig sits beside the metadata and must come along
	if err := os.WriteFile(s.KubeconfigPath(m.Name), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrent(m.Name); err != nil {
		t.Fatal(err)
	}

	if err := s.Rename("193-168-140-88", "dgx"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get("193-168-140-88"); err == nil {
		t.Fatal("old name still resolves")
	}
	got, err := s.Get("dgx")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "dgx" {
		t.Errorf("meta still records %q: 'context show' would disagree with 'context ls'", got.Name)
	}
	if got.SSH != m.SSH || got.IP != m.IP || got.Storage != m.Storage || got.Arch != m.Arch {
		t.Errorf("fields lost: %+v", got)
	}
	if got.Registry == nil || got.Registry.Password != "pw" {
		t.Errorf("registry credentials lost: %+v", got.Registry)
	}
	if got.Logins["ghcr.io"].Password != "p" {
		t.Errorf("registry logins lost: %+v", got.Logins)
	}
	if data, err := os.ReadFile(s.KubeconfigPath("dgx")); err != nil || !strings.Contains(string(data), "apiVersion") {
		t.Errorf("kubeconfig did not move: %v", err)
	}
	if cur, _ := s.CurrentName(); cur != "dgx" {
		t.Errorf("current context is %q, want dgx", cur)
	}
}

// Renaming a context that is not current must not make it current.
func TestRenameLeavesOtherCurrentAlone(t *testing.T) {
	s := newTestStore(t)
	for _, n := range []string{"a", "b"} {
		if err := s.Save(Meta{Name: n, SSH: "root@h", IP: "1.1.1.1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetCurrent("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("b", "c"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.CurrentName(); cur != "a" {
		t.Fatalf("current context changed to %q", cur)
	}
}

func TestRenameErrors(t *testing.T) {
	s := newTestStore(t)
	for _, n := range []string{"one", "two"} {
		if err := s.Save(Meta{Name: n, SSH: "root@h", IP: "1.1.1.1"}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct{ name, from, to, want string }{
		{"unknown source", "nope", "x", "not found"},
		{"target exists", "one", "two", "already exists"},
		{"same name", "one", "one", "already has that name"},
		{"path traversal", "one", "../escape", "must be letters"},
		{"slash", "one", "a/b", "must be letters"},
		{"empty", "one", "", "must be letters"},
		{"leading dot", "one", ".hidden", "must be letters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Rename(tc.from, tc.to)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got: %v", tc.want, err)
			}
			// a refused rename must leave the source intact
			if tc.from == "one" {
				if _, err := s.Get("one"); err != nil {
					t.Fatalf("source context lost: %v", err)
				}
			}
		})
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"dgx", "prod-1", "a.b_c", "193-168-140-88", "A1"} {
		if !ValidName(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", ".x", "-x", "a/b", "a b", "../x", strings.Repeat("a", 65)} {
		if ValidName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
