package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/Sudantha123/Tg-webdav2/internal/davname"
	"golang.org/x/net/webdav"
)

// BlockSize is the telegram chunk granularity used for cache/prefetch.
const BlockSize = 1 << 20 // 1 MiB

// Telegram is the storage backend contract used by the WebDAV filesystem.
// It is implemented by the tgram package (and faked in tests).
type Telegram interface {
	// ReadBlock reads one aligned 1MiB block of a channel message file
	// into buf (len(buf) >= BlockSize). Returns 0 at EOF.
	ReadBlock(ctx context.Context, msgID int64, block int64, buf []byte) (int, error)
	// Upload streams r (size bytes) into the channel as a document
	// named name and returns the new channel message id.
	Upload(ctx context.Context, name, mime string, r io.Reader, size int64) (int64, error)
	// Delete removes a channel message (best effort).
	Delete(ctx context.Context, msgID int64) error
	// Copy duplicates a channel message and returns the new message id.
	Copy(ctx context.Context, msgID int64) (int64, error)
}

// FS implements webdav.FileSystem backed by the sqlite index + telegram.
type FS struct {
	St        *Store
	TG        Telegram
	Cache     *Cache
	TmpDir    string
	Prefetch  int  // blocks to read ahead per stream
	DelRemote bool // also delete telegram posts on file delete

	inflight sync.Map // blockKey -> struct{} (dedupe prefetch)
	pool     sync.Pool
}

// NewFS builds the virtual filesystem.
func NewFS(st *Store, tg Telegram, cache *Cache, tmpDir string, prefetch int, delRemote bool) *FS {
	return &FS{
		St: st, TG: tg, Cache: cache, TmpDir: tmpDir,
		Prefetch:  prefetch,
		DelRemote: delRemote,
		pool:      sync.Pool{New: func() any { b := make([]byte, BlockSize); return &b }},
	}
}

func clean(p string) (string, error) {
	n := Normalize(p)
	if n == "" {
		return "", ErrNotExist
	}
	return n, nil
}

func blockKey(msgID int64, block int64) string {
	return fmt.Sprintf("%d:%d", msgID, block)
}

// ---------- os.FileInfo ----------

type fileInfo struct {
	e Entry
}

func (f fileInfo) Name() string       { return f.e.Name }
func (f fileInfo) Size() int64        { return f.e.Size }
func (f fileInfo) Mode() fs.FileMode {
	if f.e.IsDir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (f fileInfo) ModTime() time.Time { return f.e.Modified }
func (f fileInfo) IsDir() bool        { return f.e.IsDir }
func (f fileInfo) Sys() any           { return nil }

// ---------- webdav.FileSystem ----------

// Mkdir creates a directory.
func (v *FS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	p, err := clean(name)
	if err != nil || p == "/" {
		return os.ErrInvalid
	}
	parentExists, err := v.St.FolderExists(ctx, Parent(p))
	if err != nil {
		return err
	}
	if !parentExists {
		return os.ErrNotExist
	}
	exists, err := v.St.FolderExists(ctx, p)
	if err != nil {
		return err
	}
	if exists {
		return os.ErrExist
	}
	return v.St.EnsureFolder(ctx, p)
}

// OpenFile opens a file for reading or writing.
func (v *FS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	p, err := clean(name)
	if err != nil {
		return nil, err
	}
	st, err := v.St.Stat(ctx, p)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		if st.IsDir {
			return nil, os.ErrInvalid
		}
		return &writeFile{fs: v, entry: *st, ctxv: ctx}, nil
	}
	if st.IsDir {
		children, err := v.St.List(ctx, p)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(children))
		for _, c := range children {
			infos = append(infos, fileInfo{e: c})
		}
		return &dirFile{name: Base(p), children: infos}, nil
	}
	return &readFile{fs: v, entry: *st, ctxv: ctx, block: -1}, nil
}

// RemoveAll deletes a file or folder recursively.
func (v *FS) RemoveAll(ctx context.Context, name string) error {
	p, err := clean(name)
	if err != nil || p == "/" {
		return os.ErrInvalid
	}
	msgIDs, err := v.St.Delete(ctx, p)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return os.ErrNotExist
		}
		return err
	}
	v.Cache.DropPrefix(p)
	if v.DelRemote && len(msgIDs) > 0 {
		ids := msgIDs
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			for _, id := range ids {
				_ = v.TG.Delete(ctx, id)
			}
		}()
	}
	return nil
}

