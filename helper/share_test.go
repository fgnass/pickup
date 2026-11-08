package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
