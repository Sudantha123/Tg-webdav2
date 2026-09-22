package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Sudantha123/Tg-webdav2/config"
	"github.com/Sudantha123/Tg-webdav2/store"
)

// fakeTG mirrors store.fakeTG (kept separate to avoid export gymnastics).
type fakeTG struct {
	mu     sync.Mutex
	blocks map[int64][][]byte
	nextID int64
}

func newFakeTG() *fakeTG { return &fakeTG{blocks: map[int64][][]byte{}, nextID: 500} }

func (f *fakeTG) ReadBlock(_ context.Context, msgID, block int64, buf []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bs := f.blocks[msgID]
	if block >= int64(len(bs)) {
		return 0, nil
	}
	return copy(buf, bs[block]), nil
}

func (f *fakeTG) Upload(_ context.Context, _ string, _ string, r io.Reader, size int64) (int64, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, err
	}
	f.mu.Lock()
	id := f.nextID
	f.nextID++
	f.mu.Unlock()
	for off := 0; off < len(data); off += store.BlockSize {
		end := off + store.BlockSize
		if end > len(data) {
			end = len(data)
		}
		blk := make([]byte, end-off)
		copy(blk, data[off:end])
		f.mu.Lock()
		f.blocks[id] = append(f.blocks[id], blk)
		f.mu.Unlock()
	}
	return id, nil
}

func (f *fakeTG) Delete(_ context.Context, msgID int64) error { return nil }
func (f *fakeTG) Copy(_ context.Context, msgID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID
	f.nextID++
	f.blocks[id] = f.blocks[msgID]
	return id, nil
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	_ = st.EnsureFolder(ctx, "/")
	_ = st.SeedDefaultFolder(ctx, "general")
	_ = st.EnsureFolder(ctx, "/general")

	tg := newFakeTG()
	vfs := store.NewFS(st, tg, store.NewCache(4<<20), dir, 2, false)

	cfg := &config.Config{
		Port: 0, DataDir: dir, TmpDir: dir,
		BotToken: "t", APIID: 1, APIHash: "h", ChannelID: -1001,
		DefaultFolder: "general", WebUser: "admin", WebPass: "pass",
		CacheMB: 4, Prefetch: 2, DeleteFromTelegram: false, LogLevel: "error",
	}
	s := New(cfg, st, vfs, nil, nil, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func basicAuth(req *http.Request) { req.SetBasicAuth("admin", "pass") }

func TestAuthRequired(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/dav/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)
	res, _ := http.Get(ts.URL + "/healthz")
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("healthz = %d", res.StatusCode)
	}
}

func TestWebDAVFlow(t *testing.T) {
	_, ts := newTestServer(t)
	c := &http.Client{}

	// PROPFIND on root
	req, _ := http.NewRequest("PROPFIND", ts.URL+"/dav/", nil)
	req.Header.Set("Depth", "1")
	basicAuth(req)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 207 {
		t.Fatalf("propfind = %d %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "general") {
		t.Fatalf("general folder missing in propfind: %s", body)
	}

	// MKCOL
	req, _ = http.NewRequest("MKCOL", ts.URL+"/dav/movies/", nil)
	basicAuth(req)
	res, _ = c.Do(req)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("mkcol = %d", res.StatusCode)
	}

	// PUT a file
	payload := bytes.Repeat([]byte("hello-webdav "), store.BlockSize/13)
	req, _ = http.NewRequest("PUT", ts.URL+"/dav/movies/clip.mp4", bytes.NewReader(payload))
	basicAuth(req)
	res, _ = c.Do(req)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("put = %d", res.StatusCode)
	}

	// GET with Range (seek!)
	req, _ = http.NewRequest("GET", ts.URL+"/dav/movies/clip.mp4", nil)
	req.Header.Set("Range", "bytes=100-199")
	basicAuth(req)
	res, _ = c.Do(req)
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("range get = %d", res.StatusCode)
	}
	if !bytes.Equal(got, payload[100:200]) {
		t.Fatalf("range mismatch: got %d bytes", len(got))
	}

	// COPY (smart, server-side)
	req, _ = http.NewRequest("COPY", ts.URL+"/dav/movies/clip.mp4", nil)
	req.Header.Set("Destination", ts.URL+"/dav/movies/clip-copy.mp4")
	basicAuth(req)
	res, _ = c.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("copy = %d", res.StatusCode)
	}

	// GET the copy
	req, _ = http.NewRequest("GET", ts.URL+"/dav/movies/clip-copy.mp4", nil)
	basicAuth(req)
	res, _ = c.Do(req)
	got, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Equal(got, payload) {
		t.Fatal("copy content mismatch")
	}

	// DELETE
	req, _ = http.NewRequest("DELETE", ts.URL+"/dav/movies/", nil)
	basicAuth(req)
	res, _ = c.Do(req)
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("delete = %d", res.StatusCode)
	}
}

func TestWebAPI(t *testing.T) {
	_, ts := newTestServer(t)
	c := &http.Client{}

	// list
	req, _ := http.NewRequest("GET", ts.URL+"/web/api/list?path=/", nil)
	basicAuth(req)
	res, _ := c.Do(req)
	var data struct {
		Folders []json.RawMessage `json:"folders"`
	}
	_ = json.NewDecoder(res.Body).Decode(&data)
	res.Body.Close()
	if len(data.Folders) != 1 {
		t.Fatalf("folders = %s", data.Folders)
	}

	// mkdir + settings
	req, _ = http.NewRequest("POST", ts.URL+"/web/api/settings", strings.NewReader(`{"default_folder":"media"}`))
	basicAuth(req)
	res, _ = c.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("settings = %d", res.StatusCode)
	}
	res.Body.Close()

	// upload via api
	var buf bytes.Buffer
	buf.WriteString("--XBOUND\r\nContent-Disposition: form-data; name=\"path\"\r\n\r\n/media\r\n")
	buf.WriteString("--XBOUND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"song.mp3\"\r\nContent-Type: audio/mpeg\r\n\r\n")
	buf.Write(bytes.Repeat([]byte("A"), 2048))
	buf.WriteString("\r\n--XBOUND--\r\n")
	req, _ = http.NewRequest("POST", ts.URL+"/web/api/upload", &buf)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=XBOUND")
	basicAuth(req)
	res, _ = c.Do(req)
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload = %d %s", res.StatusCode, b)
	}
	res.Body.Close()

	// verify listed under /media
	req, _ = http.NewRequest("GET", ts.URL+"/web/api/list?path=/media", nil)
	basicAuth(req)
	res, _ = c.Do(req)
	var lst struct {
		Files []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	_ = json.NewDecoder(res.Body).Decode(&lst)
	res.Body.Close()
	if len(lst.Files) != 1 || lst.Files[0].Name != "song.mp3" || lst.Files[0].Size != 2048 {
		t.Fatalf("files = %+v", lst.Files)
	}

	// web ui index
	req, _ = http.NewRequest("GET", ts.URL+"/web/", nil)
	basicAuth(req)
	res, _ = c.Do(req)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "TG WebDAV") {
		t.Fatal("index.html content missing")
	}
}