// Rename moves/renames a file or folder.
func (v *FS) Rename(ctx context.Context, oldName, newName string) error {
	o, err := clean(oldName)
	if err != nil {
		return err
	}
	n, err := clean(newName)
	if err != nil {
		return err
	}
	if n == "/" {
		return os.ErrExist
	}
	if err := v.St.Rename(ctx, o, n); err != nil {
		switch {
		case errors.Is(err, os.ErrExist):
			return os.ErrExist
		case errors.Is(err, ErrNotExist):
			return os.ErrNotExist
		}
		return err
	}
	v.Cache.DropPrefix(o)
	return nil
}

// Stat returns file info for a path.
func (v *FS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	p, err := clean(name)
	if err != nil {
		return nil, err
	}
	e, err := v.St.Stat(ctx, p)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return fileInfo{e: *e}, nil
}

// ---------- readFile (http.File over telegram blocks) ----------

type readFile struct {
	fs    *FS
	entry Entry
	ctxv  context.Context
	pos   int64
	buf   []byte // current block
	block int64  // block index of buf, -1 = none
	eof   bool
}

func (r *readFile) prefetch(from int64) {
	n := r.fs.Prefetch
	if n <= 0 || r.entry.MsgID == 0 {
		return
	}
	last := (r.entry.Size + BlockSize - 1) / BlockSize
	for i := from; i < from+int64(n) && i < last; i++ {
		key := blockKey(r.entry.MsgID, i)
		if _, ok := r.fs.Cache.Get(key); ok {
			continue
		}
		if _, loaded := r.fs.inflight.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		go func(msgID, block int64, key string) {
			defer r.fs.inflight.Delete(key)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			bp := r.fs.pool.Get().(*[]byte)
			nread, err := r.fs.TG.ReadBlock(ctx, msgID, block, *bp)
			if err == nil && nread > 0 {
				data := make([]byte, nread)
				copy(data, (*bp)[:nread])
				r.fs.Cache.Add(key, data)
			}
			r.fs.pool.Put(bp)
		}(r.entry.MsgID, i, key)
	}
}

// blockAt returns the block containing idx, using the cache.
func (r *readFile) blockAt(ctx context.Context, idx int64) ([]byte, error) {
	if r.block == idx && r.buf != nil {
		return r.buf, nil
	}
	key := blockKey(r.entry.MsgID, idx)
	if data, ok := r.fs.Cache.Get(key); ok {
		r.buf, r.block = data, idx
		r.prefetch(idx + 1)
		return data, nil
	}
	bp := r.fs.pool.Get().(*[]byte)
	nread, err := r.fs.TG.ReadBlock(ctx, r.entry.MsgID, idx, *bp)
	if err != nil {
		r.fs.pool.Put(bp)
		return nil, err
	}
	if nread == 0 {
		r.fs.pool.Put(bp)
		r.buf, r.block = nil, -1
		r.eof = true
		return nil, io.EOF
	}
	data := make([]byte, nread)
	copy(data, (*bp)[:nread])
	r.fs.pool.Put(bp)
	r.fs.Cache.Add(key, data)
	r.buf, r.block = data, idx
	r.prefetch(idx + 1)
	return data, nil
}

func (r *readFile) Read(p []byte) (int, error) {
	if r.entry.MsgID == 0 || r.eof {
		return 0, io.EOF
	}
	if r.pos >= r.entry.Size {
		return 0, io.EOF
	}
	want := int64(len(p))
	if want > r.entry.Size-r.pos {
		want = r.entry.Size - r.pos
	}
	total := 0
	for total < int(want) {
		idx := (r.pos + int64(total)) / BlockSize
		data, err := r.blockAt(r.ctxv, idx)
		if err != nil {
			if errors.Is(err, io.EOF) && total > 0 {
				return total, nil
			}
			return total, err
		}
		off := (r.pos + int64(total)) % BlockSize
		if off >= int64(len(data)) {
			// block shorter than expected (size mismatch) -> EOF
			if total > 0 {
				return total, nil
			}
			return 0, io.EOF
		}
		n := copy(p[total:], data[off:])
		if n == 0 {
			if total > 0 {
				break
			}
			return 0, io.EOF
		}
		total += n
	}
	r.pos += int64(total)
	if r.pos >= r.entry.Size {
		return total, io.EOF
	}
	return total, nil
}

