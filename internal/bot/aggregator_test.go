package bot

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestStitchChunks(t *testing.T) {
	// Case 1: Empty chunks
	if res := StitchChunks(nil); res != "" {
		t.Fatalf("Expected empty, got %q", res)
	}

	// Case 2: Single chunk
	chunks1 := []MessageChunk{{MessageID: 1, Text: "Hello world"}}
	if res := StitchChunks(chunks1); res != "Hello world" {
		t.Fatalf("Expected 'Hello world', got %q", res)
	}

	// Case 3: Sliced chunk (length >= 4000)
	longPart1 := strings.Repeat("A", 4000)
	part2 := "BBBB"
	chunks2 := []MessageChunk{
		{MessageID: 1, Text: longPart1},
		{MessageID: 2, Text: part2},
	}
	res2 := StitchChunks(chunks2)
	expected2 := longPart1 + part2
	if res2 != expected2 {
		t.Fatalf("Expected direct concatenation for 4000+ char chunk, length got %d want %d", len(res2), len(expected2))
	}

	// Case 4: Rapid short messages without newline
	chunks3 := []MessageChunk{
		{MessageID: 1, Text: "Line 1"},
		{MessageID: 2, Text: "Line 2"},
	}
	res3 := StitchChunks(chunks3)
	if res3 != "Line 1\nLine 2" {
		t.Fatalf("Expected 'Line 1\\nLine 2', got %q", res3)
	}

	// Case 5: Short messages with existing newline
	chunks4 := []MessageChunk{
		{MessageID: 1, Text: "Line 1\n"},
		{MessageID: 2, Text: "Line 2"},
	}
	res4 := StitchChunks(chunks4)
	if res4 != "Line 1\nLine 2" {
		t.Fatalf("Expected 'Line 1\\nLine 2', got %q", res4)
	}
}

