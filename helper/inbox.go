package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// inbox is a folder on the Mac that the phone can drop files into. The phone
// cannot see what is in the folder. It only writes new files; it never
// overwrites or deletes existing ones.
type inbox struct {
	name string
	dir  string
	root *os.Root

	mu    sync.Mutex
	count int // files received in this session
}

// upload is one file on its way from the phone. It is written to a hidden
// part file and gets its real name only when it is complete.
type upload struct {
	in    *inbox // the folder it goes to, even when the user picks another one meanwhile
	name  string // final name, already sanitized
	size  int64
	mtime time.Time
	part  string
	f     *os.File
	got   int64
	last  time.Time
}

// Keep this much space free on the disk.
const diskReserve = 256 << 20

func newInbox(dir string) (*inbox, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("not a folder")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &inbox{name: filepath.Base(abs), dir: abs, root: root}, nil
}

func (in *inbox) summary() (string, string) {
	return nfc(in.name), "Drop-off folder"
}

// begin starts a new upload. The caller writes the data and then calls
// commit or abort.
func (in *inbox) begin(name string, size, mtime int64) (*upload, error) {
	name = safeName(name)
	if size < 0 {
		return nil, errors.New("bad size")
	}
	if free, err := in.free(); err == nil && uint64(size)+diskReserve > free {
		return nil, errors.New("not enough space on the Mac")
	}
	rnd := make([]byte, 6)
	rand.Read(rnd)
	part := ".pickup-" + hex.EncodeToString(rnd) + ".part"
	f, err := in.root.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	u := &upload{in: in, name: name, size: size, part: part, f: f}
	if mtime > 0 {
		u.mtime = time.UnixMilli(mtime)
	}
	return u, nil
}

func (in *inbox) free() (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(in.dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func (u *upload) write(b []byte) error {
	if u.got+int64(len(b)) > u.size {
		return errors.New("more data than announced")
	}
	n, err := u.f.Write(b)
	u.got += int64(n)
	return err
}

// commit checks the size, marks the file as downloaded from the network, and
// moves it to a free name. It returns the final name.
func (in *inbox) commit(u *upload) (string, error) {
	if u.got != u.size {
		in.abort(u)
		return "", fmt.Errorf("incomplete file (%d of %d bytes)", u.got, u.size)
	}
	// Like a browser download: Gatekeeper checks the file before it opens.
	quarantine(u.f)
	err := u.f.Close()
	u.f = nil
	if err == nil && !u.mtime.IsZero() {
		in.root.Chtimes(u.part, time.Now(), u.mtime)
	}
	if err != nil {
		in.abort(u)
		return "", err
	}
	// Link fails when the name exists, so we never replace a file.
	ext := path.Ext(u.name)
	base := strings.TrimSuffix(u.name, ext)
	for i := 1; i < 1000; i++ {
		name := u.name
		if i > 1 {
			name = fmt.Sprintf("%s %d%s", base, i, ext)
		}
		err = in.root.Link(u.part, name)
		if err == nil {
			in.root.Remove(u.part)
			in.mu.Lock()
			in.count++
			in.mu.Unlock()
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	in.abort(u)
	if err == nil {
		err = errors.New("no free file name")
	}
	return "", err
}

// abort removes the part file.
func (in *inbox) abort(u *upload) {
	if u.f != nil {
		u.f.Close()
		u.f = nil
	}
	in.root.Remove(u.part)
}

func quarantine(f *os.File) {
	v := fmt.Sprintf("0083;%x;Pickup;", time.Now().Unix())
	unix.Fsetxattr(int(f.Fd()), "com.apple.quarantine", []byte(v), 0)
}

// safeName turns a name from the phone into a plain, visible file name.
func safeName(name string) string {
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "_")
	}
	name = nfc(name)
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '\\' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "."))
	if name == "" {
		name = "File"
	}
	// APFS allows 255 bytes per name. Keep room for " 999".
	const max = 240
	if len(name) > max {
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		base := name[:max-len(ext)]
		for !utf8.ValidString(base) {
			base = base[:len(base)-1]
		}
		name = base + ext
	}
	return name
}
