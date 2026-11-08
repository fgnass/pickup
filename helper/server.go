package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

const (
	maxPending  = 4                // unauthenticated peers at the same time
	authTimeout = 15 * time.Second // time to send the secret after signaling

	// The share ends by itself when nobody uses it:
	idleAfterUse  = 10 * time.Minute // after the last phone disconnected
	idleNeverUsed = 30 * time.Minute // when no phone ever connected
)

type server struct {
	share       *share
	name        string
	api         *webrtc.API
	cert        webrtc.Certificate
	secret      []byte
	fingerprint []byte

	mu        sync.Mutex
	pending   int
	connected int
	used      bool        // a phone was connected at least once
	delivered bool        // single-file share: the file was sent completely
	idle      *time.Timer // ends the share when it fires
}

func newServer(sh *share, ip net.IP, key *ecdsa.PrivateKey, name string) (*server, error) {
	cert, err := webrtc.GenerateCertificate(key)
	if err != nil {
		return nil, err
	}
	var fp []byte
	fps, err := cert.GetFingerprints()
	if err != nil {
		return nil, err
	}
	for _, f := range fps {
		if f.Algorithm == "sha-256" {
			fp, err = fingerprintBytes(f.Value)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(fp) != 32 {
		return nil, errors.New("no sha-256 fingerprint")
	}

	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}

	var se webrtc.SettingEngine
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	// Only offer the address that is in the QR code. This keeps VPN and
	// virtual interfaces out of the candidate list.
	se.SetIPFilter(func(a net.IP) bool { return a.Equal(ip) })
	// Browsers hide their host address behind an mDNS name.
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeQueryOnly)
	se.SetSCTPMaxReceiveBufferSize(4 << 20)

	return &server{
		share:       sh,
		name:        name,
		api:         webrtc.NewAPI(webrtc.WithSettingEngine(se)),
		cert:        *cert,
		secret:      secret,
		fingerprint: fp,
	}, nil
}

// handleSignal takes the browser's SDP offer and returns our answer. This
// endpoint is not secret. Access control happens in the data channel: the
// browser must send the secret from the QR code before it gets anything.
func (s *server) handleSignal(w http.ResponseWriter, r *http.Request) {
	var offer webrtc.SessionDescription
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&offer); err != nil || offer.Type != webrtc.SDPTypeOffer {
		http.Error(w, "bad offer", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.pending >= maxPending {
		s.mu.Unlock()
		http.Error(w, "busy", http.StatusTooManyRequests)
		return
	}
	s.pending++
	s.mu.Unlock()

	p, answer, err := s.newPeer(offer)
	if err != nil {
		s.mu.Lock()
		s.pending--
		s.mu.Unlock()
		http.Error(w, "webrtc error", http.StatusInternalServerError)
		return
	}
	time.AfterFunc(authTimeout, func() {
		if !p.authed.Load() {
			p.close()
		}
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

func (s *server) newPeer(offer webrtc.SessionDescription) (*peer, *webrtc.SessionDescription, error) {
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{
		Certificates: []webrtc.Certificate{s.cert},
	})
	if err != nil {
		return nil, nil, err
	}
	p := newPeer(s, pc)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "pickup" {
			dc.Close()
			return
		}
		p.attach(dc)
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			p.close()
		}
	})

	fail := func(err error) (*peer, *webrtc.SessionDescription, error) {
		pc.Close()
		return nil, nil, err
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		return fail(err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(err)
	}
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
	}
	return p, pc.LocalDescription(), nil
}

// peerAuthed and peerClosed keep the counters and tell the macOS app.
func (s *server) peerAuthed() {
	s.mu.Lock()
	s.pending--
	s.connected++
	s.used = true
	s.idle.Stop()
	n := s.connected
	s.mu.Unlock()
	emit("peers", map[string]any{"count": n})
}

func (s *server) peerClosed(wasAuthed bool) {
	s.mu.Lock()
	if wasAuthed {
		s.connected--
	} else {
		s.pending--
	}
	n, delivered := s.connected, s.delivered
	if n == 0 && wasAuthed {
		s.idle.Reset(idleAfterUse)
	}
	s.mu.Unlock()
	if !wasAuthed {
		return
	}
	emit("peers", map[string]any{"count": n})
	// A single file that the phone has: nothing left to share.
	if n == 0 && delivered {
		end("delivered")
	}
}

// fileSent records a complete download. For a single-file share, the share
// ends when the last phone disconnects after this.
func (s *server) fileSent() {
	if !s.share.single() {
		return
	}
	s.mu.Lock()
	s.delivered = true
	s.mu.Unlock()
}

// startIdleTimer arms the timer that ends an unused share.
func (s *server) startIdleTimer() {
	s.mu.Lock()
	s.idle = time.AfterFunc(idleNeverUsed, func() { end("idle") })
	s.mu.Unlock()
}
