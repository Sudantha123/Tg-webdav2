// Package tgram talks to Telegram: MTProto file streaming (gotd),
// the Bot API long-poll ingester and message forwarding.
package tgram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Config for the MTProto client.
type Config struct {
	APIID       int
	APIHash     string
	BotToken    string
	ChannelID   int64 // bot api style (-100...) or raw channel id
	SessionPath string
	AppVersion  string
	Log         *slog.Logger
}

// Client wraps a long-running gotd MTProto connection.
type Client struct {
	cfg    Config
	client *telegram.Client
	api    *tg.Client

	ready chan struct{}
	once  sync.Once

	mu      sync.Mutex
	handles map[int64]*fileHandle

	peerMu      sync.Mutex
	peerHash    int64
	peerKnown   bool
}

type fileHandle struct {
	loc  tg.InputFileLocationClass
	size int64
	exp  time.Time
}

// Connect starts the MTProto client, logs the bot in and waits until ready.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	c := &Client{
		cfg:     cfg,
		ready:   make(chan struct{}),
		handles: make(map[int64]*fileHandle),
	}
	c.client = telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		NoUpdates:      true, // ingestion runs over the bot api instead
		SessionStorage: &session.FileStorage{Path: cfg.SessionPath},
		Device: telegram.DeviceConfig{
			DeviceModel:   "TgWebDAV",
			SystemVersion: "linux",
			AppVersion:    cfg.AppVersion,
			LangCode:      "en",
		},
	})

	errCh := make(chan error, 1)
	go func() {
		err := c.client.Run(ctx, func(ctx context.Context) error {
			status, err := c.client.Auth().Status(ctx)
			if err != nil {
				return fmt.Errorf("auth status: %w", err)
			}
			if !status.Authorized {
				if _, err := c.client.Auth().Bot(ctx, cfg.BotToken); err != nil {
					return fmt.Errorf("bot login: %w", err)
				}
				cfg.Log.Info("telegram: bot authorized via MTProto")
			}
			c.api = c.client.API()
			c.once.Do(func() { close(c.ready) })
			<-ctx.Done()
			return nil
		})
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	select {
	case <-c.ready:
		return c, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(90 * time.Second):
		return nil, errors.New("telegram connect timeout (90s) — check API_ID/API_HASH/BOT_TOKEN and network")
	}
}

func (c *Client) API() *tg.Client {
	<-c.ready
	return c.api
}

// ---------- channel peer resolution ----------

// rawChannelID converts a bot api chat id (-100xxxxxxxxxx) to the raw
// telegram channel id.
func (c *Client) rawChannelID() int64 {
	id := c.cfg.ChannelID
	if id < 0 {
		id = -id
		if strings.HasPrefix(fmt.Sprint(id), "100") {
			id -= 1000000000000 // strip the -100 marker
		}
	}
	return id
}

// inputChannel resolves the target channel (with access hash) for MTProto.
func (c *Client) inputChannel(ctx context.Context) (tg.InputChannelClass, error) {
	c.peerMu.Lock()
	defer c.peerMu.Unlock()
	raw := c.rawChannelID()
	if raw <= 0 {
		return nil, fmt.Errorf("invalid CHANNEL_ID %d", c.cfg.ChannelID)
	}

	try := func(hash int64) (tg.InputChannelClass, *tg.Channel, error) {
		res, err := c.API().ChannelsGetChannels(ctx, []tg.InputChannelClass{
			&tg.InputChannel{ChannelID: raw, AccessHash: hash},
		})
		if err != nil {
			return nil, nil, err
		}
		box, ok := res.(*tg.MessagesChats)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected channels.getChannels response %T", res)
		}
		for _, ch := range box.Chats {
			if cc, ok := ch.(*tg.Channel); ok && cc.ID == raw {
				return &tg.InputChannel{ChannelID: raw, AccessHash: cc.AccessHash}, cc, nil
			}
		}
		return nil, nil, fmt.Errorf("channel %d not found", raw)
	}

	if c.peerKnown {
		if _, ch, err := try(c.peerHash); err == nil {
			c.peerHash = ch.AccessHash
			return &tg.InputChannel{ChannelID: raw, AccessHash: ch.AccessHash}, nil
		}
	}

	if peer, ch, err := try(0); err == nil {
		c.peerHash, c.peerKnown = ch.AccessHash, true
		return peer, nil
	}

	// fallback: scan dialogs for the channel access hash
	res, err := c.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		Limit:      100,
		OffsetPeer: &tg.InputPeerEmpty{},
	})
	if err != nil {
		return nil, fmt.Errorf("resolve channel %d: %w", raw, err)
	}
	var chats []tg.ChatClass
	switch d := res.(type) {
	case *tg.MessagesDialogs:
		chats = d.Chats
	case *tg.MessagesDialogsSlice:
		chats = d.Chats
	}
	for _, ch := range chats {
		if cc, ok := ch.(*tg.Channel); ok && cc.ID == raw {
			c.peerHash, c.peerKnown = cc.AccessHash, true
			return &tg.InputChannel{ChannelID: raw, AccessHash: cc.AccessHash}, nil
		}
	}
	return nil, fmt.Errorf("channel %d not found — add the bot to the channel as admin, then restart", raw)
}

