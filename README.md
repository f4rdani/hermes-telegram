<div align="center">

# 🪽 hermes-telegram

**Ultra-lightweight, native Go Telegram Gateway & On-Demand Bridge for Hermes Agent CLI**

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS-blue?style=flat)](https://github.com)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Memory Footprint](https://img.shields.io/badge/Idle%20RAM-%E2%89%A4%2010MB-success?style=flat)](#-performance--resource-budget)

*Interact with and control your local or remote Hermes AI Agent directly from Telegram with real-time tool activity streaming, Camoufox anti-detect browser transparency, multi-turn session persistence, and zero chat clutter.*

---

</div>

## 📑 Daftar Isi / Table of Contents
- [Kenapa hermes-telegram?](#-kenapa-hermes-telegram)
- [Fitur Utama (Key Features)](#-fitur-utama-key-features)
- [Arsitektur Sistem](#-arsitektur-sistem)
- [Prasyarat (Prerequisites)](#-prasyarat-prerequisites)
- [Panduan Setup & Instalasi Detail](#-panduan-setup--instalasi-detail)
  - [1. Clone Repository](#1-clone-repository)
  - [2. Siapkan File Konfigurasi](#2-siapkan-file-konfigurasi)
  - [3. Build Binary](#3-build-binary)
  - [4. Jalankan Secara Manual (Testing)](#4-jalankan-secara-manual-testing)
  - [5. Setup Systemd Service (Production 24/7)](#5-setup-systemd-service-production-247)
- [Daftar Perintah (Commands Reference)](#-daftar-perintah-commands-reference)
- [Struktur Konfigurasi (`config.json`)](#-struktur-konfigurasi-configjson)
- [Integrasi Browser Camoufox](#-integrasi-browser-camoufox)
- [Troubleshooting & FAQ](#-troubleshooting--faq)
- [Lisensi](#-lisensi)

---

## 💡 Kenapa hermes-telegram?

Menjalankan agen otonom seperti **Hermes Agent** biasanya membutuhkan terminal aktif atau sesi SSH interaktif. `hermes-telegram` hadir sebagai jembatan *on-demand* berbasis Go yang ringan dan tangguh:

- **Ultra Ringan**: Ditulis murni dengan Go (`go-telegram-bot-api`), konsumsi memori idle sangat minim (≤ 10 MB RAM), tanpa runtime berat seperti Node.js atau Python wrapper tambahan.
- **Transparansi Eksekusi Tool**: Setiap kali Hermes menjalankan bash command, membaca browser Camoufox, atau mengekstrak konten, aktivitasnya di-stream secara real-time ke Telegram.
- **Zero Chat Clutter**: Pesan status berpikir diperbarui langsung di tempat (*in-place message editing*), dan ringkasan aktivitas tool dibersihkan saat respons final selesai.
- **Aman**: Hanya Telegram User ID yang terdaftar dalam daftar izin (*whitelist*) yang dapat berinteraksi dengan bot.

---

## ✨ Fitur Utama (Key Features)

1. **Real-Time Tool Activity Streaming**
   - Mendeteksi dan menampilkan tool yang sedang dieksekusi oleh Hermes secara live:
     - 🌐 **Camoufox Web Browser**: `Membuka Web`, `Membaca Tampilan Web (Camoufox)`, `Navigasi Browser`, `Snapshot Browser`.
     - 💻 **Terminal / Bash**: Menampilkan potongan perintah bash beserta durasi eksekusinya.
     - 📄 **Web Extractor & Vision**: Menampilkan progres ekstraksi konten teks dan pemrosesan visual.
2. **Detail Metrik Turn Stats Lengkap**
   - Setiap respons final dilengkapi footer metrik transparan:
     ```text
     (⏱ 25.6s • 🪙 211841 tokens • 🔄 Turn 20 • 🆔 1e0df6 • 🏷️ v2.0.2)
     ```
     Menampilkan durasi eksekusi, token terpakai, giliran percakapan (*turn*), ID sesi, dan versi rilis.
3. **Smart Session & `/retry` Fallback Engine**
   - Jika model AI mengalami rate limit (429) atau timeout saat giliran tertentu, perintah `/retry` dapat langsung dikirim untuk mencoba ulang giliran terakhir secara otomatis tanpa mereset atau menghilangkan riwayat percakapan sebelumnya.
   - Perintah `/new` mereset konteks percakapan untuk memulai topik baru.
4. **Multimodal Media Ingestion**
   - Mendukung pengiriman foto dan gambar dari Telegram. Gambar otomatis disimpan ke direktori kerja sementara (`/tmp/hermes_images/`) dan diteruskan ke Hermes untuk dianalisis dengan konteks penglihatan (*vision*).
5. **Session Persistence**
   - Status sesi setiap pengguna disimpan secara persisten di file JSON lokal (`sessions.json`), memastikan sesi percakapan tidak hilang saat bot di-restart.

---

## 🏗️ Arsitektur Sistem

```text
┌─────────────────┐       Telegram Bot API       ┌────────────────────────┐
│  Telegram User  │ ◄──────────────────────────► │     hermes-telegram    │
└─────────────────┘                              │  (Go Native Gateway)   │
                                                 └──────────┬─────────────┘
                                                            │
                                         Exec On-Demand     │ JSON / ANSI Stream
                                         (Session Context)  │ Process Group
                                                            ▼
                                                 ┌────────────────────────┐
                                                 │    Hermes Agent CLI    │
                                                 │  (/usr/local/bin/...)  │
                                                 └──────────┬─────────────┘
                                                            │
                                              ┌─────────────┴─────────────┐
                                              ▼                           ▼
                                      ┌──────────────┐            ┌──────────────┐
                                      │ Bash / Tools │            │ Camoufox MCP │
                                      └──────────────┘            └──────────────┘
```

---

## 📋 Prasyarat (Prerequisites)

Sebelum melakukan instalasi, pastikan sistem Anda telah memiliki:
1. **Linux OS** (Ubuntu / Debian / CentOS / Arch Linux)
2. **Go (Golang)** versi 1.22 atau lebih baru
3. **Hermes Agent CLI** terpasang dan dapat dijalankan (misal di `/usr/local/bin/hermes` atau tersimpan di `PATH`)
4. **Telegram Bot Token** didapatkan dari [@BotFather](https://t.me/BotFather)
5. **Telegram User ID** Anda (dapat diperoleh melalui bot seperti [@userinfobot](https://t.me/userinfobot))

---

## 🚀 Panduan Setup & Instalasi Detail

### 1. Clone Repository

Pilih direktori tempat Anda ingin memasang bot (contoh: `/root/apps/hermes-tele`):

```bash
mkdir -p /root/apps
cd /root/apps
git clone https://github.com/f4rdani/hermes-telegram.git hermes-tele
cd hermes-tele
```

### 2. Siapkan File Konfigurasi

Salin template konfigurasi `config.example.json` menjadi `config.json`:

```bash
cp config.example.json config.json
```

Edit file `config.json` menggunakan text editor kesukaan Anda (`nano`, `vim`, atau `micro`):

```json
{
  "telegram": {
    "bot_token": "1234567890:ABCdefGhIJKlmNoPQRsTUVwxyZ",
    "allowed_user_ids": [
      709560759
    ]
  },
  "hermes": {
    "binary_path": "/usr/local/bin/hermes",
    "default_model": "9router",
    "working_dir": "/root"
  }
}
```

> [!WARNING]
> Jangan pernah membagikan atau meng-commit file `config.json` ke repository publik karena berisi token bot Telegram Anda. File ini sudah diatur agar diabaikan oleh `.gitignore`.

### 3. Build Binary

Anda dapat mengompilasi binary menggunakan `make` atau perintah `go build` langsung:

**Menggunakan Make:**
```bash
make build
```

**Atau menggunakan Go langsung:**
```bash
go build -ldflags="-s -w" -o hermes-tele ./cmd/hermes-tele
```

Binary mandiri bernama `hermes-tele` akan terbentuk di folder tersebut.

### 4. Jalankan Secara Manual (Testing)

Jalankan bot untuk memastikan konfigurasi dan koneksi ke Telegram berfungsi normal:

```bash
./hermes-tele -config config.json
```

Output log akan muncul seperti ini:
```text
2026/09/17 15:30:36 [hermes-tele] Starting Hermes Telegram Gateway v2.0.2 (Golang Native 1:1 Engine)
2026/09/17 15:30:36 [hermes-tele] Bot terautentikasi sebagai @NamaBotAnda
2026/09/17 15:30:36 [hermes-tele] Siap menerima pesan Telegram...
```

Kirim pesan `/start` atau pesan teks biasa ke bot Telegram Anda untuk menguji balasan. Tekan `Ctrl + C` untuk menghentikan pengujian manual.

---

### 5. Setup Systemd Service (Production 24/7)

Agar bot berjalan otomatis di latar belakang dan selalu menyala kembali jika server reboot, pasang unit systemd:

1. Salin template service ke direktori systemd:
   ```bash
   cp service/hermes-tele.service /etc/systemd/system/hermes-tele.service
   ```

2. Pastikan path di dalam `/etc/systemd/system/hermes-tele.service` sesuai dengan lokasi direktori Anda:
   ```ini
   [Unit]
   Description=Hermes Telegram On-Demand Bridge (Native Go)
   After=network.target

   [Service]
   Type=simple
   User=root
   WorkingDirectory=/root/apps/hermes-tele
   ExecStart=/root/apps/hermes-tele/hermes-tele -config /root/apps/hermes-tele/config.json
   Restart=always
   RestartSec=5
   LimitNOFILE=65535

   [Install]
   WantedBy=multi-user.target
   ```

3. Reload systemd, aktifkan service saat boot, dan jalankan:
   ```bash
   systemctl daemon-reload
   systemctl enable hermes-tele
   systemctl start hermes-tele
   ```

4. Cek status service untuk memastikan bot aktif:
   ```bash
   systemctl status hermes-tele
   ```

5. Memantau log bot secara live:
   ```bash
   journalctl -u hermes-tele -f
   ```

---

## 🎮 Daftar Perintah (Commands Reference)

| Perintah | Deskripsi |
| :--- | :--- |
| `/start` | Menampilkan pesan selamat datang dan status kesiapan sistem. |
| `/help` | Menampilkan panduan bantuan dan daftar perintah yang tersedia. |
| `/new` | Mereset sesi percakapan aktif dan memulai konteks baru di Hermes Agent. |
| `/retry` | Mencoba ulang giliran terakhir secara instan tanpa menghapus riwayat sesi sebelumnya (sangat berguna jika kena rate limit 429 atau koneksi provider terputus). |
| `/status` | Menampilkan informasi sesi saat ini (Turn count, Session ID, Engine model). |

---

## ⚙️ Struktur Konfigurasi (`config.json`)

| Kunci Konfigurasi | Tipe | Wajib | Deskripsi |
| :--- | :--- | :--- | :--- |
| `telegram.bot_token` | `string` | **Ya** | Token Bot API dari Telegram [@BotFather](https://t.me/BotFather). |
| `telegram.allowed_user_ids` | `array[int]` | **Ya** | Daftar ID Telegram pengguna yang diizinkan mengakses bot. |
| `hermes.binary_path` | `string` | Tidak | Path absolut binary Hermes Agent CLI (default: `/usr/local/bin/hermes`). |
| `hermes.default_model` | `string` | Tidak | Nama model default yang digunakan oleh Hermes CLI. |
| `hermes.working_dir` | `string` | Tidak | Direktori kerja (*working directory*) saat proses Hermes dieksekusi. |

---

## 🌐 Integrasi Browser Camoufox

Hermes Agent dilengkapi kemampuan memanggil browser anti-deteksi **Camoufox** (Firefox-based anti-bot bypass). Saat Hermes menggunakan Camoufox untuk membuka halaman yang diproteksi Cloudflare Turnstile / WAF, `hermes-telegram` secara transparan memetakan event internal menjadi indikator visual real-time di Telegram:

- `camoufox_navigate` ➔ `🌐 Membuka Web (Camoufox): <url>`
- `camoufox_screenshot` / `camoufox_snapshot` ➔ `📸 Membaca Tampilan Web (Camoufox)`
- `fetch_web_content` ➔ `📄 Ekstrak Konten Web`
- `execute_bash` ➔ `💻 Menjalankan Terminal: <command>`

Ketika jawaban selesai disusun, header aktivitas tool otomatis dirapikan sehingga riwayat chat Telegram Anda tetap bersih dan nyaman dibaca.

---

## ❓ Troubleshooting & FAQ

### 1. Bot tidak merespons pesan sama sekali?
- Pastikan User ID Telegram Anda sudah dimasukkan ke dalam daftar `allowed_user_ids` pada `config.json`.
- Periksa log melalui `journalctl -u hermes-tele -f` untuk melihat pesan akses yang ditolak (*unauthorized*).

### 2. Provider LLM mengembalikan error 429 (Rate Limit)?
- Kirim perintah `/retry` ke bot Telegram. Bot akan membatalkan status turn terakhir yang gagal dan mengirimkan ulang prompt sebelumnya ke Hermes.

### 3. Ingin memperbarui kode ke versi terbaru?
```bash
cd /root/apps/hermes-tele
git pull origin main
make build
systemctl restart hermes-tele
```

---

## 📄 Lisensi

Proyek ini dirilis di bawah lisensi [MIT License](LICENSE).
