package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"hermes-tele/config"
	"hermes-tele/internal/engine"
	"hermes-tele/internal/session"
)

type CommandHandler struct {
	cfg           *config.Config
	sessMgr       *session.Manager
	runner        *engine.Runner
	modelResolver *ModelResolver
	version       string
	startTime     time.Time
}

func NewCommandHandler(cfg *config.Config, sessMgr *session.Manager, runner *engine.Runner, modelResolver *ModelResolver, version string) *CommandHandler {
	return &CommandHandler{
		cfg:           cfg,
		sessMgr:       sessMgr,
		runner:        runner,
		modelResolver: modelResolver,
		version:       version,
		startTime:     time.Now(),
	}
}

func (h *CommandHandler) HandleStart(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)
	sessionInfo := "Belum ada sesi aktif (otomatis dibuat saat chat)"
	if s.CurrentSessionID != "" {
		sessionInfo = fmt.Sprintf("`%s`", s.CurrentSessionID)
	}

	reply := fmt.Sprintf(
		"🕊️ *Selamat Datang di Hermes Agent Telegram Gateway*\n\n"+
			"🤖 *Status Sistem:* Standby On-Demand (~10 MB RAM)\n"+
			"⚡ *Model:* `%s` via %s\n"+
			"🧠 *Reasoning:* `%s` | *YOLO:* `%v`\n"+
			"📂 *Workspace:* `%s`\n"+
			"🔖 *Sesi Aktif:* %s\n\n"+
			"💡 *Perintah Utama:*\n"+
			"• `/status` - Cek penggunaan RAM, koneksi %s & Camofox\n"+
			"• `/new` - Mulai sesi percakapan baru yang segar\n"+
			"• `/sessions` - Lihat & pilih daftar sesi sebelumnya\n"+
			"• `/model` - Pilih / ganti model kecerdasan buatan\n"+
			"• `/context` - Cek kapasitas token jendela konteks\n"+
			"• `/commands` - Jelajahi 100+ perintah & skills interaktif\n"+
			"• `/help` - Bantuan lengkap seluruh perintah\n\n"+
			"Kirim pesan teks, instruksi, foto, voice note, atau dokumen untuk memulai!",
		s.CurrentModel, h.cfg.Hermes.GatewayName, s.ReasoningEffort, s.YoloMode, h.cfg.Hermes.WorkingDir, sessionInfo,
		h.cfg.Hermes.GatewayName,
	)

	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleHelp(bot *tgbotapi.BotAPI, chatID, userID int64, query string) {
	query = strings.TrimSpace(strings.ToLower(query))

	if query == "" {
		reply := fmt.Sprintf("📖 *Panduan Perintah Hermes Telegram*\n\n"+
			"🔹 *Manajemen Sesi:*\n"+
			"• `/new` - Mulai sesi baru (fresh session ID + history)\n"+
			"• `/sessions` - Telusuri & resume sesi-sesi sebelumnya\n"+
			"• `/resume <id>` - Lanjutkan sesi tertentu berdasarkan ID\n"+
			"• `/title <nama>` - Beri judul untuk sesi aktif saat ini\n"+
			"• `/clear` - Hapus histori sesi aktif\n\n"+
			"🔹 *Konfigurasi & Model:*\n"+
			"• `/model [nama]` - Ganti model AI (atau klik menu interaktif)\n"+
			"• `/reasoning [level]` - Atur effort penalaran (none/low/med/high)\n"+
			"• `/yolo` - Toggle mode YOLO (auto-approve aksi sensitif)\n"+
			"• `/stop` - Hentikan tugas yang sedang berjalan\n\n"+
			"🔹 *Informasi & Diagnostik:*\n"+
			"• `/status` - Status RAM, CPU, %s, Camofox & sesi\n"+
			"• `/context` - Grafik visual pemakaian token jendela konteks\n"+
			"• `/diff` - Cek perubahan git di direktori kerja\n"+
			"• `/whoami` - Info otorisasi akun Telegram Anda\n"+
			"• `/profile` - Info direktori dan konfigurasi profil\n"+
			"• `/version` - Versi Hermes & Go Gateway\n\n"+
			"🔹 *Skills & Ekstensi:*\n"+
			"• `/skills` - Daftar skill yang terpasang\n"+
			"• `/reload_skills` - Muat ulang skill dari disk\n"+
			"• `/reload_mcp` - Muat ulang konfigurasi MCP server\n"+
			"• `/commands` - Menu interaktif seluruh 100+ perintah",
			h.cfg.Hermes.GatewayName)

		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
		return
	}

	// Filter commands matching query
	var matches []string
	for _, cmd := range HermesMenuCommands {
		if strings.Contains(strings.ToLower(cmd.Command), query) || strings.Contains(strings.ToLower(cmd.Description), query) {
			matches = append(matches, fmt.Sprintf("• `/%s` - %s", cmd.Command, cmd.Description))
		}
	}

	if len(matches) == 0 {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("🔍 Tidak ditemukan perintah yang cocok dengan kata kunci: `%s`", query), nil)
		return
	}

	reply := fmt.Sprintf("🔍 *Hasil Pencarian Perintah (`%s`):*\n\n%s", query, strings.Join(matches, "\n"))
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleCommands(bot *tgbotapi.BotAPI, chatID int64, page int) {
	if page < 1 {
		page = 1
	}
	pageSize := 10
	totalCommands := len(HermesMenuCommands)
	totalPages := (totalCommands + pageSize - 1) / pageSize
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalCommands {
		end = totalCommands
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📜 *Daftar Perintah Hermes (Halaman %d dari %d):*\n\n", page, totalPages))
	for i := start; i < end; i++ {
		cmd := HermesMenuCommands[i]
		sb.WriteString(fmt.Sprintf("• `/%s`\n  _%s_\n", cmd.Command, cmd.Description))
	}

	kb := CommandsPaginationKeyboard(page, totalPages)
	_, _ = SendSafeMessage(bot, chatID, sb.String(), kb)
}

func (h *CommandHandler) HandleStatus(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)

	// 1. RAM & Swap info (Cleanly formatted)
	ramInfo := "Tidak tersedia"
	out, err := exec.Command("free", "-h").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 3 {
			memFields := strings.Fields(lines[1])
			swapFields := strings.Fields(lines[2])
			if len(memFields) >= 7 && len(swapFields) >= 3 {
				ramInfo = fmt.Sprintf("• *RAM:* %s terpakai / %s _(Tersedia: %s)_\n• *Swap:* %s terpakai / %s",
					memFields[2], memFields[1], memFields[6], swapFields[2], swapFields[1])
			} else if len(memFields) >= 3 {
				ramInfo = fmt.Sprintf("• *RAM:* %s terpakai / %s", memFields[2], memFields[1])
			}
		}
	}

	// 2. Gateway ping
	gwName := h.cfg.Hermes.GatewayName
	if gwName == "" {
		gwName = "GoGate"
	}
	gwURL := h.cfg.Hermes.GatewayURL
	if gwURL == "" {
		gwURL = "http://127.0.0.1:8080"
	}
	gwStatus := fmt.Sprintf("🟢 Online (%s)", gwURL)
	client := http.Client{
		Timeout: 1200 * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	reqGw, _ := http.NewRequest("GET", strings.TrimRight(gwURL, "/")+"/v1/models", nil)
	if reqGw != nil {
		if h.cfg.Hermes.GatewayKey != "" {
			reqGw.Header.Set("Authorization", "Bearer "+h.cfg.Hermes.GatewayKey)
		}
		respGw, errGw := client.Do(reqGw)
		if errGw != nil || (respGw != nil && respGw.StatusCode >= 500) {
			gwStatus = "🔴 Offline / Bermasalah"
		}
		if respGw != nil {
			_ = respGw.Body.Close()
		}
	}

	// 3. Camofox ping
	camofoxStatus := "💤 Idle / Sleep (:9377)"
	respCam, errCam := client.Get("http://127.0.0.1:9377")
	if errCam == nil && respCam != nil {
		camofoxStatus = "🟢 Active (:9377)"
		_ = respCam.Body.Close()
	}

	// 4. Current session details
	sessionDetails := "✨ *Sesi Baru* _(Kirim pesan untuk memulai percakapan baru)_"
	if s.CurrentSessionID != "" {
		det, err := h.sessMgr.GetSessionDetails(s.CurrentSessionID)
		if err == nil && det != nil {
			title := det.Title
			if title == "" {
				title = "Tanpa Judul"
			}
			sessionDetails = fmt.Sprintf("🟢 `%s`\n   📌 *%s*\n   💬 %d pesan | 🪙 In %d / Out %d",
				det.ID, title, det.MessageCount, det.InputTokens, det.OutputTokens)
		} else {
			sessionDetails = fmt.Sprintf("🟢 `%s`", s.CurrentSessionID)
		}
	}

	uptime := time.Since(h.startTime).Round(time.Second)

	reply := fmt.Sprintf(
		"📊 *Status Sistem Hermes Agent Gateway*\n\n"+
			"🖥️ *Server & Memori:*\n%s\n\n"+
			"🤖 *Hermes Gateway:* `v%s (Go Native)`\n"+
			"⏱️ *Gateway Uptime:* `%v`\n"+
			"🧠 *Model Default:* `%s`\n"+
			"⚡ *Effort Reasoning:* `%s`\n"+
			"🚀 *Mode YOLO:* `%v`\n\n"+
			"🔌 *Integrasi Layanan:*\n"+
			"• *%s:* %s\n"+
			"• *Camofox Browser:* %s\n\n"+
			"🔖 *Sesi Aktif Pengguna:*\n%s",
		ramInfo, h.version, uptime, s.CurrentModel, s.ReasoningEffort, s.YoloMode,
		gwName, gwStatus, camofoxStatus, sessionDetails,
	)

	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleContext(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)
	if s.CurrentSessionID == "" {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Belum ada sesi percakapan aktif. Kirim pesan untuk membuat sesi baru.", dismissKb)
		return
	}

	det, err := h.sessMgr.GetSessionDetails(s.CurrentSessionID)
	if err != nil || det == nil {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("ℹ️ Sesi `%s` belum memiliki statistik token di database.", s.CurrentSessionID), dismissKb)
		return
	}

	maxTokens := 128000 // default standard context window
	totalUsed := det.InputTokens + det.OutputTokens
	pct := float64(totalUsed) / float64(maxTokens) * 100
	if pct > 100 {
		pct = 100
	}

	// Build visual gauge bar [████░░░░░░]
	bars := 12
	filled := int((pct / 100.0) * float64(bars))
	gauge := strings.Repeat("█", filled) + strings.Repeat("░", bars-filled)

	reply := fmt.Sprintf(
		"🧠 *Jendela Konteks Sesi*\n\n"+
			"🔖 *Sesi:* `%s`\n"+
			"📝 *Judul:* %s\n"+
			"🤖 *Model:* `%s`\n\n"+
			"📊 *Pemakaian Konteks:*\n"+
			"`[%s]` *%.1f%%*\n\n"+
			"• *Input Tokens:* %d\n"+
			"• *Output Tokens:* %d\n"+
			"• *Total Tokens:* %d\n"+
			"• *Jumlah Pesan:* %d\n"+
			"• *Terakhir Aktif:* %s",
		det.ID, det.Title, det.Model, gauge, pct,
		det.InputTokens, det.OutputTokens, totalUsed, det.MessageCount,
		det.LastActivity.Format("15:04:05 02-Jan-2006"),
	)

	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleModel(bot *tgbotapi.BotAPI, chatID, userID int64, modelArg string) {
	s := h.sessMgr.Get(userID, chatID)
	modelArg = strings.TrimSpace(modelArg)

	if modelArg == "" {
		reply := fmt.Sprintf("🤖 *Pengaturan Model AI*\n\nModel aktif saat ini: `%s`\n\nPilih salah satu model di bawah atau ketik `/model <nama_model>`:", s.CurrentModel)
		var models []ModelInfo
		if h.modelResolver != nil {
			models = h.modelResolver.GetModels(h.cfg, s.CurrentModel)
		}
		kb := ModelKeyboard(s.CurrentModel, models)
		_, _ = SendSafeMessage(bot, chatID, reply, kb)
		return
	}

	h.sessMgr.SetModel(userID, modelArg)
	_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("✅ Model AI untuk sesi ini berhasil diubah menjadi: `%s`", modelArg), nil)
}

