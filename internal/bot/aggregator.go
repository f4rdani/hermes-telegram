package bot

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// MessageChunk represents a single sliced message part from Telegram (legacy/compatibility).
type MessageChunk struct {
	MessageID int
	Text      string
}

// AggregatedItem represents an incoming Telegram message, attachment, or split chunk in a debounce batch.
type AggregatedItem struct {
	MessageID    int
	Text         string
	Caption      string
	Media        *DownloadedMediaItem
	IsForwarded  bool
	ForwardFrom  string
	ForwardDate  time.Time
	MediaGroupID string
	RawMsg       *tgbotapi.Message
}

// ExecuteTaskFunc is the callback signature for executing tasks in Hermes.
type ExecuteTaskFunc func(chatID, userID int64, threadID int, replyToMsgID int, prompt, imagePath, preloadSkills string, partsCount int)

// UserBuffer stores incoming message chunks, forwarded messages, and media for a user within a sliding debounce window.
type UserBuffer struct {
	mu         sync.Mutex
	chatID     int64
	userID     int64
	threadID   int
	bot        *tgbotapi.BotAPI
	items      []AggregatedItem
	mediaFiles []*DownloadedMediaItem
	msgIDs     []int
	firstSeen  time.Time
	lastSeen   time.Time
	timer      *time.Timer
	executeFn  ExecuteTaskFunc
}

// MessageAggregator manages per-user buffers to reassemble messages split by Telegram limits,
// debouncing forwarded message bursts and multi-file uploads into a single coherent prompt.
type MessageAggregator struct {
	mu      sync.Mutex
	buffers map[string]*UserBuffer
}

// NewMessageAggregator initializes a thread-safe MessageAggregator.
func NewMessageAggregator() *MessageAggregator {
	return &MessageAggregator{
		buffers: make(map[string]*UserBuffer),
	}
}

// ExtractForwardInfo inspects Telegram message headers to extract forward metadata.
func ExtractForwardInfo(msg *tgbotapi.Message) (bool, string, time.Time) {
	if msg == nil {
		return false, "", time.Time{}
	}
	isForwarded := false
	var sender string
	var fwdTime time.Time

	if msg.ForwardFrom != nil {
		isForwarded = true
		name := strings.TrimSpace(msg.ForwardFrom.FirstName + " " + msg.ForwardFrom.LastName)
		if msg.ForwardFrom.UserName != "" {
			if name != "" {
				name = fmt.Sprintf("%s (@%s)", name, msg.ForwardFrom.UserName)
			} else {
				name = "@" + msg.ForwardFrom.UserName
			}
		}
		if name == "" {
			name = fmt.Sprintf("User %d", msg.ForwardFrom.ID)
		}
		sender = name
	} else if msg.ForwardSenderName != "" {
		isForwarded = true
		sender = msg.ForwardSenderName
	} else if msg.ForwardFromChat != nil {
		isForwarded = true
		if msg.ForwardFromChat.Title != "" {
			sender = msg.ForwardFromChat.Title
		} else if msg.ForwardFromChat.UserName != "" {
			sender = "@" + msg.ForwardFromChat.UserName
		} else {
			sender = fmt.Sprintf("Chat %d", msg.ForwardFromChat.ID)
		}
	} else if msg.ForwardDate > 0 {
		isForwarded = true
		sender = "Pesan Terusan"
	}

	if msg.IsAutomaticForward {
		isForwarded = true
		if sender == "" {
			sender = "Channel Post"
		}
	}

	if msg.ForwardDate > 0 {
		fwdTime = time.Unix(int64(msg.ForwardDate), 0)
	}

	return isForwarded, sender, fwdTime
}

// IsForwardedMessage reports whether a message was forwarded from another chat, user, or channel.
func IsForwardedMessage(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	return msg.ForwardFrom != nil || msg.ForwardSenderName != "" || msg.ForwardFromChat != nil || msg.ForwardDate > 0 || msg.IsAutomaticForward
}

