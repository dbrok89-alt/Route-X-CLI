# Changelog

## v0.3.2 — 2026-11-10 (perbaikan model placeholder)

Bug yang ditemukan saat verifikasi binary v0.3.1: config dengan
`model: "local"` (mudah ditulis tangan) dikirim ke API Intern
apa adanya sebagai nama model. Sekarang nilai placeholder
(kosong, `"local"`, atau default generik `gemini-2.0-flash`)
dipetakan ke model default provider saat dipakai — untuk intern
jadi `Atria-Dawn`. Ada test regresi untuk kasus `"local"`.

## v0.3.1 — 2026-11-10 (provider Intern + --help lengkap)

- **Provider `intern` baru:** Shanghai AI Lab discovery API
  (`https://discovery-api.intern-ai.org.cn/v1`), diverifikasi
  langsung hidup: tanpa kunci, `GET /v1/models` menjawab 401
  `invalid_api_key` dengan envelope error ala OpenAI — jadi
  endpoint ini memang OpenAI-compatible. Kunci lewat
  `INTERN_API_KEY` (dibuat di konsol Intern; ada token gratis
  bulanan selama preview). Model default `Atria-Dawn` (model agen,
  konteks 512K, teks saja); model lain seperti `Intern-S2` bisa
  dipilih dengan `--model`. Host yang sama tanpa `/v1` melayani
  klien Anthropic-compatible (`--provider anthropic --base-url
  https://discovery-api.intern-ai.org.cn`).
- **Perbaikan `--help`:** v0.3.0 sudah punya subcommand
  serve/share/cron, tapi `--help` binary rilisnya tidak
  menyebutnya, dan daftar slash command-nya ketinggalan
  `/compact`, `/plugins`, `/checkpoint`, `/search`, dan `/lint`.
  Perintahnya sendiri sudah berfungsi sejak v0.3.0 — hanya
  bantuannya yang belum lengkap. Sekarang `--help` menyebut
  ketiganya plus daftar slash yang sinkron dengan `/help`,
  dengan test regresi yang gagal kalau salah satunya hilang lagi.
- **Dites:** provider intern dites lewat mock server (SSE +
  agentic loop), default model + base URL dites, model eksplisit
  dan base URL kustom tidak ditimpa. Tes live belum bisa dari
  mesin ini: `GET /v1/models` hidup (401 envelope OpenAI), tapi
  `POST /v1/chat/completions` diputus di edge (EOF, terjadi juga
  lewat curl mentah — bukan bug Route-X). Bos perlu coba sendiri
  dengan `INTERN_API_KEY` asli dari jaringan sendiri.

## v0.3.0 — 2026-11-10 (agentic loop + OpenCode/ngrok/Hermes)

Menutup jurang terbesar v0.2.1: model kini benar-benar menggerakkan
tool, bukan sekadar membalas teks sekali jalan. Semua slice dites
ketat (unit + race + mock server + smoke + mutation check).

- **Agentic loop (inti):** paket baru `internal/loop` — model
  memanggil tool, hasil tool dikembalikan ke model, ulang sampai
  jawaban akhir atau batas 10 langkah. Error tool dikembalikan ke
  model supaya bisa coba cara lain. Dites dengan provider scripted
  (tugas 2 langkah baca→tulis di file asli) dan end-to-end dengan
  mock server OpenAI (3 panggilan: read → write → jawaban).
- **Tool calling 3 provider:** `CompleteTools` untuk OpenAI
  (tools/tool_calls), Anthropic (tools/input_schema, tool_use/
  tool_result), Gemini (functionDeclarations/functionCall).
  Masing-masing dites dengan mock server yang memeriksa request
  benar-benar mendeklarasikan tool dan parsing-nya benar.
- **Streaming:** `StreamTools` untuk OpenAI dan Anthropic (SSE):
  teks mengalir token demi token, tool call yang terpotong
  dirakit ulang dari fragment (dites dengan chunk yang sengaja
  dipotong di tengah string JSON). Gemini tetap non-streaming.
- **Tool `edit` baru:** ganti satu potong teks (bukan tulis ulang
  file), menolak teks ambigu (>1 kemunculan) dan teks yang tidak
  ditemukan, fallback whitespace-tolerant. Dites termasuk mode plan.
- **Executor tetap patuh sandbox/approval:** perintah berbahaya
  `go vet ./...; rm -rf /` diblokir di loop (dites), tulis di
  sandbox read-only ditolak (dites). Mutation check membuktikan
  test keamanan gagal kalau exact-match dikembalikan ke substring.
