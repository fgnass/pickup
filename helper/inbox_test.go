package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func receiveFile(t *testing.T, in *inbox, name, data string) (string, error) {
	t.Helper()
	u, err := in.begin(name, int64(len(data)), time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := u.write([]byte(data)); err != nil {
		in.abort(u)
		return "", err
	}
	return in.commit(u)
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	sort.Strings(out)
	return out
}

func TestInboxNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("old"), 0o644)
	in, err := newInbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a 2.jpg", "a 3.jpg"} {
		got, err := receiveFile(t, in, "a.jpg", "new")
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.jpg")); string(b) != "old" {
		t.Fatalf("a.jpg changed to %q", b)
	}
	info, _ := os.Stat(filepath.Join(dir, "a 2.jpg"))
	if info.ModTime().Year() != 2024 {
		t.Fatalf("mtime = %v", info.ModTime())
	}
	if got := strings.Join(names(t, dir), ","); got != "a 2.jpg,a 3.jpg,a.jpg" {
		t.Fatalf("files = %s", got)
	}
}

func TestInboxSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	in, _ := newInbox(dir)

	u, _ := in.begin("x.txt", 5, 0)
	u.write([]byte("abc"))
	if _, err := in.commit(u); err == nil {
		t.Fatal("short file was accepted")
	}

	u, _ = in.begin("y.txt", 2, 0)
	if err := u.write([]byte("abc")); err == nil {
		t.Fatal("long file was accepted")
	}
	in.abort(u)

	if n := names(t, dir); len(n) != 0 {
		t.Fatalf("left over: %v", n)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":                 "_.._etc_passwd",
		".hidden":                          "hidden",
		"  ":                               "File",
		"a\x00b:c.txt":                     "a_b_c.txt",
		"Café.jpg":                        "Café.jpg",
		strings.Repeat("é", 200) + ".jpeg": strings.Repeat("é", 117) + ".jpeg",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
