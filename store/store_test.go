package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":                 "/",
		"/":                "/",
		"/a/b/":            "/a/b",
		"a/b":              "/a/b",
		"//a//b":           "/a/b",
		"/a/../b":          "/b",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFoldersAndFiles(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.EnsureFolder(ctx, "/general/sub"); err != nil {
		t.Fatal(err)
	}
	// parents created implicitly
	for _, p := range []string{"/", "/general", "/general/sub"} {
		ok, err := s.FolderExists(ctx, p)
		if err != nil || !ok {
			t.Fatalf("folder %s missing (%v)", p, err)
		}
	}

	e, err := s.PutFileUnique(ctx, "/general", "video.mp4", 11, 100, "video/mp4", "video", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != "/general/video.mp4" || e.MsgID != 11 {
		t.Fatalf("entry %+v", e)
	}

	// collision -> renamed
	e2, err := s.PutFileUnique(ctx, "/general", "video.mp4", 12, 200, "video/mp4", "video", "")
	if err != nil {
		t.Fatal(err)
	}
	if e2.Name != "video (2).mp4" {
		t.Fatalf("collision name = %q", e2.Name)
	}

	list, err := s.List(ctx, "/general")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 { // sub folder + 2 files
		t.Fatalf("list = %+v", list)
	}

	st, err := s.Stat(ctx, "/general/video.mp4")
	if err != nil || st.Size != 100 {
		t.Fatalf("stat %+v %v", st, err)
	}
}

func TestOverwrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _, _ = s.PutFileOverwrite(ctx, "/", "a.bin", 1, 10, "application/octet-stream", "file", "")
	e, old, err := s.PutFileOverwrite(ctx, "/", "a.bin", 2, 20, "application/octet-stream", "file", "")
	if err != nil {
		t.Fatal(err)
	}
	if old != 1 || e.MsgID != 2 || e.Size != 20 {
		t.Fatalf("overwrite %+v old=%d", e, old)
	}
}

func TestRenameCascade(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.EnsureFolder(ctx, "/a/b")
	_, err := s.PutFileUnique(ctx, "/a/b", "f.txt", 5, 1, "text/plain", "file", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, "/a", "/c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "/c/b/f.txt"); err != nil {
		t.Fatalf("cascaded stat: %v", err)
	}
	if _, err := s.Stat(ctx, "/a"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("old path should be gone: %v", err)
	}
}

func TestDeleteCascade(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.EnsureFolder(ctx, "/d/e")
	_, _ = s.PutFileUnique(ctx, "/d", "x.bin", 7, 1, "application/octet-stream", "file", "")
	_, _ = s.PutFileUnique(ctx, "/d/e", "y.bin", 8, 1, "application/octet-stream", "file", "")

	ids, err := s.Delete(ctx, "/d")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("deleted ids = %v", ids)
	}
	if _, err := s.Stat(ctx, "/d"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("folder should be gone: %v", err)
	}
	if _, err := s.Stat(ctx, "/d/e/y.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("file should be gone: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if v, _ := s.DefaultFolder(ctx, "general"); v != "/general" {
		t.Fatalf("fallback default = %q", v)
	}
	if err := s.SetDefaultFolder(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.DefaultFolder(ctx, "general"); v != "/movies" {
		t.Fatalf("default = %q", v)
	}
}

func TestStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.EnsureFolder(ctx, "/x")
	_, _ = s.PutFileUnique(ctx, "/x", "a.bin", 1, 100, "", "file", "")
	_, _ = s.PutFileUnique(ctx, "/x", "b.bin", 2, 250, "", "file", "")
	files, bytes, folders, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 350 || folders != 1 {
		t.Fatalf("stats %d %d %d", files, bytes, folders)
	}
}

func TestOSFile(t *testing.T) {
	// ensure temp db files are actually created on disk
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "m.db")); err != nil {
		t.Fatal(err)
	}
}
