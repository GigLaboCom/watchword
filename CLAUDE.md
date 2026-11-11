# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Watchword — an MCP server in Go that stores prompts/data keyed by human-readable words (code words). Supports SQLite and PostgreSQL backends, background expiration, bearer token auth, S3 presigned file upload/download, and 10 MCP tools over stdio/SSE/Streamable HTTP.

## Build & Test

```bash
go build ./...          # Build all packages
go test ./...           # Run all tests
go build -o watchword ./cmd/server   # Build binary
docker compose up -d    # Run with PostgreSQL + Streamable HTTP (via supergateway)
```

## Architecture

- `cmd/server/main.go` — Entry point, config loading, DI wiring, graceful shutdown
- `internal/domain/` — Entry struct, validation, sentinel errors
- `internal/config/` — YAML + env var config loading
- `internal/repository/` — Repository interface + SQLite/PostgreSQL implementations
- `internal/service/` — Business logic (collision resolution, store, restore, search, file ops)
- `internal/s3/` — S3 client (aws-sdk-go-v2): Presigner interface (presigned URLs, delete) + Streamer interface (GetObject/PutObject for the proxy)
- `internal/proxy/` — HMAC-signed proxy endpoints `/dl` (download) and `/ul` (upload) that stream file bytes between client and S3
- `internal/auth/` — Bearer token + JWT/JWKS validation, RFC 9728 Protected Resource Metadata, WWW-Authenticate headers
- `internal/mcp/` — MCP server setup + tool handlers (text + file)
- `internal/worker/` — Background expiration goroutine
- `migrations/` — Embedded SQL migrations for SQLite and PostgreSQL

## Key Design Decisions

- **WithTx on Repository interface** — enables transactional collision resolution from service layer
- **SQLite timestamps as RFC3339 strings** — consistent, parseable, sortable
- **Domain errors → tool result errors** (not transport errors) — MCP client gets structured messages
- **Collision resolution** — base word, then word2..word999, all within a transaction
- **Auth per-transport** — stdio validates WORDSTORE_AUTH_TOKEN once at startup; SSE uses HTTP middleware
- **S3 presigned URLs + optional proxy** — the `upload_file`/`download_file` MCP tools only return URLs, never file bytes. They always include presigned S3 PUT/GET URLs. When `s3.proxy` is configured they also include HMAC-signed `proxy_upload_url`/`proxy_url`, and then the server **does** carry file data: `GET /dl` streams S3 GetObject → client, `PUT /ul` streams client → S3 PutObject. Both must stay streaming (`io.Copy`, no `io.ReadAll`/whole-file buffers); the container runs with a 128Mi memory limit. `/dl` records completed downloads in `download_history` in the background with a detached context (`context.WithoutCancel` + timeout), because the request context is cancelled once the handler returns. File tools are registered only when S3 is configured
- **Compact list/search responses** — `list_entries`, `search_entries`, `search_words` omit payload to reduce token usage; `get_entry`/`get_entry_by_word` return full content
- **delete_entry accepts UUID or word** — tries UUID parse first, falls back to word lookup
- **entry_type column** — `text` (default) or `file`; file entries store JSON metadata in payload field
- **Streamable HTTP served natively** — the `http` transport serves both `/mcp` and `/sse` on one port; no supergateway sidecar is involved anymore.

## Commit Guidelines

- Never add Claude as a co-author in commit messages (no `Co-Authored-By: Claude` lines).

## Dependency Updates (Renovate)

`renovate.json` drives the Renovate GitHub App (Dependency Dashboard issue in this repo):
- Runs Mondays before 9am Europe/Berlin, max 5 open PRs; vulnerability fixes (GitHub + OSV) open immediately, labelled `security`.
- Covers go.mod (`gomodTidy` keeps go.sum in sync), Dockerfile + docker-compose images, GitHub Actions, and the staticcheck version in `ci.yml` (via the `# renovate:` comment).
- Groups: "Go modules (non-major)", "GitHub Actions", and "Go toolchain" (go directive + `golang` builder image move together).
- Major updates wait for approval on the Dependency Dashboard. Dockerfile base images are pinned to digests.
- Automerge only for minor/patch GitHub Actions and patch updates of direct Go modules, after CI is green. Never for Docker images, the toolchain, or majors.
- Merging Renovate PRs does not release anything; releases still need a `v*` tag.

## MCP Library

Uses `github.com/mark3labs/mcp-go` for MCP protocol support (stdio + SSE transports).

## Docker

The Dockerfile is a two-stage build:
1. `golang:1.24-alpine` builder — cross-compiles a static `CGO_ENABLED=0` binary for `$TARGETOS/$TARGETARCH`
2. Final `alpine:3.21` image — `/usr/local/bin/watchword` + `config.yaml` at `/etc/watchword/config.yaml`

`.github/workflows/release.yml` builds and pushes multi-arch (`linux/amd64`, `linux/arm64`)
images to `ghcr.io/giglabo/watchword` on every `v*` tag, tagged via `docker/metadata-action`
with `{version}`, `{major}.{minor}`, `{major}`, `latest`, and `sha-<commit>`.
Keep the README's Container image tag table in sync when those tag rules change.

`docker-compose.yml` runs PostgreSQL + watchword with Streamable HTTP on port 8001.
`docker-compose.postgres.yml` runs PostgreSQL only (for local stdio development on port 5434).
