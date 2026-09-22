package tgram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FloodError is a 429 with retry_after.
type FloodError struct{ RetryAfter time.Duration }

func (e *FloodError) Error() string { return "telegram flood wait " + e.RetryAfter.String() }

// BotAPI is a tiny dependency-free Telegram Bot API client (long polling).
type BotAPI struct {
	Token string
	hc    *http.Client
}

// NewBotAPI creates the client.
func NewBotAPI(token string) *BotAPI {
	return &BotAPI{
		Token: token,
		hc:    &http.Client{Timeout: 90 * time.Second},
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (b *BotAPI) call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.Token, method)
	var body io.Reader
	if params != nil {
		body = strings.NewReader(params.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var ar apiResponse
	if err := json.Unmarshal(data, &ar); err != nil {
		return nil, fmt.Errorf("%s: bad response: %w", method, err)
	}
	if !ar.OK {
		if ar.Parameters != nil && ar.Parameters.RetryAfter > 0 {
			return nil, &FloodError{RetryAfter: time.Duration(ar.Parameters.RetryAfter) * time.Second}
		}
		return nil, fmt.Errorf("%s failed (%d): %s", method, ar.ErrorCode, ar.Description)
	}
	return ar.Result, nil
}

// ---------- types ----------

// User is a telegram user.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// Chat is a telegram chat.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// PhotoSize is one photo resolution.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// Document is a generic telegram file.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// Video is a telegram video.
type Video struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	Duration int    `json:"duration"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// Audio is a telegram audio file.
type Audio struct {
	FileID    string `json:"file_id"`
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	FileSize  int64  `json:"file_size"`
	Duration  int    `json:"duration"`
	Title     string `json:"title"`
	Performer string `json:"performer"`
}

// Voice is a voice note.
type Voice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	Duration int    `json:"duration"`
}

// Message is the (trimmed) bot api message.
type Message struct {
	MessageID int       `json:"message_id"`
	From      *User     `json:"from"`
	Chat      Chat      `json:"chat"`
	Date      int64     `json:"date"`
	Text      string    `json:"text"`
	Caption   string    `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
	Document  *Document `json:"document"`
	Video     *Video    `json:"video"`
	Audio     *Audio    `json:"audio"`
	Voice     *Voice    `json:"voice"`
	Sticker   *Document `json:"sticker"`
}

// Update is a long-poll update.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// ---------- api calls ----------

// GetMe validates the token.
func (b *BotAPI) GetMe(ctx context.Context) (*User, error) {
	res, err := b.call(ctx, "getMe", nil)
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(res, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUpdates long-polls for new updates.
func (b *BotAPI) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	if timeoutSec <= 0 {
		timeoutSec = 50
	}
	v := url.Values{
		"timeout":      {strconv.Itoa(timeoutSec)},
		"offset":       {strconv.FormatInt(offset, 10)},
		"allowed_updates": {`["message"]`},
	}
	res, err := b.call(ctx, "getUpdates", v)
	if err != nil {
		return nil, err
	}
	var updates []Update
	if err := json.Unmarshal(res, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// ForwardMessage forwards a message and returns the new message.
func (b *BotAPI) ForwardMessage(ctx context.Context, fromChatID, chatID int64, messageID int) (*Message, error) {
	v := url.Values{
		"chat_id":      {strconv.FormatInt(chatID, 10)},
		"from_chat_id": {strconv.FormatInt(fromChatID, 10)},
		"message_id":   {strconv.Itoa(messageID)},
	}
	res, err := b.call(ctx, "forwardMessage", v)
	if err != nil {
		return nil, err
	}
	var m Message
	if err := json.Unmarshal(res, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// CopyMessage copies a message without the "forwarded from" header.
func (b *BotAPI) CopyMessage(ctx context.Context, fromChatID, chatID int64, messageID int) (*Message, error) {
	v := url.Values{
		"chat_id":      {strconv.FormatInt(chatID, 10)},
		"from_chat_id": {strconv.FormatInt(fromChatID, 10)},
		"message_id":   {strconv.Itoa(messageID)},
	}
	var res json.RawMessage
	var err error
	if res, err = b.call(ctx, "copyMessage", v); err != nil {
		return nil, err
	}
	// copyMessage returns {message_id: N}
	var r struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	return &Message{MessageID: r.MessageID, Chat: Chat{ID: chatID}}, nil
}

// SendMessage sends a plain text reply.
func (b *BotAPI) SendMessage(ctx context.Context, chatID int64, text string) error {
	v := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {text},
	}
	_, err := b.call(ctx, "sendMessage", v)
	return err
}

// ExtractMedia returns mime type, original file name, size and presence
// for whichever media a message carries.
func (m *Message) ExtractMedia() (mime, attrName string, size int64, ok bool) {
	switch {
	case m.Document != nil:
		return m.Document.MimeType, m.Document.FileName, m.Document.FileSize, true
	case m.Video != nil:
		return m.Video.MimeType, m.Video.FileName, m.Video.FileSize, true
	case m.Audio != nil:
		return m.Audio.MimeType, m.Audio.FileName, m.Audio.FileSize, true
	case m.Voice != nil:
		if m.Voice.MimeType == "" {
			return "audio/ogg", "", m.Voice.FileSize, true
		}
		return m.Voice.MimeType, "", m.Voice.FileSize, true
	case m.Sticker != nil:
		return m.Sticker.MimeType, m.Sticker.FileName, m.Sticker.FileSize, true
	case len(m.Photo) > 0:
		best := m.Photo[len(m.Photo)-1]
		for _, p := range m.Photo {
			if p.FileSize >= best.FileSize {
				best = p
			}
		}
		return "image/jpeg", "", best.FileSize, true
	}
	return "", "", 0, false
}
