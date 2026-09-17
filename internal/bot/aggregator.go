package bot

import (
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// MessageChunk represents a single sliced message part from Telegram.
type MessageChunk struct {
	MessageID int
	Text      string
}

// UserBuffer stores incoming message chunks for a specific user within a sliding debounce window.
type UserBuffer struct {
	mu        sync.Mutex
	chatID    int64
	userID    int64
	chunks    []MessageChunk
	timer     *time.Timer
	executeFn func(chatID, userID int64, prompt, imagePath, preloadSkills string, partsCount int)
}

// MessageAggregator manages per-user buffers to reassemble messages split by Telegram's 4096-char limit.
type MessageAggregator struct {
	mu      sync.Mutex
	buffers map[int64]*UserBuffer
}

// NewMessageAggregator initializes a thread-safe MessageAggregator.
func NewMessageAggregator() *MessageAggregator {
	return &MessageAggregator{
		buffers: make(map[int64]*UserBuffer),
	}
}

// Add appends an incoming message to the user's aggregation buffer.
func (a *MessageAggregator) Add(
	msg *tgbotapi.Message,
	executeFn func(chatID, userID int64, prompt, imagePath, preloadSkills string, partsCount int),
) {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	text := msg.Text

	a.mu.Lock()
	buf, exists := a.buffers[userID]
	if !exists {
		buf = &UserBuffer{
			chatID:    chatID,
			userID:    userID,
			chunks:    make([]MessageChunk, 0, 4),
			executeFn: executeFn,
		}
		a.buffers[userID] = buf
	}
	a.mu.Unlock()

	buf.mu.Lock()
	defer buf.mu.Unlock()

	buf.chunks = append(buf.chunks, MessageChunk{
		MessageID: msg.MessageID,
		Text:      text,
	})

	if buf.timer != nil {
		buf.timer.Stop()
	}

	// Dynamic debounce delay:
	// If message is >= 4000 characters, Telegram sliced it mid-stream; allow 1200ms for subsequent bursts.
	// Otherwise, allow a quick 600ms window for rapid successive pastes.
	delay := 600 * time.Millisecond
	if len([]rune(text)) >= 4000 {
		delay = 1200 * time.Millisecond
	}

	buf.timer = time.AfterFunc(delay, func() {
		a.flush(userID)
	})
}

// Cancel terminates and discards any pending aggregation buffer for a user (e.g. on /stop).
func (a *MessageAggregator) Cancel(userID int64) bool {
	a.mu.Lock()
	buf, exists := a.buffers[userID]
	if exists {
		delete(a.buffers, userID)
	}
	a.mu.Unlock()

	if !exists || buf == nil {
		return false
	}

	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.timer != nil {
		buf.timer.Stop()
		buf.timer = nil
	}
	buf.chunks = nil
	return true
}

// flush combines all accumulated chunks and sends them to the execution function.
func (a *MessageAggregator) flush(userID int64) {
	a.mu.Lock()
	buf, exists := a.buffers[userID]
	if exists {
		delete(a.buffers, userID)
	}
	a.mu.Unlock()

	if !exists || buf == nil {
		return
	}

	buf.mu.Lock()
	chunks := buf.chunks
	buf.chunks = nil
	chatID := buf.chatID
	exec := buf.executeFn
	buf.mu.Unlock()

	if len(chunks) == 0 {
		return
	}

	combinedPrompt := StitchChunks(chunks)
	partsCount := len(chunks)

	if partsCount > 1 {
		log.Printf("[aggregator] Successfully merged %d split Telegram messages into 1 complete prompt (%d chars) for User %d",
			partsCount, len([]rune(combinedPrompt)), userID)
	}

	if exec != nil {
		exec(chatID, userID, combinedPrompt, "", "", partsCount)
	}
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
