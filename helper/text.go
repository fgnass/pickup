package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// Text from the Mac's clipboard or selection, for the phone's clipboard.
//
// The Mac app sends it as a command on stdin. The helper keeps every text of
// the session and sends it to all connected phones:
//
//	{"t":"text","id":N,"text":...}  a new text
//	hello carries "texts": [{"id":N,"text":...}, ...] for phones that connect later
//
// The phone answers {"t":"copied","id":N} when the user copied or opened it.

// maxTextBytes keeps one text message well below the data channel's message
// size limit (64 KiB when the browser does not say more), also when JSON
// escaping makes it larger.
const maxTextBytes = 64000

type textItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

var errTextEmpty = errors.New("the text is empty")
var errTextLong = errors.New("the text is too long; save it as a file and share the file")

// addText stores the text and sends it to all connected phones. It returns the
// number of phones that got it.
func (s *server) addText(text string) (int, error) {
	if strings.TrimSpace(text) == "" {
		return 0, errTextEmpty
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	s.mu.Lock()
	t := textItem{ID: len(s.texts) + 1, Text: text}
	msg := map[string]any{"t": "text", "id": t.ID, "text": t.Text}
	if len(marshal(msg)) > maxTextBytes {
		s.mu.Unlock()
		return 0, errTextLong
	}
	s.texts = append(s.texts, t)
	peers := s.peerList()
	s.mu.Unlock()
	for _, p := range peers {
		p.sendJSON(msg)
	}
	return len(peers), nil
}

// textList returns a copy of all texts of this session.
func (s *server) textList() []textItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]textItem{}, s.texts...)
}

// textCopied records that a phone has the text. In text mode, the share ends
// when the last phone disconnects after this, like a single file.
func (s *server) textCopied(id int) {
	s.mu.Lock()
	ok := id >= 1 && id <= len(s.texts)
	if ok && s.textOnly {
		s.delivered = true
	}
	s.mu.Unlock()
	if ok {
		emit("copied", map[string]any{"id": id})
	}
}

// marshal encodes v as JSON without HTML escaping, so "<" stays one byte.
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}
