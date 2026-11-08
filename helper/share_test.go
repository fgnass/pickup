package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestHiddenPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".ssh/id_ed25519", "public/.private/secret.txt", "public/.env", "public/visible.txt"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("contents"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sh, err := newShare([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sh.items[0].root.Close() })
	for _, rel := range []string{".ssh", ".ssh/id_ed25519", "public/.private", "public/.private/secret.txt", "public/.env", "/public/.env", "public/../.ssh/id_ed25519"} {
		t.Run(rel, func(t *testing.T) {
			if f, _, err := sh.open(0, rel); !errors.Is(err, fs.ErrPermission) {
				if f != nil {
					f.Close()
				}
				t.Errorf("open: got %v, want permission error", err)
			}
			if _, err := sh.list(0, rel); !errors.Is(err, fs.ErrPermission) {
				t.Errorf("list: got %v, want permission error", err)
			}
			if _, _, err := sh.zipEntries(0, rel); !errors.Is(err, fs.ErrPermission) {
				t.Errorf("zip: got %v, want permission error", err)
			}
			if _, err := sh.diskPath(0, rel); !errors.Is(err, fs.ErrPermission) {
				t.Errorf("thumbnail: got %v, want permission error", err)
			}
		})
	}
	entries, err := sh.list(0, "public")
	if err != nil || len(entries) != 1 || entries[0].Name != "visible.txt" {
		t.Fatalf("visible directory listing: %v, %v", entries, err)
	}
	f, _, err := sh.open(0, "public/visible.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := sh.diskPath(0, "public/visible.txt"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []int{-1, 0} {
		entries, _, err := sh.zipEntries(item, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 3 { // root, public/, and public/visible.txt
			t.Fatalf("ZIP contains hidden entries or omits visible entries: %+v", entries)
		}
		for _, e := range entries {
			if _, err := visiblePath(e.name); err != nil {
				t.Errorf("ZIP contains hidden entry %q", e.name)
			}
		}
	}
}

func TestZip(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Fotos", "Urlaub"), 0o755)
	os.WriteFile(filepath.Join(dir, "Fotos", "Ärger.txt"), []byte("ä"), 0o644)
	os.WriteFile(filepath.Join(dir, "Fotos", ".DS_Store"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "Fotos", "Urlaub", "a.jpg"), bytes.Repeat([]byte("x"), 100000), 0o644)
	os.Symlink("/etc/hosts", filepath.Join(dir, "Fotos", "escape"))
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)
	sh, err := newShare([]string{filepath.Join(dir, "Fotos"), filepath.Join(dir, "notes.txt")})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		item int
		path string
	}{{-1, ""}, {0, ""}, {0, "Urlaub"}} {
		es, _, err := sh.zipEntries(c.item, c.path)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := writeZip(context.Background(), &buf, es); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b := new(bytes.Buffer)
			if _, err := b.ReadFrom(rc); err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		}
		t.Logf("%v %q -> %q (%s)", c.item, c.path, names, sh.zipName(c.item, c.path))
		if c.item == -1 {
			os.WriteFile(os.Getenv("ZIPOUT"), buf.Bytes(), 0o644)
		}
	}
	// path escapes must fail
	for _, p := range []string{"../notes.txt", "escape", "/etc/hosts"} {
		if f, _, err := sh.open(0, p); err == nil {
			f.Close()
			t.Errorf("open %q should fail", p)
		}
	}
}
