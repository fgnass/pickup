package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailedSignalReleasesPendingSlotOnce(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newServer(nil, net.ParseIP("127.0.0.1"), key, "")
	if err != nil {
		t.Fatal(err)
	}
	// Keep one unrelated pending peer to ensure failure cleanup preserves it.
	s.pending = 1
	for range 8 {
		r := httptest.NewRequest(http.MethodPost, "/signal", strings.NewReader(`{"type":"offer","sdp":"invalid"}`))
		w := httptest.NewRecorder()
		s.handleSignal(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", w.Code)
		}
	}
	// Allow asynchronous connection-state callbacks to run after setup cleanup.
	time.Sleep(100 * time.Millisecond)
	s.mu.Lock()
	pending := s.pending
	s.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	s.mu.Lock()
	s.pending = maxPending
	s.mu.Unlock()
	r := httptest.NewRequest(http.MethodPost, "/signal", strings.NewReader(`{"type":"offer","sdp":"invalid"}`))
	w := httptest.NewRecorder()
	s.handleSignal(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status at pending limit = %d, want 429", w.Code)
	}
}
