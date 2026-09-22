package server

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Sudantha123/Tg-webdav2/internal/davname"
	"github.com/Sudantha123/Tg-webdav2/store"
)

//go:embed all:web/static
var staticFS embed.FS

// handleWeb serves the embedded UI and the JSON API under /web/api/*.
func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/web")
	if p == "" || p == "/" {
		s.serveIndex(w, r)
		return
	}
	if strings.HasPrefix(p, "/api/") {
		s.handleAPI(w, r, strings.TrimPrefix(p, "/api/"))
		return
	}

	name := strings.TrimPrefix(path.Clean(p), "/")
	data, err := staticFS.ReadFile("web/static/" + name)
	if err != nil {
		// Missing CSS/JS must be a real 404, not index.html. Returning HTML
		// for an asset request can make the browser refuse the asset and
		// leave the page apparently blank/broken.
		http.NotFound(w, r)
		return
	}

	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytesReader(data))
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("web/static/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func bytesReader(b []byte) io.ReadSeeker { return &byteSeeker{b: b} }

type byteSeeker struct {
	b []byte
	i int64
}

func (s *byteSeeker) Read(p []byte) (int, error) {
	if s.i >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.i:])
	s.i += int64(n)
	return n, nil
}
func (s *byteSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		off += s.i
	case io.SeekEnd:
		off += int64(len(s.b))
	default:
		return 0, fs.ErrInvalid
	}
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	s.i = off
	return s.i, nil
}

// handleAPI routes /web/api/<action>.
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, action string) {
	ctx := r.Context()
	switch action {
	case "list":
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		p := store.Normalize(r.URL.Query().Get("path"))
		if p == "" {
			fail(w, http.StatusBadRequest, "bad path")
			return
		}
		entries, err := s.St.List(ctx, p)
		if err != nil {
			fail(w, http.StatusNotFound, "folder not found")
			return
		}
		folders, files := []store.Entry{}, []store.Entry{}
		for _, e := range entries {
			if e.IsDir {
				folders = append(folders, e)
			} else {
				files = append(files, e)
			}
		}
		def, _ := s.St.DefaultFolder(ctx, s.Cfg.DefaultFolder)
		writeJSON(w, http.StatusOK, map[string]any{
			"path": p, "folders": folders, "files": files, "default_folder": def,
		})

	case "stats":
		files, bytesCount, folders, err := s.St.Stats(ctx)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"files": files, "bytes": bytesCount, "folders": folders,
			"version": Version, "cache_bytes": s.FS.Cache.Len(),
		})

	case "settings":
		switch r.Method {
		case http.MethodGet:
			def, _ := s.St.DefaultFolder(ctx, s.Cfg.DefaultFolder)
			writeJSON(w, http.StatusOK, map[string]string{"default_folder": def})
		case http.MethodPost:
			var body struct {
				DefaultFolder string `json:"default_folder"`
			}
			if !decodeJSONBody(w, r, &body) {
				return
			}
			f := strings.Trim(strings.TrimSpace(body.DefaultFolder), "/")
			if f == "" || strings.Contains(f, "..") {
				fail(w, http.StatusBadRequest, "invalid folder")
				return
			}
			if err := s.St.SetDefaultFolder(ctx, f); err != nil {
				fail(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = s.St.EnsureFolder(ctx, "/"+f)
			writeJSON(w, http.StatusOK, map[string]string{"default_folder": store.Normalize("/" + f)})
		default:
			fail(w, http.StatusMethodNotAllowed, "GET/POST only")
		}

	case "mkdir":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		p := store.Normalize(body.Path)
		if p == "" || p == "/" {
			fail(w, http.StatusBadRequest, "invalid folder")
			return
		}
		if err := s.St.EnsureFolder(ctx, p); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": p})

	case "delete":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		msgIDs, err := s.St.Delete(ctx, store.Normalize(body.Path))
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if s.Cfg.DeleteFromTelegram {
			for _, id := range msgIDs {
				_ = s.TG.Delete(ctx, id)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "rename":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Path    string `json:"path"`
			NewName string `json:"new_name"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		p := store.Normalize(body.Path)
		newName := davname.Sanitize(body.NewName)
		if p == "" || p == "/" || newName == "" {
			fail(w, http.StatusBadRequest, "invalid rename")
			return
		}
		np := store.Normalize(store.Parent(p) + "/" + newName)
		if err := s.St.Rename(ctx, p, np); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": np})

	case "move":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Path string `json:"path"`
			Dest string `json:"dest"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		p := store.Normalize(body.Path)
		dest := store.Normalize(body.Dest)
		if p == "" || dest == "" || dest == "/" {
			fail(w, http.StatusBadRequest, "invalid move")
			return
		}
		if err := s.St.EnsureFolder(ctx, dest); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		np := store.Normalize(dest + "/" + store.Base(p))
		if err := s.St.Rename(ctx, p, np); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": np})

	case "copy":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Path string `json:"path"`
			Dest string `json:"dest"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		p := store.Normalize(body.Path)
		dest := store.Normalize(body.Dest)
		e, err := s.St.Stat(ctx, p)
		if err != nil || e.IsDir {
			fail(w, http.StatusBadRequest, "only files can be copied")
			return
		}
		if err := s.St.EnsureFolder(ctx, dest); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		newMsgID, err := s.TG.Copy(ctx, e.MsgID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "telegram copy: "+err.Error())
			return
		}
		entry, err := s.St.PutFileUnique(ctx, dest, store.Base(p), newMsgID, e.Size, e.Mime, e.Kind, "")
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, entry)

	case "upload":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		s.handleUpload(w, r)

	default:
		fail(w, http.StatusNotFound, "unknown api action")
	}
}

// handleUpload streams a multipart upload straight into telegram.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, http.StatusBadRequest, "bad multipart: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	folder := store.Normalize(r.FormValue("path"))
	if folder == "" || folder == "/" {
		def, _ := s.St.DefaultFolder(ctx, s.Cfg.DefaultFolder)
		folder = def
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	name := davname.Sanitize(hdr.Filename)
	if name == "" {
		name = davname.Random("file", "", time.Now())
	}
	mime := hdr.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		mime = davname.DetectMime(name)
	}
	size := hdr.Size

	if err := s.St.EnsureFolder(ctx, folder); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	msgID, err := s.TG.Upload(ctx, name, mime, file, size)
	if err != nil {
		fail(w, http.StatusBadGateway, "telegram upload: "+err.Error())
		return
	}
	entry, err := s.St.PutFileUnique(ctx, folder, name, msgID, size, mime, davname.KindFor(mime, name), "")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}