// ---------- messages ----------

func messagesOfClass(res tg.MessagesMessagesClass) []tg.MessageClass {
	switch m := res.(type) {
	case *tg.Messages:
		return m.Messages
	case *tg.MessagesSlice:
		return m.Messages
	case *tg.MessagesChannelMessages:
		return m.Messages
	case *tg.MessagesMessages:
		return m.Messages
	case *tg.MessagesMessagesSlice:
		return m.Messages
	}
	return nil
}

// GetMessage fetches a single channel message by id.
func (c *Client) GetMessage(ctx context.Context, msgID int64) (*tg.Message, error) {
	peer, err := c.inputChannel(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.API().ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: peer,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: int(msgID)}},
	})
	if err != nil {
		return nil, err
	}
	for _, m := range messagesOfClass(res) {
		if msg, ok := m.(*tg.Message); ok {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("message %d not found in channel", msgID)
}

// ---------- file locations ----------

var errUnsupportedMedia = errors.New("unsupported media type")

func locationFromMessage(msg *tg.Message) (tg.InputFileLocationClass, int64, error) {
	switch media := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			return nil, 0, errUnsupportedMedia
		}
		loc := &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     "",
		}
		return loc, doc.Size, nil
	case *tg.MessageMediaPhoto:
		ph, ok := media.Photo.(*tg.Photo)
		if !ok {
			return nil, 0, errUnsupportedMedia
		}
		bestType := ""
		bestBytes := 0
		bestPixels := 0
		for _, sz := range ph.Sizes {
			switch s := sz.(type) {
			case *tg.PhotoSize:
				if s.W*s.H >= bestPixels {
					bestPixels, bestBytes, bestType = s.W*s.H, s.Size, s.Type
				}
			case *tg.PhotoSizeProgressive:
				bytes := 0
				for _, n := range s.Sizes {
					if n > bytes {
						bytes = n
					}
				}
				if s.W*s.H >= bestPixels {
					bestPixels, bestBytes, bestType = s.W*s.H, bytes, s.Type
				}
			}
		}
		if bestType == "" {
			return nil, 0, errUnsupportedMedia
		}
		loc := &tg.InputPhotoFileLocation{
			ID:            ph.ID,
			AccessHash:    ph.AccessHash,
			FileReference: ph.FileReference,
			ThumbSize:     bestType,
		}
		return loc, int64(bestBytes), nil
	}
	return nil, 0, errUnsupportedMedia
}

func (c *Client) handle(ctx context.Context, msgID int64) (*fileHandle, error) {
	c.mu.Lock()
	if h, ok := c.handles[msgID]; ok && time.Now().Before(h.exp) {
		c.mu.Unlock()
		return h, nil
	}
	c.mu.Unlock()

	msg, err := c.GetMessage(ctx, msgID)
	if err != nil {
		return nil, err
	}
	loc, size, err := locationFromMessage(msg)
	if err != nil {
		return nil, fmt.Errorf("message %d: %w", msgID, err)
	}
	h := &fileHandle{loc: loc, size: size, exp: time.Now().Add(30 * time.Minute)}
	c.mu.Lock()
	c.handles[msgID] = h
	c.mu.Unlock()
	return h, nil
}

func (c *Client) invalidate(msgID int64) {
	c.mu.Lock()
	delete(c.handles, msgID)
	c.mu.Unlock()
}

// ---------- reading ----------

// ReadBlock reads one aligned block (max 1MiB, offset multiple of 4096)
// of the file attached to a channel message.
func (c *Client) ReadBlock(ctx context.Context, msgID int64, block int64, buf []byte) (int, error) {
	return c.readChunk(ctx, msgID, block*BlockSz, buf)
}

