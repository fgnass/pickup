package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	chunkSize = 64 << 10 // bytes per data channel message, including the 4-byte header
	highWater = 4 << 20  // pause sending above this many buffered bytes
	lowWater  = 1 << 20  // resume below this
)

// Protocol on the "pickup" data channel:
//
//	text   = JSON control message {"t": type, "id": request id, ...}
//	binary = 4-byte big-endian request id + payload bytes
//
// The browser must send {"t":"auth","secret":...} first.
type request struct {
	T      string   `json:"t"`
	ID     uint32   `json:"id"`
	Item   int      `json:"item"`
	Path   string   `json:"path"`
	Paths  []string `json:"paths"`
	Size   int      `json:"size"`
	Secret string   `json:"secret"`
}

type peer struct {
	s      *server
	pc     *webrtc.PeerConnection
	dc     *webrtc.DataChannel
	authed atomic.Bool
	closed atomic.Bool
	ctx    context.Context
	cancel context.CancelFunc

	sendMu sync.Mutex
	lowMu  sync.Mutex
	lowCh  chan struct{}

	reqMu sync.Mutex
	reqs  map[uint32]context.CancelFunc
}

func newPeer(s *server, pc *webrtc.PeerConnection) *peer {
	ctx, cancel := context.WithCancel(context.Background())
	return &peer{s: s, pc: pc, ctx: ctx, cancel: cancel, lowCh: make(chan struct{}), reqs: map[uint32]context.CancelFunc{}}
}

func (p *peer) attach(dc *webrtc.DataChannel) {
	p.dc = dc
	dc.SetBufferedAmountLowThreshold(lowWater)
	dc.OnBufferedAmountLow(func() {
		p.lowMu.Lock()
		close(p.lowCh)
		p.lowCh = make(chan struct{})
		p.lowMu.Unlock()
	})
	dc.OnClose(p.close)
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !msg.IsString {
			return
		}
		var req request
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			p.close()
			return
		}
		if !p.authed.Load() {
			p.auth(req)
			return
		}
		p.handle(req)
	})
}

func (p *peer) auth(req request) {
	got, err := base64.RawURLEncoding.DecodeString(req.Secret)
	if req.T != "auth" || err != nil || subtle.ConstantTimeCompare(got, p.s.secret) != 1 {
		emit("auth-failed", nil)
		p.close()
		return
	}
	p.authed.Store(true)
	p.s.peerAuthed()
	p.sendJSON(map[string]any{
		"t":      "hello",
		"name":   p.s.name,
		"items":  p.s.share.items,
		"zip":    p.s.share.zipName(-1, ""),
		"single": p.s.share.single(),
	})
}

func (p *peer) close() {
	if p.closed.Swap(true) {
		return
	}
	p.cancel()
	p.pc.Close()
	p.s.peerClosed(p.authed.Load())
}

func (p *peer) handle(req request) {
	switch req.T {
	case "list":
		go func() {
			entries, err := p.s.share.list(req.Item, req.Path)
			if err != nil {
				p.sendErr(req.ID, err)
				return
			}
			p.sendJSON(map[string]any{"t": "list", "id": req.ID, "entries": entries})
		}()
	case "thumbs":
		go p.s.share.thumbnails(p.ctx, req.Item, req.Paths, req.Size, func(rel, url string) {
			p.sendJSON(map[string]any{"t": "thumb", "item": req.Item, "path": rel, "url": url})
		})
	case "get", "zip":
		ctx, cancel := context.WithCancel(p.ctx)
		p.reqMu.Lock()
		p.reqs[req.ID] = cancel
		p.reqMu.Unlock()
		go func() {
			defer func() {
				cancel()
				p.reqMu.Lock()
				delete(p.reqs, req.ID)
				p.reqMu.Unlock()
			}()
			var err error
			if req.T == "get" {
				err = p.sendFile(ctx, req)
			} else {
				err = p.sendZip(ctx, req)
			}
			if err != nil && ctx.Err() == nil {
				p.sendErr(req.ID, err)
			}
		}()
	case "cancel":
		p.reqMu.Lock()
		if c := p.reqs[req.ID]; c != nil {
			c()
		}
		p.reqMu.Unlock()
	}
}