func TestMessageAggregatorDebounce(t *testing.T) {
	agg := NewMessageAggregator()

	var resultPrompt string
	var resultParts int
	done := make(chan struct{})

	exec := func(chatID, userID int64, threadID int, prompt, imagePath, preloadSkills string, partsCount int) {
		resultPrompt = prompt
		resultParts = partsCount
		close(done)
	}

	msg1 := &tgbotapi.Message{
		MessageID: 101,
		From:      &tgbotapi.User{ID: 12345},
		Chat:      &tgbotapi.Chat{ID: 67890},
		Text:      "Part 1 of message",
	}
	msg2 := &tgbotapi.Message{
		MessageID: 102,
		From:      &tgbotapi.User{ID: 12345},
		Chat:      &tgbotapi.Chat{ID: 67890},
		Text:      "Part 2 of message",
	}

	agg.Add(msg1, 0, exec)
	time.Sleep(100 * time.Millisecond)
	agg.Add(msg2, 0, exec)

	select {
	case <-done:
		if resultParts != 2 {
			t.Errorf("Expected 2 parts, got %d", resultParts)
		}
		if resultPrompt != "Part 1 of message\nPart 2 of message" {
			t.Errorf("Unexpected prompt: %q", resultPrompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Debounce timer did not fire in time")
	}
}

func TestMessageAggregator_ForwardedBurst(t *testing.T) {
	agg := NewMessageAggregator()

	var resultPrompt string
	var resultParts int
	done := make(chan struct{})

	exec := func(chatID, userID int64, threadID int, replyToID int, prompt, imagePath, preloadSkills string, partsCount int) {
		resultPrompt = prompt
		resultParts = partsCount
		close(done)
	}

	msg1 := &tgbotapi.Message{
		MessageID: 201,
		From:      &tgbotapi.User{ID: 11111},
		Chat:      &tgbotapi.Chat{ID: 22222},
		Text:      "Lirik lagu bagian intro",
		ForwardFrom: &tgbotapi.User{
			ID:        999,
			FirstName: "Composer",
		},
	}
	msg2 := &tgbotapi.Message{
		MessageID: 202,
		From:      &tgbotapi.User{ID: 11111},
		Chat:      &tgbotapi.Chat{ID: 22222},
		Text:      "Bait kedua di menit 01:20",
		ForwardFrom: &tgbotapi.User{
			ID:        999,
			FirstName: "Composer",
		},
	}

	agg.AddWithMedia(msg1, 0, nil, nil, exec)
	time.Sleep(50 * time.Millisecond)
	agg.AddWithMedia(msg2, 0, nil, nil, exec)

	select {
	case <-done:
		if resultParts != 2 {
			t.Errorf("Expected 2 parts, got %d", resultParts)
		}
		if !strings.Contains(resultPrompt, "[Pesan Terusan (Forwarded Messages)]") {
			t.Errorf("Expected forwarded section, got: %s", resultPrompt)
		}
		if !strings.Contains(resultPrompt, "Composer") {
			t.Errorf("Expected composer attribution, got: %s", resultPrompt)
		}
		if !strings.Contains(resultPrompt, "Lirik lagu bagian intro") || !strings.Contains(resultPrompt, "Bait kedua di menit 01:20") {
			t.Errorf("Missing expected lyrics in prompt: %s", resultPrompt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Forward debounce timer did not fire in time")
	}
}

func TestMessageAggregator_MediaBatchAndUserInstruction(t *testing.T) {
	agg := NewMessageAggregator()

	var resultPrompt string
	var resultImg string
	done := make(chan struct{})

	exec := func(chatID, userID int64, threadID int, replyToID int, prompt, imagePath, preloadSkills string, partsCount int) {
		resultPrompt = prompt
		resultImg = imagePath
		close(done)
	}

	media1 := &DownloadedMediaItem{
		FileName:  "shorekeeper_crop.jpg",
		FilePath:  "/tmp/shorekeeper_crop.jpg",
		MediaType: "photo",
		IsImage:   true,
		SizeBytes: 150000,
	}
	msg1 := &tgbotapi.Message{
		MessageID: 301,
		From:      &tgbotapi.User{ID: 33333},
		Chat:      &tgbotapi.Chat{ID: 44444},
	}

	media2 := &DownloadedMediaItem{
		FileName:  "AnUnfinishedSong.ass",
		FilePath:  "/tmp/AnUnfinishedSong.ass",
		MediaType: "document",
		IsText:    true,
		TextContent: "[Script Info]\nTitle: An Unfinished Song",
		SizeBytes: 5000,
	}
	msg2 := &tgbotapi.Message{
		MessageID: 302,
		From:      &tgbotapi.User{ID: 33333},
		Chat:      &tgbotapi.Chat{ID: 44444},
		Caption:   "Ini file subtitle dan crop",
	}

	msg3 := &tgbotapi.Message{
		MessageID: 303,
		From:      &tgbotapi.User{ID: 33333},
		Chat:      &tgbotapi.Chat{ID: 44444},
		Text:      "Tolong render video liriknya sekarang",
	}

	agg.AddWithMedia(msg1, 0, media1, nil, exec)
	time.Sleep(50 * time.Millisecond)
	agg.AddWithMedia(msg2, 0, media2, nil, exec)
	time.Sleep(50 * time.Millisecond)
	agg.AddWithMedia(msg3, 0, nil, nil, exec)

	select {
	case <-done:
		if resultImg != "/tmp/shorekeeper_crop.jpg" {
			t.Errorf("Expected primary image /tmp/shorekeeper_crop.jpg, got: %s", resultImg)
		}
		if !strings.Contains(resultPrompt, "/tmp/shorekeeper_crop.jpg") {
			t.Errorf("Missing photo in prompt: %s", resultPrompt)
		}
		if !strings.Contains(resultPrompt, "AnUnfinishedSong.ass") {
			t.Errorf("Missing ass subtitle in prompt: %s", resultPrompt)
		}
		if !strings.Contains(resultPrompt, "Tolong render video liriknya sekarang") {
			t.Errorf("Missing user instruction in prompt: %s", resultPrompt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Media batch debounce timer did not fire in time")
	}
}
