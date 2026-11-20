package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tiffWith builds a TIFF body: IFD0 with a pointer to an EXIF IFD that holds
// DateTimeOriginal and, if offset is not empty, OffsetTimeOriginal.
func tiffWith(bo binary.ByteOrder, date, offset string) []byte {
	var b bytes.Buffer
	if bo == binary.LittleEndian {
		b.WriteString("II*\x00")
	} else {
		b.WriteString("MM\x00*")
	}
	u16 := func(v uint16) { binary.Write(&b, bo, v) }
	u32 := func(v uint32) { binary.Write(&b, bo, v) }
	entry := func(tag, typ uint16, count, value uint32) { u16(tag); u16(typ); u32(count); u32(value) }

	u32(8) // IFD0 at 8
	// IFD0: one entry, then the next-IFD link.
	exifAt := uint32(8 + 2 + 12 + 4)
	u16(1)
	entry(tagExifIFD, 4, 1, exifAt)
	u32(0)
	// EXIF IFD.
	n := uint16(1)
	if offset != "" {
		n = 2
	}
	dataAt := exifAt + 2 + uint32(n)*12 + 4
	u16(n)
	entry(tagDateOriginal, 2, uint32(len(date)+1), dataAt)
	if offset != "" {
		entry(tagOffsetOrig, 2, uint32(len(offset)+1), dataAt+uint32(len(date)+1))
	}
	u32(0)
	b.WriteString(date + "\x00")
	if offset != "" {
		b.WriteString(offset + "\x00")
	}
	return b.Bytes()
}

func jpegWith(tiff []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	binary.Write(&b, binary.BigEndian, uint16(2+6+len(tiff)))
	b.WriteString("Exif\x00\x00")
	b.Write(tiff)
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

func TestExifTime(t *testing.T) {
	cet := time.FixedZone("", 3600)
	cases := []struct {
		name string
		data []byte
		want time.Time
	}{
		{"big endian with offset", jpegWith(tiffWith(binary.BigEndian, "2024:07:14 18:30:05", "+01:00")),
			time.Date(2024, 7, 14, 18, 30, 5, 0, cet)},
		{"little endian, local time", jpegWith(tiffWith(binary.LittleEndian, "2023:01:02 03:04:05", "")),
			time.Date(2023, 1, 2, 3, 4, 5, 0, time.Local)},
		// HEIC: the EXIF item sits somewhere after other boxes.
		{"inside other data", append(append(bytes.Repeat([]byte{0}, 5000), "Exif\x00\x00"...), tiffWith(binary.BigEndian, "2022:12:31 23:59:59", "-05:00")...),
			time.Date(2022, 12, 31, 23, 59, 59, 0, time.FixedZone("", -5*3600))},
	}
	for _, c := range cases {
		got, ok := exifTime(c.data)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s: got %v %v, want %v", c.name, got, ok, c.want)
		}
	}

	for name, data := range map[string][]byte{
		"no exif":    []byte("\xFF\xD8hello"),
		"cut off":    jpegWith(tiffWith(binary.BigEndian, "2024:07:14 18:30:05", ""))[:30],
		"bad date":   jpegWith(tiffWith(binary.BigEndian, "0000:00:00 00:00:00", "")),
		"future":     jpegWith(tiffWith(binary.BigEndian, "2999:01:01 00:00:00", "")),
		"bad offset": jpegWith(tiffWith(binary.BigEndian, "2024:07:14 18:30:0", "")),
	} {
		if got, ok := exifTime(data); ok {
			t.Errorf("%s: got %v, want nothing", name, got)
		}
	}
}

func TestReceivedPhotoGetsCaptureTime(t *testing.T) {
	dir := t.TempDir()
	in, err := newInbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	photo := string(jpegWith(tiffWith(binary.BigEndian, "2024:07:14 18:30:05", "+02:00")))
	want := time.Date(2024, 7, 14, 16, 30, 5, 0, time.UTC)

	for name, wantTime := range map[string]time.Time{
		"IMG_1.JPG": want,
		// Not a photo by name: keeps the time from the phone (see receiveFile).
		"notes.txt": time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC),
	} {
		got, err := receiveFile(t, in, name, photo)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, got))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(wantTime) {
			t.Errorf("%s: mtime = %v, want %v", name, info.ModTime().UTC(), wantTime)
		}
	}
}
