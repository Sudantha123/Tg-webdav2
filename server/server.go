// Package server wires the HTTP layer: /dav (WebDAV), /web (UI + JSON API).
package server

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Sudantha123/Tg-webdav2/config"
	"github.com/Sudantha123/Tg-webdav2/store"
	"github.com/Sudantha123/Tg-webdav2/tgram"
	"golang.org/x/net/webdav"
)

// Version is set via ldflags at build time.
var Version = "dev"

// Server is the top level HTTP app.
type Server struct {
	Cfg    *config.Config
	St     *store.Store
	FS     *store.FS
	TG     store.Telegram
	Bot    *tgram.BotAPI
	Log    *slog.Logger
	DAV    webdav.Handler
}

// New builds the server.
func New(cfg *config.Config, st *store.Store, vfs *store.FS, tg store.Telegram, bot *tgram.BotAPI, log *slog.Logger) *Server {
	s := &Server{Cfg: cfg, St: st, FS: vfs, TG: tg, Bot: bot, Log: log}
	s.DAV = webdav.Handler{
		Prefix:     "/dav",
		FileSystem: vfs,
		LockSystem: webdav.NewMemLS(),
	}
	return s
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/web/", http.StatusFound)
	})

	mux.Handle("/dav", s.auth(http.HandlerFunc(s.davRoot)))
	mux.Handle("/dav/", s.auth(http.HandlerFunc(s.handleDav)))
	mux.Handle("/web", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusFound)
	})))
	mux.Handle("/web/", s.auth(http.HandlerFunc(s.handleWeb)))

	return mux
}

// auth enforces basic auth for every configured credential pair.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		valid := false
		if ok {
			if subtle.ConstantTimeCompare([]byte(user), []byte(s.Cfg.WebUser)) == 1 &&
				subtle.ConstantTimeCompare([]byte(pass), []byte(s.Cfg.WebPass)) == 1 {
				valid = true
			}
			if !valid {
				for u, p := range s.Cfg.WebUsers {
					if subtle.ConstantTimeCompare([]byte(user), []byte(u)) == 1 &&
						subtle.ConstantTimeCompare([]byte(pass), []byte(p)) == 1 {
						valid = true
						break
					}
				}
			}
		}
		if !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="TgWebDAV", charset="UTF-8"`)
			http.Error(w, "401 unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) davRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dav/", http.StatusFound)
}

// handleDav serves WebDAV methods, intercepting COPY/MOVE for instant
// server-side copies inside telegram.
func (s *Server) handleDav(w http.ResponseWriter, r *http.Request) {
	if r.Method == "COPY" {
		if s.smartCopy(w, r) {
			return
		}
	}
	s.DAV.ServeHTTP(w, r)
}
// smartCopy performs a channel-level copy (no bytes through the server).
// Returns false when the request should fall through to the default
// webdav handler.
func (s *Server) smartCopy(w http.ResponseWriter, r *http.Request) bool {
	src, ok := davPath(r.URL.Path)
	if !ok {
		return false
	}
	dstHdr := r.Header.Get("Destination")
	if dstHdr == "" {
		return false
	}
	dstURL, err := url.Parse(dstHdr)
	if err != nil {
		return false
	}
	dst, ok := davPath(dstURL.Path)
	if !ok || src == dst {
		return false
	}

	if s.TG == nil {
		return false // no telegram client (tests)
	}
	ctx := r.Context()
	srcEntry, err := s.St.Stat(ctx, src)
	if err != nil || srcEntry.IsDir {
		return false // folders/missing fall through
	}
	if _, err := s.St.Stat(ctx, dst); err == nil {
		return false // overwrite -> let the webdav handler deal with it
	}

	newMsgID, err := s.TG.Copy(ctx, srcEntry.MsgID)
	if err != nil {
		s.Log.Warn("dav copy: telegram copy failed", "err", err)
		return false
	}
	folder := store.Parent(dst)
	name := store.Base(dst)
	_ = s.St.EnsureFolder(ctx, folder)
	if _, err := s.St.PutFileUnique(ctx, folder, name, newMsgID, srcEntry.Size, srcEntry.Mime, srcEntry.Kind, ""); err != nil {
		s.Log.Error("dav copy: index failed", "err", err)
		http.Error(w, "500 copy failed", http.StatusInternalServerError)
		return true
	}
	w.Header().Set("Location", "/dav"+dst)
	w.WriteHeader(http.StatusCreated)
	return true
}

// davPath strips the /dav prefix from a URL path and normalizes it.
func davPath(p string) (string, bool) {
	if p == "/dav" || p == "/dav/" {
		return "/", true
	}
	if !strings.HasPrefix(p, "/dav/") {
		return "", false
	}
	n := store.Normalize(strings.TrimPrefix(p, "/dav"))
	if n == "" {
		return "", false
	}
	return n, true
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSONBody decodes a JSON request body into v (max 1MB).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "bad json body: "+err.Error())
		return false
	}
	return true
}
