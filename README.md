# Route-X

[![CI](https://github.com/dbrok89-alt/Route-X-CLI/actions/workflows/ci.yml/badge.svg)](https://github.com/dbrok89-alt/Route-X-CLI/actions/workflows/ci.yml)

Route-X adalah CLI coding agent ala **Antigravity CLI (`agy`)** dari Google — ditulis ulang dari nol dalam **Go, single binary, stdlib only** (tanpa dependensi eksternal).

v0.2.0 menggabungkan empat keluarga CLI: Antigravity (mode + subagent), Codex (sandbox, approval, `AGENTS.md`, `/review`), Claude Code (hooks, MCP, permissions, `/context`), dan Hermes (multi-provider, memory persisten, skill belajar, `/verify`).

v0.2.1 adalah audit ketat atas v0.2.0: allow list kini exact-match (bukan substring), provider OpenAI/Anthropic bisa dites dengan mock, dan `/review` dites dengan repo git asli.

v0.3.0 menutup jurang terbesar v0.2.1: **agentic loop sungguhan**. Model kini memanggil tool (read/search/list/write/edit/run), membaca hasilnya, dan mengulang sampai tugas selesai atau batas 10 langkah, untuk provider Gemini, OpenAI, Anthropic, dan Ollama — plus streaming token, `/compact`, pelacak token/biaya, subagent persisten, plugin, `serve`/`share` (ngrok/cloudflared/localhost.run), cron, checkpoint, dan pencarian sesi.

v0.3.1 adalah rilis stabil terbaru — menambah provider `intern` (Shanghai AI Lab discovery API, model default Atria-Dawn) dan memperbaiki `--help` yang di v0.3.0 belum menyebut subcommand serve/share/cron. Lihat `CHANGELOG.md`.

v0.3.1 melengkapi `--help`: subcommand `serve`/`share`/`cron` dan slash command baru kini tercantum, dengan test regresi.

Bukan tiruan resmi Google, dan bukan `agy` itu sendiri. Route-X meniru arsitektur dan cara pakainya:

- satu binary Go, jalan di terminal, bekerja langsung di folder proyek
- mode eksekusi: `default` (tanya dulu sebelum edit/jalankan), `accept-edits`, `auto`, `plan` (tidak menulis apa-apa)
- review edit sebagai **diff** sebelum file disentuh
- subagent latar belakang yang jalan paralel (`/agent`, `/agents`)
- skills dari file Markdown di `.route-x/skills/`
- session tersimpan dan bisa di-resume (`/resume`)
- print mode `-p` untuk script dan CI, output `text` / `json` / `stream-json`

## Yang tidak sama dengan `agy` asli

`agy` asli memakai model Gemini/Claude/GPT lewat akun Google dan sandbox terminal. Route-X:

- **Provider default adalah `local`** — offline, tanpa API key, planner deterministik berbasis kata kunci + tools nyata (baca, cari, tulis, jalankan). Cocok untuk demo dan belajar arsitektur agent.
- **Provider `gemini` opsional** — kalau `GEMINI_API_KEY` diset, Route-X memanggil Gemini REST API (`generativelanguage.googleapis.com`). Model default `gemini-2.0-flash`, bisa diubah dengan `--model` atau `/model`. Nama model bisa berubah mengikuti katalog Google, cek `/usage` dan dokumentasi Google.
- **Provider lain (v0.2.0)** — `openai` (`OPENAI_API_KEY`, endpoint bisa diubah lewat `--base-url`), `ollama` (endpoint OpenAI-compatible lokal, default `http://localhost:11434/v1`), dan `anthropic` (`ANTHROPIC_API_KEY`, belum dites live — lihat `/usage`).
- **Provider `intern` (v0.3.1)** — Shanghai AI Lab, discovery API yang OpenAI-compatible (`https://discovery-api.intern-ai.org.cn/v1`, kunci lewat `INTERN_API_KEY`). Model default `Atria-Dawn` (model agen, konteks 512K, teks saja). Endpoint yang sama tanpa `/v1` melayani klien Anthropic-compatible, jadi `--provider anthropic --base-url https://discovery-api.intern-ai.org.cn` juga bisa dipakai. Kunci API dibuat di konsol Intern (daftar, ada token gratis bulanan selama preview) — jangan taruh kunci di config, pakai env var.
- Tidak ada TUI penuh (Bubble Tea), tidak ada MCP server, tidak ada sandbox OS-level. Perintah berbahaya seperti `rm -rf /` ditolak mentah-mentah, dan perintah shell tetap lewat approval sesuai mode. Sandbox v0.2.0 adalah penegakan kebijakan di level aplikasi (bukan Seatbelt/Landlock kernel), jadi jangan jadikan satu-satunya pertahanan di mesin yang tidak tepercaya.
- `serve`/`share`: `route-x serve --port 8080` membuka HTTP lokal (loopback saja) dengan bearer token yang dibuat baru tiap jalan; `route-x share` menambahkan tunnel (ngrok butuh akun + `NGROK_AUTHTOKEN`, cloudflared dan localhost.run tidak). Siapa pun yang memegang URL publik **dan** token bisa meminta agen membaca file dan menjalankan perintah — token jangan dibagikan sembarangan.
- Cron: `route-x cron add "pagi" "0 9 * * *" "cari TODO"` lalu jalankan `route-x cron run` dari scheduler Anda sendiri — Route-X bukan daemon, jadi ia tidak jalan sendiri di latar belakang.

## Install / build

Butuh Go 1.23+.

```bash
cd route-x
go build -o route-x .
./route-x --version
# atau
make build
sudo make install   # menyalin ke /usr/local/bin
```

## Pakai

```bash
# interaktif
./route-x

# print mode (untuk script / CI)
./route-x -p "cari TODO"
./route-x -p --output json "baca main.go"
./route-x --mode plan -p "buat file demo.go"

# contoh tugas di REPL
route-x> baca main.go
route-x> cari TODO
route-x> buat file hello.go
route-x> jalankan go vet ./...
route-x> /goal perbaiki semua TODO di main.go
route-x> /agent cari FIXME
route-x> /agents
```

## Slash command

| Perintah | Fungsi |
|---|---|
| `/help` | daftar perintah |
| `/model [nama]` | lihat / ganti model |
| `/mode [nama]` | lihat / ganti mode |
| `/provider [nama]` | `local`, `gemini`, `openai`, `anthropic`, atau `ollama` |
| `/sandbox [nama]` | `read-only`, `workspace-write`, atau `danger-full-access` |
| `/approval [nama]` | `on-request` atau `never` |
| `/permissions` | sandbox, approval, daftar allow/deny |
| `/review` | review git diff + TODO/FIXME |
| `/verify` | jalankan `go vet` + `go test`, laporkan bukti |
| `/context` | pemakaian konteks sesi |
| `/memory` | lihat memory persisten |
| `/remember <fakta>` | simpan fakta ke memory persisten |
| `/learn <nama> [desc]` | simpan tugas berulang jadi skill |
| `/hooks` | daftar lifecycle hooks |
| `/mcp` | daftar server MCP dan tools-nya |
| `/mcp-call <srv> <tool> [json]` | panggil satu MCP tool |
| `/goal <tugas>` | jalankan autonomus, auto-approve edit |
| `/plan <tugas>` | rencana saja, tidak menulis |
| `/agent <tugas>` | kirim subagent latar belakang |
| `/agents` | daftar subagent |
| `/usage` | model, mode, provider, kuota |
| `/skills`, `/lint` | daftar skill, jalankan skill lint |
| `/config` | lihat config (API key disamarkan) |
| `/resume [id]` | daftar / lanjutkan sesi tersimpan |
| `/diff` | diff terakhir |
| `/clear`, `/exit` | bersihkan layar, keluar |

## Config

Config tersimpan di `~/.route-x/config.json`:

```json
{
  "model": "gemini-2.0-flash",
  "mode": "default",
  "provider": "local"
}
```

API key **sebaiknya lewat environment**, bukan config:

```bash
export GEMINI_API_KEY="..."
./route-x --provider gemini -p "jelaskan struktur proyek ini"
```

Sesi tersimpan di `~/.route-x/sessions/<id>.json`.

## Skills

```bash
./route-x init   # membuat .route-x/skills/lint.md contoh
./route-x
route-x> /skills
route-x> /lint
```

Skill adalah file Markdown biasa. Tambah file baru di `.route-x/skills/nama.md`, dan skill itu langsung terdaftar.

## Struktur proyek

```
route-x/
├── main.go                  # flag, print mode, REPL, slash command
├── internal/
│   ├── agent/agent.go       # provider local/gemini/openai/anthropic/ollama, dispatch tools, subagent, /review, /verify
│   ├── config/config.go     # ~/.route-x/config.json
│   ├── hooks/hooks.go       # lifecycle hooks SessionStart..Stop
│   ├── mcp/mcp.go           # client MCP stdio (tools/list, tools/call)
│   ├── memory/memory.go     # memory persisten + /learn
│   ├── session/session.go   # simpan / resume sesi (JSON)
│   ├── skills/skills.go     # load .route-x/skills/*.md
│   └── tools/tools.go       # read, search, write+diff, run, approval
├── Makefile
└── README.md
```

## Keamanan

- Mode `default` selalu minta persetujuan `y/N` sebelum menulis file atau menjalankan shell.
- Mode `plan` tidak pernah menulis atau menjalankan apa-apa.
- Perintah berbahaya ditolak sebelum approval.
- Jangan jalankan `auto` di repo yang belum di-commit — selalu cek diff dulu (`/diff`).

## CI/CD

Repo ini publik, dengan GitHub Actions di `.github/workflows/`:

- **CI** (`ci.yml`) — jalan setiap push ke `main` dan setiap pull request: `gofmt`, `go vet`, `go test -race`, `go build`, smoke test (`--version`, `-p "cari TODO"`, `-p --output json`, dan `--mode plan` yang harus tidak membuat file), plus cross-compile Linux/Windows/macOS (amd64 + arm64). Tidak butuh secret atau API key.
- **Aturan merge** — branch `main` dilindungi: check `ci-gate` wajib hijau (strict, branch harus up-to-date). CI merah berarti merge dilarang. CI hijau pun belum cukup: merge hanya setelah verifikasi manual yang ketat — baca diff PR dan cek hasil run di tab Actions. Admin tidak dikecualikan dari aturan ini.
- **Release** (`release.yml`) — jalan hanya saat ada tag `vX.Y.Z`, contoh:
  ```bash
  git tag v0.1.0
  git push origin v0.1.0
  ```
  Workflow ini mem-build 5 binary (Linux amd64+arm64, Windows amd64, macOS amd64+arm64), lalu membuat GitHub Release otomatis dengan binary itu sebagai lampiran. Memakai `GITHUB_TOKEN` bawaan Actions, tidak butuh secret tambahan.

Status CI terbaru bisa dilihat di tab **Actions** di GitHub.

## Lisensi

Contoh belajar, bebas dipakai dan diubah. `agy` / Antigravity adalah merek Google.
