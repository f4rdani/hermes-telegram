package bot

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// DownloadTelegramFile fetches a file from Telegram API and saves it locally.
func DownloadTelegramFile(bot *tgbotapi.BotAPI, fileID string, destPath string) error {
	fileURL, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		return fmt.Errorf("failed to get file direct URL: %w", err)
	}

	resp, err := http.Get(fileURL)
	if err != nil {
		return fmt.Errorf("failed to download file from telegram: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram download returned status: %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// ProcessMediaMessage extracts media from a Telegram message, saves to disk, and returns the prompt and image path.
func ProcessMediaMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, mediaDir string) (prompt string, imagePath string, err error) {
	caption := msg.Caption

	// 1. Photo handling
	if len(msg.Photo) > 0 {
		bestPhoto := msg.Photo[len(msg.Photo)-1]
		fileName := fmt.Sprintf("photo_%d_%s.jpg", time.Now().Unix(), bestPhoto.FileID[:8])
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, bestPhoto.FileID, savePath); err != nil {
			return "", "", fmt.Errorf("gagal mengunduh foto: %w", err)
		}

		if caption == "" {
			caption = "Analisis gambar ini dan berikan penjelasan mendalam mengenai isinya."
		}
		return caption, savePath, nil
	}

	// 2. Voice Note handling
	if msg.Voice != nil {
		fileName := fmt.Sprintf("voice_%d_%s.ogg", time.Now().Unix(), msg.Voice.FileID[:8])
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, msg.Voice.FileID, savePath); err != nil {
			return "", "", fmt.Errorf("gagal mengunduh pesan suara: %w", err)
		}

		userPrompt := fmt.Sprintf("Pengguna mengirim pesan suara yang tersimpan di file: `%s`.\nTranskrip dan tanggapi pesan suara tersebut.", savePath)
		if caption != "" {
			userPrompt += "\nCatatan tambahan pengguna: " + caption
		}
		return userPrompt, "", nil
	}

	// 3. Audio File handling
	if msg.Audio != nil {
		ext := filepath.Ext(msg.Audio.FileName)
		if ext == "" {
			ext = ".mp3"
		}
		fileName := fmt.Sprintf("audio_%d%s", time.Now().Unix(), ext)
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, msg.Audio.FileID, savePath); err != nil {
			return "", "", fmt.Errorf("gagal mengunduh audio: %w", err)
		}

		userPrompt := fmt.Sprintf("Pengguna mengirim file audio `%s` yang tersimpan di: `%s`.\nTolong periksa dan proses audio ini.", msg.Audio.FileName, savePath)
		if caption != "" {
			userPrompt += "\nCatatan: " + caption
		}
		return userPrompt, "", nil
	}

	// 4. Document handling
	if msg.Document != nil {
		fileName := msg.Document.FileName
		if fileName == "" {
			fileName = fmt.Sprintf("doc_%d", time.Now().Unix())
		}
		savePath := filepath.Join(mediaDir, fmt.Sprintf("%d_%s", time.Now().Unix(), fileName))

		if err := DownloadTelegramFile(bot, msg.Document.FileID, savePath); err != nil {
			return "", "", fmt.Errorf("gagal mengunduh dokumen: %w", err)
		}

		userPrompt := fmt.Sprintf("Pengguna mengirim dokumen `%s` yang tersimpan di: `%s`.\nBaca dan analisis dokumen ini.", fileName, savePath)
		if caption != "" {
			userPrompt += "\nInstruksi tambahan: " + caption
		}
		return userPrompt, "", nil
	}

	return "", "", fmt.Errorf("pesan tidak mengandung media yang didukung")
}