- **`/compact`:** ringkas sesi jadi satu checkpoint (tujuan,
  keputusan, perubahan, yang masih terbuka); menolak sesi <4 pesan.
- **Pelacak token/biaya:** `/usage` kini menampilkan token run
  terakhir + total sesi + estimasi biaya USD dari harga list
  (model tak dikenal dilaporkan "unknown", bukan harga karangan).
- **Subagent persisten:** subagent selesai disimpan ke
  `~/.route-x/subagents/`, `/agents` menampilkannya setelah restart.
- **Plugin:** `.route-x/plugins/*.md` dimuat sebagai perintah
  tambahan dan ikut dibaca model lewat konteks workspace.
- **`serve`/`share`:** server HTTP loopback dengan bearer token
  (`/health` terbuka, `/prompt` + `/sessions` wajib token);
  `share` menjalankan tunnel ngrok (butuh akun), cloudflared, atau
  localhost.run. Bind non-loopback ditolak tanpa flag eksplisit;
  token kosong ditolak. Dites dengan httptest + smoke curl nyata.
- **Cron:** `route-x cron add/list/run/remove`, parser cron
  5-field (*, angka, range, list, step), tugas yang sudah jalan
  di menit yang sama tidak diulang. Route-X bukan daemon —
  `cron run` harus dipanggil scheduler Anda sendiri.
- **Checkpoint:** snapshot workspace otomatis sebelum tulis/edit
  pertama; `/checkpoint list` dan `/checkpoint restore <id>`.
- **Pencarian sesi:** `/search <kata>` mencari di sesi tersimpan.
- Yang masih jujur belum ada: TUI penuh, sandbox OS-level, LSP,
  MCP server, tes live Anthropic/OpenAI/Gemini (butuh API key).

## v0.2.1 — 2026-11-10 (audit ketat, tanpa data palsu)

Audit ulang v0.2.0 menemukan dan memperbaiki kelemahan nyata,
bukan sekadar polesan luar:

- **Lubang keamanan di allow list:** `IsAllowed` memakai pencocokan
  substring, sehingga `go vet ./...; rm -rf /` ikut auto-approve
  karena mengandung entri allow. Sekarang pencocokan harus persis
  (exact match), dengan test mutasi yang membuktikan test-nya gagal
  kalau lubang itu dikembalikan.
- **AnthropicProvider tidak bisa dites:** endpoint di-hardcode ke
  `api.anthropic.com`, jadi satu-satunya test adalah "kunci kosong
  = error". Sekarang ada `BaseURL` (default endpoint asli) dan test
  mock yang memeriksa header `x-api-key`, `anthropic-version`,
  serta isi body (model + pesan). Test OpenAI diperketat sama:
  memeriksa header `Authorization` dan body, bukan cuma balasan.
- **`/review` tanpa test:** sekarang dites dengan repo git asli
  (init, commit, ubah file berisi TODO, lalu `Review()`).
- **Bug config pointer jadi test regresi:** `/sandbox` dan `/mode`
  yang tidak terlihat di `/permissions` dan `/usage` kini punya
  test di `main_test.go`.
- **Release tidak konsisten dengan CI:** release hanya membangun
  4 binary, CI membangun 5 — `route-x-linux-arm64` kini ikut
  di-release.
- **Test baru lain:** `config.Load` mereset sandbox/approval tidak
  valid ke default; error JSON-RPC MCP (`CallTool` ke tool yang
  tidak ada) harus muncul sebagai error, bukan balasan kosong.
- Anthropic tetap belum dites live — butuh `ANTHROPIC_API_KEY`.

## v0.2.0 — 2026-11-10 (gabungan Antigravity + Codex + Claude + Hermes)

Semua slice A–D dikerjakan di atas v0.1.1 (bukan menimpa WIP mentah),
sehingga 12 perbaikan v0.1.1 tetap ada. WIP yang gagal kompilasi
diperbaiki: import tak terpakai dihapus/diisi, hooks dan MCP di-wire
ke `main.go`, dan provider baru di-wire ke seleksi provider.