func (p *peer) sendFile(ctx context.Context, req request) error {
	f, info, err := p.s.share.open(req.Item, req.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	name := nfc(info.Name())
	p.sendJSON(map[string]any{"t": "start", "id": req.ID, "name": name, "size": info.Size(), "mime": mimeType(name)})
	w := p.frames(ctx, req.ID, name, info.Size())
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	if err := w.finish(); err != nil {
		return err
	}
	p.s.fileSent()
	return nil
}

func (p *peer) sendZip(ctx context.Context, req request) error {
	entries, total, err := p.s.share.zipEntries(req.Item, req.Path)
	if err != nil {
		return err
	}
	name := p.s.share.zipName(req.Item, req.Path)
	p.sendJSON(map[string]any{"t": "start", "id": req.ID, "name": name, "size": total, "approx": true, "mime": "application/zip"})
	w := p.frames(ctx, req.ID, name, total)
	if err := writeZip(ctx, w, entries); err != nil {
		return err
	}
	return w.finish()
}

func (p *peer) sendErr(id uint32, err error) {
	p.sendJSON(map[string]any{"t": "error", "id": id, "message": err.Error()})
}

func (p *peer) sendJSON(v any) {
	b, _ := json.Marshal(v)
	p.sendMu.Lock()
	p.dc.SendText(string(b))
	p.sendMu.Unlock()
}

// waitLow blocks while too much data is waiting in the send buffer.
func (p *peer) waitLow(ctx context.Context) error {
	for p.dc.BufferedAmount() > highWater {
		p.lowMu.Lock()
		ch := p.lowCh
		p.lowMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return ctx.Err()
}

// frameWriter cuts a byte stream into binary data channel messages.
type frameWriter struct {
	p     *peer
	ctx   context.Context
	id    uint32
	name  string
	total int64
	buf   []byte
	sent  int64
	last  time.Time
}

func (p *peer) frames(ctx context.Context, id uint32, name string, total int64) *frameWriter {
	w := &frameWriter{p: p, ctx: ctx, id: id, name: name, total: total}
	w.reset()
	return w
}

func (w *frameWriter) reset() {
	w.buf = make([]byte, 4, chunkSize)
	binary.BigEndian.PutUint32(w.buf, w.id)
}

func (w *frameWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		take := min(chunkSize-len(w.buf), len(b))
		w.buf = append(w.buf, b[:take]...)
		b = b[take:]
		if len(w.buf) == chunkSize {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (w *frameWriter) flush() error {
	if len(w.buf) == 4 {
		return nil
	}
	if err := w.p.waitLow(w.ctx); err != nil {
		return err
	}
	w.p.sendMu.Lock()
	err := w.p.dc.Send(w.buf)
	w.p.sendMu.Unlock()
	if err != nil {
		return err
	}
	w.sent += int64(len(w.buf) - 4)
	w.reset()
	if time.Since(w.last) > 250*time.Millisecond {
		w.last = time.Now()
		emit("progress", map[string]any{"id": w.id, "name": w.name, "sent": w.sent, "total": w.total})
	}
	return nil
}

func (w *frameWriter) finish() error {
	if err := w.flush(); err != nil {
		return err
	}
	w.p.sendJSON(map[string]any{"t": "end", "id": w.id, "size": w.sent})
	emit("sent", map[string]any{"id": w.id, "name": w.name, "size": w.sent})
	return nil
}

var extraTypes = map[string]string{
	".apk":  "application/vnd.android.package-archive",
	".heic": "image/heic",
	".md":   "text/markdown",
	".mkv":  "video/x-matroska",
}

func mimeType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := extraTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
