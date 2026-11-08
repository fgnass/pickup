// pickup-helper shares files and folders with a phone on the local network.
//
// It serves a small web app over plain HTTP. The QR code URL carries a secret
// and the DTLS fingerprint of this process in the URL fragment, which the
// browser never sends over the network. The web app connects back over WebRTC,
// checks the fingerprint, and sends the secret inside the encrypted data
// channel. All file data travels over that channel.
//
// The helper writes JSON lines to stdout (events for the macOS app) and exits
// when stdin closes.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

//go:embed web
var webFiles embed.FS

var outMu sync.Mutex

// emit writes one event line to stdout for the macOS app.
func emit(typ string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = typ
	b, _ := json.Marshal(fields)
	outMu.Lock()
	os.Stdout.Write(append(b, '\n'))
	outMu.Unlock()
}

// end tells the macOS app why the share is over, then exits.
func end(reason string) {
	emit("ended", map[string]any{"reason": reason})
	os.Exit(0)
}

func fatal(err error) {
	emit("error", map[string]any{"message": err.Error()})
	os.Exit(1)
}

func main() {
	name := flag.String("name", "", "name of this Mac, shown on the phone")
	ipFlag := flag.String("ip", "", "local IPv4 address to use (default: auto)")
	receive := flag.String("receive", "", "let the phone drop files into this folder instead of sharing PATHs")
	flag.Parse()
	if (flag.NArg() == 0) == (*receive == "") {
		fmt.Fprintln(os.Stderr, "usage: pickup-helper [-name NAME] [-ip IP] PATH...\n       pickup-helper [-name NAME] [-ip IP] -receive FOLDER")
		os.Exit(2)
	}

	var (
		sh  *share
		in  *inbox
		err error
	)
	if *receive != "" {
		in, err = newInbox(*receive)
	} else {
		sh, err = newShare(flag.Args())
	}
	if err != nil {
		fatal(err)
	}

	ip, err := pickIP(*ipFlag)
	if err != nil {
		fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(err)
	}
	srv, err := newServer(sh, ip, key, *name)
	if err != nil {
		fatal(err)
	}
	srv.receive = in != nil
	srv.inbox = in

	ln, err := net.Listen("tcp4", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	// Fragment: base64url(secret[16] || sha-256 fingerprint[32]).
	frag := base64.RawURLEncoding.EncodeToString(append(append([]byte{}, srv.secret...), srv.fingerprint...))
	url := fmt.Sprintf("http://%s:%d/#%s", ip, port, frag)

	var title, subtitle string
	if in != nil {
		title, subtitle = in.summary()
	} else {
		title, subtitle = sh.summary()
	}
	emit("ready", map[string]any{"url": url, "title": title, "subtitle": subtitle, "receive": in != nil})

	srv.startIdleTimer()

	// The macOS app holds our stdin open and sends commands as JSON lines.
	// When it goes away, we go too.
	go func() {
		commands(srv, os.Stdin)
		os.Exit(0)
	}()

	if err := http.Serve(ln, srv.routes()); err != nil {
		fatal(err)
	}
}

// commands reads JSON lines from the macOS app until r ends.
//
//	{"folder": "/path"}  receive mode: save new files in this folder
func commands(s *server, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var cmd struct {
			Folder string `json:"folder"`
		}
		if json.Unmarshal(sc.Bytes(), &cmd) != nil || cmd.Folder == "" || !s.receive {
			continue
		}
		in, err := newInbox(cmd.Folder)
		if err != nil {
			emit("folder-failed", map[string]any{"message": err.Error()})
			continue
		}
		s.setDropOff(in)
		title, subtitle := in.summary()
		emit("folder", map[string]any{"path": in.dir, "title": title, "subtitle": subtitle})
	}
}

func (s *server) routes() http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /signal", s.handleSignal)
	mux.HandleFunc("GET /{file...}", func(w http.ResponseWriter, r *http.Request) {
		f := r.PathValue("file")
		if f != "" && (strings.Contains(f, "/") || !exists(static, f)) {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	return securityHeaders(mux)
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)
	return err == nil
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Cache-Control", "no-store")
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Content-Type-Options", "nosniff")
		h.ServeHTTP(w, r)
	})
}

func fingerprintBytes(s string) ([]byte, error) {
	return hex.DecodeString(strings.ReplaceAll(s, ":", ""))
}
