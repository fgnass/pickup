package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// item is one file or folder that the user selected in the Finder.
type item struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`  // file size; for folders the number of visible entries
	Mtime int64  `json:"mtime"` // ms since epoch

	path string
	root *os.Root // only for folders; confines all access to the folder
}

type share struct {
	items  []*item
	parent string // name of the folder that holds the selection
}

type entry struct {
	Name  string `json:"name"` // raw name on disk; the browser shows it NFC-normalized
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

func newShare(paths []string) (*share, error) {
	sh := &share{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		it := &item{Name: filepath.Base(abs), Dir: info.IsDir(), Size: info.Size(), Mtime: info.ModTime().UnixMilli(), path: abs}
		if it.Dir {
			if it.root, err = os.OpenRoot(abs); err != nil {
				return nil, err
			}
			es, _ := it.list(".")
			it.Size = int64(len(es))
		}
		sh.items = append(sh.items, it)
		if sh.parent == "" {
			sh.parent = filepath.Base(filepath.Dir(abs))
		}
	}
	return sh, nil
}

// summary returns a title and subtitle for the QR window.
func (sh *share) summary() (string, string) {
	if len(sh.items) == 1 {
		it := sh.items[0]
		if it.Dir {
			return nfc(it.Name), "Folder · " + plural(int(it.Size), "item")
		}
		return nfc(it.Name), humanSize(it.Size)
	}
	files, dirs := 0, 0
	for _, it := range sh.items {
		if it.Dir {
			dirs++
		} else {
			files++
		}
	}
	var parts []string
	if files > 0 {
		parts = append(parts, plural(files, "file"))
	}
	if dirs > 0 {
		parts = append(parts, plural(dirs, "folder"))
	}
	return nfc(sh.parent), strings.Join(parts, ", ")
}

// single reports whether exactly one file (not a folder) is shared.
func (sh *share) single() bool { return len(sh.items) == 1 && !sh.items[0].Dir }

func (sh *share) item(i int) (*item, error) {
	if i < 0 || i >= len(sh.items) {
		return nil, errors.New("no such item")
	}
	return sh.items[i], nil
}

// clean turns a browser path into a relative path for os.Root. os.Root itself
// rejects anything that leaves the folder, including via symlinks.
func clean(rel string) string {
	rel = path.Clean("/" + rel)
	if rel == "/" {
		return "."
	}
	return rel[1:]
}

func hidden(name string) bool { return strings.HasPrefix(name, ".") }

func (sh *share) list(i int, rel string) ([]entry, error) {
	it, err := sh.item(i)
	if err != nil {
		return nil, err
	}
	if !it.Dir {
		return nil, errors.New("not a folder")
	}
	return it.list(clean(rel))
}

func (it *item) list(rel string) ([]entry, error) {
	f, err := it.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := []entry{}
	for _, de := range des {
		if hidden(de.Name()) {
			continue
		}
		info, err := it.root.Stat(path.Join(rel, de.Name())) // follows symlinks inside the folder
		if err != nil || !(info.IsDir() || info.Mode().IsRegular()) {
			continue
		}
		out = append(out, entry{Name: de.Name(), Dir: info.IsDir(), Size: info.Size(), Mtime: info.ModTime().UnixMilli()})
	}
	return out, nil
}

func (sh *share) open(i int, rel string) (*os.File, fs.FileInfo, error) {
	it, err := sh.item(i)
	if err != nil {
		return nil, nil, err
	}
	var f *os.File
	if it.Dir {
		f, err = it.root.Open(clean(rel))
	} else if clean(rel) == "." {
		f, err = os.Open(it.path)
	} else {
		err = errors.New("not a folder")
	}
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("not a file")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// zipName is the file name for a ZIP of an item path, or of everything (i = -1).
func (sh *share) zipName(i int, rel string) string {
	name := sh.parent
	if i < 0 && len(sh.items) == 1 {
		name = sh.items[0].Name
	} else if i >= 0 && i < len(sh.items) {
		name = sh.items[i].Name
		if c := clean(rel); c != "." {
			name = path.Base(c)
		}
	}
	return nfc(strings.TrimSuffix(name, path.Ext(name))) + ".zip"
}

// zipEntry is one file or folder in a ZIP download.
type zipEntry struct {
	name  string // path inside the ZIP, "/"-terminated for folders
	open  func() (fs.File, error)
	size  int64
	mtime time.Time
}

func (sh *share) zipEntries(i int, rel string) ([]zipEntry, int64, error) {
	var out []zipEntry
	var total int64
	add := func(it *item, start string, prefix string) error {
		if !it.Dir {
			info, err := os.Stat(it.path)
			if err != nil {
				return err
			}
			p := it.path
			out = append(out, zipEntry{name: prefix, size: info.Size(), mtime: info.ModTime(), open: func() (fs.File, error) { return os.Open(p) }})
			total += info.Size()
			return nil
		}
		fsys := it.root.FS()
		return fs.WalkDir(fsys, start, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip what we cannot read
			}
			if p != start && hidden(de.Name()) {
				if de.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			info, err := fs.Stat(fsys, p) // follows file symlinks inside the folder
			if err != nil {
				return nil
			}
			name := prefix
			if p != start {
				name = prefix + "/" + strings.TrimPrefix(p, start+"/")
				if start == "." {
					name = prefix + "/" + p
				}
			}
			switch {
			case de.IsDir():
				out = append(out, zipEntry{name: name + "/", mtime: info.ModTime()})
			case info.Mode().IsRegular():
				fp := p
				out = append(out, zipEntry{name: name, size: info.Size(), mtime: info.ModTime(), open: func() (fs.File, error) { return fsys.Open(fp) }})
				total += info.Size()
			}
			return nil
		})
	}

	if i < 0 {
		for _, it := range sh.items {
			if err := add(it, ".", it.Name); err != nil {
				return nil, 0, err
			}
		}
	} else {
		it, err := sh.item(i)
		if err != nil {
			return nil, 0, err
		}
		start := clean(rel)
		prefix := it.Name
		if start != "." {
			prefix = path.Base(start)
		}
		if err := add(it, start, prefix); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

// writeZip streams an uncompressed ZIP. Each file is read twice: once for the
// CRC, once for the data. With CRC and size known up front, the ZIP needs no
// data descriptors, which every unzip tool handles well.
func writeZip(ctx context.Context, w io.Writer, entries []zipEntry) error {
	bw := bufio.NewWriterSize(w, chunkSize)
	zw := zip.NewWriter(bw)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		fh := &zip.FileHeader{Name: nfc(e.name), Method: zip.Store, Flags: 0x800}
		setModTime(fh, e.mtime)
		if e.open == nil {
			fh.SetMode(fs.ModeDir | 0o755)
			if _, err := zw.CreateRaw(fh); err != nil {
				return err
			}
			continue
		}
		fh.SetMode(0o644)
		crc, n, err := checksum(e.open)
		if err != nil {
			continue // file vanished or unreadable: leave it out
		}
		fh.CRC32, fh.CompressedSize64, fh.UncompressedSize64 = crc, uint64(n), uint64(n)
		fw, err := zw.CreateRaw(fh)
		if err != nil {
			return err
		}
		f, err := e.open()
		if err != nil {
			return err
		}
		_, err = io.CopyN(fw, f, n)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return bw.Flush()
}

// setModTime does what zip.Writer.CreateHeader does for Modified, which
// CreateRaw skips: local-time DOS fields plus an "extended timestamp" field.
func setModTime(fh *zip.FileHeader, t time.Time) {
	t = t.Local()
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local)
	}
	fh.ModifiedDate = uint16(t.Day() + int(t.Month())<<5 + (t.Year()-1980)<<9)
	fh.ModifiedTime = uint16(t.Second()/2 + t.Minute()<<5 + t.Hour()<<11)
	var x [9]byte
	binary.LittleEndian.PutUint16(x[0:], 0x5455) // extended timestamp
	binary.LittleEndian.PutUint16(x[2:], 5)
	x[4] = 1 // mod time only
	binary.LittleEndian.PutUint32(x[5:], uint32(t.Unix()))
	fh.Extra = append(fh.Extra, x[:]...)
}

func checksum(open func() (fs.File, error)) (uint32, int64, error) {
	f, err := open()
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	n, err := io.Copy(h, f)
	return h.Sum32(), n, err
}

// nfc converts macOS file names (decomposed umlauts) to the composed form
// that other systems expect.
func nfc(s string) string { return norm.NFC.String(s) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func humanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
