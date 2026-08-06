# CLIProxyAPI Custom Fork

This is a custom fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) with additional features for enhanced auth management, quota handling, and OpenAI-compatible provider support.

## Fork Features

### 1. Client Header Forwarding to OpenAI-Compatible Upstreams

**Commit:** `eacbba4e` — `feat(executor): forward client headers to OpenAI-compatible upstreams`

Inbound client request headers are forwarded to OpenAI-compatible upstream providers, in addition to any headers configured in `config.yaml`. Hop-by-hop, auth, and transport headers are stripped.

**Config:**
```yaml
openai-compatibility:
  - name: "my-provider"
    base-url: "https://api.example.com/v1"
    # Client headers are forwarded automatically. Headers set here or by the
    # executor always win over client headers.
    headers:
      X-Custom-Header: "custom-value"
```

**Stripped headers:** `Authorization`, `Cookie`, `Content-Type`, `Content-Length`, `Host`, `Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`

**Files changed:**
- `internal/runtime/executor/openai_compat_executor.go`
- `internal/util/header_helpers.go`
- `internal/util/header_helpers_test.go`
- `internal/runtime/executor/openai_compat_executor_headers_test.go`

---

### 2. Antigravity Quota & Auth Management

**Commit:** `25489532` — `feat(antigravity): classify plain-text 429 quota, auto-disable deleted accounts, add skip_models`

#### 2a. Plain-Text 429 Classification

Antigravity returns plain-text 429 responses like `Individual quota reached` instead of JSON. These are now correctly classified as quota exhaustion (not soft retries), triggering proper cooldown and fallback.

**Files changed:**
- `internal/runtime/executor/antigravity_executor_credits.go`
- `internal/runtime/executor/antigravity_executor_credits_test.go`

#### 2b. Auto-Disable Deleted Accounts

When Google's OAuth token endpoint returns `invalid_grant` with `Account has been deleted`, the auth is permanently disabled (`StatusDisabled`) instead of being retried indefinitely.

**Files changed:**
- `sdk/cliproxy/auth/conductor_refresh.go`
- `sdk/cliproxy/auth/conductor_scheduler_refresh_test.go`

#### 2c. Per-Auth `skip_models` Field

Auth JSON files now support an optional `skip_models` array that excludes entire model categories from that auth.

**Auth file example:**
```json
{
  "type": "antigravity",
  "email": "user@example.com",
  "skip_models": ["claude", "gemini"],
  "access_token": "...",
  "refresh_token": "..."
}
```

**Supported categories:**
- `"claude"` → excludes all `claude-*` models
- `"gemini"` → excludes all `gemini-*` models
- `"gpt-oss"` → excludes all `gpt-oss-*` models

You can also use explicit wildcards (e.g., `"claude-opus-*"`) or mix categories with patterns.

**Files changed:**
- `internal/watcher/synthesizer/file.go`
- `internal/watcher/synthesizer/file_test.go`

---

### 3. Reset Time Parsing from Plain-Text 429 Bodies

**Commit:** `2c83b74d` — `fix(antigravity): parse 'Resets in Xh Ym Zs' from plain-text 429 bodies`

Antigravity's plain-text 429 responses include a human-readable reset time like `Individual quota reached. Resets in 150h3m44s`. This is now parsed and used as the cooldown duration instead of falling back to the default 30-minute backoff.

**Before:** Quota-exhausted auths were retried after 30 minutes, causing a cycle of failures.
**After:** The actual reset time (e.g., 150 hours) is respected, preventing unnecessary retries.

**Files changed:**
- `internal/runtime/executor/helps/json_retry_helpers.go`
- `internal/runtime/executor/antigravity_executor_credits_test.go`

---

### 4. Auto-Refresh Loop Fix for Disabled Auths

**Uncommitted** — `fix(auth): prevent infinite refresh loop for disabled auths`

The auto-refresh worker unconditionally re-queued auths after every refresh attempt, even when the refresh permanently disabled the auth (e.g., deleted account). This caused an infinite loop of refresh attempts against dead credentials.

**Fix:** The worker now checks the auth's status after refresh and skips the re-queue if the auth is now `StatusDisabled`.

**Files changed:**
- `sdk/cliproxy/auth/auto_refresh_loop.go`
- `sdk/cliproxy/auth/auto_refresh_loop_test.go`

---

### 5. Auto-Add `skip_models` on Quota Exhaustion

**Uncommitted** — `feat(auth): auto-add skip_models category on 429 quota exhaustion`

When a 429 quota-exceeded response is received for a model, the model's category is automatically added to the auth's `skip_models` metadata and persisted to the auth JSON file.

**Example flow:**
1. `made10` gets 429 on `claude-opus-4-6-thinking`
2. `skip_models: ["claude"]` is auto-added to `made10`'s auth file
3. `made10` is excluded from all Claude models on next load
4. `made10` remains available for Gemini and GPT-OSS models

**Files changed:**
- `sdk/cliproxy/auth/conductor_cooldown.go`
- `sdk/cliproxy/auth/cooldown_backoff_test.go`

---

## Configuration

### `config.yaml` additions

```yaml
# When true, persist per-auth cooldown status as .cds files next to auth files.
# Default is false; when false, cooldown status is kept in memory only.
save-cooldown-status: false
```

### Auth file fields

| Field | Type | Description |
|-------|------|-------------|
| `skip_models` | `[]string` | Model categories to exclude (e.g., `["claude", "gemini"]`) |
| `disabled` | `bool` | Set to `true` to disable the auth without removing it |

---

## Building

```bash
# Windows
go build -o cli-proxy-api.exe ./cmd/server

# Linux amd64
GOOS=linux GOARCH=amd64 go build -o cli-proxy-api-linux-amd64 ./cmd/server
```

---

## Syncing with Upstream

```bash
git fetch upstream
git merge upstream/main
# Resolve conflicts, then:
gofmt -w .
go build -o cli-proxy-api.exe ./cmd/server
go test ./...
```

---

## Commit History

| Commit | Description |
|--------|-------------|
| `eacbba4e` | Forward client headers to OpenAI-compatible upstreams |
| `25489532` | Classify plain-text 429 quota, auto-disable deleted accounts, add skip_models |
| `2c83b74d` | Parse 'Resets in Xh Ym Zs' from plain-text 429 bodies |
| *(uncommitted)* | Fix infinite refresh loop for disabled auths |
| *(uncommitted)* | Auto-add skip_models category on 429 quota exhaustion |
