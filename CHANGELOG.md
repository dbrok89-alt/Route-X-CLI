# Changelog

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