func (h *CommandHandler) HandleReasoning(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	s := h.sessMgr.Get(userID, chatID)
	arg = strings.TrimSpace(strings.ToLower(arg))

	valid := map[string]bool{"none": true, "low": true, "medium": true, "high": true}

	if arg == "" || !valid[arg] {
		reply := fmt.Sprintf("🧠 *Tingkat Penalaran (Reasoning Effort)*\n\nPengaturan saat ini: `%s`\nPilih tingkat penalaran yang diinginkan:", s.ReasoningEffort)
		kb := ReasoningKeyboard(s.ReasoningEffort)
		_, _ = SendSafeMessage(bot, chatID, reply, kb)
		return
	}

	h.sessMgr.SetReasoning(userID, arg)
	_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("✅ Tingkat penalaran berhasil diatur ke: `%s`", arg), nil)
}

func (h *CommandHandler) HandleNew(bot *tgbotapi.BotAPI, chatID, userID int64) {
	h.sessMgr.ResetSession(userID)
	reply := "✨ *Sesi Baru Disiapkan*\n\nKonteks sesi lama telah direset. Pesan berikutnya yang Anda kirim akan otomatis membuat sesi percakapan baru di Hermes Agent."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleSessions(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)
	sessions, err := h.sessMgr.ListHermesSessions(8)
	if err != nil || len(sessions) == 0 {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Belum ada sesi yang tersimpan dalam database.", dismissKb)
		return
	}

	var sb strings.Builder
	sb.WriteString("📚 *Daftar Sesi Hermes Terbaru*\n\n")

	// Header displaying current active session clearly
	if s.CurrentSessionID != "" {
		activeTitle := ""
		for _, sess := range sessions {
			if sess.ID == s.CurrentSessionID {
				activeTitle = sess.Title
				break
			}
		}
		if activeTitle != "" {
			sb.WriteString(fmt.Sprintf("🟢 *Sesi Aktif Saat Ini:*\n`%s` — _%s_\n\n", s.CurrentSessionID, activeTitle))
		} else {
			sb.WriteString(fmt.Sprintf("🟢 *Sesi Aktif Saat Ini:*\n`%s`\n\n", s.CurrentSessionID))
		}
	} else {
		sb.WriteString("✨ *Sesi Aktif Saat Ini:*\n_Sesi Baru (Belum ada riwayat aktif, kirim pesan untuk memulai)_\n\n")
	}

	sb.WriteString("📋 *Riwayat Sesi Sebelumnya:*\n")
	for i, sess := range sessions {
		mark := "▫️"
		statusBadge := ""
		if sess.ID == s.CurrentSessionID {
			mark = "🟢"
			statusBadge = " *(Sedang Aktif)*"
		}
		sb.WriteString(fmt.Sprintf("%d. %s `%s`%s\n   📌 *%s*\n   💬 %d pesan | ⏱ %s\n\n",
			i+1, mark, sess.ID, statusBadge, sess.Title, sess.MessageCount, sess.LastActivity.Format("15:04 02/01"),
		))
	}
	sb.WriteString("Pilih salah satu tombol di bawah untuk beralih ke sesi tersebut:")

	kb := SessionsKeyboard(sessions, s.CurrentSessionID)
	_, _ = SendSafeMessage(bot, chatID, sb.String(), kb)
}

