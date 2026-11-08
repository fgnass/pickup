package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxThumbBatch = 12  // files per qlmanage run
	maxThumbSize  = 640 // pixels
)

// At most a few Quick Look processes at the same time.
var thumbSem = make(chan struct{}, 3)

// thumbBg is the page's panel colour. Thumbnails with transparency (PDF
// pages, icons) are flattened onto it.
var thumbBg = color.RGBA{0x1e, 0x23, 0x2b, 0xff}

// diskPath returns the absolute path of a shared file. os.Root.Stat fails for
// anything outside the folder, also through symlinks.
func (sh *share) diskPath(i int, rel string) (string, error) {
	it, err := sh.item(i)
	if err != nil {
		return "", err
	}
	rel, err = visiblePath(rel)
	if err != nil {
		return "", err
	}
	if !it.Dir {
		if rel != "." {
			return "", errors.New("not a folder")
		}
		return it.path, nil
	}
	info, err := it.root.Stat(rel)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	return filepath.Join(it.path, filepath.FromSlash(rel)), nil
}

// thumbnails renders Quick Look thumbnails and calls emit for each one that
// worked, as a JPEG data URL. Files without a thumbnail are left out.
//
// Images go to Quick Look in one batch. Videos and PDFs get one run each: a
// broken file can stall Quick Look until the timeout, and should not hold up
// the others.
func (sh *share) thumbnails(ctx context.Context, i int, rels []string, size int, emit func(rel, url string)) {
	size = max(64, min(size, maxThumbSize))
	var images []thumbJob
	var wg sync.WaitGroup
	for n, rel := range rels {
		if n == maxThumbBatch {
			break
		}
		p, err := sh.diskPath(i, rel)
		if err != nil {
			continue
		}
		job := thumbJob{rel: rel, file: p}
		if imageExt[strings.ToLower(filepath.Ext(p))] {
			images = append(images, job)
			continue
		}
		wg.Go(func() { quickLook(ctx, []thumbJob{job}, size, emit) })
	}
	if len(images) > 0 {
		wg.Go(func() { quickLook(ctx, images, size, emit) })
	}
	wg.Wait()
}

type thumbJob struct{ rel, file string }

var imageExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true,
	".heif": true, ".avif": true, ".bmp": true, ".tif": true, ".tiff": true,
}

func quickLook(ctx context.Context, jobs []thumbJob, size int, emit func(rel, url string)) {
	select {
	case thumbSem <- struct{}{}:
		defer func() { <-thumbSem }()
	case <-ctx.Done():
		return
	}

	out, err := os.MkdirTemp("", "pickup-thumbs-")
	if err != nil {
		return
	}
	defer os.RemoveAll(out)

	byBase := map[string]string{} // output file name -> request path
	args := []string{"-t", "-s", strconv.Itoa(size), "-o", out}
	for _, j := range jobs {
		base := filepath.Base(j.file) + ".png"
		if _, dup := byBase[base]; dup {
			continue
		}
		byBase[base] = j.rel
		args = append(args, j.file)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "/usr/bin/qlmanage", args...).Run() // use whatever it wrote

	for base, rel := range byBase {
		b, err := os.ReadFile(filepath.Join(out, base))
		if err != nil {
			continue
		}
		if url, err := toJPEG(b); err == nil {
			emit(rel, url)
		}
	}
}

func toJPEG(pngData []byte) (string, error) {
	src, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return "", err
	}
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), &image.Uniform{thumbBg}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