- **Slice A — sandbox + approval (Codex):** config `sandbox`
  (`read-only | workspace-write | danger-full-access`) dan `approval`
  (`on-request | never`) terpisah dari `mode`; daftar allow/deny;
  penegakan di `agent.CanWrite`/`CanRun`; perintah baru `/sandbox`,
  `/approval`, `/permissions`; flag `--sandbox`, `--approval`,
  `--base-url`; `AGENTS.md` dan `CLAUDE.md` dibaca sebagai instruksi
  proyek, `route-x init` membuat draf `AGENTS.md` bila belum ada.
- **Slice B — provider (Hermes):** provider `openai`, `anthropic`,
  dan `ollama` (endpoint OpenAI-compatible, default Ollama lokal);
  kunci dibaca dari env per provider (`OPENAI_API_KEY`,
  `ANTHROPIC_API_KEY`, `OLLAMA_API_KEY`, `GEMINI_API_KEY`).
  Anthropic ditandai belum dites live — butuh API key dan tes live.
- **Slice C — hooks + MCP (Claude):** lifecycle hooks `SessionStart`,
  `PreToolUse`, `PostToolUse`, `PostEdit`, `Stop` dari
  `.route-x/hooks.json` (tidak jalan di mode plan); client MCP stdio
  minimal (initialize, `tools/list`, `tools/call`) dari
  `.route-x/mcp.json`; perintah `/hooks`, `/mcp`, `/mcp-call`.
- **Slice D — memory (Hermes):** memory persisten terbatas
  (`~/.route-x/memory/MEMORY.md`, maks 4000 karakter) dengan
  `/remember`, `/memory`, pencarian, dan `/learn` untuk menyimpan
  tugas berulang sebagai skill; `/context` menampilkan pemakaian
  konteks sesi; `/review` (git diff + TODO/FIXME) dan `/verify`
  (`go vet` + `go test` dengan bukti); flag `-z` (one-shot, alias `-p`).
- **Tidak ikut:** TUI penuh, MCP server, sandbox OS-level, scheduled
  cron, messaging gateway — di luar scope v0.2.0, dicatat di README.

## v0.1.1 — 2026-11-10 (rilis stabilitas)

Perbaikan bug yang ditemukan lewat audit dan pengujian silang:

- Diff sekarang berbasis LCS (urut dan duplikat dipertahankan); diff lama
  berbasis set diam-diam menghilangkan baris yang dipindah/duplikat.
- `WriteFile` menolak menulis ke luar folder proyek (path absolut di
  luar proyek dan `..`); path absolut di dalam proyek tetap boleh.
- `RunCommand` menolak lebih banyak perintah berbahaya (`rm -rf`,
  `git push --force`, `> /dev/`, `chmod 777 /`) dan perintah kosong.
- `session.Load` menolak id berisi traversal (`../`, `/`, spasi).
- `/goal` dan `/plan` sekarang mengembalikan approval seperti semula;
  sebelumnya `/goal` membuat semua perintah berikutnya auto-approve,
  dan `/plan` membuat semua perintah berikutnya selalu ditolak.
- `/mode` sekarang benar-benar mengganti approval gate; sebelumnya
  ganti mode di REPL tidak berpengaruh sampai restart.
- REPL dan prompt approval sekarang berbagi satu pembaca stdin;
  sebelumnya Scanner REPL bisa menelan jawaban `y` sehingga
  approval hilang diam-diam.
- `route-x -p "init"` tidak lagi membuat file skill; `init` kedua
  tidak lagi menimpa skill yang sudah diubah.
- Kunci API Gemini dikirim lewat header `x-goog-api-key`, bukan
  lagi di URL (tidak bocor ke log/history).
- `make test` tidak lagi menyembunyikan kegagalan test (`|| true`).
- Tes unit ditambahkan (tools, config, session, skills, agent);
  `go test -race` lolos; build multi-OS (linux/windows/darwin,
  amd64/arm64) lolos.
- CI diperketat: gofmt, vet, test -race, cross-compile, dan satu
  job gerbang `ci-gate`. Branch `main` dilindungi: merge hanya
  jika `ci-gate` hijau dan setelah verifikasi manual yang ketat.

## v0.1.0 — 2026-11-10

- Route-X pertama: CLI coding agent Go, single binary, stdlib only.
- REPL interaktif, slash command, mode default / accept-edits / auto / plan.
- Diff sebelum tulis file, subagent latar belakang, skills, session resume.
- Print mode `-p` dengan output text / json / stream-json.
- Provider local (offline) dan Gemini opsional.
- Repo dijadikan publik, CI/CD GitHub Actions diaktifkan.