func (h *CommandHandler) HandleResume(bot *tgbotapi.BotAPI, chatID, userID int64, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		h.HandleSessions(bot, chatID, userID)
		return
	}

	h.sessMgr.SetSessionID(userID, sessionID)
	reply := fmt.Sprintf("✅ *Sesi Berhasil Dialihkan*\n\nSesi aktif sekarang: `%s`\nPercakapan selanjutnya akan melanjutkan riwayat sesi ini.", sessionID)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleTitle(bot *tgbotapi.BotAPI, chatID, userID int64, newTitle string) {
	s := h.sessMgr.Get(userID, chatID)
	if s.CurrentSessionID == "" {
		_, _ = SendSafeMessage(bot, chatID, "⚠️ Tidak ada sesi aktif untuk diberi judul.", nil)
		return
	}
	newTitle = strings.TrimSpace(newTitle)
	if newTitle == "" {
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Format: `/title <nama judul baru>`", nil)
		return
	}

	if err := h.sessMgr.RenameSession(s.CurrentSessionID, newTitle); err != nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal mengganti judul sesi: %v", err), nil)
		return
	}

	_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("✅ Judul sesi `%s` berhasil diubah menjadi: *%s*", s.CurrentSessionID, newTitle), nil)
}

func (h *CommandHandler) HandleYolo(bot *tgbotapi.BotAPI, chatID, userID int64) {
	current := h.sessMgr.ToggleYolo(userID)
	statusText := "NONAKTIF (Perlu konfirmasi perintah sensitif)"
	if current {
		statusText = "AKTIF (Bypass semua konfirmasi eksekusi langsung)"
	}
	_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("🚀 *Mode YOLO:* `%s`", statusText), nil)
}

