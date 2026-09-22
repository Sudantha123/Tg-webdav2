package davname

import (
	"strings"
	"testing"
	"time"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"my file.mp4":            "my file.mp4",
		"bad/name\\here":         "bad_name_here",
		"  spaced  out  ":        "spaced  out",
		"ctrl\x00char":           "ctrlchar",
		"colon:star*q?":          "colon_star_q_",
		".hidden":                "hidden",
		"trail...":               "trail",
		"emoji 🎬 video":          "emoji 🎬 video",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitCaptionPath(t *testing.T) {
	f, n, ok := SplitCaptionPath("movies/action My Film.mp4")
	if !ok || f != "movies/action" || n != "My Film.mp4" {
		t.Fatalf("got %q %q %v", f, n, ok)
	}
	if _, _, ok := SplitCaptionPath("nofile/"); ok {
		t.Error("trailing slash should not split")
	}
	if _, _, ok := SplitCaptionPath("plain name.mp4"); ok {
		t.Error("plain name should not split")
	}
}

func TestExtFor(t *testing.T) {
	if e := ExtFor("clip.mp4", "video/mp4"); e != ".mp4" {
		t.Errorf("attr ext: %q", e)
	}
	if e := ExtFor("", "video/x-matroska"); e != ".mkv" {
		t.Errorf("mime ext: %q", e)
	}
	if e := ExtFor("", "weird/mime"); e != "" {
		t.Errorf("unknown: %q", e)
	}
}

func TestWithExt(t *testing.T) {
	if got := WithExt("My Video", "", "video/mp4"); got != "My Video.mp4" {
		t.Errorf("got %q", got)
	}
	if got := WithExt("Song", "track.mp3", "audio/mpeg"); got != "Song.mp3" {
		t.Errorf("got %q", got)
	}
	if got := WithExt("Photo.png", "", "image/jpeg"); got != "Photo.png" {
		t.Errorf("got %q", got)
	}
}

func TestKindFor(t *testing.T) {
	if KindFor("video/mp4", "a.mp4") != "video" {
		t.Error("video")
	}
	if KindFor("image/jpeg", "a.jpg") != "image" {
		t.Error("image")
	}
	if KindFor("audio/mpeg", "a.mp3") != "audio" {
		t.Error("audio")
	}
	if KindFor("application/octet-stream", "game.apk") != "archive" {
		t.Error("archive")
	}
	if KindFor("application/octet-stream", "notes.txt") != "file" {
		t.Error("file")
	}
}

func TestRandom(t *testing.T) {
	now := time.Date(2026, 9, 22, 14, 5, 30, 0, time.UTC)
	got := Random("video", ".mp4", now)
	if !strings.HasPrefix(got, "video_20260922_140530_") || !strings.HasSuffix(got, ".mp4") {
		t.Errorf("got %q", got)
	}
	if Random("file", ".bin", now) == Random("file", ".bin", now) {
		t.Error("random names must differ")
	}
}

func TestBuild(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	// caption path
	folder, name := Build("music/My Song.mp3", "", "audio/mpeg", now)
	if folder != "music" || name != "My Song.mp3" {
		t.Errorf("caption path: %q %q", folder, name)
	}

	// caption with missing ext
	folder, name = Build("notes todo", "file.txt", "text/plain", now)
	if folder != "" || name != "notes todo.txt" {
		t.Errorf("caption ext: %q %q", folder, name)
	}

	// no caption but telegram filename
	_, name = Build("", "IMG_2024.jpg", "image/jpeg", now)
	if name != "IMG_2024.jpg" {
		t.Errorf("attr name: %q", name)
	}

	// nothing at all
	folder, name = Build("", "", "video/mp4", now)
	if folder != "" || !strings.HasPrefix(name, "video_20260922_100000_") || !strings.HasSuffix(name, ".mp4") {
		t.Errorf("random: %q %q", folder, name)
	}
}

func TestDetectMime(t *testing.T) {
	if DetectMime("x.mp4") != "video/mp4" {
		t.Error("mp4")
	}
	if DetectMime("x.unknownext") != "application/octet-stream" {
		t.Error("unknown")
	}
}
