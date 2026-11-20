package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

// Photos from a phone often arrive with the date of the export as their
// modification time, not the date of the shot. For JPEG and HEIC files we read
// the capture time from the EXIF data and use that instead.

// exifScan is how far into the file we look for the EXIF block. In JPEG it is
// at the start; in HEIC from phones it is near the start of the media data.
const exifScan = 1 << 20

var photoExts = map[string]bool{".jpg": true, ".jpeg": true, ".heic": true, ".heif": true}

// photoTime returns the capture time of the photo in file, which root holds.
// name is the file's final name; its extension says if it is a photo.
func photoTime(root *os.Root, file, name string) (time.Time, bool) {
	if !photoExts[strings.ToLower(path.Ext(name))] {
		return time.Time{}, false
	}
	f, err := root.Open(file)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, exifScan))
	if err != nil {
		return time.Time{}, false
	}
	return exifTime(b)
}

// exifTime finds the first EXIF block in b and returns DateTimeOriginal,
// with OffsetTimeOriginal when it is there, else in local time.
func exifTime(b []byte) (time.Time, bool) {
	for off := 0; ; {
		i := bytes.Index(b[off:], []byte("Exif\x00\x00"))
		if i < 0 {
			return time.Time{}, false
		}
		start := off + i + 6
		if t, ok := tiffTime(b[start:]); ok {
			return t, true
		}
		off = start
	}
}

const (
	tagExifIFD       = 0x8769
	tagDateTime      = 0x0132
	tagDateOriginal  = 0x9003
	tagDateDigitized = 0x9004
	tagOffsetOrig    = 0x9011
)

// tiffTime reads the capture time from a TIFF structure (the body of an
// EXIF block).
func tiffTime(t []byte) (time.Time, bool) {
	if len(t) < 8 {
		return time.Time{}, false
	}
	var bo binary.ByteOrder
	switch string(t[:4]) {
	case "II*\x00":
		bo = binary.LittleEndian
	case "MM\x00*":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}
	ifd0 := ifdEntries(t, bo, bo.Uint32(t[4:]))
	if ifd0 == nil {
		return time.Time{}, false
	}
	var exif map[uint16][]byte
	if v, ok := ifd0[tagExifIFD]; ok && len(v) == 4 {
		exif = ifdEntries(t, bo, bo.Uint32(v))
	}
	for _, s := range [][]byte{exif[tagDateOriginal], exif[tagDateDigitized], ifd0[tagDateTime]} {
		if tm, ok := parseExifDate(s, exif[tagOffsetOrig]); ok {
			return tm, true
		}
	}
	return time.Time{}, false
}

// ifdEntries reads one IFD. It returns the value bytes per tag: for pointers
// (type LONG) the 4 raw bytes, for ASCII the text.
func ifdEntries(t []byte, bo binary.ByteOrder, off uint32) map[uint16][]byte {
	if uint64(off)+2 > uint64(len(t)) {
		return nil
	}
	n := int(bo.Uint16(t[off:]))
	p := int(off) + 2
	if p+n*12 > len(t) {
		return nil
	}
	out := map[uint16][]byte{}
	for i := 0; i < n; i++ {
		e := t[p+i*12 : p+i*12+12]
		tag, typ, count := bo.Uint16(e), bo.Uint16(e[2:]), bo.Uint32(e[4:])
		switch typ {
		case 4: // LONG
			if count == 1 {
				out[tag] = e[8:12]
			}
		case 2: // ASCII
			if count <= 4 {
				out[tag] = e[8 : 8+count]
				break
			}
			at := uint64(bo.Uint32(e[8:]))
			if count > 64 || at+uint64(count) > uint64(len(t)) {
				continue
			}
			out[tag] = t[at : at+uint64(count)]
		}
	}
	return out
}

// parseExifDate parses "2006:01:02 15:04:05" and an optional "+01:00".
func parseExifDate(s, offset []byte) (time.Time, bool) {
	ds := strings.TrimRight(string(s), "\x00 ")
	if len(ds) < 19 {
		return time.Time{}, false
	}
	ds = ds[:19]
	loc := time.Local
	if zs := strings.TrimRight(string(offset), "\x00 "); len(zs) == 6 {
		if z, err := time.Parse("-07:00", zs); err == nil {
			loc = z.Location()
		}
	}
	tm, err := time.ParseInLocation("2006:01:02 15:04:05", ds, loc)
	if err != nil || tm.Year() < 1990 || tm.After(time.Now().Add(48*time.Hour)) {
		return time.Time{}, false
	}
	return tm, true
}
