package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
)

// fakeTG is an in-memory telegram used by the filesystem tests.
type fakeTG struct {
	mu     sync.Mutex
	blocks map[int64][][]byte // msgID -> blocks
	nextID int64
}

func newFakeTG() *fakeTG { return &fakeTG{blocks: map[int64][][]byte{}, nextID: 100} }

func (f *fakeTG) add(msgID int64, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for off := 0; off < len(data) || (off == 0 && len(data) == 0); off += BlockSize {
		end := off + BlockSize
		if end > len(data) {
			end = len(data)
		}
		blk := make([]byte, end-off)
		copy(blk, data[off:end])
		f.blocks[msgID] = append(f.blocks[msgID], blk)
		if len(data) == 0 {
			break
		}
	}
}

func (f *fakeTG) ReadBlock(_ context.Context, msgID int64, block int64, buf []byte) (int, error) {
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
	f.blocks[id] = nil
	f.mu.Unlock()
	f.add(id, data)
	return id, nil
}

func (f *fakeTG) Delete(_ context.Context, msgID int64) error {
	f.mu.Lock()
	delete(f.blocks, msgID)
	f.mu.Unlock()
	return nil
}

func (f *fakeTG) Copy(_ context.Context, msgID int64) (int64, error) {
	f.mu.Lock()
	data := f.blocks[msgID]
	id := f.nextID
	f.nextID++
	f.blocks[id] = data
	f.mu.Unlock()
	return id, nil
}

func openTestFS(t *testing.T) (*FS, *fakeTG) {
	t.Helper()
	st, err := Open(t.TempDir() + "/meta.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tg := newFakeTG()
	vfs := NewFS(st, tg, NewCache(8<<20), t.TempDir(), 2, true)
	ctx := context.Background()
	if err := vfs.Mkdir(ctx, "/general", 0o755); err != nil {
		t.Fatal(err)
	}
	return vfs, tg
}

func TestFSReadSeek(t *testing.T) {
	vfs, tg := openTestFS(t)
	ctx := context.Background()

	// 2.5 blocks of data
	data := bytes.Repeat([]byte("0123456789ABCDEF"), (BlockSize*5)/16/2+100)[:BlockSize*2+524288]
	for i := range data {
		data[i] = byte(i % 251)
	}
	tg.add(42, data)

	e := Entry{Name: "big.bin", Path: "/general/big.bin", Folder: "/general",
		Size: int64(len(data)), MsgID: 42, Kind: "file"}
	if _, err := vfs.St.PutFileUnique(ctx, "/general", "big.bin", 42, e.Size, "application/octet-stream", "file", ""); err != nil {
		t.Fatal(err)
	}
	_ = e

	f, err := vfs.OpenFile(ctx, "/general/big.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// sequential read
	got := make([]byte, len(data))
	if _, err := io.ReadFull(f, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("sequential read mismatch")
	}

	// seek + read (simulates HTTP range)
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		t.Fatal("file is not a seeker")
	}
	if _, err := rs.Seek(1234567, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1000)
	if _, err := io.ReadFull(f, chunk); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(chunk, data[1234567:1235567]) {
		t.Fatal("seek read mismatch")
	}
}

func TestFSPutAndDelete(t *testing.T) {
	vfs, tg := openTestFS(t)
	ctx := context.Background()

	w, err := vfs.OpenFile(ctx, "/general/upload.bin", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("Z"), BlockSize+777)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := vfs.Stat(ctx, "/general/upload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != int64(len(payload)) {
		t.Fatalf("size = %d", st.Size())
	}

	// read back through fs
	rf, err := vfs.OpenFile(ctx, "/general/upload.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("roundtrip mismatch")
	}

	// delete removes the telegram message
	if err := vfs.RemoveAll(ctx, "/general/upload.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := vfs.Stat(ctx, "/general/upload.bin"); err == nil {
		t.Fatal("file still exists")
	}
	tg.mu.Lock()
	_, stillThere := tg.blocks[101] // first uploaded id
	tg.mu.Unlock()
	if stillThere {
		t.Fatal("telegram message not deleted")
	}
}

func TestFSFolders(t *testing.T) {
	vfs, _ := openTestFS(t)
	ctx := context.Background()

	if err := vfs.Mkdir(ctx, "/nope/child", 0o755); err == nil {
		t.Fatal("mkdir with missing parent should fail")
	}
	if err := vfs.Mkdir(ctx, "/general/movies", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := vfs.Mkdir(ctx, "/general/movies", 0o755); err == nil {
		t.Fatal("duplicate mkdir should fail")
	}
	dir, err := vfs.OpenFile(ctx, "/general", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := dir.(interface {
		Readdir(int) ([]os.FileInfo, error)
	})
	if !ok {
		t.Fatal("dir does not implement Readdir")
	}
	infos, err := d.Readdir(-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name() != "movies" {
		t.Fatalf("readdir = %+v", infos)
	}
}
