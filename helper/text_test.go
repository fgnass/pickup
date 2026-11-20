package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"strings"
	"testing"
)

func newTextServer(t *testing.T) *server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newServer(nil, net.ParseIP("127.0.0.1"), key, "")
	if err != nil {
		t.Fatal(err)
	}
	s.textOnly = true
	return s
}

func TestAddText(t *testing.T) {
	s := newTextServer(t)
	if _, err := s.addText(" \n\t"); err != errTextEmpty {
		t.Fatalf("blank text: err = %v, want errTextEmpty", err)
	}
	// "<" would grow to 6 bytes with HTML escaping; it must not.
	if _, err := s.addText(strings.Repeat("<", maxTextBytes-100)); err != nil {
		t.Fatalf("text below the limit: %v", err)
	}
	// Newlines grow to 2 bytes in JSON, so this is above the limit.
	if _, err := s.addText("x" + strings.Repeat("\n", maxTextBytes/2+10)); err != errTextLong {
		t.Fatalf("long text: err = %v, want errTextLong", err)
	}
	if _, err := s.addText("hello"); err != nil {
		t.Fatal(err)
	}
	texts := s.textList()
	if len(texts) != 2 || texts[1].ID != 2 || texts[1].Text != "hello" {
		t.Fatalf("texts = %+v", texts)
	}
}

func TestTextCopiedMarksDelivered(t *testing.T) {
	s := newTextServer(t)
	s.textCopied(1) // no such text yet
	if s.delivered {
		t.Fatal("delivered before a text exists")
	}
	if _, err := s.addText("hello"); err != nil {
		t.Fatal(err)
	}
	s.textCopied(1)
	if !s.delivered {
		t.Fatal("not delivered after copy")
	}
}