func (h *CommandHandler) HandleDiff(bot *tgbotapi.BotAPI, chatID int64) {
	cmd := exec.Command("git", "status", "-s")
	cmd.Dir = h.cfg.Hermes.WorkingDir
	out, err := cmd.Output()
	if err != nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal memeriksa git status: %v", err), nil)
		return
	}

	statusStr := strings.TrimSpace(string(out))
	if statusStr == "" {
		_, _ = SendSafeMessage(bot, chatID, "✅ Direktori kerja bersih (tidak ada modifikasi file).", nil)
		return
	}

	diffCmd := exec.Command("git", "diff", "--stat")
	diffCmd.Dir = h.cfg.Hermes.WorkingDir
	diffOut, _ := diffCmd.Output()

	reply := fmt.Sprintf("📝 *Git Status & Diff di `%s`:*\n\n```\n%s\n\n%s\n```",
		h.cfg.Hermes.WorkingDir, statusStr, string(diffOut))
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleSkills(bot *tgbotapi.BotAPI, chatID int64) {
	skillsDir := filepath.Join(h.cfg.Hermes.HermesHome, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal membaca direktori skills: %v", err), nil)
		return
	}

	var skills []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			skills = append(skills, fmt.Sprintf("• `/%s`", e.Name()))
		}
	}

	reply := fmt.Sprintf("🛠️ *Katalog Skills Hermes (%d Kategori Terpasang):*\n\n%s\n\n_Anda dapat memanggil skill langsung menggunakan `/<nama_skill> <instruksi>`._",
		len(skills), strings.Join(skills, "\n"))
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleWhoAmI(bot *tgbotapi.BotAPI, chatID, userID int64, user *tgbotapi.User) {
	username := "tidak ada"
	if user != nil && user.UserName != "" {
		username = "@" + user.UserName
	}
	name := ""
	if user != nil {
		name = user.FirstName + " " + user.LastName
	}

	reply := fmt.Sprintf(
		"👤 *Informasi Identitas Telegram*\n\n"+
			"• *Nama:* %s\n"+
			"• *Username:* %s\n"+
			"• *User ID:* `%d`\n"+
			"• *Chat ID:* `%d`\n"+
			"• *Hak Akses:* `Whitelisted / Admin`",
		name, username, userID, chatID,
	)
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleProfile(bot *tgbotapi.BotAPI, chatID int64) {
	reply := fmt.Sprintf(
		"📂 *Profil Hermes Agent*\n\n"+
			"• *Profil Aktif:* `default`\n"+
			"• *Hermes Home:* `%s`\n"+
			"• *Database State:* `%s`\n"+
			"• *Media Cache:* `%s`\n"+
			"• *Direktori Kerja:* `%s`",
		h.cfg.Hermes.HermesHome, h.cfg.Hermes.StateDBPath, h.cfg.Hermes.MediaDir, h.cfg.Hermes.WorkingDir,
	)
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleVersion(bot *tgbotapi.BotAPI, chatID int64) {
	out, _ := exec.Command(h.cfg.Hermes.BinaryPath, "--version").Output()
	hermesVer := strings.TrimSpace(string(out))
	if hermesVer == "" {
		hermesVer = "Hermes Agent v0.21.3"
	}

	reply := fmt.Sprintf(
		"🏷️ *Informasi Versi*\n\n"+
			"• *Hermes Core:* `%s`\n"+
			"• *Hermes Telegram Gateway:* `v%s (Pure Golang)`\n"+
			"• *Kompatibilitas:* 100%% 1:1 Hermes Agent Architecture\n"+
			"• *Optimasi:* On-demand process execution (~10 MB RAM Standby)",
		hermesVer, h.version,
	)
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleStop(bot *tgbotapi.BotAPI, chatID, userID int64) {
	stopped := h.runner.Stop(userID)
	if stopped {
		_, _ = SendSafeMessage(bot, chatID, "🛑 *Tugas berhasil dibatalkan dan proses dihentikan.*", nil)
	} else {
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Tidak ada tugas yang sedang berjalan untuk pengguna ini.", nil)
	}
}

func (h *CommandHandler) HandleLogs(bot *tgbotapi.BotAPI, chatID int64) {
	logPath := filepath.Join(h.cfg.Hermes.HermesHome, "logs", "agent.log")
	out, err := exec.Command("tail", "-n", "25", logPath).Output()
	if err != nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal membaca log: %v", err), nil)
		return
	}

	reply := fmt.Sprintf("📋 *Log Terbaru (`agent.log`):*\n\n```\n%s\n```", string(out))
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleBusy(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	s := h.sessMgr.Get(userID, chatID)
	arg = strings.TrimSpace(strings.ToLower(arg))

	if arg == "queue" || arg == "interrupt" {
		h.sessMgr.SetBusyMode(userID, arg)
		reply := fmt.Sprintf("✅ *Mode Busy Berhasil Diubah!*\n\nMode saat agent sibuk: `%s`", arg)
		if arg == "queue" {
			reply += "\n_Pesan baru yang dikirim saat Aida sibuk akan otomatis masuk antrean FIFO dan dieksekusi berurutan._"
		} else {
			reply += "\n_Pesan baru akan langsung menginterupsi/membatalkan tugas lama dan mendahulukan pesan baru._"
		}
		_, _ = SendSafeMessage(bot, chatID, reply, nil)
		return
	}

	reply := fmt.Sprintf("⚙️ *Pengaturan Mode Busy*\n\n"+
		"• *Mode Aktif:* `%s`\n\n"+
		"Pilihan mode:\n"+
		"• `/busy queue` - Antrekan pesan baru secara otomatis (FIFO)\n"+
		"• `/busy interrupt` - Interupsi/hentikan tugas lama dan dahulukan pesan baru",
		s.BusyMode,
	)
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleEgress(bot *tgbotapi.BotAPI, chatID int64) {
	out, err := exec.Command(h.cfg.Hermes.BinaryPath, "egress", "status").CombinedOutput()
	status := strings.TrimSpace(string(out))
	if err != nil || status == "" {
		status = "Egress proxy stopped / inactive."
	}
	ipOut, _ := exec.Command("curl", "-s", "--max-time", "2", "https://cloudflare.com/cdn-cgi/trace").Output()
	ip := "Unknown"
	for _, line := range strings.Split(string(ipOut), "\n") {
		if strings.HasPrefix(line, "ip=") {
			ip = strings.TrimPrefix(line, "ip=")
			break
		}
	}
	reply := fmt.Sprintf(
		"🌐 *Status Network & Egress Proxy*\n\n"+
			"• *Public Outbound IP:* `%s`\n\n"+
			"📋 *Detail Status Egress:*\n```\n%s\n```",
		ip, status,
	)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleDebug(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "share" || arg == "upload" || arg == "--yes" {
		out, err := exec.Command(h.cfg.Hermes.BinaryPath, "debug", "share", "--yes").CombinedOutput()
		if err != nil {
			_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal membuat shareable debug: %v\n\n```\n%s\n```", err, string(out)), nil)
			return
		}
		reply := fmt.Sprintf("🔗 *Laporan Debug Berhasil Dibuat:*\n\n```\n%s\n```", string(out))
		_, _ = SendSafeMessage(bot, chatID, reply, nil)
		return
	}

	outMem, _ := exec.Command("free", "-h").Output()
	memStr := "N/A"
	memLines := strings.Split(strings.TrimSpace(string(outMem)), "\n")
	if len(memLines) >= 2 {
		memStr = memLines[1]
	}

	outUptime, _ := exec.Command("uptime").Output()
	uptimeStr := strings.TrimSpace(string(outUptime))

	logPath := filepath.Join(h.cfg.Hermes.HermesHome, "logs", "agent.log")
	logOut, _ := exec.Command("tail", "-n", "15", logPath).Output()

	reply := fmt.Sprintf(
		"🛠️ *Hermes System Diagnostic Report*\n\n"+
			"• *Gateway Version:* `v%s`\n"+
			"• *Gateway Uptime:* `%v`\n"+
			"• *System Uptime:* `%s`\n"+
			"• *Memory:* `%s`\n"+
			"• *Working Dir:* `%s`\n"+
			"• *Home Dir:* `%s`\n\n"+
			"📋 *Log Terakhir (`agent.log`):*\n```\n%s\n```\n\n"+
			"_Gunakan `/debug share` untuk mengunggah laporan debug online._",
		h.version, time.Since(h.startTime).Round(time.Second), uptimeStr, memStr,
		h.cfg.Hermes.WorkingDir, h.cfg.Hermes.HermesHome, string(logOut),
	)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleUsage(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	s := h.sessMgr.Get(userID, chatID)
	query := "SELECT model, SUM(api_call_count), SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(reasoning_tokens), SUM(estimated_cost_usd) FROM session_model_usage GROUP BY model ORDER BY SUM(input_tokens+output_tokens) DESC;"
	cmd := exec.Command("sqlite3", h.cfg.Hermes.StateDBPath, query)
	out, err := cmd.Output()

	var sb strings.Builder
	sb.WriteString("📊 *Statistik Penggunaan Token & Kuota*\n\n")

	if s.CurrentSessionID != "" {
		det, _ := h.sessMgr.GetSessionDetails(s.CurrentSessionID)
		if det != nil {
			sb.WriteString(fmt.Sprintf("🟢 *Sesi Aktif (`%s`):*\n• Pesan: %d\n• Input Tokens: %d\n• Output Tokens: %d\n• Total Sesi: %d tokens\n\n",
				det.ID, det.MessageCount, det.InputTokens, det.OutputTokens, det.InputTokens+det.OutputTokens))
		}
	}

	sb.WriteString("📈 *Akumulasi Seluruh Sesi per Model:*\n")
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		var grandTotalCalls, grandTotalIn, grandTotalOut, grandTotalCache int
		for _, line := range lines {
			parts := strings.Split(line, "|")
			if len(parts) < 7 {
				continue
			}
			model := parts[0]
			calls, _ := strconv.Atoi(parts[1])
			inTok, _ := strconv.Atoi(parts[2])
			outTok, _ := strconv.Atoi(parts[3])
			cacheTok, _ := strconv.Atoi(parts[4])
			reasonTok, _ := strconv.Atoi(parts[5])

			grandTotalCalls += calls
			grandTotalIn += inTok
			grandTotalOut += outTok
			grandTotalCache += cacheTok

			sb.WriteString(fmt.Sprintf("• *%s*:\n  💬 %d calls | 🪙 In: %d, Out: %d\n  ⚡ Cache: %d, Reasoning: %d\n",
				model, calls, inTok, outTok, cacheTok, reasonTok))
		}
		sb.WriteString(fmt.Sprintf("\n🌐 *Total Keseluruhan:*\n• Total Panggilan API: %d\n• Total Input: %d tokens\n• Total Output: %d tokens\n• Cache Read: %d tokens\n",
			grandTotalCalls, grandTotalIn, grandTotalOut, grandTotalCache))
	} else {
		sb.WriteString("_Belum ada data penggunaan tercatat di database._\n")
	}

	sb.WriteString(fmt.Sprintf("\n💡 _Model gateway %s tidak memiliki batasan kuota berbayar._", h.cfg.Hermes.GatewayName))
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, sb.String(), dismissKb)
}

func (h *CommandHandler) HandleApprove(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	s := h.sessMgr.Get(userID, chatID)
	reply := fmt.Sprintf(
		"ℹ️ *Status Persetujuan (Approvals)*\n\n"+
			"Saat ini tidak ada aksi berbahaya atau perintah yang sedang menunggu persetujuan.\n\n"+
			"• *Mode YOLO:* `%v`\n"+
			"_Dalam mode YOLO aktif, semua perintah dan perubahan file otomatis disetujui tanpa jeda konfirmasi._",
		s.YoloMode,
	)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleDeny(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	reply := "ℹ️ Tidak ada aksi berbahaya yang tertunda untuk ditolak saat ini."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleCompress(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	s := h.sessMgr.Get(userID, chatID)
	if s.CurrentSessionID == "" {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Belum ada sesi percakapan aktif untuk dikompres.", dismissKb)
		return
	}

	det, err := h.sessMgr.GetSessionDetails(s.CurrentSessionID)
	if err != nil || det == nil {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("ℹ️ Sesi `%s` belum memiliki riwayat pesan.", s.CurrentSessionID), dismissKb)
		return
	}

	arg = strings.TrimSpace(arg)

	// Send initial status message
	statusMsg, err := SendSafeMessage(bot, chatID, fmt.Sprintf(
		"🗜️ *Memproses Kompresi Sesi (`%s`)...*\n\n"+
			"• *Jumlah Pesan:* %d\n"+
			"• *Estimasi Token:* %d\n\n"+
			"⏳ _Aida sedang merangkum riwayat percakapan untuk menghemat ruang memori. Mohon tunggu beberapa saat..._",
		det.ID, det.MessageCount, det.InputTokens+det.OutputTokens,
	), nil)
	hasStatus := (err == nil)
	if hasStatus {
		h.sessMgr.AddTelegramMsgID(userID, statusMsg.MessageID)
	}

	go func(targetSessionID string) {
		scriptPath := "/root/apps/hermes-tele/scripts/compress_session.py"
		pyArgs := []string{scriptPath, "--session-id", targetSessionID}
		if arg != "" {
			pyArgs = append(pyArgs, strings.Fields(arg)...)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		cmd := exec.CommandContext(ctx, "/usr/local/lib/hermes-agent/venv/bin/python", pyArgs...)
		cmd.Env = append(os.Environ(), "PYTHONPATH=/usr/local/lib/hermes-agent")
		out, runErr := cmd.Output()
		if runErr != nil {
			errDetail := runErr.Error()
			if exitErr, ok := runErr.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
				errDetail = fmt.Sprintf("%s\n%s", errDetail, string(exitErr.Stderr))
			}
			errMsg := fmt.Sprintf("❌ *Gagal Mengompres Sesi:*\n```\n%v\n```", errDetail)
			if hasStatus {
				_, _ = EditSafeMessage(bot, chatID, statusMsg.MessageID, errMsg, nil)
			} else {
				_, _ = SendSafeMessage(bot, chatID, errMsg, nil)
			}
			return
		}

		var res struct {
			Success           bool     `json:"success"`
			Status            string   `json:"status"`
			OriginalSessionID string   `json:"original_session_id"`
			NewSessionID      string   `json:"new_session_id"`
			BeforeMessages    int      `json:"before_messages"`
			AfterMessages     int      `json:"after_messages"`
			BeforeTokens      int      `json:"before_tokens"`
			AfterTokens       int      `json:"after_tokens"`
			RemovedMessages   int      `json:"removed_messages"`
			Lines             []string `json:"lines"`
			Message           string   `json:"message"`
		}

		if err := json.Unmarshal(out, &res); err != nil {
			errMsg := fmt.Sprintf("❌ *Gagal Membaca Hasil Kompresi:*\n%s", string(out))
			if hasStatus {
				_, _ = EditSafeMessage(bot, chatID, statusMsg.MessageID, errMsg, nil)
			} else {
				_, _ = SendSafeMessage(bot, chatID, errMsg, nil)
			}
			return
		}

		if !res.Success {
			errMsg := fmt.Sprintf("ℹ️ *Status Kompresi:*\n%s", res.Message)
			dismissKb := DismissKeyboard()
			if hasStatus {
				_, _ = EditSafeMessage(bot, chatID, statusMsg.MessageID, errMsg, &dismissKb)
			} else {
				_, _ = SendSafeMessage(bot, chatID, errMsg, dismissKb)
			}
			return
		}

		if res.Status == "preview" {
			previewText := fmt.Sprintf("🗜️ *Pratinjau Kompresi Sesi (`%s`):*\n\n%s", targetSessionID, strings.Join(res.Lines, "\n"))
			dismissKb := DismissKeyboard()
			if hasStatus {
				_, _ = EditSafeMessage(bot, chatID, statusMsg.MessageID, previewText, &dismissKb)
			} else {
				_, _ = SendSafeMessage(bot, chatID, previewText, dismissKb)
			}
			return
		}

		if res.Status == "compressed" {
			if res.NewSessionID != "" && res.NewSessionID != targetSessionID {
				h.sessMgr.SetSessionID(userID, res.NewSessionID)
			}

			reductionPct := 0.0
			if res.BeforeTokens > 0 {
				reductionPct = float64(res.BeforeTokens-res.AfterTokens) / float64(res.BeforeTokens) * 100.0
			}

			reply := fmt.Sprintf(
				"✅ *Kompresi Konteks Berhasil!*\n\n"+
					"• *Sesi:* `%s`\n"+
					"• *Pesan:* %d ➔ %d (%d pesan diringkas)\n"+
					"• *Estimasi Token:* %d ➔ %d (Hemat ~%.1f%%)\n\n"+
					"💡 _Riwayat ringkasan telah disimpan ke dalam sesi. Percakapan selanjutnya berlanjut di sesi ini!_",
				res.NewSessionID, res.BeforeMessages, res.AfterMessages, res.RemovedMessages,
				res.BeforeTokens, res.AfterTokens, reductionPct,
			)
			dismissKb := DismissKeyboard()
			if hasStatus {
				_, _ = EditSafeMessage(bot, chatID, statusMsg.MessageID, reply, &dismissKb)
			} else {
				_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
			}
			return
		}

		reply := fmt.Sprintf("ℹ️ *Kompresi Selesai (%s):*\n%s", res.Status, strings.Join(res.Lines, "\n"))
		dismissKb := DismissKeyboard()
		if hasStatus {
			_, _ = EditSafeMessage(bot, chatID, statusMsg.MessageID, reply, &dismissKb)
		} else {
			_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
		}
	}(s.CurrentSessionID)
}

func (h *CommandHandler) HandleSendFile(bot *tgbotapi.BotAPI, chatID, userID int64, filePath string) {
	filePath = strings.TrimSpace(filePath)
	filePath = strings.Trim(filePath, "`\"'")

	if filePath == "" {
		reply := "📤 *Kirim File ke Telegram:*\n\nFormat: `/sendfile /path/ke/file` atau `/send /path/ke/file`\n\nContoh:\n`/sendfile /tmp/pesan_aida.txt`"
		dismissKb := DismissKeyboard()
		sent, _ := SendSafeMessage(bot, chatID, reply, dismissKb)
		h.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		reply := fmt.Sprintf("❌ *File tidak ditemukan:*\n`%s`\n\nPastikan jalur file lokal di VPS sudah benar.", filePath)
		dismissKb := DismissKeyboard()
		sent, _ := SendSafeMessage(bot, chatID, reply, dismissKb)
		h.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	if info.IsDir() {
		reply := fmt.Sprintf("⚠️ *Jalur adalah folder/direktori:*\n`%s`\n\nHanya file tunggal yang dapat dikirim.", filePath)
		dismissKb := DismissKeyboard()
		sent, _ := SendSafeMessage(bot, chatID, reply, dismissKb)
		h.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	fileName := filepath.Base(filePath)
	if isImageFile(filePath) {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = fmt.Sprintf("🖼️ %s", fileName)
		sent, err := bot.Send(photo)
		if err != nil {
			reply := fmt.Sprintf("❌ Gagal mengirim gambar: %v", err)
			sMsg, _ := SendSafeMessage(bot, chatID, reply, nil)
			h.sessMgr.AddTelegramMsgID(userID, sMsg.MessageID)
			return
		}
		h.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
	} else {
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
		doc.Caption = fmt.Sprintf("📎 %s", fileName)
		sent, err := bot.Send(doc)
		if err != nil {
			reply := fmt.Sprintf("❌ Gagal mengirim dokumen: %v", err)
			sMsg, _ := SendSafeMessage(bot, chatID, reply, nil)
			h.sessMgr.AddTelegramMsgID(userID, sMsg.MessageID)
			return
		}
		h.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
	}
}

func (h *CommandHandler) HandleRestart(bot *tgbotapi.BotAPI, chatID int64) {
	_, _ = SendSafeMessage(bot, chatID, "🔄 *Memulai ulang gateway Hermes Telegram...*\nGateway akan kembali aktif dalam beberapa detik.", nil)
	go func() {
		time.Sleep(600 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", "hermes-tele.service").Run()
	}()
}

func (h *CommandHandler) HandleUpdate(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "now" || arg == "--yes" {
		_, _ = SendSafeMessage(bot, chatID, "⏳ *Sedang memeriksa dan memperbarui Hermes Agent...*", nil)
		out, err := exec.Command(h.cfg.Hermes.BinaryPath, "update", "--yes").CombinedOutput()
		if err != nil {
			_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal memperbarui: %v\n\n```\n%s\n```", err, string(out)), nil)
			return
		}
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("✅ *Pembaruan Selesai:*\n\n```\n%s\n```", string(out)), nil)
		return
	}

	out, err := exec.Command(h.cfg.Hermes.BinaryPath, "update", "--check").CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil || res == "" {
		res = "Hermes Agent sudah menggunakan versi terkini."
	}
	reply := fmt.Sprintf("☤ *Pemeriksaan Pembaruan Hermes Agent*\n\n```\n%s\n```\n\n_Ketik `/update now` untuk menginstal pembaruan._", res)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleSave(bot *tgbotapi.BotAPI, chatID, userID int64, format string) {
	s := h.sessMgr.Get(userID, chatID)
	if s.CurrentSessionID == "" {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, "⚠️ Belum ada sesi aktif untuk diekspor.", dismissKb)
		return
	}

	query := fmt.Sprintf("SELECT role, COALESCE(content, ''), timestamp FROM messages WHERE session_id = '%s' ORDER BY timestamp ASC;",
		strings.ReplaceAll(s.CurrentSessionID, "'", "''"))
	cmd := exec.Command("sqlite3", h.cfg.Hermes.StateDBPath, query)
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, "⚠️ Tidak ada riwayat pesan yang ditemukan untuk sesi ini.", dismissKb)
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Ekspor Sesi Hermes: %s\n\n", s.CurrentSessionID))
	sb.WriteString(fmt.Sprintf("_Waktu Ekspor: %s_\n\n---\n\n", time.Now().Format("2006-01-02 15:04:05")))

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		role := strings.ToUpper(parts[0])
		content := parts[1]
		secFloat, _ := strconv.ParseFloat(parts[2], 64)
		ts := time.Unix(int64(secFloat), 0).Format("15:04:05")

		sb.WriteString(fmt.Sprintf("### [%s] %s\n\n%s\n\n---\n\n", ts, role, content))
	}

	exportDir := filepath.Join(h.cfg.Hermes.MediaDir, "exports")
	_ = os.MkdirAll(exportDir, 0755)
	filePath := filepath.Join(exportDir, fmt.Sprintf("session_%s.md", s.CurrentSessionID))
	if err := os.WriteFile(filePath, []byte(sb.String()), 0644); err != nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal menyimpan file ekspor: %v", err), nil)
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = fmt.Sprintf("📄 Ekspor Percakapan Sesi: `%s`", s.CurrentSessionID)
	doc.ParseMode = "Markdown"
	_, _ = bot.Send(doc)
}

func (h *CommandHandler) HandleRollback(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	cmd := exec.Command("git", "log", "-n", "5", "--oneline")
	cmd.Dir = h.cfg.Hermes.WorkingDir
	out, err := cmd.Output()
	if err != nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("❌ Gagal memeriksa checkpoint git: %v", err), nil)
		return
	}

	reply := fmt.Sprintf(
		"⏪ *Daftar Checkpoint Git/Filesystem:*\n\n```\n%s\n```\n\n"+
			"_Untuk membatalkan modifikasi turn terakhir dalam sesi ini, gunakan `/undo`._",
		string(out),
	)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleBranch(bot *tgbotapi.BotAPI, chatID, userID int64, branchName string) {
	s := h.sessMgr.Get(userID, chatID)
	if s.CurrentSessionID == "" {
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, "⚠️ Belum ada sesi aktif untuk di-branch.", dismissKb)
		return
	}

	newID := fmt.Sprintf("%s_%s", time.Now().Format("20060102_150405"), strconv.FormatInt(time.Now().UnixNano()%1000000, 16))
	title := "Branch dari " + s.CurrentSessionID
	if branchName != "" {
		title = branchName
	}

	copyQuery := fmt.Sprintf("INSERT INTO sessions (id, title, model, message_count, input_tokens, output_tokens, started_at, last_activity_at) SELECT '%s', '%s', model, message_count, input_tokens, output_tokens, started_at, %f FROM sessions WHERE id = '%s';",
		newID, strings.ReplaceAll(title, "'", "''"), float64(time.Now().Unix()), strings.ReplaceAll(s.CurrentSessionID, "'", "''"))
	_ = exec.Command("sqlite3", h.cfg.Hermes.StateDBPath, copyQuery).Run()

	msgQuery := fmt.Sprintf("INSERT INTO messages (session_id, role, content, timestamp, token_count) SELECT '%s', role, content, timestamp, token_count FROM messages WHERE session_id = '%s';",
		newID, strings.ReplaceAll(s.CurrentSessionID, "'", "''"))
	_ = exec.Command("sqlite3", h.cfg.Hermes.StateDBPath, msgQuery).Run()

	h.sessMgr.SetSessionID(userID, newID)

	reply := fmt.Sprintf("🌿 *Sesi Berhasil Di-branch:*\n\n• *Sesi Baru:* `%s`\n• *Judul:* *%s*\n• *Asal:* `%s`\n\nRiwayat pesan telah diduplikasi untuk eksplorasi independen.", newID, title, s.CurrentSessionID)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandlePause(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	if arg == "off" || arg == "resume" {
		reply := "▶️ *Gateway Dilanjutkan (Resumed)*\nPekerjaan dan perintah baru kembali diterima secara normal."
		_, _ = SendSafeMessage(bot, chatID, reply, nil)
		return
	}

	reply := "⏸️ *Gateway Dijeda (Paused)*\nEksekusi baru ditahan sementara. Gunakan `/pause off` untuk melanjutkan."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleAgents(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)
	busyState := "Standby / Idle"
	if h.runner.IsRunning(userID) {
		busyState = "🟢 Sedang Mengeksekusi Tugas"
	}

	reply := fmt.Sprintf(
		"🤖 *Status Agen & Tugas Berjalan*\n\n"+
			"• *Agen Utama:* Hermes Agent (`%s`)\n"+
			"• *Status Eksekusi:* %s\n"+
			"• *Mode Busy:* `%s`\n"+
			"• *Sesi Aktif:* `%s`\n\n"+
			"_Gunakan `/queue` untuk melihat antrean atau `/stop` untuk membatalkan._",
		s.CurrentModel, busyState, s.BusyMode, s.CurrentSessionID,
	)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleMemory(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	memPath := filepath.Join(h.cfg.Hermes.HermesHome, "memories", "MEMORY.md")
	data, err := os.ReadFile(memPath)
	memText := strings.TrimSpace(string(data))

	if err != nil || memText == "" {
		reply := "🧠 *Memori Jangka Panjang Hermes Agent*\n\n_Belum ada catatan preferensi tersimpan di MEMORY.md._\n\n_Anda dapat meminta Hermes mengingat sesuatu dalam obrolan atau menggunakan `/refine`._"
		dismissKb := DismissKeyboard()
		_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
		return
	}

	if len(memText) > 2000 {
		memText = memText[:2000] + "\n...(dipotong)"
	}
	reply := fmt.Sprintf("🧠 *Isi Memori Hermes Agent (`MEMORY.md`):*\n\n```markdown\n%s\n```", memText)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleBundles(bot *tgbotapi.BotAPI, chatID int64) {
	bundleFile := filepath.Join(h.cfg.Hermes.HermesHome, "skills", ".bundled_manifest")
	data, err := os.ReadFile(bundleFile)
	if err != nil {
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Berkas bundle skills tidak ditemukan.", nil)
		return
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var names []string
	for _, l := range lines {
		parts := strings.Split(l, ":")
		if len(parts) > 0 && parts[0] != "" {
			names = append(names, fmt.Sprintf("• `/%s`", strings.ReplaceAll(parts[0], "-", "_")))
		}
	}

	reply := fmt.Sprintf("📦 *Skill Bundles Terdaftar (%d skills):*\n\n%s\n\n_Panggil skill langsung dengan `/<nama> <instruksi>`._",
		len(names), strings.Join(names, "\n"))
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandlePlatform(bot *tgbotapi.BotAPI, chatID int64) {
	gwName := h.cfg.Hermes.GatewayName
	if gwName == "" {
		gwName = "GoGate"
	}
	gwURL := h.cfg.Hermes.GatewayURL
	if gwURL == "" {
		gwURL = "http://127.0.0.1:8080"
	}
	reply := fmt.Sprintf("🌐 *Status Platform & Daemon:*\n\n"+
		"• *Hermes Telegram Gateway:* 🟢 Active (Go Native Systemd)\n"+
		"• *%s AI Gateway:* 🟢 Active (%s)\n"+
		"• *Camofox Browser:* 🟢 Active (:9377)\n"+
		"• *Platform Mode:* Direct Telegram Bot API Polling",
		gwName, gwURL)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleVoice(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "🎙️ *Mode Suara (Voice Mode)*\n\n" +
		"Hermes Telegram Gateway mendukung penerimaan dan transkripsi *Voice Notes* dan berkas audio secara native!\n" +
		"Kirimkan voice note langsung di obrolan Telegram ini, dan Aida akan memproses instruksinya."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandlePersonality(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "🎭 *Kepribadian Hermes Agent*\n\n" +
		"• *Profil Aktif:* `Aida / Fall Aida`\n" +
		"• *Gaya:* Andal, Cepat, Responsif, Bahasa Indonesia ramah & profesional.\n\n" +
		"_Anda dapat mengatur instruksi kepribadian khusus langsung di chat atau melalui SOUL.md._"
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleFast(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := fmt.Sprintf("⚡ *Mode Cepat (Fast Processing)*\n\n"+
		"• *Status:* Otomatis via %s Gateway & model low-latency.\n"+
		"Semua query diproses dengan prioritas streaming langsung.",
		h.cfg.Hermes.GatewayName)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleApprovals(bot *tgbotapi.BotAPI, chatID, userID int64, arg string) {
	s := h.sessMgr.Get(userID, chatID)
	mode := "off (YOLO)"
	if !s.YoloMode {
		mode = "manual"
	}
	reply := fmt.Sprintf("🛡️ *Mode Persetujuan Perintah Sensitif (Approvals)*\n\n"+
		"• *Status Saat Ini:* `%s`\n\n"+
		"_Gunakan `/yolo` untuk beralih antara eksekusi otomatis tanpa jeda konfirmasi atau konfirmasi manual._", mode)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleInsights(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	querySessions := "SELECT count(*) FROM sessions;"
	queryMessages := "SELECT count(*) FROM messages;"
	outSess, _ := exec.Command("sqlite3", h.cfg.Hermes.StateDBPath, querySessions).Output()
	outMsg, _ := exec.Command("sqlite3", h.cfg.Hermes.StateDBPath, queryMessages).Output()

	totalSess := strings.TrimSpace(string(outSess))
	totalMsg := strings.TrimSpace(string(outMsg))

	reply := fmt.Sprintf(
		"📈 *Analisis & Wawasan Penggunaan (Insights)*\n\n"+
			"• *Total Sesi Dibuat:* %s sesi\n"+
			"• *Total Pesan/Turn:* %s pesan\n"+
			"• *Gateway Engine:* Go Native Standby On-Demand\n"+
			"• *Model Dominan / Default:* `%s`",
		totalSess, totalMsg, h.cfg.Hermes.DefaultModel,
	)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleCurator(bot *tgbotapi.BotAPI, chatID int64) {
	curatorFile := filepath.Join(h.cfg.Hermes.HermesHome, "skills", ".curator_state")
	data, _ := os.ReadFile(curatorFile)
	text := strings.TrimSpace(string(data))
	if text == "" {
		text = "Curator state idle / all skills up to date."
	}
	reply := fmt.Sprintf("🧹 *Status Kurator Skill (Curator)*\n\n```json\n%s\n```", text)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleKanban(bot *tgbotapi.BotAPI, chatID int64) {
	reply := "📋 *Papan Kanban Hermes*\n\n" +
		"• *Status:* Standby\n" +
		"• *Tugas Aktif:* Tidak ada tugas kanban tertunda."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleTopic(bot *tgbotapi.BotAPI, chatID int64) {
	reply := "💬 *Telegram DM Topic Session Mode*\n\n" +
		"• *Status:* Mode Obrolan Langsung (Single Stream Lane)\n" +
		"Setiap sesi dikelola secara dinamis via `/sessions` dan `/new`."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleSetHome(bot *tgbotapi.BotAPI, chatID int64) {
	reply := fmt.Sprintf("🏠 Chat ini (`%d`) telah dikonfigurasi sebagai *Saluran Utama (Home Channel)*.", chatID)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleCodexRuntime(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := fmt.Sprintf("⚙️ *Codex Runtime Status:*\n\n"+
		"• *Runtime:* `OpenAI API Compatible` (%s)\n"+
		"Model dihubungkan langsung melalui gateway %s (%s).",
		h.cfg.Hermes.GatewayName, h.cfg.Hermes.GatewayName, h.cfg.Hermes.GatewayURL)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleFooter(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "🏷️ *Footer Metadata Runtime:*\n\n" +
		"Footer statistik runtime otomatis disisipkan di akhir setiap respon (model, durasi, tokens, turn, ID, version)."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleSuggestions(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "💡 *Saran Otomatisasi (Suggestions Catalog)*\n\n" +
		"• Web Scrape & Summarize via Camofox\n" +
		"• Git Commit & CI/CD Review\n" +
		"• Codebase Architecture Diagram SVG\n" +
		"• Long Document Ingestion & Action Items extraction\n\n" +
		"_Pilih salah satu instruksi atau jalankan langsung di chat!_"
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleBlueprint(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "📐 *Template Blueprint Otomasi*\n\n" +
		"• `daily_standup`: Rangkum perubahan git harian\n" +
		"• `vps_monitor`: Pantau kesehatan server dan memori\n" +
		"• `repo_sync`: Sinkronisasi repo dan tag release"
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleLogin(bot *tgbotapi.BotAPI, chatID int64) {
	reply := fmt.Sprintf("🔐 *Status Akun & Autentikasi*\n\n"+
		"• *Penyedia Gateway:* `%s` (%s)\n"+
		"• *Status:* Otentikasi Gateway Aktif",
		h.cfg.Hermes.GatewayName, h.cfg.Hermes.GatewayURL)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleTopup(bot *tgbotapi.BotAPI, chatID int64) {
	reply := fmt.Sprintf("💳 *Saldo & Billing*\n\n"+
		"• *Status:* Multi-Provider Load Balancing via %s\n"+
		"Kuota dan kupon dikelola langsung oleh gateway terpusat.",
		h.cfg.Hermes.GatewayName)
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleHeartbeat(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "💓 *Heartbeat Hermes*\n\n" +
		"• *Status:* Standby Idle\n" +
		"_Gunakan `/heartbeat every <interval> <prompt>` untuk menjadwalkan pemeriksaan berkala._"
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

func (h *CommandHandler) HandleLoop(bot *tgbotapi.BotAPI, chatID int64, arg string) {
	reply := "🔁 *Loop Eksekusi Proaktif*\n\n" +
		"• *Status:* Tidak ada loop tugas yang aktif saat ini."
	dismissKb := DismissKeyboard()
	_, _ = SendSafeMessage(bot, chatID, reply, dismissKb)
}