const BlockSz = 1 << 20

func (c *Client) readChunk(ctx context.Context, msgID int64, offset int64, buf []byte) (int, error) {
	if len(buf) < BlockSz {
		return 0, errors.New("read buffer too small")
	}
	limit := BlockSz
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if lastErr != nil {
			select {
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		h, err := c.handle(ctx, msgID)
		if err != nil {
			return 0, err
		}
		if offset >= h.size && h.size > 0 {
			return 0, nil // EOF
		}
		limit = BlockSz
		if h.size > 0 {
			rem := h.size - offset
			if rem < int64(limit) {
				limit = int(align4096(rem))
			}
		}
		res, err := c.API().UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: h.loc,
			Offset:   offset,
			Limit:    limit,
		})
		if err != nil {
			lastErr = err
			if tgerr.Is(err, tg.ErrFileReferenceExpired) {
				c.invalidate(msgID)
				continue
			}
			if wait, ok := telegram.AsFloodWait(err); ok {
				c.cfg.Log.Warn("telegram: FLOOD_WAIT", "duration", wait.String())
				select {
				case <-time.After(wait + time.Second):
				case <-ctx.Done():
					return 0, ctx.Err()
				}
				continue
			}
			if rpcErr, ok := tgerr.As(err); ok && rpcErr.IsOneOf("OFFSET_INVALID") {
				return 0, nil // past EOF
			}
			c.cfg.Log.Warn("telegram: getFile failed", "msg_id", msgID, "offset", offset, "err", err)
			continue
		}
		f, ok := res.(*tg.UploadFile)
		if !ok {
			return 0, fmt.Errorf("unexpected upload.getFile response %T", res)
		}
		return copy(buf, f.Bytes), nil
	}
	return 0, fmt.Errorf("download failed after retries: %w", lastErr)
}

func align4096(n int64) int64 {
	return ((n + 4095) / 4096) * 4096
}

// ---------- writing / admin ----------

// Upload streams r into the channel as a document named name.
func (c *Client) Upload(ctx context.Context, name, mime string, r io.Reader, size int64) (int64, error) {
	peer, err := c.inputChannel(ctx)
	if err != nil {
		return 0, err
	}
	up := uploader.NewUploader(c.API()).WithThreads(4)
	file, err := up.Upload(ctx, uploader.NewUpload(name, r, size))
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	res, err := c.API().MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer:     peer,
		RandomID: randInt63(),
		Media: &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: mime,
			ForceFile: true,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: name},
			},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("send media: %w", err)
	}
	return messageIDFromUpdates(res)
}

// Delete removes a channel message.
func (c *Client) Delete(ctx context.Context, msgID int64) error {
	peer, err := c.inputChannel(ctx)
	if err != nil {
		return err
	}
	_, err = c.API().ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
		Channel: peer,
		ID:      []int{int(msgID)},
	})
	return err
}

// Copy duplicates a channel message (server-side, no re-upload).
func (c *Client) Copy(ctx context.Context, msgID int64) (int64, error) {
	peer, err := c.inputChannel(ctx)
	if err != nil {
		return 0, err
	}
	res, err := c.API().MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer:   peer,
		ToPeer:     peer,
		ID:         []int{int(msgID)},
		RandomID:   []int64{randInt63()},
		DropAuthor: true,
	})
	if err != nil {
		return 0, err
	}
	return messageIDFromUpdates(res)
}

func messageIDFromUpdates(res tg.UpdatesClass) (int64, error) {
	var updates []tg.UpdateClass
	switch u := res.(type) {
	case *tg.Updates:
		updates = u.Updates
	case *tg.UpdatesCombined:
		updates = u.Updates
	case *tg.UpdateShortMessage:
		return int64(u.ID), nil
	case *tg.UpdateShortSentMessage:
		return int64(u.ID), nil
	}
	for _, u := range updates {
		var mc tg.MessageClass
		switch m := u.(type) {
		case *tg.UpdateNewChannelMessage:
			mc = m.Message
		case *tg.UpdateNewMessage:
			mc = m.Message
		}
		if msg, ok := mc.(*tg.Message); ok {
			return int64(msg.ID), nil
		}
	}
	return 0, errors.New("no message id in updates")
}

func randInt63() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return time.Now().UnixNano()
	}
	return n.Int64()
}
