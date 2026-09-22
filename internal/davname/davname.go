// Package davname turns Telegram captions/attributes into WebDAV file names.
package davname

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// mime -> extension table (the ones people actually send through Telegram).
var mimeExts = map[string]string{
	"image/jpeg":               ".jpg",
	"image/png":                ".png",
	"image/gif":                ".gif",
	"image/webp":               ".webp",
	"image/heic":               ".heic",
	"image/tiff":               ".tiff",
	"image/bmp":                ".bmp",
	"image/svg+xml":            ".svg",
	"video/mp4":                ".mp4",
	"video/x-matroska":         ".mkv",
	"video/webm":               ".webm",
	"video/quicktime":          ".mov",
	"video/x-msvideo":          ".avi",
	"video/mpeg":               ".mpeg",
	"video/3gpp":               ".3gp",
	"video/x-flv":              ".flv",
	"video/x-m4v":              ".m4v",
	"video/mp2t":               ".ts",
	"audio/mpeg":               ".mp3",
	"audio/ogg":                ".ogg",
	"audio/opus":               ".opus",
	"audio/flac":               ".flac",
	"audio/x-m4a":              ".m4a",
	"audio/mp4":                ".m4a",
	"audio/aac":                ".aac",
	"audio/wav":                ".wav",
	"audio/x-wav":              ".wav",
	"audio/amr":                ".amr",
	"audio/x-ms-wma":           ".wma",
	"application/pdf":          ".pdf",
	"application/zip":          ".zip",
	"application/x-rar-compressed": ".rar",
	"application/x-7z-compressed":  ".7z",
	"application/gzip":         ".gz",
	"application/x-tar":        ".tar",
	"application/vnd.android.package-archive": ".apk",
	"application/x-abiword":    ".abw",
	"application/msword":       ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":    ".docx",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":          ".xlsx",
	"application/vnd.ms-powerpoint": ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation":  ".pptx",
	"application/epub+zip":     ".epub",
	"application/json":         ".json",
	"application/xml":          ".xml",
	"text/plain":               ".txt",
	"text/html":                ".html",
	"text/markdown":            ".md",
	"text/csv":                 ".csv",
	"application/octet-stream": ".bin",
}

// extension -> mime, built from the same table plus a few extras.
var extMimes = func() map[string]string {
	m := map[string]string{
		".iso": "application/x-iso9660-image",
		".exe": "application/x-msdownload",
		".deb": "application/vnd.debian.binary-package",
		".rpm": "application/x-rpm",
		".ttf": "font/ttf",
		".otf": "font/otf",
		".srt": "application/x-subrip",
		".ass": "text/x-ssa",
		".nfo": "text/x-nfo",
		".log": "text/plain",
	}
	for mime, ext := range mimeExts {
		if _, ok := m[ext]; !ok {
			m[ext] = mime
		}
	}
	return m
}()

// Sanitize makes a string safe to use as a file/folder name.
func Sanitize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('_')
			space = false
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !space {
				b.WriteByte(' ')
			}
			space = true
		default:
			b.WriteRune(r)
			space = false
		}
	}
	out := strings.Trim(b.String(), " .")
	// keep it reasonable
	r := []rune(out)
	if len(r) > 150 {
		out = string(r[:150])
	}
	return strings.TrimSpace(out)
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// SplitCaptionPath splits "folder/sub name.ext" caption syntax.
// Returns the folder path ("sub" style, no leading slash), the file part
// and whether the split is valid.
func SplitCaptionPath(line string) (folder, name string, ok bool) {
	i := strings.LastIndex(line, "/")
	if i <= 0 || i == len(line)-1 {
		return "", "", false
	}
	rawFolder, rawName := line[:i], line[i+1:]
	var segs []string
	for _, seg := range strings.Split(rawFolder, "/") {
		seg = Sanitize(seg)
		if seg != "" && seg != "." && seg != ".." {
			segs = append(segs, seg)
		}
	}
	name = Sanitize(rawName)
	if len(segs) == 0 || name == "" || name == "." || name == ".." {
		return "", "", false
	}
	return strings.Join(segs, "/"), name, true
}

// ExtFor picks the best extension for a file: prefer the telegram file
// name attribute, fall back to the mime type table.
func ExtFor(attrName, mime string) string {
	if ext := strings.ToLower(filepath.Ext(Sanitize(attrName))); ext != "" {
		return ext
	}
	if ext, ok := mimeExts[strings.ToLower(mime)]; ok {
		return ext
	}
	return ""
}

// WithExt appends a matching extension to name when it has none.
func WithExt(name, attrName, mime string) string {
	if strings.ToLower(filepath.Ext(name)) != "" {
		return name
	}
	if ext := ExtFor(attrName, mime); ext != "" {
		return name + ext
	}
	return name
}

// DetectMime guesses the mime type from a file name extension.
func DetectMime(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if m, ok := extMimes[ext]; ok {
		return m
	}
	return "application/octet-stream"
}

// KindFor classifies a file as image / video / audio / archive / file.
func KindFor(mime, name string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".wmv", ".flv", ".m4v", ".mpg", ".mpeg", ".3gp", ".ts":
		return "video"
	case ".mp3", ".flac", ".wav", ".ogg", ".opus", ".m4a", ".aac", ".wma", ".amr":
		return "audio"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".svg", ".tiff":
		return "image"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".iso", ".apk":
		return "archive"
	}
	return "file"
}

// Random builds a deterministic-ish friendly name for files without any
// caption or attribute name, e.g. "video_20260922_141530_a3f1.mp4".
func Random(kind, ext string, now time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	if kind == "" {
		kind = "file"
	}
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s_%s_%s%s", kind, now.Format("20060102_150405"), hex.EncodeToString(b), ext)
}

// Build is the main entry point used by the bot ingester.
//
// Rules:
//   - caption "folder/sub name.ext"  -> folder="folder/sub", name="name.ext"
//   - caption "My Video"             -> name="My Video.<ext>"
//   - no caption, telegram file name -> use it
//   - nothing at all                 -> kind_date_time_rand.<ext>
//
// folder may be "" meaning "use the default folder".
func Build(caption, attrName, mime string, now time.Time) (folder, name string) {
	ext := ExtFor(attrName, mime)

	if line := firstLine(caption); line != "" {
		if f, n, ok := SplitCaptionPath(line); ok {
			return f, WithExt(n, attrName, mime)
		}
		n := Sanitize(line)
		if n != "" {
			if ext != "" {
				n = WithExt(n, attrName, mime)
			}
			return "", n
		}
	}

	if n := Sanitize(attrName); n != "" {
		if ext != "" && strings.ToLower(filepath.Ext(n)) == "" {
			n += ext
		}
		return "", n
	}

	return "", Random(KindFor(mime, attrName), ext, now)
}
