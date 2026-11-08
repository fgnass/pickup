package main

import (
	"encoding/binary"
	"errors"
	"time"
)

// Receive mode protocol (phone → Mac) on the same data channel:
//
//	{"t":"put","id":N,"name":...,"size":...,"mtime":...}  announce a file
//	binary frames: 4-byte id + data, in order
//	{"t":"done","id":N}    all data sent; Mac answers {"t":"saved","id":N,"name":final}
//	{"t":"cancel","id":N}  drop the file
//	{"t":"run","run":{...}} the phone added files to the batch
//
// "put" and "run" carry the batch that the phone is sending (files and
// bytes in total, and how many of them are done), so the Mac can show the
// total progress.
//
// On any problem the Mac answers {"t":"error","id":N,"message":...} and
// ignores further frames for that id.
//
// Pion delivers messages one by one, so the Mac writes to disk in order. A
// slow disk slows down the sender through SCTP flow control.

const maxUploads = 8 // open uploads per phone

type run struct {
	Files     int   `json:"files"`
	Bytes     int64 `json:"bytes"`
	Done      int   `json:"done"`
	DoneBytes int64 `json:"doneBytes"`
}

// progress reports the total progress of the batch. Call with upMu held.
func (p *peer) progress(id uint32, u *upload) {
	r := p.run
	if r.Files < 1 || r.Bytes < r.DoneBytes+u.size {
		r = run{Files: 1, Bytes: u.size} // no batch info: just this file
	}
	emit("progress", map[string]any{
		"id": id, "name": u.name,
		"sent": r.DoneBytes + u.got, "total": r.Bytes,
		"index": r.Done + 1, "files": r.Files,
	})
}

func (p *peer) handleReceive(req request) {
	switch req.T {
	case "run":
		if req.Run != nil {
			p.upMu.Lock()
			p.run = *req.Run
			p.upMu.Unlock()
		}
	case "put":
		p.upMu.Lock()
		defer p.upMu.Unlock()
		if req.Run != nil {
			p.run = *req.Run
		}
		if p.closed.Load() {
			return
		}
		if p.ups[req.ID] != nil || len(p.ups) >= maxUploads {
			p.sendErr(req.ID, errors.New("too many files at the same time"))
			return
		}
		u, err := p.s.dropOff().begin(req.Name, int64(req.Size), req.Mtime)
		if err != nil {
			p.sendErr(req.ID, err)
			return
		}
		p.ups[req.ID] = u
		p.progress(req.ID, u)
	case "done":
		p.upMu.Lock()
		u := p.ups[req.ID]
		delete(p.ups, req.ID)
		p.upMu.Unlock()
		if u == nil {
			return
		}
		name, err := u.in.commit(u)
		if err != nil {
			p.sendErr(req.ID, err)
			emit("receive-failed", map[string]any{"name": u.name, "message": err.Error()})
			return
		}
		p.sendJSON(map[string]any{"t": "saved", "id": req.ID, "name": name})
		emit("received", map[string]any{"id": req.ID, "name": name, "path": u.in.dir + "/" + name, "size": u.size})
	case "cancel":
		p.upMu.Lock()
		u := p.ups[req.ID]
		delete(p.ups, req.ID)
		p.upMu.Unlock()
		if u != nil {
			u.in.abort(u)
			emit("receive-failed", map[string]any{"name": u.name, "message": "cancelled"})
		}
	}
}

func (p *peer) receiveData(b []byte) {
	if len(b) < 4 {
		return
	}
	id := binary.BigEndian.Uint32(b)
	p.upMu.Lock()
	defer p.upMu.Unlock()
	u := p.ups[id]
	if u == nil {
		return
	}
	if err := u.write(b[4:]); err != nil {
		delete(p.ups, id)
		u.in.abort(u)
		p.sendErr(id, err)
		emit("receive-failed", map[string]any{"name": u.name, "message": err.Error()})
		return
	}
	if time.Since(u.last) > 250*time.Millisecond {
		u.last = time.Now()
		p.progress(id, u)
	}
}

// abortUploads removes the part files of a phone that went away.
func (p *peer) abortUploads() {
	p.upMu.Lock()
	defer p.upMu.Unlock()
	for id, u := range p.ups {
		u.in.abort(u)
		delete(p.ups, id)
		emit("receive-failed", map[string]any{"name": u.name, "message": "connection lost"})
	}
}
