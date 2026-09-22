package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds every runtime option. Everything is configurable via
// environment variables or a .env file placed next to the binary.
type Config struct {
	Port        int    // PORT
	DataDir     string // DATA_DIR
	DBPath      string // derived: DATA_DIR/metadata.db
	SessionPath string // derived: DATA_DIR/session.json
	TmpDir      string // derived: DATA_DIR/tmp

	BotToken  string // BOT_TOKEN
	APIID     int    // API_ID
	APIHash   string // API_HASH
	ChannelID int64  // CHANNEL_ID  (bot api style: -100xxxxxxxxxx)

	DefaultFolder   string  // DEFAULT_FOLDER (default "general")
	AllowedUserIDs  []int64 // ALLOWED_USER_IDS (csv, empty = allow everyone)
	WebUser         string  // WEB_USER
	WebPass         string  // WEB_PASS
	WebUsers        map[string]string // WEB_USERS extra "user:pass" pairs (csv)
	CacheMB         int     // CACHE_MB (chunk cache size, megabytes)
	Prefetch        int     // PREFETCH (blocks read ahead per stream)
	DeleteFromTelegram bool // DELETE_FROM_TELEGRAM (also delete channel post)
	LogLevel        string  // LOG_LEVEL (debug|info|warn|error)
}

// Load reads an optional .env file (does not override real env vars),
// then builds the configuration from the environment.
func Load() (*Config, error) {
	_ = loadDotEnv(envFile(".env"))

	c := &Config{
		Port:               getInt("PORT", 8080),
		DataDir:            getStr("DATA_DIR", "./data"),
		BotToken:           strings.TrimSpace(getStr("BOT_TOKEN", "")),
		APIID:              getInt("API_ID", 0),
		APIHash:            strings.TrimSpace(getStr("API_HASH", "")),
		ChannelID:          getInt64("CHANNEL_ID", 0),
		DefaultFolder:      sanitizeFolder(getStr("DEFAULT_FOLDER", "general")),
		WebUser:            getStr("WEB_USER", ""),
		WebPass:            getStr("WEB_PASS", ""),
		CacheMB:            getInt("CACHE_MB", 64),
		Prefetch:           getInt("PREFETCH", 8),
		DeleteFromTelegram: getBool("DELETE_FROM_TELEGRAM", true),
		LogLevel:           strings.ToLower(getStr("LOG_LEVEL", "info")),
	}

	c.AllowedUserIDs = parseInt64List(getStr("ALLOWED_USER_IDS", ""))
	c.WebUsers = parseUsers(getStr("WEB_USERS", ""))
	c.DBPath = filepath.Join(c.DataDir, "metadata.db")
	c.SessionPath = filepath.Join(c.DataDir, "session.json")
	c.TmpDir = filepath.Join(c.DataDir, "tmp")

	if c.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required (get one from @BotFather)")
	}
	if c.APIID == 0 || c.APIHash == "" {
		return nil, fmt.Errorf("API_ID and API_HASH are required (get them from https://my.telegram.org -> API development tools)")
	}
	if c.ChannelID == 0 {
		return nil, fmt.Errorf("CHANNEL_ID is required (e.g. -1001234567890, see README)")
	}
	if c.WebUser == "" || c.WebPass == "" {
		return nil, fmt.Errorf("WEB_USER and WEB_PASS are required (login for the WebDAV/Web UI)")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return nil, fmt.Errorf("PORT must be 1-65535, got %d", c.Port)
	}
	if c.CacheMB < 1 {
		c.CacheMB = 1
	}
	if c.Prefetch < 0 {
		c.Prefetch = 0
	}
	return c, nil
}

// envFile returns the path given, or $ENV_FILE if set (fallback ".env").
func envFile(p string) string {
	if v := os.Getenv("ENV_FILE"); v != "" {
		return v
	}
	return p
}

// loadDotEnv parses KEY=VALUE lines and sets them into the process env.
// Real environment variables always win.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// strip inline comments (only when unquoted and preceded by a space)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return nil
}

func getStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func getInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func getInt64(key string, def int64) int64 {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func getBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func parseInt64List(s string) []int64 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []int64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func parseUsers(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if i := strings.Index(pair, ":"); i > 0 && i < len(pair)-1 {
			out[pair[:i]] = pair[i+1:]
		}
	}
	return out
}

// sanitizeFolder keeps only safe folder characters.
func sanitizeFolder(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == ' ', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "general"
	}
	return out
}
