package contexts

import (
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("JEKYO_HOME", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSaveGetListRemove(t *testing.T) {
	s := newTestStore(t)

	m := Meta{Name: "prod", SSH: "root@1.2.3.4", IP: "1.2.3.4", Domain: "jekyo.com", CreatedAt: time.Now()}
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Meta{Name: "staging", SSH: "root@5.6.7.8", IP: "5.6.7.8", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.IP != "1.2.3.4" || got.Domain != "jekyo.com" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "prod" || all[1].Name != "staging" {
		t.Fatalf("list mismatch: %+v", all)
	}

	if err := s.Remove("staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("staging"); err == nil {
		t.Fatal("expected error after remove")
	}
}

func TestCurrentAndResolve(t *testing.T) {
	s := newTestStore(t)

	// No contexts at all: helpful error.
	_, err := s.Resolve("")
	if err == nil || !strings.Contains(err.Error(), "server install") {
		t.Fatalf("expected install hint, got %v", err)
	}

	if err := s.Save(Meta{Name: "prod", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// Contexts exist but none selected.
	_, err = s.Resolve("")
	if err == nil || !strings.Contains(err.Error(), "context use") {
		t.Fatalf("expected 'context use' hint, got %v", err)
	}

	if err := s.SetCurrent("prod"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Resolve("")
	if err != nil || m.Name != "prod" {
		t.Fatalf("resolve current: %v %+v", err, m)
	}

	// Explicit flag wins over current.
	if err := s.Save(Meta{Name: "other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m, err = s.Resolve("other")
	if err != nil || m.Name != "other" {
		t.Fatalf("resolve flag: %v %+v", err, m)
	}

	// SetCurrent refuses unknown contexts.
	if err := s.SetCurrent("nope"); err == nil {
		t.Fatal("expected error for unknown context")
	}

	// Removing the current context clears the selection.
	if err := s.Remove("prod"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.CurrentName()
	if err != nil {
		t.Fatal(err)
	}
	if cur != "" {
		t.Fatalf("expected cleared current, got %q", cur)
	}
}
