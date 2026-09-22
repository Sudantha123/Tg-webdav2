package config

import (
	"os"
	"path/filepath"
	"testing"
)

func baseEnv(t *testing.T) map[string]string {
	return map[string]string{
		"BOT_TOKEN":   "123:test",
		"API_ID":      "111",
		"API_HASH":    "abc",
		"CHANNEL_ID":  "-1001234567890",
		"WEB_USER":    "admin",
		"WEB_PASS":    "secret",
		"DATA_DIR":    t.TempDir(),
	}
}

func TestLoadValid(t *testing.T) {
	for k, v := range baseEnv(t) {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 {
		t.Errorf("default port = %d", c.Port)
	}
	if c.DefaultFolder != "general" {
		t.Errorf("default folder = %q", c.DefaultFolder)
	}
	if c.DeleteFromTelegram != true {
		t.Error("default delete-from-telegram should be true")
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	envContent := `
# comment
BOT_TOKEN=987:fromfile
API_ID=42
API_HASH=hashhash
CHANNEL_ID=-1009999999999
WEB_USER="quoted"
WEB_PASS='single'
PORT=9999
`
	if err := os.WriteFile(".env", []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BotToken != "987:fromfile" || c.APIID != 42 || c.WebUser != "quoted" || c.WebPass != "single" || c.Port != 9999 {
		t.Errorf("dotenv values wrong: %+v", c)
	}
	_ = os.Remove(filepath.Join(dir, ".env"))
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("BOT_TOKEN", "")
	for k, v := range baseEnv(t) {
		t.Setenv(k, v)
	}
	t.Setenv("BOT_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for empty BOT_TOKEN")
	}
}

func TestAllowedUsers(t *testing.T) {
	for k, v := range baseEnv(t) {
		t.Setenv(k, v)
	}
	t.Setenv("ALLOWED_USER_IDS", " 11, 22 ,bad,33")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AllowedUserIDs) != 3 || c.AllowedUserIDs[0] != 11 || c.AllowedUserIDs[2] != 33 {
		t.Errorf("allowed = %v", c.AllowedUserIDs)
	}
}

func TestWebUsersExtra(t *testing.T) {
	for k, v := range baseEnv(t) {
		t.Setenv(k, v)
	}
	t.Setenv("WEB_USERS", "bob:p1, alice : p2")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebUsers["bob"] != "p1" || c.WebUsers["alice "] != " p2" {
		t.Errorf("webusers = %v", c.WebUsers)
	}
}
