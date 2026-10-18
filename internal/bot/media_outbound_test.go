package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessOutboundMedia_ExtractionAndSanitization(t *testing.T) {
	// Setup temporary files
	tmpDir := t.TempDir()
	imgFile := filepath.Join(tmpDir, "test_screenshot.png")
	pdfFile := filepath.Join(tmpDir, "test_report.pdf")
	vidFile := filepath.Join(tmpDir, "test_clip.mp4")

	if err := os.WriteFile(imgFile, []byte("fake image data"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	if err := os.WriteFile(pdfFile, []byte("fake pdf data"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	if err := os.WriteFile(vidFile, []byte("fake video data"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	botServer := &BotServer{} // bot is nil, so no network call is executed

	// Test 1: Bare image path in conversational sentence
	text1 := "File screenshot bisa dibuka langsung di path ini: " + imgFile
	sanitized1 := botServer.ProcessOutboundMedia(12345, 67890, text1)
	// The path existed, so it was detected. Since bot is nil, upload returns nil error, cleanText preserves sentence.
	if !strings.Contains(sanitized1, "test_screenshot.png") {
		t.Errorf("expected sanitized text to keep context, got: %s", sanitized1)
	}

	// Test 2: Tag-based media
	text2 := "SCREENSHOT: " + imgFile + "\nFILE: " + pdfFile
	sanitized2 := botServer.ProcessOutboundMedia(12345, 67890, text2)
	if !strings.Contains(sanitized2, "Foto / Screenshot terkirim") {
		t.Errorf("expected screenshot replacement notice, got: %s", sanitized2)
	}
	if !strings.Contains(sanitized2, "File dokumen terkirim") {
		t.Errorf("expected document replacement notice, got: %s", sanitized2)
	}

	// Test 3: Markdown image syntax
	text3 := "Berikut buktinya: ![" + "Bukti" + "](" + imgFile + ")"
	sanitized3 := botServer.ProcessOutboundMedia(12345, 67890, text3)
	if !strings.Contains(sanitized3, "Foto / Screenshot terkirim: `test_screenshot.png`") {
		t.Errorf("expected markdown image to be cleaned up, got: %s", sanitized3)
	}

	// Test 4: Video file markdown
	text4 := "[Video Rekaman](" + vidFile + ")"
	sanitized4 := botServer.ProcessOutboundMedia(12345, 67890, text4)
	if !strings.Contains(sanitized4, "Video terkirim: `test_clip.mp4`") {
		t.Errorf("expected video to be cleaned up, got: %s", sanitized4)
	}

	// Test 5: Non-existent file should be ignored
	nonExistent := "/tmp/definitely_not_existing_1234567.png"
	text5 := "SCREENSHOT: " + nonExistent
	sanitized5 := botServer.ProcessOutboundMedia(12345, 67890, text5)
	if sanitized5 != text5 {
		t.Errorf("expected non-existent file to be left unchanged, got: %s", sanitized5)
	}

	// Test 6: Sensitive files (.sh, .go, .py, /root/scripts/) must NEVER be uploaded
	scriptFile := filepath.Join(tmpDir, "notify_telegram.sh")
	_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\necho secret"), 0755)
	text6 := "Kirim file via " + scriptFile
	sanitized6 := botServer.ProcessOutboundMedia(12345, 67890, text6)
	if sanitized6 != text6 {
		t.Errorf("sensitive script was not preserved in text: %s", sanitized6)
	}

	// Test 7: Paths in inline code backticks should NOT be picked up by bare path scanner
	text7 := "File lama jangan dipakai: `" + imgFile + "`"
	sanitized7 := botServer.ProcessOutboundMedia(12345, 67890, text7)
	if sanitized7 != text7 {
		t.Errorf("path inside backticks was modified or picked up: %s", sanitized7)
	}
}
