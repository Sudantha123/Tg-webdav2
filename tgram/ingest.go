package tgram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Sudantha123/Tg-webdav2/internal/davname"
	"github.com/Sudantha123/Tg-webdav2/store"
)

// IngesterConfig carries what the ingester needs.
type IngesterConfig struct {
	ChannelID      int64
	AllowedUserIDs []int64 // empty = allow everyone
	Log            *slog.Logger
}

// Ingester receives files sent to the bot inbox, forwards them to the
// channel and indexes them in the WebDAV tree.
type Ingester struct {
	Bot *BotAPI
	St  *store.Store
	Cfg IngesterConfig
}

// Run blocks, long-polling updates until ctx is cancelled.
func (in *Ingester) Run(ctx context.Context) {
	log := in.Cfg.Log
	offset := int64(0)
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := in.Bot.GetUpdates(ctx, offset+1, 50)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var fe *FloodError
			if errors.As(err, &fe) {
				select {
				case <-time.After(fe.RetryAfter):
				case <-ctx.Done():
					return
				}
				continue
			}
			failures++
			log.Warn("bot getUpdates failed", "err", err, "failures", failures)
			wait := time.Duration(failures) * 3 * time.Second
			if wait > time.Minute {
				wait = time.Minute
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			continue
		}
		failures = 0
		for _, upd := range updates {
			if upd.UpdateID > offset {
				offset = upd.UpdateID
			}
			if upd.Message != nil {
				in.handle(ctx, upd.Message)
			}
		}
	}
}

func (in *Ingester) allowed(m *Message) bool {
	if len(in.Cfg.AllowedUserIDs) == 0 || m.From == nil {
		return true
	}
	for _, id := range in.Cfg.AllowedUserIDs {
		if id == m.From.ID {
			return true
		}
	}
	return false
}

func (in *Ingester) handle(ctx context.Context, m *Message) {
	log := in.Cfg.Log
	if m.Chat.Type != "private" {
		return
	}
	if !in.allowed(m) {
		log.Warn("rejected message from unallowed user", "user_id", m.FromID())
		return
	}

	// help / ping
	if m.Text != "" && m.Document == nil && m.Video == nil && m.Audio == nil &&
		m.Voice == nil && m.Sticker == nil && len(m.Photo) == 0 {
		_ = in.Bot.SendMessage(ctx, m.Chat.ID,
			"Send me any *file* (video / photo / audio / document) and it will be "+
				"saved to the WebDAV storage.\n\n"+
				"• Caption = file name\n"+
				"• Caption like `movies/My Movie.mp4` = custom folder\n"+
				"• No caption = automatic name\n\n"+
				"Browse: /web\nWebDAV: /dav")
		return
	}

	mime, attrName, size, ok := m.ExtractMedia()
	if !ok {
		return
	}

	folder, name := davname.Build(m.Caption, attrName, mime, time.Now())
	if folder == "" {
		def, err := in.St.DefaultFolder(ctx, "")
		if err != nil || def == "" {
			def = "/general"
		}
		folder = strings.TrimPrefix(def, "/")
	}

	// forward into the channel (falls back to copy)
	var newMsg *Message
	var err error
	if newMsg, err = in.Bot.ForwardMessage(ctx, m.Chat.ID, in.Cfg.ChannelID, m.MessageID); err != nil {
		var fe *FloodError
		if errors.As(err, &fe) {
			select {
			case <-time.After(fe.RetryAfter):
			case <-ctx.Done():
				return
			}
		}
		if newMsg, err = in.Bot.CopyMessage(ctx, m.Chat.ID, in.Cfg.ChannelID, m.MessageID); err != nil {
			log.Error("forward to channel failed", "err", err)
			_ = in.Bot.SendMessage(ctx, m.Chat.ID, "❌ Failed to save: "+err.Error())
			return
		}
	}

	// the forwarded message content tells us the real size/mime
	if fMime, _, fSize, fok := newMsg.ExtractMedia(); fok {
		if fSize > 0 {
			size = fSize
		}
		if fMime != "" {
			mime = fMime
		}
	}

	folderPath := "/" + folder
	if err := in.St.EnsureFolder(ctx, folderPath); err != nil {
		log.Error("ensure folder failed", "err", err)
	}
	entry, err := in.St.PutFileUnique(ctx, folderPath, name, int64(newMsg.MessageID), size, mime, davname.KindFor(mime, name), m.Caption)
	if err != nil {
		log.Error("index file failed", "err", err)
		_ = in.Bot.SendMessage(ctx, m.Chat.ID, "❌ Failed to index: "+err.Error())
		return
	}

	log.Info("ingested", "from", m.FromID(), "path", entry.Path, "size", entry.Size)
	_ = in.Bot.SendMessage(ctx, m.Chat.ID,
		fmt.Sprintf("✅ Saved: %s (%s)", entry.Path, humanBytes(entry.Size)))
}

func (m *Message) FromID() int64 {
	if m.From != nil {
		return m.From.ID
	}
	return 0
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for num := n / unit; num >= unit; num /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