func (r *readFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.pos
	case io.SeekEnd:
		offset += r.entry.Size
	default:
		return 0, os.ErrInvalid
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if offset != r.pos {
		r.pos = offset
		if offset >= r.entry.Size {
			r.eof = true
		} else {
			r.eof = false
			r.buf, r.block = nil, -1
		}
	}
	return r.pos, nil
}

func (r *readFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
func (r *readFile) Stat() (os.FileInfo, error)         { return fileInfo{e: r.entry}, nil }
func (r *readFile) Close() error                       { return nil }
func (r *readFile) Write([]byte) (int, error)          { return 0, os.ErrInvalid }

// ---------- dirFile ----------

type dirFile struct {
	name     string
	children []os.FileInfo
	offset   int
}

func (d *dirFile) Read([]byte) (int, error)             { return 0, os.ErrInvalid }
func (d *dirFile) Write([]byte) (int, error)            { return 0, os.ErrInvalid }
func (d *dirFile) Close() error                         { return nil }
func (d *dirFile) Seek(int64, int) (int64, error)       { return 0, os.ErrInvalid }
func (d *dirFile) Stat() (os.FileInfo, error) {
	return fileInfo{e: Entry{Name: d.name, IsDir: true}}, nil
}
func (d *dirFile) Readdir(count int) ([]os.FileInfo, error) {
	n := len(d.children) - d.offset
	if n <= 0 {
		if count <= 0 {
			return nil, nil
		}
		return nil, io.EOF
	}
	if count > 0 && n > count {
		n = count
	}
	out := d.children[d.offset : d.offset+n]
	d.offset += n
	return out, nil
}

// ---------- writeFile (PUT -> temp file -> telegram upload) ----------

type writeFile struct {
	fs    *FS
	entry Entry
	ctxv  context.Context
	tmp   *os.File
	n     int64
	done  bool
}

func (r *writeFile) ensureTemp() error {
	if r.tmp != nil {
		return nil
	}
	if err := os.MkdirAll(r.fs.TmpDir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(r.fs.TmpDir, "upload-*")
	if err != nil {
		return err
	}
	r.tmp = f
	return nil
}

func (r *writeFile) Write(p []byte) (int, error) {
	if err := r.ensureTemp(); err != nil {
		return 0, err
	}
	n, err := r.tmp.Write(p)
	r.n += int64(n)
	return n, err
}

func (r *writeFile) Read([]byte) (int, error)            { return 0, os.ErrInvalid }
func (r *writeFile) Readdir(int) ([]os.FileInfo, error)  { return nil, os.ErrInvalid }
func (r *writeFile) Seek(int64, int) (int64, error)      { return 0, os.ErrInvalid }
func (r *writeFile) Stat() (os.FileInfo, error)          { return fileInfo{e: r.entry}, nil }

func (r *writeFile) Close() error {
	if r.done {
		return nil
	}
	r.done = true
	if r.tmp == nil {
		// zero-byte PUT: metadata-only entry
		_, _, err := r.fs.St.PutFileOverwrite(context.Background(),
			r.entry.Folder, r.entry.Name, 0, 0, "application/x-empty", "file", "")
		return err
	}
	size := r.n
	name := r.entry.Name
	tmpPath := r.tmp.Name()
	_ = r.tmp.Close()
	r.tmp = nil
	defer func() { _ = os.Remove(tmpPath) }()

	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	mime := davname.DetectMime(name)
	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	msgID, err := r.fs.TG.Upload(ctx, name, mime, f, size)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("telegram upload: %w", err)
	}
	_, old, err := r.fs.St.PutFileOverwrite(ctx, r.entry.Folder, name, msgID, size, mime, davname.KindFor(mime, name), "")
	if err != nil {
		return err
	}
	if old != 0 && r.fs.DelRemote {
		go func(id int64) {
			c, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_ = r.fs.TG.Delete(c, id)
		}(old)
	}
	return nil
}