// AddWithMedia appends an incoming message (with optional media attachment) to the user's aggregation buffer.
func (a *MessageAggregator) AddWithMedia(
	msg *tgbotapi.Message,
	threadID int,
	media *DownloadedMediaItem,
	bot *tgbotapi.BotAPI,
	executeFn ExecuteTaskFunc,
) {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	text := msg.Text
	caption := msg.Caption
	key := fmt.Sprintf("%d:%d", userID, threadID)

	isFwd, fwdSender, fwdDate := ExtractForwardInfo(msg)

	item := AggregatedItem{
		MessageID:    msg.MessageID,
		Text:         text,
		Caption:      caption,
		Media:        media,
		IsForwarded:  isFwd,
		ForwardFrom:  fwdSender,
		ForwardDate:  fwdDate,
		MediaGroupID: msg.MediaGroupID,
		RawMsg:       msg,
	}

	a.mu.Lock()
	buf, exists := a.buffers[key]
	if !exists {
		buf = &UserBuffer{
			chatID:     chatID,
			userID:     userID,
			threadID:   threadID,
			bot:        bot,
			items:      []AggregatedItem{item},
			mediaFiles: nil,
			msgIDs:     []int{msg.MessageID},
			firstSeen:  time.Now(),
			lastSeen:   time.Now(),
			executeFn:  executeFn,
		}
		if media != nil {
			buf.mediaFiles = []*DownloadedMediaItem{media}
		}
		a.buffers[key] = buf

		delay := 600 * time.Millisecond
		if media != nil || isFwd || msg.MediaGroupID != "" {
			delay = 950 * time.Millisecond
		} else if len([]rune(text)) >= 4000 {
			delay = 1200 * time.Millisecond
		}

		buf.timer = time.AfterFunc(delay, func() {
			a.flush(key)
		})
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	buf.mu.Lock()
	defer buf.mu.Unlock()

	buf.items = append(buf.items, item)
	buf.msgIDs = append(buf.msgIDs, msg.MessageID)
	if media != nil {
		buf.mediaFiles = append(buf.mediaFiles, media)
	}
	buf.lastSeen = time.Now()
	buf.executeFn = executeFn
	if bot != nil {
		buf.bot = bot
	}

	if buf.timer != nil {
		buf.timer.Stop()
	}

	// Max ceiling check: if burst exceeds 6 seconds, flush immediately
	if time.Since(buf.firstSeen) >= 6*time.Second {
		go a.flush(key)
		return
	}

	delay := 600 * time.Millisecond
	if len(buf.mediaFiles) > 0 || isFwd || msg.MediaGroupID != "" {
		delay = 950 * time.Millisecond
	} else if len([]rune(text)) >= 4000 {
		delay = 1200 * time.Millisecond
	}

	buf.timer = time.AfterFunc(delay, func() {
		a.flush(key)
	})
}

// Add is the backward-compatible entrypoint for text-only chunks.
func (a *MessageAggregator) Add(
	msg *tgbotapi.Message,
	threadID int,
	legacyExecFn func(chatID, userID int64, threadID int, prompt, imagePath, preloadSkills string, partsCount int),
) {
	a.AddWithMedia(msg, threadID, nil, nil, func(cID, uID int64, tID int, replyID int, prompt, img, skills string, partsCount int) {
		legacyExecFn(cID, uID, tID, prompt, img, skills, partsCount)
	})
}

// Cancel terminates and discards any pending aggregation buffer for a user (e.g. on /stop).
func (a *MessageAggregator) Cancel(userID int64) bool {
	a.mu.Lock()
	var keysToCancel []string
	prefix := fmt.Sprintf("%d:", userID)
	for k, buf := range a.buffers {
		if strings.HasPrefix(k, prefix) {
			keysToCancel = append(keysToCancel, k)
			if buf != nil && buf.timer != nil {
				buf.timer.Stop()
			}
		}
	}
	for _, k := range keysToCancel {
		delete(a.buffers, k)
	}
	a.mu.Unlock()

	return len(keysToCancel) > 0
}

// flush combines all accumulated items and sends them to the execution function as a single coherent prompt.
func (a *MessageAggregator) flush(key string) {
	a.mu.Lock()
	buf, exists := a.buffers[key]
	if exists {
		delete(a.buffers, key)
	}
	a.mu.Unlock()

	if !exists || buf == nil {
		return
	}

	buf.mu.Lock()
	items := buf.items
	mediaFiles := buf.mediaFiles
	buf.items = nil
	buf.mediaFiles = nil
	chatID := buf.chatID
	userID := buf.userID
	threadID := buf.threadID
	bot := buf.bot
	exec := buf.executeFn
	buf.mu.Unlock()

	if len(items) == 0 {
		return
	}

	replyToMsgID := items[0].MessageID
	hasMedia := len(mediaFiles) > 0
	hasForwarded := false
	for _, it := range items {
		if it.IsForwarded {
			hasForwarded = true
			break
		}
	}

	// Send single consolidated user notice if applicable
	if bot != nil {
		if hasMedia && len(mediaFiles) > 1 {
			var sbNotice strings.Builder
			sbNotice.WriteString(fmt.Sprintf("📥 *%d File Diterima:*\n", len(mediaFiles)))
			for _, m := range mediaFiles {
				sizeKB := float64(m.SizeBytes) / 1024.0
				sbNotice.WriteString(fmt.Sprintf("• `%s` (%.1f KB)\n", m.FileName, sizeKB))
			}
			sbNotice.WriteString("⏳ *Hermes sedang memproses berkas & instruksi...*")
			_, _ = SendSafeMessage(bot, chatID, sbNotice.String(), nil, threadID)
		} else if hasForwarded && len(items) > 1 {
			notice := fmt.Sprintf("📥 *%d pesan terusan diterima.*\n⏳ *Menggabungkan pesan menjadi satu konteks utuh...*", len(items))
			_, _ = SendSafeMessage(bot, chatID, notice, nil, threadID)
		} else if len(items) > 1 && !hasMedia {
			notice := fmt.Sprintf("📥 *Teks panjang terdeteksi (%d bagian).*\n⏳ *Berhasil digabungkan. Memproses...*", len(items))
			_, _ = SendSafeMessage(bot, chatID, notice, nil, threadID)
		}
	}

	combinedPrompt, primaryImage := BuildHermesPrompt(items, mediaFiles)
	partsCount := len(items)

	if partsCount > 1 {
		log.Printf("[aggregator] Successfully merged %d items (files: %d, forwarded: %v) into 1 complete prompt (%d chars) for User %d (Thread %d)",
			partsCount, len(mediaFiles), hasForwarded, len([]rune(combinedPrompt)), userID, threadID)
	}

	if exec != nil {
		exec(chatID, userID, threadID, replyToMsgID, combinedPrompt, primaryImage, "", partsCount)
	}
}

// BuildHermesPrompt structures accumulated items, attachments, and forwarded contexts into a single clean prompt.
func BuildHermesPrompt(items []AggregatedItem, mediaFiles []*DownloadedMediaItem) (prompt string, imagePath string) {
	hasMedia := len(mediaFiles) > 0
	hasForwarded := false
	for _, it := range items {
		if it.IsForwarded {
			hasForwarded = true
			break
		}
	}

	// Determine primary image for vision if any
	for _, m := range mediaFiles {
		if m.IsImage && imagePath == "" {
			imagePath = m.FilePath
			break
		}
	}

	// 1. Single plain text message (fast path)
	if !hasMedia && !hasForwarded && len(items) <= 1 {
		if len(items) == 1 {
			return items[0].Text, imagePath
		}
		return "", imagePath
	}

	// 2. Pure plain text multi-part message (split message)
	if !hasMedia && !hasForwarded {
		var chunks []MessageChunk
		for _, it := range items {
			chunks = append(chunks, MessageChunk{
				MessageID: it.MessageID,
				Text:      it.Text,
			})
		}
		return StitchChunks(chunks), imagePath
	}

	var sb strings.Builder

	// Section A: Uploaded files list
	if hasMedia {
		if len(mediaFiles) == 1 {
			sb.WriteString(fmt.Sprintf("Pengguna telah mengunggah file ke server:\n- `%s`\n\n", mediaFiles[0].FilePath))
		} else {
			sb.WriteString(fmt.Sprintf("Pengguna telah mengunggah %d file ke server:\n", len(mediaFiles)))
			for _, m := range mediaFiles {
				sb.WriteString(fmt.Sprintf("- `%s` (%s)\n", m.FilePath, m.FileName))
			}
			sb.WriteString("\n")
		}

		// If any text file was embedded, include its content
		for _, m := range mediaFiles {
			if m.IsText && m.TextContent != "" {
				sb.WriteString(fmt.Sprintf("--- Isi Dokumen `%s`:\n```\n%s\n```\n\n", m.FileName, m.TextContent))
			}
		}
	}

	// Section B: Forwarded messages
	if hasForwarded {
		sb.WriteString("[Pesan Terusan (Forwarded Messages)]:\n")
		var lastSender string

		for _, item := range items {
			if !item.IsForwarded {
				continue
			}

			senderHeader := item.ForwardFrom
			if senderHeader == "" {
				senderHeader = "Pesan Terusan"
			}
			if !item.ForwardDate.IsZero() {
				dateStr := item.ForwardDate.Format("02/01/2006 15:04")
				senderHeader = fmt.Sprintf("%s (%s)", senderHeader, dateStr)
			}

			content := strings.TrimSpace(item.Text)
			if content == "" && item.Caption != "" {
				content = strings.TrimSpace(item.Caption)
			}

			if item.Media != nil {
				mediaNote := fmt.Sprintf("[File: %s]", filepath.Base(item.Media.FileName))
				if content != "" {
					content = mediaNote + "\n" + content
				} else {
					content = mediaNote
				}
			}

			if item.ForwardFrom != "" && item.ForwardFrom == lastSender {
				if content != "" {
					sb.WriteString(content + "\n")
				}
			} else {
				lastSender = item.ForwardFrom
				sb.WriteString(fmt.Sprintf("--- Dari %s:\n", senderHeader))
				if content != "" {
					sb.WriteString(content + "\n")
				}
			}
		}
		sb.WriteString("\n")
	}

	// Section C: User's direct instructions or media captions
	var userNotes []string
	for _, item := range items {
		if item.IsForwarded {
			continue
		}
		txt := strings.TrimSpace(item.Text)
		if txt != "" {
			userNotes = append(userNotes, txt)
		} else if item.Caption != "" {
			capt := strings.TrimSpace(item.Caption)
			if capt != "" {
				userNotes = append(userNotes, capt)
			}
		}
	}

	if len(userNotes) > 0 {
		combinedUserNotes := strings.Join(userNotes, "\n")
		sb.WriteString("[Instruksi Pengguna]:\n" + combinedUserNotes + "\n")
	} else {
		// Fallback instruction
		if hasMedia && hasForwarded {
			sb.WriteString("Silakan periksa berkas di atas dan tanggapi rangkaian pesan terusan tersebut.\n")
		} else if hasMedia {
			sb.WriteString("Silakan periksa dan proses berkas yang diunggah di atas.\n")
		} else if hasForwarded {
			sb.WriteString("Silakan tinjau dan tanggapi rangkaian pesan terusan di atas.\n")
		}
	}

	return strings.TrimSpace(sb.String()), imagePath
}

// StitchChunks reassembles split message chunks into the exact original text.
func StitchChunks(chunks []MessageChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	if len(chunks) == 1 {
		return chunks[0].Text
	}

	var sb strings.Builder
	for i, chunk := range chunks {
		if i == 0 {
			sb.WriteString(chunk.Text)
			continue
		}
		prev := chunks[i-1].Text
		curr := chunk.Text

		// If previous chunk was >= 4000 characters, Telegram sliced it at protocol limits.
		// Direct concatenation restores the original text 1:1 without injecting artificial whitespace.
		if len([]rune(prev)) >= 4000 {
			sb.WriteString(curr)
		} else {
			// If previous chunk was short, ensure newline boundary between rapid distinct messages.
			if !strings.HasSuffix(prev, "\n") && !strings.HasPrefix(curr, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString(curr)
		}
	}
	return sb.String()
}
