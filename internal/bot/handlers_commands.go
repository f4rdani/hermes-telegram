package bot

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"hermes-tele/config"
	"hermes-tele/internal/engine"
	"hermes-tele/internal/session"
)

type CommandHandler struct {
	cfg        *config.Config
	sessMgr    *session.Manager
	runner     *engine.Runner
	version    string
	startTime  time.Time
}

func NewCommandHandler(cfg *config.Config, sessMgr *session.Manager, runner *engine.Runner, version string) *CommandHandler {
	return &CommandHandler{
		cfg:       cfg,
		sessMgr:   sessMgr,
		runner:    runner,
		version:   version,
		startTime: time.Now(),
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
			"⚡ *Model:* `%s` via 9router\n"+
			"🧠 *Reasoning:* `%s` | *YOLO:* `%v`\n"+
			"📂 *Workspace:* `%s`\n"+
			"🔖 *Sesi Aktif:* %s\n\n"+
			"💡 *Perintah Utama:*\n"+
			"• `/status` - Cek penggunaan RAM, koneksi 9router & Camofox\n"+
			"• `/new` - Mulai sesi percakapan baru yang segar\n"+
			"• `/sessions` - Lihat & pilih daftar sesi sebelumnya\n"+
			"• `/model` - Pilih / ganti model kecerdasan buatan\n"+
			"• `/context` - Cek kapasitas token jendela konteks\n"+
			"• `/commands` - Jelajahi 100+ perintah & skills interaktif\n"+
			"• `/help` - Bantuan lengkap seluruh perintah\n\n"+
			"Kirim pesan teks, instruksi, foto, voice note, atau dokumen untuk memulai!",
		s.CurrentModel, s.ReasoningEffort, s.YoloMode, h.cfg.Hermes.WorkingDir, sessionInfo,
	)

	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleHelp(bot *tgbotapi.BotAPI, chatID, userID int64, query string) {
	query = strings.TrimSpace(strings.ToLower(query))

	if query == "" {
		reply := "📖 *Panduan Perintah Hermes Telegram*\n\n" +
			"🔹 *Manajemen Sesi:*\n" +
			"• `/new` - Mulai sesi baru (fresh session ID + history)\n" +
			"• `/sessions` - Telusuri & resume sesi-sesi sebelumnya\n" +
			"• `/resume <id>` - Lanjutkan sesi tertentu berdasarkan ID\n" +
			"• `/title <nama>` - Beri judul untuk sesi aktif saat ini\n" +
			"• `/clear` - Hapus histori sesi aktif\n\n" +
			"🔹 *Konfigurasi & Model:*\n" +
			"• `/model [nama]` - Ganti model AI (atau klik menu interaktif)\n" +
			"• `/reasoning [level]` - Atur effort penalaran (none/low/med/high)\n" +
			"• `/yolo` - Toggle mode YOLO (auto-approve aksi sensitif)\n" +
			"• `/stop` - Hentikan tugas yang sedang berjalan\n\n" +
			"🔹 *Informasi & Diagnostik:*\n" +
			"• `/status` - Status RAM, CPU, 9router, Camofox & sesi\n" +
			"• `/context` - Grafik visual pemakaian token jendela konteks\n" +
			"• `/diff` - Cek perubahan git di direktori kerja\n" +
			"• `/whoami` - Info otorisasi akun Telegram Anda\n" +
			"• `/profile` - Info direktori dan konfigurasi profil\n" +
			"• `/version` - Versi Hermes & Go Gateway\n\n" +
			"🔹 *Skills & Ekstensi:*\n" +
			"• `/skills` - Daftar skill yang terpasang\n" +
			"• `/reload_skills` - Muat ulang skill dari disk\n" +
			"• `/reload_mcp` - Muat ulang konfigurasi MCP server\n" +
			"• `/commands` - Menu interaktif seluruh 100+ perintah"

		_, _ = SendSafeMessage(bot, chatID, reply, nil)
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

	// 1. RAM info
	ramInfo := "Tidak tersedia"
	out, err := exec.Command("free", "-h").Output()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) >= 3 {
			ramInfo = fmt.Sprintf("Mem: %s\n  Swap: %s", lines[1], lines[2])
		} else if len(lines) >= 2 {
			ramInfo = lines[1]
		}
	}

	// 2. 9router ping
	routerStatus := "🟢 Online (localhost:20128)"
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:20128/v1/models")
	if err != nil || (resp != nil && resp.StatusCode != http.StatusOK) {
		routerStatus = "🔴 Offline / Bermasalah"
	}
	if resp != nil {
		_ = resp.Body.Close()
	}

	// 3. Camofox ping
	camofoxStatus := "💤 Idle / Sleep (Auto-wake on demand :9377)"
	respCam, errCam := client.Get("http://localhost:9377")
	if errCam == nil && respCam != nil {
		camofoxStatus = "🟢 Active (:9377)"
		_ = respCam.Body.Close()
	}

	// 4. Session tokens
	sessionDetails := "Belum ada riwayat"
	if s.CurrentSessionID != "" {
		det, err := h.sessMgr.GetSessionDetails(s.CurrentSessionID)
		if err == nil && det != nil {
			sessionDetails = fmt.Sprintf("ID: `%s`\nPesan: %d | Tokens: In %d / Out %d",
				det.ID, det.MessageCount, det.InputTokens, det.OutputTokens)
		} else {
			sessionDetails = fmt.Sprintf("ID: `%s`", s.CurrentSessionID)
		}
	}

	uptime := time.Since(h.startTime).Round(time.Second)

	reply := fmt.Sprintf(
		"📊 *Status Sistem Hermes Agent Gateway*\n\n"+
			"🖥️ *Server & Memori:*\n```\n%s\n```\n"+
			"🤖 *Hermes Gateway:* `v%s (Go Native)`\n"+
			"⏱️ *Gateway Uptime:* `%v`\n"+
			"🧠 *Model Default:* `%s`\n"+
			"⚡ *Effort Reasoning:* `%s`\n"+
			"🚀 *Mode YOLO:* `%v`\n\n"+
			"🔌 *Integrasi Layanan:*\n"+
			"• *9router:* %s\n"+
			"• *Camofox Browser:* %s\n\n"+
			"🔖 *Sesi Aktif Pengguna:*\n%s",
		ramInfo, h.version, uptime, s.CurrentModel, s.ReasoningEffort, s.YoloMode,
		routerStatus, camofoxStatus, sessionDetails,
	)

	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleContext(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)
	if s.CurrentSessionID == "" {
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Belum ada sesi percakapan aktif. Kirim pesan untuk membuat sesi baru.", nil)
		return
	}

	det, err := h.sessMgr.GetSessionDetails(s.CurrentSessionID)
	if err != nil || det == nil {
		_, _ = SendSafeMessage(bot, chatID, fmt.Sprintf("ℹ️ Sesi `%s` belum memiliki statistik token di database.", s.CurrentSessionID), nil)
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

	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleModel(bot *tgbotapi.BotAPI, chatID, userID int64, modelArg string) {
	s := h.sessMgr.Get(userID, chatID)
	modelArg = strings.TrimSpace(modelArg)

	if modelArg == "" {
		reply := fmt.Sprintf("🤖 *Pengaturan Model AI*\n\nModel aktif saat ini: `%s`\n\nPilih salah satu model di bawah atau ketik `/model <nama_model>`:", s.CurrentModel)
		kb := ModelKeyboard(s.CurrentModel)
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
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
}

func (h *CommandHandler) HandleSessions(bot *tgbotapi.BotAPI, chatID, userID int64) {
	s := h.sessMgr.Get(userID, chatID)
	sessions, err := h.sessMgr.ListHermesSessions(8)
	if err != nil || len(sessions) == 0 {
		_, _ = SendSafeMessage(bot, chatID, "ℹ️ Belum ada sesi yang tersimpan dalam database.", nil)
		return
	}

	var sb strings.Builder
	sb.WriteString("📚 *Daftar Sesi Hermes Terbaru:*\n\n")
	for i, sess := range sessions {
		mark := "▫️"
		if sess.ID == s.CurrentSessionID {
			mark = "⭐️ *(Aktif)*"
		}
		sb.WriteString(fmt.Sprintf("%d. %s `%s`\n   📌 *%s*\n   💬 %d pesan | ⏱ %s\n\n",
			i+1, mark, sess.ID, sess.Title, sess.MessageCount, sess.LastActivity.Format("15:04 02/01"),
		))
	}
	sb.WriteString("Klik salah satu tombol di bawah untuk langsung beralih ke sesi tersebut:")

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
	_, _ = SendSafeMessage(bot, chatID, reply, nil)
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
