# rtsp-sftp-uploader — Implementation Plan

> **Audience:** an AI coding assistant executing this plan step by step.
> **Rule:** execute steps in order. After **every** step, append a detailed entry to
> `rtsp-sftp-uploader-process.md` (see “Process logging contract”) so work can be paused
> and resumed at any point. Never mark a step done while its tests fail.

---

## 0. Context

### 0.1 Repository facts (verified 2026-09-26)

| Fact | Value |
|---|---|
| Local directory | `/Users/danielroth/Documents/Workspace/flashpi/rtsp-ftp-upload` |
| Git remote | `git@github.com:danieldenktmit/rtsp-sftp-uploader.git` |
| Branch | `main`, 1 commit (`89774c4 first commit`) |
| Existing files | `README.md` (empty), `.idea/`, `.git/` |
| Go toolchain | go1.26.5 darwin/arm64 |
| Helm | v4.2.3 |
| Docker | present |

**Naming decision:** the local folder is `rtsp-ftp-upload` but the remote is
`rtsp-sftp-uploader`, and the requirement is **SFTP** (SSH File Transfer Protocol), not FTP.
Everything therefore uses the remote’s name:

- Go module path: `github.com/danieldenktmit/rtsp-sftp-uploader`
- Binary: `rtsp-sftp-uploader`
- Container image: `ghcr.io/danieldenktmit/rtsp-sftp-uploader`
- Helm chart: `rtsp-sftp-uploader`, pushed to `oci://ghcr.io/danieldenktmit/charts`

> If the owner later renames the repo, only `go.mod`, the workflow `env.IMAGE_NAME`,
> and `values.yaml` `image.repository` need changing.

### 0.2 What the application does

1. Connect to an RTSP camera (host, port, path, username, password all configurable).
2. Grab **one** frame from the live stream and write it as a JPEG (default `image.jpg`).
3. Upload that JPEG to an SFTP server (host/url, port, username, password, remote folder
   all configurable) — atomically, so a consumer never reads a half-written file.
4. Repeat on a configurable interval (**default 60s**), forever, until SIGTERM/SIGINT.
5. Expose `/healthz` + `/readyz` + `/status` so Kubernetes can tell a wedged pod from a
   healthy one.

### 0.3 Architecture decisions (already made — do not relitigate)

| # | Decision | Rationale |
|---|---|---|
| D1 | **Go binary orchestrates, `ffmpeg` decodes.** Go owns config, scheduling, SFTP, health, retries, logging. `ffmpeg` is invoked as a subprocess purely to turn a stream into a JPEG. | There is no practical pure-Go H.264/H.265 decoder; the alternatives need cgo, which breaks static builds and painless arm64/arm-v7 cross-compilation. ffmpeg on Alpine is ~90 MB and dominates image size either way, so Go adds ~2 MB and buys full testability. |
| D2 | **Native Go SFTP** via `github.com/pkg/sftp` + `golang.org/x/crypto/ssh`. Never shell out to `sftp`/`sshpass`. | `sshpass` exposes the password via process args, forces `StrictHostKeyChecking=no` in practice, and breaks on passwords containing `$ " ' \`. Native SFTP gives real host-key verification and `PosixRename` for atomic publish. |
| D3 | **Internal interval loop only.** One long-running Deployment with a ticker; no Kubernetes CronJob template. Interval configurable (`CAPTURE_INTERVAL`, default `60s`); `0` means “run once and exit” (kept for local/manual runs and tests). | Explicit product decision. Sub-minute intervals are possible (k8s cron is 1-minute-minimum) and the process stays warm, avoiding per-run RTSP setup cost. |
| D4 | **Config: env vars first, CLI flags override.** No config file, no Viper. Hand-rolled loader taking an injected `lookup func(string) (string, bool)`. | 12-factor, which is exactly what Helm/Docker want; injected lookup means config tests never touch `os.Environ`, so precedence and validation are trivially testable with zero dependencies. |
| D5 | **Interfaces at the seams** — `capture.Grabber`, `uploader.Uploader` — with fakes in `capturetest`/`uploadertest` packages. | Lets `internal/app` be tested with no camera, no SSH server, no ffmpeg. |
| D6 | **Credentials are redacted everywhere.** A `redact` helper scrubs the RTSP password out of all log lines *and* out of captured ffmpeg stderr (ffmpeg echoes the full URL, credentials included, on failure). | Otherwise camera credentials land in pod logs on every connection error. |
| D7 | **Atomic writes on both ends.** Local: ffmpeg writes `image.jpg.part`, then `os.Rename`. Remote: upload to `image.jpg.<random>.part`, then `PosixRename` (fallback `Remove`+`Rename`). | A consumer polling `image.jpg` must never see a truncated JPEG. |
| D8 | **Static binary, distro-less-ish runtime.** `CGO_ENABLED=0`, multi-stage build, `alpine:3.24` runtime (needed for the ffmpeg package), non-root UID 65532, read-only root filesystem + `emptyDir` at `/tmp`. | Small attack surface; read-only rootfs is why `CAPTURE_OUTPUT_DIR` defaults to `/tmp`. |
| D9 | **Multi-arch images:** `linux/amd64`, `linux/arm64`, `linux/arm/v7`. | The parent workspace is `flashpi` — Raspberry Pi targets are expected. |
| D10 | **Helm chart tested with `helm-unittest`**, container smoke-tested in CI. | “Everything must be unittested” covers the chart and the image, not only the Go code. |

### 0.4 Rejected alternatives (recorded so they are not re-proposed)

- **Shell-script-only container** (`ffmpeg` + `sshpass` + `sleep` loop): works, but see D2; and shell
  cannot meaningfully unit-test retry/redaction/error paths.
- **`gortsplib` + cgo/OpenCV decode**: see D1.
- **Alpine `curl` for SFTP**: *verified impossible* — Alpine’s curl is built without libssh2;
  `curl -V` lists no `sftp`/`scp` protocol.
- **Kubernetes CronJob**: see D3.

### 0.5 Non-goals (v1)

Motion detection, video recording, multiple cameras per pod, Prometheus metrics, S3/HTTP
upload targets, ONVIF discovery, web UI. Keep the code open to them; do not build them.

---

## 1. Conventions for the implementing assistant

1. **Work in the project root** `/Users/danielroth/Documents/Workspace/flashpi/rtsp-ftp-upload`.
2. **Do not commit** unless the user asks. Leave changes in the working tree.
3. **Run the verification commands** listed at the end of each step. If one fails, fix it
   within that step — do not carry breakage forward.
4. **Every new `.go` file with logic gets a `_test.go` sibling in the same step.** Tests are
   part of the step, not a later cleanup.
5. **Test style:** standard library `testing`, table-driven where there is more than one case,
   `t.TempDir()` for filesystem work, `httptest` for HTTP, `context.WithTimeout` on anything
   that could hang. `github.com/stretchr/testify/require` is allowed for terse assertions.
6. **No network access in any unit test.** SFTP tests run an in-process SSH server on
   `127.0.0.1:0`. ffmpeg tests use the exec-helper-process pattern — no real ffmpeg binary.
7. **Every test must pass with `-race` and must not depend on wall-clock sleeping.** Inject
   clocks (`func() time.Time`), tickers, and sleep functions.
8. **Coverage target: ≥ 85%** over `./internal/...` (`cmd/` excluded). CI enforces it.
9. **Secrets never appear in logs, errors, process args, or chart ConfigMaps.**
10. **After each step**, append to `rtsp-sftp-uploader-process.md`.

### Process logging contract

Append one section per completed step, in this exact shape:

~~~markdown
## Step N — <title>  ·  DONE <YYYY-MM-DD HH:MM>

**Goal:** <one line>

**Files created/changed**
- `path/to/file.go` (new, 120 lines) — <what it contains>
- `path/to/other.go` (changed) — <what changed and why>

**Decisions made while implementing**
- <any deviation from the plan, and the reason>

**Tests added** (N cases)
- `TestX/case` — <what it proves>

**Verification run**
```
$ <command>
<abridged but real output, including PASS/ok lines and coverage %>
```

**Open items / notes for the next session**
- <anything the next step depends on, or nothing>
~~~

If a step is only partially finished, still write the entry, mark it
`IN PROGRESS`, and list precisely what remains.

---

## 2. Target file tree

```
rtsp-ftp-upload/
├── .dockerignore
├── .gitignore
├── .golangci.yml
├── Dockerfile
├── Makefile
├── README.md
├── go.mod
├── go.sum
├── rtsp-sftp-uploader-plan.md          # this file
├── rtsp-sftp-uploader-process.md       # implementation journal
├── .github/
│   └── workflows/
│       ├── ci.yml
│       └── release.yml
├── cmd/
│   └── rtsp-sftp-uploader/
│       └── main.go
├── internal/
│   ├── app/
│   │   ├── app.go
│   │   └── app_test.go
│   ├── capture/
│   │   ├── capture.go
│   │   ├── ffmpeg.go
│   │   ├── ffmpeg_test.go
│   │   └── capturetest/
│   │       └── fake.go
│   ├── config/
│   │   ├── config.go
│   │   ├── config_test.go
│   │   ├── duration.go            (optional helpers)
│   │   └── redact.go + redact_test.go
│   ├── health/
│   │   ├── health.go
│   │   └── health_test.go
│   ├── logging/
│   │   ├── logging.go
│   │   └── logging_test.go
│   └── uploader/
│       ├── uploader.go
│       ├── sftp.go
│       ├── sftp_test.go
│       ├── retry.go
│       ├── retry_test.go
│       ├── testsshserver_test.go
│       └── uploadertest/
│           └── fake.go
├── charts/
│   └── rtsp-sftp-uploader/
│       ├── .helmignore
│       ├── Chart.yaml
│       ├── values.yaml
│       ├── README.md
│       ├── templates/
│       │   ├── _helpers.tpl
│       │   ├── configmap.yaml
│       │   ├── deployment.yaml
│       │   ├── NOTES.txt
│       │   ├── secret.yaml
│       │   ├── service.yaml
│       │   └── serviceaccount.yaml
│       └── tests/
│           ├── configmap_test.yaml
│           ├── deployment_test.yaml
│           ├── secret_test.yaml
│           └── service_test.yaml
└── scripts/
    ├── coverage.sh
    └── docker-smoke.sh
```

---

## 3. Complete configuration surface

This table is the **single source of truth**. Step 2 implements it, Step 10 exposes it through
Helm, Step 14 documents it. Flag name = env name lowercased with `_` → `-`
(e.g. `RTSP_HOST` → `--rtsp-host`). Precedence: **flag > env > default**.

### RTSP source

| Env | Flag | Type | Default | Notes |
|---|---|---|---|---|
| `RTSP_URL` | `--rtsp-url` | string | — | Full URL, e.g. `rtsp://cam.lan:554/Streaming/Channels/101`. If set, wins over host/port/path. Credentials in the URL are honoured; `RTSP_USERNAME`/`RTSP_PASSWORD` override any userinfo present. |
| `RTSP_HOST` | `--rtsp-host` | string | — | Required unless `RTSP_URL` is set. |
| `RTSP_PORT` | `--rtsp-port` | int | `554` | 1–65535. |
| `RTSP_PATH` | `--rtsp-path` | string | `/` | Leading `/` added if missing. |
| `RTSP_USERNAME` | `--rtsp-username` | string | — | Optional (some cameras are open). |
| `RTSP_PASSWORD` | `--rtsp-password` | string | — | **Secret.** Percent-encoded into the URL userinfo. |
| `RTSP_TRANSPORT` | `--rtsp-transport` | enum | `tcp` | `tcp` \| `udp`. TCP avoids packet loss artefacts. |
| `RTSP_TIMEOUT` | `--rtsp-timeout` | duration | `15s` | Socket timeout handed to ffmpeg (`-timeout`, microseconds). |

### Capture

| Env | Flag | Type | Default | Notes |
|---|---|---|---|---|
| `CAPTURE_INTERVAL` | `--capture-interval` | duration | `60s` | **The user-facing “every minute”.** `0` ⇒ single capture, then exit 0. |
| `CAPTURE_OUTPUT_DIR` | `--capture-output-dir` | string | `/tmp` | Must be writable; read-only rootfs in k8s means this is an `emptyDir`. |
| `CAPTURE_FILENAME` | `--capture-filename` | string | `image.jpg` | Local filename. |
| `CAPTURE_JPEG_QUALITY` | `--capture-jpeg-quality` | int | `2` | ffmpeg `-q:v`, 2 (best) … 31 (worst). |
| `CAPTURE_TIMEOUT` | `--capture-timeout` | duration | `30s` | Hard wall-clock cap on one ffmpeg run (context deadline). Must be > `RTSP_TIMEOUT`. |
| `FFMPEG_PATH` | `--ffmpeg-path` | string | `ffmpeg` | Resolved via `exec.LookPath`. |

### SFTP destination

| Env | Flag | Type | Default | Notes |
|---|---|---|---|---|
| `SFTP_URL` | `--sftp-url` | string | — | Alternative single-value form: `sftp://user:pass@host:2222/remote/folder`. Parsed into host/port/user/password/dir; individual vars override parsed parts. |
| `SFTP_HOST` | `--sftp-host` | string | — | Required unless `SFTP_URL` is set. |
| `SFTP_PORT` | `--sftp-port` | int | `22` | |
| `SFTP_USERNAME` | `--sftp-username` | string | — | Required. |
| `SFTP_PASSWORD` | `--sftp-password` | string | — | **Secret.** Required unless a private key is given. |
| `SFTP_PRIVATE_KEY_PATH` | `--sftp-private-key-path` | string | — | Optional; enables publickey auth alongside/instead of password. |
| `SFTP_PRIVATE_KEY_PASSPHRASE` | `--sftp-private-key-passphrase` | string | — | **Secret.** |
| `SFTP_REMOTE_DIR` | `--sftp-remote-dir` | string | `/upload` | The configurable “folder”. |
| `SFTP_REMOTE_FILENAME` | `--sftp-remote-filename` | string | = `CAPTURE_FILENAME` | |
| `SFTP_TIMEOUT` | `--sftp-timeout` | duration | `30s` | Dial + handshake + transfer budget. |
| `SFTP_KNOWN_HOSTS_PATH` | `--sftp-known-hosts-path` | string | — | Verify against an OpenSSH `known_hosts` file. |
| `SFTP_HOST_KEY_FINGERPRINT` | `--sftp-host-key-fingerprint` | string | — | Pin a single `SHA256:…` fingerprint. Mutually exclusive with the above. |
| `SFTP_INSECURE_IGNORE_HOST_KEY` | `--sftp-insecure-ignore-host-key` | bool | `false` | Escape hatch; logs a `WARN` on every connect. |
| `SFTP_MKDIR` | `--sftp-mkdir` | bool | `true` | `MkdirAll` the remote dir. |
| `SFTP_ATOMIC` | `--sftp-atomic` | bool | `true` | Upload to `.part` then rename. |
| `SFTP_FILE_MODE` | `--sftp-file-mode` | octal string | `0644` | Chmod after upload. |
| `SFTP_RETRY_ATTEMPTS` | `--sftp-retry-attempts` | int | `3` | Total attempts, ≥1. |
| `SFTP_RETRY_BACKOFF` | `--sftp-retry-backoff` | duration | `2s` | Base for exponential backoff (`base * 2^(n-1)`). |

### Runtime

| Env | Flag | Type | Default | Notes |
|---|---|---|---|---|
| `HTTP_ADDR` | `--http-addr` | string | `:8080` | Empty string disables the HTTP server entirely. |
| `HTTP_READY_MAX_STALENESS` | `--http-ready-max-staleness` | duration | `0` | `0` ⇒ computed as `3 × CAPTURE_INTERVAL` (min `90s`). `/readyz` fails if the last success is older. |
| `LOG_LEVEL` | `--log-level` | enum | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `LOG_FORMAT` | `--log-format` | enum | `json` | `json` \| `text`. |
| — | `--version` | bool | — | Print version/commit/date, exit 0. |

### Validation rules (all enforced in `Config.Validate`, all unit-tested)

- `RTSP_URL` or `RTSP_HOST` present; otherwise error `rtsp: either RTSP_URL or RTSP_HOST must be set`.
- `SFTP_URL` or `SFTP_HOST` present; `SFTP_USERNAME` non-empty.
- At least one SFTP credential: password **or** private key path.
- Exactly one host-key strategy: `known_hosts` **xor** fingerprint **xor** insecure.
  Zero strategies ⇒ error telling the user to pick one (fail closed, never silently insecure).
- Ports in 1–65535. `CAPTURE_JPEG_QUALITY` in 2–31. `SFTP_RETRY_ATTEMPTS` ≥ 1.
- `CAPTURE_INTERVAL` ≥ 0; if > 0 then `CAPTURE_TIMEOUT` < `CAPTURE_INTERVAL` (else warn, do not fail).
- `CAPTURE_TIMEOUT` > `RTSP_TIMEOUT`.
- `SFTP_FILE_MODE` parses as octal and is ≤ `0777`.
- `CAPTURE_FILENAME` / `SFTP_REMOTE_FILENAME` contain no path separator.
- **Errors are aggregated** with `errors.Join` so a misconfigured deployment reports every
  problem at once instead of one per restart.

---

# Implementation Steps

---

## Step 1 — Repository scaffold and tooling

**Goal:** a buildable, lintable, empty-but-correct Go project.

### 1.1 Files to create

**`go.mod`** — create with:
```bash
go mod init github.com/danieldenktmit/rtsp-sftp-uploader
```
Then add dependencies (versions verified available on 2026-09-26; use `go get` so `go.sum` is correct):
```bash
go get github.com/pkg/sftp@v1.13.11
go get golang.org/x/crypto@v0.57.0
go get github.com/stretchr/testify@v1.12.1
go get github.com/gliderlabs/ssh@v0.3.8   # test-only: in-process SSH server
go mod tidy
```
`go.mod` must end up with `go 1.26`.

> `golang.org/x/crypto` supplies `ssh`, `ssh/knownhosts`, `ssh/agent`.
> `gliderlabs/ssh` is used **only** in `_test.go` files; `go mod tidy` will keep it in the
> main require block (Go has no test-only section) — that is expected and fine.

**`.gitignore`** — append to whatever exists:
```gitignore
/bin/
/dist/
*.test
coverage.out
coverage.html
*.tgz
.env
.env.local
image.jpg
/charts/**/charts/
```
Keep the existing `.idea/` handling; add `.idea/workspace.xml` if not already ignored.

**`.golangci.yml`** (golangci-lint v2 schema — `version: "2"` is required by v2.x):
```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard
  enable:
    - bodyclose
    - copyloopvar
    - errorlint
    - gocritic
    - gosec
    - misspell
    - nilerr
    - noctx
    - revive
    - unconvert
    - unparam
    - usetesting
    - wastedassign
  settings:
    gosec:
      excludes:
        - G204   # we intentionally exec ffmpeg with a config-supplied path
    revive:
      rules:
        - name: exported
          disabled: false
  exclusions:
    rules:
      - path: _test\.go
        linters: [gosec, unparam, errcheck]
formatters:
  enable: [gofmt, goimports]
  settings:
    goimports:
      local-prefixes: [github.com/danieldenktmit/rtsp-sftp-uploader]
```

**`Makefile`** (tabs, not spaces, for recipe lines):
```makefile
BINARY      := rtsp-sftp-uploader
PKG         := github.com/danieldenktmit/rtsp-sftp-uploader
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
IMAGE       ?= ghcr.io/danieldenktmit/rtsp-sftp-uploader
CHART       := charts/rtsp-sftp-uploader

.PHONY: all build test test-race cover lint fmt tidy docker docker-smoke helm-lint helm-test verify clean

all: verify build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

cover:
	./scripts/coverage.sh

lint:
	golangci-lint run ./...

fmt:
	gofmt -l -w . && go run golang.org/x/tools/cmd/goimports@latest -w .

tidy:
	go mod tidy && git diff --exit-code go.mod go.sum

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMAGE):$(VERSION) .

docker-smoke: docker
	IMAGE=$(IMAGE):$(VERSION) ./scripts/docker-smoke.sh

helm-lint:
	helm lint $(CHART)
	helm template test $(CHART) --set rtsp.host=cam.lan --set sftp.host=sftp.lan --set sftp.username=u --set sftp.password=p --set sftp.insecureIgnoreHostKey=true > /dev/null

helm-test:
	helm unittest $(CHART)

verify: tidy lint test-race cover helm-lint helm-test

clean:
	rm -rf bin dist coverage.out coverage.html
```

**`scripts/coverage.sh`** (`chmod +x`):
```bash
#!/usr/bin/env bash
set -euo pipefail
THRESHOLD=${THRESHOLD:-85}
go test -race -covermode=atomic -coverprofile=coverage.out ./internal/... ./cmd/...
# cmd/ is thin wiring; exclude it and the test-only fake packages from the gate
grep -v -E '/(cmd|capturetest|uploadertest)/' coverage.out > coverage.internal.out || true
head -1 coverage.out > /tmp/cov.head
total=$(go tool cover -func=coverage.internal.out | awk '/^total:/ {print substr($3, 1, length($3)-1)}')
echo "internal coverage: ${total}% (threshold ${THRESHOLD}%)"
awk -v t="$total" -v th="$THRESHOLD" 'BEGIN { exit (t+0 >= th+0) ? 0 : 1 }' \
  || { echo "FAIL: coverage ${total}% < ${THRESHOLD}%"; exit 1; }
go tool cover -html=coverage.out -o coverage.html
```
> Note: the first line of a coverage profile is `mode: atomic`; the `grep -v` above keeps it
> because it does not match the pattern. Verify `go tool cover` accepts the filtered file; if
> it complains, prepend the mode line explicitly.

**`scripts/docker-smoke.sh`** (`chmod +x`):
```bash
#!/usr/bin/env bash
set -euo pipefail
IMAGE=${IMAGE:?set IMAGE}
echo "--- binary runs and reports version"
docker run --rm "$IMAGE" --version
echo "--- ffmpeg present in runtime image"
docker run --rm --entrypoint ffmpeg "$IMAGE" -version | head -1
echo "--- runs as non-root"
uid=$(docker run --rm --entrypoint id "$IMAGE" -u)
[ "$uid" = "65532" ] || { echo "FAIL: expected uid 65532, got $uid"; exit 1; }
echo "--- exits non-zero with a clear message when unconfigured"
if docker run --rm "$IMAGE" 2>&1 | tee /dev/stderr | grep -q 'RTSP_HOST'; then
  echo "OK: config error mentions RTSP_HOST"
else
  echo "FAIL: expected config validation error"; exit 1
fi
echo "SMOKE OK"
```

### 1.2 Verification
```bash
go build ./... && go vet ./... && golangci-lint run ./... && make -n verify
```
(`go build ./...` on an empty module succeeds trivially; that is fine at this stage.)

### 1.3 Acceptance criteria
- `go.mod` has module path `github.com/danieldenktmit/rtsp-sftp-uploader` and `go 1.26`.
- `go.sum` committed, `go mod tidy` is a no-op.
- `golangci-lint run ./...` exits 0.
- Both scripts are executable.

---

## Step 2 — `internal/config`

**Goal:** the entire table in §3, loaded, defaulted, overridden, validated, and redacted.

### 2.1 `internal/config/config.go`

```go
// Package config loads and validates runtime configuration from environment
// variables and command-line flags. Flags take precedence over the environment.
package config

type RTSPConfig struct {
	URL       string
	Host      string
	Port      int
	Path      string
	Username  string
	Password  string
	Transport string        // "tcp" | "udp"
	Timeout   time.Duration
}

type CaptureConfig struct {
	Interval    time.Duration
	OutputDir   string
	Filename    string
	JPEGQuality int
	Timeout     time.Duration
	FFmpegPath  string
}

type SFTPConfig struct {
	URL                  string
	Host                 string
	Port                 int
	Username             string
	Password             string
	PrivateKeyPath       string
	PrivateKeyPassphrase string
	RemoteDir            string
	RemoteFilename       string
	Timeout              time.Duration
	KnownHostsPath       string
	HostKeyFingerprint   string
	InsecureIgnoreHostKey bool
	Mkdir                bool
	Atomic               bool
	FileMode             os.FileMode
	RetryAttempts        int
	RetryBackoff         time.Duration
}

type HTTPConfig struct {
	Addr             string
	ReadyMaxStaleness time.Duration
}

type LogConfig struct {
	Level  string
	Format string
}

type Config struct {
	RTSP    RTSPConfig
	Capture CaptureConfig
	SFTP    SFTPConfig
	HTTP    HTTPConfig
	Log     LogConfig
	ShowVersion bool
}

// Lookup mirrors os.LookupEnv so tests can supply a map instead of the real environment.
type Lookup func(key string) (string, bool)

// MapLookup adapts a map for tests.
func MapLookup(m map[string]string) Lookup

// Load resolves defaults, then the environment via lookup, then flags from args
// (args excludes the program name, i.e. pass os.Args[1:]).
// It returns a validated Config, or an error listing every problem found.
func Load(lookup Lookup, args []string) (*Config, error)

// Validate reports every configuration problem via errors.Join.
func (c *Config) Validate() error

// LocalPath is filepath.Join(Capture.OutputDir, Capture.Filename).
func (c *Config) LocalPath() string

// RemotePath is path.Join(SFTP.RemoteDir, SFTP.RemoteFilename) — always forward slashes.
func (c *Config) RemotePath() string

// EffectiveReadyMaxStaleness resolves the 0 => max(3*interval, 90s) rule.
func (c *Config) EffectiveReadyMaxStaleness() time.Duration

// ResolvedURL builds the ffmpeg input URL, percent-encoding credentials.
// Returns an error if neither URL nor Host is usable.
func (r RTSPConfig) ResolvedURL() (string, error)

// RedactedURL is ResolvedURL with the password replaced by "***" — safe for logs.
func (r RTSPConfig) RedactedURL() string

// Addr is net.JoinHostPort(Host, Port).
func (s SFTPConfig) Addr() string
```

**Implementation notes**

- **Loader mechanics.** Build a `flag.FlagSet` with `flag.ContinueOnError`; for each setting,
  register the flag with its *env-resolved value as the default*. Parse `args` last. This gives
  flag > env > default for free, and `--help` documents every setting.
- Set `fs.SetOutput(io.Discard)` is **not** wanted — `Load` should surface `flag.ErrHelp`
  distinctly so `main` can exit 0 on `--help`. Return it wrapped: `errors.Is(err, flag.ErrHelp)`.
- Typed env helpers, each returning `(value, error)` and accumulating into a slice:
  `envString`, `envInt`, `envBool` (`strconv.ParseBool`), `envDuration` (`time.ParseDuration`,
  but **also accept a bare integer as seconds** — `CAPTURE_INTERVAL=60` must mean 60s, because
  Helm users will write plain numbers), `envFileMode` (`strconv.ParseUint(s, 8, 32)`).
- **`ResolvedURL`**:
  1. If `URL != ""`: `url.Parse` it. Reject a scheme other than `rtsp`/`rtsps`. If
     `Username != ""`, replace userinfo with `url.UserPassword(Username, Password)`.
     If the URL had no port, inject `Port`.
  2. Else build `&url.URL{Scheme: "rtsp", Host: net.JoinHostPort(Host, strconv.Itoa(Port)),
     Path: normalizedPath, User: …}`.
  3. `u.String()` percent-encodes userinfo correctly — this is why a password like
     `p@ss/w:rd` works. **Do not** string-concatenate the URL.
- **`RedactedURL`** must never return a URL whose password is recoverable: rebuild with
  `url.User(username)` and append `:***` textually, or simply `strings.Replace` the encoded
  password. Prefer rebuilding — encoding differences make `Replace` unreliable.
- **`SFTP_URL` parsing**: scheme must be `sftp` or empty; `u.Port()` → `Port` (default 22);
  `u.User.Username()` → `Username`; password → `Password`; `u.Path` → `RemoteDir`.
  Values explicitly set by the individual env vars / flags win over parsed ones. Implement by
  parsing `SFTP_URL` **first**, using its parts as the *defaults* that the individual settings
  then override.
- `RemoteDir`: strip trailing `/`, ensure leading `/` only if the source had one (allow relative
  dirs — many SFTP servers chroot to the user’s home, where `photos/cam1` is the natural form).

### 2.2 `internal/config/redact.go`

```go
// Redactor replaces known secret values with a placeholder.
type Redactor struct{ secrets []string }

// NewRedactor collects non-empty secrets, ignoring ones shorter than 4 chars
// (too short to redact safely without mangling unrelated output).
func NewRedactor(secrets ...string) *Redactor

// String replaces every occurrence of every secret with "***".
func (r *Redactor) String(s string) string

// Error wraps err so its message is redacted. Returns nil for nil.
func (r *Redactor) Error(err error) error

// FromConfig builds a Redactor from every secret in c.
func FromConfig(c *Config) *Redactor
```
`Redactor.Error` must return a type that implements `Unwrap() error` so `errors.Is/As` still
work on the original error chain, while `Error()` returns the redacted message.

### 2.3 Tests — `config_test.go`, `redact_test.go`

Write these cases (each a named subtest):

*Loading & precedence*
1. `defaults` — empty lookup + minimal required env ⇒ every default in §3 matches exactly.
2. `env_full` — every env var set to a non-default value ⇒ all land in the struct.
3. `flag_overrides_env` — `RTSP_HOST=fromenv` + `--rtsp-host=fromflag` ⇒ `fromflag`.
4. `flag_only` — no env at all, everything via flags.
5. `interval_bare_seconds` — `CAPTURE_INTERVAL=60` ⇒ `60 * time.Second`.
6. `interval_duration_string` — `CAPTURE_INTERVAL=90s` ⇒ `90 * time.Second`.
7. `help_flag` — `args = ["--help"]` ⇒ error satisfying `errors.Is(err, flag.ErrHelp)`.
8. `unknown_flag` — ⇒ error mentioning the flag name.

*Parsing errors* (one subtest each, assert the message names the offending variable)
9. `RTSP_PORT=abc` 10. `RTSP_PORT=0` 11. `RTSP_PORT=70000`
12. `CAPTURE_INTERVAL=-5s` 13. `CAPTURE_JPEG_QUALITY=1` 14. `CAPTURE_JPEG_QUALITY=99`
15. `SFTP_FILE_MODE=0999` 16. `SFTP_RETRY_ATTEMPTS=0` 17. `RTSP_TRANSPORT=sctp`
18. `LOG_LEVEL=verbose` 19. `LOG_FORMAT=xml` 20. `SFTP_MKDIR=maybe`

*Validation*
21. `missing_rtsp_source` ⇒ error mentions `RTSP_URL` and `RTSP_HOST`.
22. `missing_sftp_host` 23. `missing_sftp_username`
24. `no_sftp_credential` (neither password nor key) ⇒ clear error.
25. `no_host_key_strategy` ⇒ error instructing to set known-hosts, fingerprint, or the insecure flag.
26. `two_host_key_strategies` (known_hosts + fingerprint) ⇒ mutually-exclusive error.
27. `capture_timeout_not_greater_than_rtsp_timeout` ⇒ error.
28. `filename_with_separator` (`CAPTURE_FILENAME=a/b.jpg`) ⇒ error.
29. `multiple_errors_aggregated` — three bad values ⇒ error string contains all three
    (`errors.Join` semantics).

*URL building*
30. `url_from_parts` ⇒ `rtsp://user:pass@cam.lan:554/Streaming/Channels/101`.
31. `url_from_parts_no_credentials` ⇒ no `@` in output.
32. `password_special_chars` — password `p@ss/w:rd#1` ⇒ parses back via `url.Parse` with
    `u.User.Password() == "p@ss/w:rd#1"` (this is the regression test for naive concatenation).
33. `explicit_url_wins` — `RTSP_URL` set plus host/port ⇒ URL used.
34. `explicit_url_credentials_injected` — `RTSP_URL=rtsp://cam/x` + username/password ⇒
    userinfo added.
35. `explicit_url_port_injected` — URL without port + `RTSP_PORT=8554` ⇒ `:8554` present.
36. `bad_scheme` — `RTSP_URL=http://x` ⇒ error.
37. `path_without_leading_slash` — `RTSP_PATH=Streaming/1` ⇒ `/Streaming/1`.
38. `redacted_url_hides_password` — assert the password substring is **absent** and `***` present.
39. `redacted_url_keeps_username_and_host`.

*SFTP URL form*
40. `sftp_url_full` — `sftp://bob:s3cr3t@host:2222/photos/cam1` ⇒ host/port/user/pass/dir set.
41. `sftp_url_no_port` ⇒ port 22.
42. `sftp_url_overridden_by_explicit_env` — URL says `bob`, `SFTP_USERNAME=alice` ⇒ `alice`.
43. `remote_path_join` — dir `/photos/` + filename `image.jpg` ⇒ `/photos/image.jpg`.
44. `remote_path_relative_dir` — `photos` ⇒ `photos/image.jpg` (no leading slash added).

*Derived values*
45. `ready_staleness_default` — interval 60s ⇒ `180s`.
46. `ready_staleness_floor` — interval 5s ⇒ `90s`.
47. `ready_staleness_explicit` — `HTTP_READY_MAX_STALENESS=10m` ⇒ `10m`.
48. `local_path` — uses `filepath.Join`.
49. `one_shot_interval_zero` — `CAPTURE_INTERVAL=0` is valid and does not trigger the
    timeout-vs-interval check.

*Redactor*
50. `redacts_all_secrets` 51. `nil_error_returns_nil` 52. `preserves_errors_is`
53. `ignores_short_secrets` (a 3-char password must not blank out unrelated text)
54. `no_secrets_is_identity`.

### 2.4 Verification
```bash
go test -race -run TestLoad -v ./internal/config/
go test -race -cover ./internal/config/     # expect >= 95%
```

### 2.5 Acceptance criteria
- ≥ 95% coverage of `internal/config`.
- No test reads the real environment (`os.Getenv`/`os.Setenv` appear nowhere in the tests).
- A password containing `@ : / # ? &` round-trips correctly through `ResolvedURL`.

---

## Step 3 — `internal/logging`

**Goal:** one `*slog.Logger` factory plus a redacting handler.

### 3.1 `internal/logging/logging.go`

```go
// New builds a slog.Logger for the given level ("debug"|"info"|"warn"|"error")
// and format ("json"|"text"), writing to w. Unknown level/format => error.
func New(w io.Writer, level, format string, r *config.Redactor) (*slog.Logger, error)

// redactHandler wraps a slog.Handler and scrubs secrets from every string
// attribute value and from the message itself.
type redactHandler struct { inner slog.Handler; r *config.Redactor }
```
Implement all four `slog.Handler` methods. In `Handle`, rewrite `record.Message` and walk
`record.Attrs` replacing `slog.StringValue`s and `err.Error()` values through the redactor.
Rebuild the record with `slog.NewRecord` + `AddAttrs` (do not mutate the original).

### 3.2 Tests
1. `json_format_emits_valid_json` — decode the output with `json.Unmarshal`.
2. `text_format` — contains `level=INFO`.
3. `level_filtering` — a `debug` call is dropped at level `info`, kept at `debug`.
4. `unknown_level` / `unknown_format` ⇒ error.
5. `redacts_message` — logging the raw password string yields `***`.
6. `redacts_string_attr`.
7. `redacts_error_attr` — `slog.Any("err", fmt.Errorf("auth failed for %s", pw))`.
8. `nested_group_attrs_redacted`.
9. `passes_through_non_string_attrs` — ints/bools/durations unchanged.

### 3.3 Verification
```bash
go test -race -cover ./internal/logging/
```

---

## Step 4 — `internal/capture`

**Goal:** grab one JPEG from the RTSP stream via ffmpeg, safely and testably.

### 4.1 `internal/capture/capture.go`

```go
// Package capture turns a live RTSP stream into a still JPEG on local disk.
package capture

// Frame describes a successfully captured still image.
type Frame struct {
	Path      string
	Size      int64
	CapturedAt time.Time
}

// Grabber captures a single frame and writes it to dst, replacing any existing
// file atomically. Implementations must honour ctx cancellation.
type Grabber interface {
	Grab(ctx context.Context, dst string) (Frame, error)
}
```

### 4.2 `internal/capture/ffmpeg.go`

```go
// FFmpegGrabber shells out to ffmpeg to decode one frame.
type FFmpegGrabber struct {
	binary      string
	sourceURL   string   // may contain credentials — never log directly
	redactedURL string   // safe for logs
	transport   string
	rtspTimeout time.Duration
	timeout     time.Duration
	quality     int
	redactor    *config.Redactor
	logger      *slog.Logger

	// seams for tests
	clock       func() time.Time
	commandContext func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// NewFFmpegGrabber validates the ffmpeg binary is resolvable and pre-builds the URL.
func NewFFmpegGrabber(rtsp config.RTSPConfig, cap config.CaptureConfig,
	r *config.Redactor, logger *slog.Logger) (*FFmpegGrabber, error)

// Args returns the exact ffmpeg argument list for dst. Exported for testing.
func (g *FFmpegGrabber) Args(dst string) []string

func (g *FFmpegGrabber) Grab(ctx context.Context, dst string) (Frame, error)
```

**`Args` must produce exactly this order** (tests assert on it):
```
-hide_banner
-nostdin
-loglevel error
-y
-rtsp_transport <tcp|udp>
-timeout <rtspTimeout in microseconds>      # ffmpeg socket timeout, integer µs
-i <sourceURL>
-frames:v 1
-q:v <quality>
-f image2
-update 1
<dst>.part
```
Notes:
- `-timeout` for the RTSP demuxer is **microseconds**: `int64(rtspTimeout / time.Microsecond)`.
- `-nostdin` prevents ffmpeg from consuming the container’s stdin.
- `-update 1` plus `-f image2` makes the single-file output unambiguous.
- `-y` overwrites the `.part` file left by a previous crashed run.

**`Grab` algorithm**
1. `ctx, cancel := context.WithTimeout(ctx, g.timeout)`; `defer cancel()`.
2. `part := dst + ".part"`; `os.MkdirAll(filepath.Dir(dst), 0o755)`.
3. `defer os.Remove(part)` — clean up on every failure path.
4. Build the command; attach a capped stderr buffer (**max 4 KiB, keep the tail**) —
   implement a tiny `tailBuffer` type; do not let a chatty ffmpeg balloon memory.
5. `cmd.Run()`. On error:
   - if `ctx.Err() != nil` ⇒ return `fmt.Errorf("capture timed out after %s: %w", g.timeout, ctx.Err())`.
   - else ⇒ `fmt.Errorf("ffmpeg failed: %w: %s", err, stderrTail)`, and **pass the whole thing
     through `g.redactor.Error`** before returning (D6).
6. `os.Stat(part)`; if `size == 0` ⇒ error `ffmpeg produced an empty image`.
7. Optional but required by the tests: verify the JPEG magic bytes `FF D8 FF` in the first 3
   bytes; mismatch ⇒ error `output is not a JPEG`.
8. `os.Rename(part, dst)` ⇒ return `Frame{Path: dst, Size: size, CapturedAt: g.clock()}`.
9. Log at debug: `"frame captured"` with `url` = **redacted** URL, `size`, `duration`.

### 4.3 `internal/capture/capturetest/fake.go`

```go
// Package capturetest provides a Grabber double for tests in other packages.
package capturetest

// Fake is a scriptable Grabber. Safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	Calls    []string      // dst of each call
	Err      error         // returned when set
	Errs     []error       // consumed one per call, before Err
	Contents []byte        // written to dst on success (default: minimal JPEG)
	OnCall   func(n int)   // hook, e.g. to cancel a context
}

func (f *Fake) Grab(ctx context.Context, dst string) (capture.Frame, error)
func (f *Fake) CallCount() int
```
`Fake.Grab` honours `ctx.Err()` first, actually writes `Contents` to `dst` so downstream
upload code has a real file, and records the call.

### 4.4 Tests — `ffmpeg_test.go`

Use the **exec helper-process pattern** so no real ffmpeg is needed:

```go
// fakeExecCommand returns a commandContext func that re-execs this test binary,
// running only TestHelperProcess, with behaviour driven by env vars.
func fakeExecCommand(t *testing.T, env ...string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		cmd.Env = append(cmd.Env, env...)
		return cmd
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	// Parse args after "--"; the last arg is the output path.
	// Behaviours, selected by env:
	//   HELPER_MODE=success        -> write JPEG magic + padding to the output path
	//   HELPER_MODE=empty          -> create a zero-byte file
	//   HELPER_MODE=notjpeg        -> write "hello"
	//   HELPER_MODE=fail           -> print HELPER_STDERR to stderr, os.Exit(1)
	//   HELPER_MODE=hang           -> block until killed
	//   HELPER_MODE=echoargs       -> print all args to stdout for assertion
}
```

Cases:
1. `args_exact_order` — assert `Args("/tmp/x.jpg")` equals the full expected slice, including
   `-timeout 15000000` for a 15s RTSP timeout and `/tmp/x.jpg.part` as the last element.
2. `args_udp_transport` — `-rtsp_transport udp`.
3. `args_quality` — `-q:v 7` for quality 7.
4. `success_writes_file_and_frame` — file exists at `dst` (not `.part`), `Frame.Size` matches,
   `CapturedAt` equals the injected clock, and **no `.part` file remains**.
5. `success_overwrites_existing_file` — pre-create `dst` with old content; assert replaced.
6. `nonzero_exit_returns_error_with_stderr_tail` — `HELPER_STDERR` present in the message.
7. `stderr_tail_is_capped` — 64 KiB of stderr ⇒ error message ≤ ~5 KiB.
8. `password_redacted_in_error` — set `HELPER_STDERR` to a string containing the password
   (simulating ffmpeg echoing the input URL); assert the password is **absent** from
   `err.Error()` and `***` is present. **This is the D6 regression test.**
9. `empty_output_is_error` 10. `non_jpeg_output_is_error`
11. `timeout_kills_process` — `HELPER_MODE=hang`, `CAPTURE_TIMEOUT=100ms` ⇒ error is/wraps
    `context.DeadlineExceeded`, returns in well under 2s.
12. `parent_context_cancel` — cancel the caller’s ctx ⇒ error wraps `context.Canceled`.
13. `part_file_cleaned_up_on_failure` — after every failure mode, `dst+".part"` does not exist.
14. `creates_output_dir` — dst inside a not-yet-existing subdirectory.
15. `new_grabber_rejects_missing_binary` — `FFMPEG_PATH=/nonexistent/ffmpeg` ⇒
    `NewFFmpegGrabber` errors (via `exec.LookPath`).
16. `new_grabber_rejects_bad_rtsp_config` — propagates `ResolvedURL` error.
17. `tail_buffer_unit` — direct table test of `tailBuffer` (short write, exact-size, overflow,
    multiple writes).

Also test the fake: 18. `capturetest_fake_records_and_writes` 19. `fake_returns_scripted_errors`.

### 4.5 Verification
```bash
go test -race -cover ./internal/capture/...
# optional manual check against a real camera, not part of CI:
RTSP_HOST=… RTSP_USERNAME=… RTSP_PASSWORD=… go run ./cmd/rtsp-sftp-uploader --capture-interval=0 --http-addr= …
```

### 4.6 Acceptance criteria
- Tests pass with **no ffmpeg installed** (verify mentally: nothing calls the real binary
  except `exec.LookPath` in `NewFFmpegGrabber`, which tests must stub or point at
  `os.Args[0]`).
- Coverage ≥ 90%.
- No test sleeps longer than 200 ms.

---

## Step 5 — `internal/uploader`

**Goal:** atomic, host-key-verified, retrying SFTP upload — with a real in-process SSH server
in the tests.

### 5.1 `internal/uploader/uploader.go`

```go
// Package uploader publishes a local file to a remote SFTP server.
package uploader

// Uploader publishes localPath to the remote destination. Implementations must
// be safe to call repeatedly and must honour ctx cancellation.
type Uploader interface {
	Upload(ctx context.Context, localPath string) error
}
```

### 5.2 `internal/uploader/sftp.go`

```go
// SFTPUploader uploads over SSH/SFTP. It opens a fresh connection per upload,
// which keeps a long-running process resilient to server restarts and idle
// timeouts at negligible cost for a once-a-minute schedule.
type SFTPUploader struct {
	cfg      config.SFTPConfig
	remote   string            // precomputed remote path
	auth     []ssh.AuthMethod
	hostKey  ssh.HostKeyCallback
	logger   *slog.Logger
	redactor *config.Redactor

	// seams for tests
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	randSuffix  func() string
}

// New validates credentials and host-key strategy, loading the private key and
// known_hosts file eagerly so misconfiguration fails at startup, not at 03:00.
func New(cfg config.SFTPConfig, remotePath string, r *config.Redactor, logger *slog.Logger) (*SFTPUploader, error)

func (u *SFTPUploader) Upload(ctx context.Context, localPath string) error
```

**`New` responsibilities**
- Auth methods, in order: `ssh.PublicKeys(signer)` when `PrivateKeyPath != ""`
  (`ssh.ParsePrivateKey`, or `ssh.ParsePrivateKeyWithPassphrase` when a passphrase is set),
  then `ssh.Password(cfg.Password)` when a password is set. Error if the list ends up empty.
- Host-key callback, exactly one of:
  - `KnownHostsPath != ""` ⇒ `knownhosts.New(path)`; wrap the returned error to say which file.
  - `HostKeyFingerprint != ""` ⇒ custom callback comparing
    `ssh.FingerprintSHA256(key)` against the configured value (accept the value with or
    without the `SHA256:` prefix); mismatch ⇒ error naming both fingerprints.
  - `InsecureIgnoreHostKey` ⇒ `ssh.InsecureIgnoreHostKey()` **and** a `logger.Warn` at
    construction time: `"SFTP host key verification disabled"`.
- `randSuffix` default: 8 hex chars from `crypto/rand`.
- `dialContext` default: `(&net.Dialer{Timeout: cfg.Timeout}).DialContext`.

**`Upload` algorithm**
1. `ctx, cancel := context.WithTimeout(ctx, u.cfg.Timeout)`; `defer cancel()`.
2. `os.Open(localPath)` first — fail fast before touching the network; `defer Close`.
   `Stat` it for the expected size.
3. `conn, err := u.dialContext(ctx, "tcp", u.cfg.Addr())`.
4. `sshConn, chans, reqs, err := ssh.NewClientConn(conn, u.cfg.Addr(), &ssh.ClientConfig{
   User, Auth, HostKeyCallback, Timeout})`; on error close `conn`.
   Wrap auth failures as `fmt.Errorf("sftp: ssh handshake with %s failed: %w", addr, err)`
   and push it through the redactor.
5. `client := ssh.NewClient(sshConn, chans, reqs)`; `defer client.Close()`.
6. Start a goroutine: `select { case <-ctx.Done(): client.Close(); case <-done: }` so an
   in-flight transfer is actually interrupted by cancellation. Close `done` via `defer`.
7. `sc, err := sftp.NewClient(client)`; `defer sc.Close()`.
8. If `cfg.Mkdir` ⇒ `sc.MkdirAll(path.Dir(u.remote))`. Treat “already exists” as success.
9. Target: `u.remote` directly when `!cfg.Atomic`, else `u.remote + "." + randSuffix() + ".part"`.
10. `f, err := sc.Create(target)`; `io.Copy(f, localFile)`; verify bytes written == expected
    size; `f.Close()` **and check its error** (SFTP reports write failures on close).
11. `sc.Chmod(target, cfg.FileMode)`.
12. If atomic: `sc.PosixRename(target, u.remote)`; if that returns an “unsupported extension”
    error, fall back to `sc.Remove(u.remote)` (ignore not-exist) then `sc.Rename(target, u.remote)`.
13. On **any** failure after step 9, best-effort `sc.Remove(target)` so the server does not
    accumulate `.part` files. Log a debug line if that cleanup fails; never mask the original error.
14. Log info: `"image uploaded"` with `remote`, `bytes`, `duration`, `host`. Never the password.

### 5.3 `internal/uploader/retry.go`

```go
// Retrier wraps an Uploader with bounded exponential backoff.
type Retrier struct {
	Inner    Uploader
	Attempts int
	Backoff  time.Duration
	Logger   *slog.Logger
	Sleep    func(ctx context.Context, d time.Duration) error // injected for tests
}

func NewRetrier(inner Uploader, attempts int, backoff time.Duration, logger *slog.Logger) *Retrier
func (r *Retrier) Upload(ctx context.Context, localPath string) error
```
Semantics: attempt `n` = 1..Attempts; on failure, if `n < Attempts`, sleep
`Backoff * 2^(n-1)` (cap at 30s) and log a `warn` with the attempt number; return the **last**
error wrapped as `after %d attempts: %w`. Abort immediately (no further attempts) when
`ctx.Err() != nil`. Default `Sleep` uses a `time.Timer` + `select` on `ctx.Done()`.

### 5.4 `internal/uploader/uploadertest/fake.go`

```go
// Fake is a scriptable Uploader double.
type Fake struct {
	mu     sync.Mutex
	Calls  []string
	Bodies [][]byte   // contents read from localPath at call time
	Err    error
	Errs   []error
	OnCall func(n int)
}
func (f *Fake) Upload(ctx context.Context, localPath string) error
func (f *Fake) CallCount() int
```

### 5.5 `internal/uploader/testsshserver_test.go` — the in-process SFTP server

```go
// testServer is a real SSH server with an SFTP subsystem, serving the local
// filesystem, listening on 127.0.0.1:0.
type testServer struct {
	Addr        string
	HostKey     ssh.PublicKey   // x/crypto/ssh public key, for fingerprint pinning
	HostKeyLine string          // known_hosts line for Addr
	Root        string          // t.TempDir()
	AuthLog     []string        // usernames that attempted auth
}

// startTestServer starts a server accepting user/password and/or an authorized key.
func startTestServer(t *testing.T, opts testServerOpts) *testServer

type testServerOpts struct {
	User          string
	Password      string
	AuthorizedKey gossh.PublicKey
	RejectAuth    bool
	FailWrites    bool   // make Create/Write fail, to test error paths
}
```
Implementation sketch (`github.com/gliderlabs/ssh` + `github.com/pkg/sftp`):
```go
signer := generateEd25519Signer(t)           // crypto/ed25519 + gossh.NewSignerFromKey
srv := &ssh.Server{
	Handler: func(s ssh.Session) { _ = s.Exit(0) },
	PasswordHandler: func(ctx ssh.Context, pw string) bool { … },
	PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool { … },
	SubsystemHandlers: map[string]ssh.SubsystemHandler{
		"sftp": func(s ssh.Session) {
			server, err := sftp.NewServer(s)
			if err != nil { return }
			_ = server.Serve()
			_ = server.Close()
		},
	},
}
srv.AddHostKey(signer)
ln, _ := net.Listen("tcp", "127.0.0.1:0")
go srv.Serve(ln)
t.Cleanup(func() { _ = srv.Close() })
```
Notes for the implementer:
- `pkg/sftp`’s server is **not chrooted** — it serves absolute host paths. So tests must use
  remote paths under `t.TempDir()`. That is fine and keeps the test honest.
- Build the `known_hosts` line with `knownhosts.Line([]string{addr}, pubKey)`.
- `gliderlabs/ssh` and `golang.org/x/crypto/ssh` have distinct `PublicKey` types; convert
  explicitly and alias the import (`gossh "golang.org/x/crypto/ssh"`) to keep it readable.

### 5.6 Tests — `sftp_test.go`

*Happy paths*
1. `password_auth_uploads_file` — content on disk at the remote path byte-identical to the local
   file; mode is `0644`.
2. `publickey_auth_uploads_file` — generate a key pair, write the private key to a temp file,
   register the public key on the server.
3. `both_credentials_configured` — password wrong, key right ⇒ still succeeds (auth-method
   ordering).
4. `mkdir_creates_nested_remote_dirs` — remote dir `<tmp>/a/b/c` does not exist beforehand.
5. `atomic_upload_leaves_no_part_file` — after success, the remote dir contains exactly one
   file, named `image.jpg`.
6. `non_atomic_upload_writes_directly` — `Atomic: false` ⇒ still correct content.
7. `overwrites_existing_remote_file` — pre-create the target with different content and a
   different size; assert fully replaced (no trailing bytes from the old file — the classic
   non-truncating-upload bug).
8. `file_mode_applied` — `FileMode: 0600` ⇒ `os.Stat(...).Mode().Perm() == 0600`.
9. `custom_remote_filename` — `snapshot.jpg`.
10. `relative_remote_dir` — dir without a leading slash resolves against the SSH user’s cwd.
    (If `pkg/sftp`’s server makes this awkward, assert the constructed path instead and note it
    in the process log.)

*Host-key strategies*
11. `known_hosts_accepts_matching_key`.
12. `known_hosts_rejects_wrong_key` — write a known_hosts line for a *different* generated key
    ⇒ `Upload` fails, error mentions host key, and **the remote file was not created**.
13. `fingerprint_pin_accepts` — with and without the `SHA256:` prefix (two subtests).
14. `fingerprint_pin_rejects_mismatch` — error contains both fingerprints.
15. `insecure_ignore_host_key_connects` — and the `WARN` line was emitted (capture the slog
    output in a buffer).
16. `known_hosts_file_missing` ⇒ `New` errors, naming the path.

*Failure paths*
17. `auth_failure_error_excludes_password` — wrong password ⇒ error mentions the host, and the
    password substring is **absent** from `err.Error()`.
18. `unreachable_host` — point at a closed port on 127.0.0.1 ⇒ dial error; completes fast.
19. `local_file_missing` ⇒ error wraps `fs.ErrNotExist` and **no connection is attempted**
    (assert via a `dialContext` seam that records calls: zero calls).
20. `context_already_cancelled` ⇒ returns promptly, wraps `context.Canceled`.
21. `context_cancelled_mid_transfer` — use a large local file and a `dialContext` wrapping the
    conn in a slow reader/writer, cancel mid-copy ⇒ error, and no complete file at the target.
22. `timeout_exceeded` — `Timeout: 50ms` against a listener that accepts but never speaks SSH
    ⇒ error within ~1s.
23. `remote_dir_not_writable` — `chmod 0500` the remote dir with `Mkdir:false` ⇒ error, and the
    error message does not contain credentials.
24. `part_file_removed_after_failed_rename` — force the rename to fail (pre-create a *directory*
    at the target path) ⇒ assert the `.part` file is gone.

*Construction*
25. `new_rejects_no_credentials` 26. `new_rejects_no_host_key_strategy`
27. `new_rejects_unparsable_private_key` 28. `new_rejects_wrong_key_passphrase`
29. `new_accepts_passphrase_protected_key`.

*Retrier* (`retry_test.go`, all with an injected `Sleep` that records durations — zero real sleeping)
30. `succeeds_first_attempt` — inner called once, no sleeps.
31. `succeeds_on_third_attempt` — 3 calls, sleeps `[2s, 4s]`.
32. `exhausts_attempts` — error message contains `after 3 attempts` and wraps the last error.
33. `single_attempt_no_retry` — `Attempts: 1`.
34. `backoff_is_capped` — `Backoff: 20s`, 4 attempts ⇒ sleeps `[20s, 30s, 30s]`.
35. `aborts_on_cancelled_context` — inner called once, no sleep.
36. `sleep_interrupted_by_cancel` — injected sleep returns `context.Canceled` ⇒ stops.
37. `logs_warning_per_retry`.

*Fake* 38. `uploadertest_fake_records_calls_and_bodies`.

### 5.7 Verification
```bash
go test -race -cover ./internal/uploader/...   # expect >= 88%
go test -race -run TestUpload -v ./internal/uploader/
```

### 5.8 Acceptance criteria
- Tests never touch the real network beyond `127.0.0.1`.
- Whole package test time < 10 s.
- Assertion in test 17 and 23 proves no credential leaks into errors.

---

## Step 6 — `internal/health`

**Goal:** Kubernetes can distinguish “alive” from “actually still uploading”.

### 6.1 `internal/health/health.go`

```go
// Package health tracks capture/upload outcomes and serves probe endpoints.
package health

// State is the concurrency-safe outcome tracker.
type State struct { /* mu sync.RWMutex, counters, lastSuccess, lastFailure, lastErr */ }

func NewState() *State
func (s *State) RecordSuccess(at time.Time)
func (s *State) RecordFailure(at time.Time, err error)
func (s *State) Snapshot() Snapshot

// Snapshot is an immutable view, suitable for JSON encoding.
type Snapshot struct {
	Successes    uint64     `json:"successes"`
	Failures     uint64     `json:"failures"`
	LastSuccess  *time.Time `json:"last_success,omitempty"`
	LastFailure  *time.Time `json:"last_failure,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	ConsecutiveFailures uint64 `json:"consecutive_failures"`
}

// Handler serves:
//   GET /healthz -> 200 always once the process is up (liveness)
//   GET /readyz  -> 200 if a success happened within maxStaleness, else 503
//                   (503 before the first success, which is correct: not ready yet)
//   GET /status  -> 200 + JSON Snapshot (human/debug)
func Handler(s *State, maxStaleness time.Duration, now func() time.Time) http.Handler

// Server wraps http.Server with graceful shutdown.
type Server struct { /* … */ }
func NewServer(addr string, h http.Handler, logger *slog.Logger) *Server
// Start listens and serves in a goroutine; it returns the resolved address
// (useful when addr is ":0") or an error if the listener cannot be created.
func (s *Server) Start() (string, error)
func (s *Server) Shutdown(ctx context.Context) error
```
`RecordFailure` stores `err.Error()` — the caller passes an **already-redacted** error
(`/status` is an HTTP endpoint; a leaked password there would be worse than in logs).

### 6.2 Tests
1. `new_state_is_not_ready` — `/readyz` ⇒ 503 with a body explaining “no successful capture yet”.
2. `healthz_always_200` — even with zero successes and many failures.
3. `ready_after_success` ⇒ 200.
4. `ready_goes_stale` — success at `t0`, `now = t0 + 2*maxStaleness` ⇒ 503, body names the age.
5. `ready_boundary_exactly_at_staleness` ⇒ 200 (inclusive), one nanosecond later ⇒ 503.
6. `failure_after_success_still_ready_within_window` — transient failures must not flap the pod.
7. `status_json_shape` — decode into a `map[string]any`; assert keys and values.
8. `status_reports_consecutive_failures` — 3 failures ⇒ 3; then a success ⇒ 0.
9. `status_includes_last_error` 10. `counters_increment`.
11. `method_not_allowed` — `POST /healthz` ⇒ 405.
12. `unknown_path` ⇒ 404.
13. `concurrent_record_and_read` — 100 goroutines × (record + snapshot), run with `-race`.
14. `server_start_shutdown` — `addr=":0"`, `Start` returns a real address, `GET /healthz` over
    it works, `Shutdown` returns nil and the port stops accepting.
15. `server_start_bad_addr` — `"256.0.0.1:99999"` ⇒ error.
16. `server_addr_empty_is_rejected` — document and test that `NewServer("")` is never
    constructed by `main` (assert `Start` errors clearly).

### 6.3 Verification
```bash
go test -race -cover ./internal/health/   # expect >= 95%
```

---

## Step 7 — `internal/app`

**Goal:** the capture→upload loop, deterministic and fully testable without time passing.

### 7.1 `internal/app/app.go`

```go
// Package app wires capture and upload into a scheduled loop.
package app

// Runner performs capture+upload cycles.
type Runner struct {
	Grabber    capture.Grabber
	Uploader   uploader.Uploader
	Health     *health.State
	Logger     *slog.Logger
	Redactor   *config.Redactor
	LocalPath  string
	Interval   time.Duration

	// seams for tests
	Clock     func() time.Time
	NewTicker func(d time.Duration) (<-chan time.Time, func())  // channel + stop
}

// RunOnce performs exactly one capture+upload and records the outcome in Health.
func (r *Runner) RunOnce(ctx context.Context) error

// Run performs one cycle immediately, then repeats every Interval until ctx is
// done. If Interval <= 0 it performs a single cycle and returns its error.
// In loop mode, cycle errors are logged and recorded but never terminate the
// loop — a camera reboot must not crash-loop the pod. Run returns nil on
// graceful shutdown.
func (r *Runner) Run(ctx context.Context) error
```

**`RunOnce`**
1. `start := r.Clock()`.
2. `frame, err := r.Grabber.Grab(ctx, r.LocalPath)`; on error ⇒
   `r.Health.RecordFailure(r.Clock(), redacted)`, log `error` with `stage=capture`, return.
3. `err = r.Uploader.Upload(ctx, frame.Path)`; on error ⇒ same, `stage=upload`.
4. Success ⇒ `r.Health.RecordSuccess(r.Clock())`, log `info` `"cycle complete"` with
   `bytes=frame.Size`, `duration=r.Clock().Sub(start)`.
5. Every error is passed through `r.Redactor.Error` before logging/recording.

**`Run`**
- Interval ≤ 0 ⇒ `return r.RunOnce(ctx)`.
- Else: run one cycle immediately (so the first image appears within seconds of pod start, not
  after a full minute — this matters for the “keeps up to date” requirement), then
  `ch, stop := r.NewTicker(r.Interval)`, `defer stop()`, and
  `for { select { case <-ctx.Done(): log "shutting down"; return nil; case <-ch: _ = r.RunOnce(ctx) } }`.
- A cycle that overruns the interval simply delays the next tick (Go tickers drop missed ticks);
  log a `warn` when `duration > Interval`.
- Default `NewTicker` returns `t.C` and `t.Stop` from `time.NewTicker`.

### 7.2 Tests — `app_test.go`

Use `capturetest.Fake` + `uploadertest.Fake` + a manual ticker channel + a fake clock:
```go
type fakeClock struct{ mu sync.Mutex; t time.Time }
func (c *fakeClock) Now() time.Time      // returns t, then advances by 1s per call
```

1. `run_once_success` — grabber called with `LocalPath`; uploader receives the same path and
   reads the exact bytes the grabber wrote; `Health.Snapshot().Successes == 1`.
2. `run_once_capture_error` — **uploader not called at all** (`CallCount() == 0`);
   `Failures == 1`; `LastError` set; returned error wraps the grabber’s error.
3. `run_once_upload_error` — grabber called once, `Failures == 1`.
4. `run_once_redacts_error` — grabber returns an error containing the password ⇒ the recorded
   `LastError` and the log output contain `***`, not the password.
5. `run_once_logs_stage` — assert a `stage=capture` / `stage=upload` attribute appears
   (capture slog into a buffer with the JSON handler and decode it).
6. `run_interval_zero_runs_once` — `Run` returns after exactly one cycle, returning that
   cycle’s error.
7. `run_interval_zero_propagates_error`.
8. `run_loop_immediate_first_cycle` — with a ticker that never fires, `Run` in a goroutine
   still performs one cycle; then cancel ⇒ `Run` returns nil.
9. `run_loop_three_ticks` — push 3 values into the manual ticker channel ⇒ 4 cycles total
   (1 immediate + 3); then cancel ⇒ nil.
10. `run_loop_survives_errors` — script `Errs = [err, err, nil]` ⇒ loop continues, ends with
    `Successes == 1`, `Failures == 2`, and `Run` returns **nil**.
11. `run_loop_stops_on_context_cancel` — cancel while blocked on the ticker ⇒ returns nil
    within 100 ms and `stop()` was called (assert via a flag in the injected ticker).
12. `run_loop_cancel_during_cycle` — `Fake.OnCall` cancels the context mid-cycle ⇒ `Run`
    returns nil, no panic, no further cycles.
13. `run_warns_on_overrun` — clock advances more than `Interval` during the cycle ⇒ a `warn`
    line mentioning the overrun.
14. `ticker_stop_called_exactly_once`.
15. `default_clock_and_ticker_are_set` — constructing a `Runner` with nil seams and calling
    `RunOnce` must not panic (implement lazy defaults in a small `init` helper).

### 7.3 Verification
```bash
go test -race -cover ./internal/app/   # expect >= 95%
go test -race -count=5 ./internal/app/  # must be non-flaky
```

### 7.4 Acceptance criteria
- No `time.Sleep` anywhere in `app_test.go`.
- Whole package runs in < 1 s.

---

## Step 8 — `cmd/rtsp-sftp-uploader/main.go`

**Goal:** thin, obvious wiring. All logic already lives in `internal/`.

```go
package main

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) { os.Exit(0) }
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := config.Load(os.LookupEnv, args)
	if errors.Is(err, flag.ErrHelp) { return err }
	if err != nil { return fmt.Errorf("configuration: %w", err) }

	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "rtsp-sftp-uploader %s (commit %s, built %s, %s/%s)\n",
			version, commit, date, runtime.GOOS, runtime.GOARCH)
		return nil
	}

	redactor := config.FromConfig(cfg)
	logger, err := logging.New(stderr, cfg.Log.Level, cfg.Log.Format, redactor)
	if err != nil { return err }

	logger.Info("starting",
		"version", version, "commit", commit,
		"rtsp_url", cfg.RTSP.RedactedURL(),
		"sftp_host", cfg.SFTP.Addr(),
		"remote_path", cfg.RemotePath(),
		"interval", cfg.Capture.Interval.String(),
		"local_path", cfg.LocalPath(),
	)

	grabber, err := capture.NewFFmpegGrabber(cfg.RTSP, cfg.Capture, redactor, logger)
	if err != nil { return err }

	up, err := uploader.New(cfg.SFTP, cfg.RemotePath(), redactor, logger)
	if err != nil { return err }
	retrying := uploader.NewRetrier(up, cfg.SFTP.RetryAttempts, cfg.SFTP.RetryBackoff, logger)

	state := health.NewState()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var srv *health.Server
	if cfg.HTTP.Addr != "" {
		h := health.Handler(state, cfg.EffectiveReadyMaxStaleness(), time.Now)
		srv = health.NewServer(cfg.HTTP.Addr, h, logger)
		addr, err := srv.Start()
		if err != nil { return fmt.Errorf("health server: %w", err) }
		logger.Info("health endpoints listening", "addr", addr)
	}

	runner := &app.Runner{ /* … */ }
	runErr := runner.Run(ctx)

	if srv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("health server shutdown", "err", err)
		}
	}
	logger.Info("stopped")
	return runErr
}
```

### 8.1 Tests — `main_test.go`
Keep them few; the heavy lifting is covered elsewhere.
1. `version_flag_prints_version` — call `run([]string{"--version"}, &buf, io.Discard)`;
   assert output contains `rtsp-sftp-uploader` and the version var.
2. `help_flag_returns_ErrHelp`.
3. `invalid_config_returns_error` — no env ⇒ error mentioning `RTSP_HOST`.
4. `bad_log_level_returns_error`.
> `run` reads `os.LookupEnv` directly, so tests 3–4 use `t.Setenv` (that is acceptable here —
> it is the one place where the real environment is the subject under test).

### 8.2 Verification
```bash
go build -o /tmp/rsu ./cmd/rtsp-sftp-uploader && /tmp/rsu --version && /tmp/rsu --help
/tmp/rsu ; echo "exit=$?"   # expect exit=1 and a config error naming RTSP_HOST
go test -race ./cmd/...
```

### 8.3 End-to-end local sanity check (manual, not CI)
```bash
# Terminal 1: throwaway SFTP server
docker run --rm -p 2222:22 -e USER_NAME=test -e USER_PASSWORD=test \
  -e PASSWORD_ACCESS=true lscr.io/linuxserver/openssh-server:latest
# Terminal 2
RTSP_URL="rtsp://<cam>/stream" RTSP_USERNAME=admin RTSP_PASSWORD=secret \
SFTP_HOST=127.0.0.1 SFTP_PORT=2222 SFTP_USERNAME=test SFTP_PASSWORD=test \
SFTP_REMOTE_DIR=/config SFTP_INSECURE_IGNORE_HOST_KEY=true \
CAPTURE_INTERVAL=10s LOG_FORMAT=text LOG_LEVEL=debug \
go run ./cmd/rtsp-sftp-uploader
# Terminal 3
curl -s localhost:8080/status | jq
```
Record the observed output in the process file. If no camera is available, substitute a local
RTSP source (`ffmpeg -re -f lavfi -i testsrc -c:v libx264 -f rtsp rtsp://127.0.0.1:8554/test`
plus a MediaMTX container) and note that in the process log.

---

## Step 9 — Docker image

**Goal:** a small, non-root, multi-arch image that contains the static binary and ffmpeg.

### 9.1 `Dockerfile`

```dockerfile
# syntax=docker/dockerfile:1

############################
# Build stage
############################
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# Dependency layer: cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/rtsp-sftp-uploader ./cmd/rtsp-sftp-uploader

############################
# Runtime stage
############################
FROM alpine:3.24

RUN apk add --no-cache ffmpeg ca-certificates tzdata wget \
 && addgroup -g 65532 -S nonroot \
 && adduser -u 65532 -S -G nonroot -H -s /sbin/nologin nonroot

COPY --from=build /out/rtsp-sftp-uploader /usr/local/bin/rtsp-sftp-uploader

ENV CAPTURE_OUTPUT_DIR=/tmp \
    HTTP_ADDR=:8080 \
    LOG_FORMAT=json \
    CAPTURE_INTERVAL=60s

USER 65532:65532
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/rtsp-sftp-uploader"]
```

**OCI labels** — add these (the release workflow also injects them via `metadata-action`, but
having them in the Dockerfile makes local builds self-describing):
```dockerfile
LABEL org.opencontainers.image.title="rtsp-sftp-uploader" \
      org.opencontainers.image.description="Captures a frame from an RTSP camera and uploads it to SFTP on an interval" \
      org.opencontainers.image.source="https://github.com/danieldenktmit/rtsp-sftp-uploader" \
      org.opencontainers.image.licenses="MIT"
```

Implementation notes / pitfalls:
- `--platform=$BUILDPLATFORM` on the build stage plus `GOOS/GOARCH` from `TARGET*` means
  **cross-compilation instead of QEMU-emulated compilation** — roughly 5–10× faster arm builds.
  Do not remove it.
- `alpine` (not `scratch`/`distroless`) because the ffmpeg package is needed. Verify ffmpeg
  exists for `linux/arm/v7` in Alpine 3.24 during the first multi-arch build; if it does not,
  drop `linux/arm/v7` from the platform list and record that in the process log.
- The binary is static (`CGO_ENABLED=0`), so no libc coupling concerns.
- `wget` comes from busybox in Alpine; the explicit `apk add wget` is harmless but can be
  dropped if the busybox applet suffices — verify and simplify.

### 9.2 `.dockerignore`
```
.git
.github
.idea
bin
dist
charts
*.md
!README.md
coverage.*
scripts
Makefile
.golangci.yml
```
> Keep `go.mod`/`go.sum`/`cmd`/`internal` in. Excluding `charts` and `scripts` keeps the build
> context small and prevents chart edits from busting the Docker layer cache.

### 9.3 Verification
```bash
make docker
make docker-smoke          # runs scripts/docker-smoke.sh: --version, ffmpeg, uid, config error
docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep rtsp-sftp-uploader
# multi-arch build check (build only, no push):
docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 -t rsu:multi .
```

### 9.4 Acceptance criteria
- `docker-smoke.sh` passes all four checks.
- Image size ≤ 130 MB for amd64 (ffmpeg is ~90 MB of it).
- `docker run --rm --read-only --tmpfs /tmp <image> --version` works — proves the read-only
  root filesystem assumption the Helm chart relies on.
- All three platforms build.

---

## Step 10 — Helm chart

**Goal:** `helm install` with four `--set` values produces a running Deployment.

### 10.1 `charts/rtsp-sftp-uploader/Chart.yaml`
```yaml
apiVersion: v2
name: rtsp-sftp-uploader
description: Captures a frame from an RTSP camera and uploads it to an SFTP server on an interval
type: application
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">=1.25.0-0"
home: https://github.com/danieldenktmit/rtsp-sftp-uploader
sources:
  - https://github.com/danieldenktmit/rtsp-sftp-uploader
keywords: [rtsp, sftp, camera, snapshot, ffmpeg]
maintainers:
  - name: danieldenktmit
```
> `version` (chart) and `appVersion` are both overwritten by the release workflow from the git
> tag. Keep them in sync manually in between.

### 10.2 `charts/rtsp-sftp-uploader/values.yaml`

Fully commented, and this is the contract the chart tests assert on:
```yaml
replicaCount: 1   # must stay 1: two pods would fight over the same remote file

image:
  repository: ghcr.io/danieldenktmit/rtsp-sftp-uploader
  # tag defaults to .Chart.AppVersion when empty
  tag: ""
  pullPolicy: IfNotPresent
imagePullSecrets: []

nameOverride: ""
fullnameOverride: ""

rtsp:
  url: ""            # full rtsp:// URL; wins over host/port/path
  host: ""           # required unless url is set
  port: 554
  path: "/"
  username: ""
  password: ""       # -> Secret
  transport: tcp     # tcp | udp
  timeout: 15s

capture:
  interval: 60s      # how often a frame is captured and uploaded
  timeout: 30s       # hard cap on one ffmpeg run
  filename: image.jpg
  jpegQuality: 2     # 2 (best) .. 31 (worst)
  outputDir: /tmp    # must be writable; an emptyDir is mounted here

sftp:
  url: ""            # sftp://user:pass@host:port/folder ; wins as defaults
  host: ""
  port: 22
  username: ""
  password: ""                 # -> Secret
  privateKey: ""               # PEM contents -> Secret, mounted at /etc/rtsp-sftp-uploader/id
  privateKeyPassphrase: ""     # -> Secret
  remoteDir: /upload
  remoteFilename: ""           # defaults to capture.filename
  timeout: 30s
  mkdir: true
  atomic: true
  fileMode: "0644"
  retryAttempts: 3
  retryBackoff: 2s
  # Host key verification — pick exactly one:
  knownHosts: ""               # contents of a known_hosts file -> Secret, mounted
  hostKeyFingerprint: ""       # SHA256:...
  insecureIgnoreHostKey: false

# Reuse an existing Secret instead of letting the chart create one.
# Expected keys: RTSP_PASSWORD, SFTP_PASSWORD, SFTP_PRIVATE_KEY_PASSPHRASE
existingSecret: ""

log:
  level: info      # debug | info | warn | error
  format: json     # json | text

http:
  enabled: true
  port: 8080
  readyMaxStaleness: ""   # empty -> 3 x capture.interval (min 90s)

service:
  enabled: true
  type: ClusterIP
  port: 8080
  annotations: {}

probes:
  liveness:
    enabled: true
    initialDelaySeconds: 10
    periodSeconds: 30
    timeoutSeconds: 5
    failureThreshold: 3
  readiness:
    enabled: true
    initialDelaySeconds: 15
    periodSeconds: 30
    timeoutSeconds: 5
    failureThreshold: 3

serviceAccount:
  create: true
  name: ""
  annotations: {}
  automountServiceAccountToken: false

podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  fsGroup: 65532
  seccompProfile:
    type: RuntimeDefault

securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: [ALL]

resources:
  requests:
    cpu: 25m
    memory: 64Mi
  limits:
    cpu: 500m
    memory: 256Mi

# Size of the emptyDir mounted at capture.outputDir
tmpVolume:
  sizeLimit: 64Mi
  medium: ""        # set to "Memory" for a tmpfs

extraEnv: []        # [{name: TZ, value: Europe/Berlin}]
extraEnvFrom: []    # [{secretRef: {name: other}}]
podAnnotations: {}
podLabels: {}
nodeSelector: {}
tolerations: []
affinity: {}
topologySpreadConstraints: []
priorityClassName: ""
terminationGracePeriodSeconds: 30
```

### 10.3 `templates/_helpers.tpl`

Standard helpers plus three chart-specific ones:
```
rtsp-sftp-uploader.name / .fullname / .chart / .labels / .selectorLabels / .serviceAccountName
rtsp-sftp-uploader.secretName      -> .Values.existingSecret | default (include "…fullname" .)
rtsp-sftp-uploader.remoteFilename  -> .Values.sftp.remoteFilename | default .Values.capture.filename
rtsp-sftp-uploader.validate        -> fail-fast checks, included by deployment.yaml
```
`rtsp-sftp-uploader.validate` must `fail` with an actionable message when:
- `rtsp.url` and `rtsp.host` are both empty →
  `"rtsp-sftp-uploader: set either rtsp.url or rtsp.host"`
- `sftp.url` and `sftp.host` are both empty → analogous
- `sftp.username` empty **and** `sftp.url` empty → `"set sftp.username"`
- `sftp.password`, `sftp.privateKey`, and `existingSecret` are all empty →
  `"provide sftp.password, sftp.privateKey, or existingSecret"`
- none of `sftp.knownHosts`, `sftp.hostKeyFingerprint`, `sftp.insecureIgnoreHostKey` is set →
  `"configure host key verification: set sftp.knownHosts or sftp.hostKeyFingerprint, or explicitly set sftp.insecureIgnoreHostKey=true"`
- more than one host-key strategy is set → mutually-exclusive message
- `replicaCount > 1` → `"replicaCount must be 1; multiple replicas would overwrite the same remote file"`

### 10.4 `templates/configmap.yaml`
All **non-secret** settings as a ConfigMap consumed with `envFrom`:
`RTSP_URL, RTSP_HOST, RTSP_PORT, RTSP_PATH, RTSP_USERNAME, RTSP_TRANSPORT, RTSP_TIMEOUT,
CAPTURE_INTERVAL, CAPTURE_TIMEOUT, CAPTURE_FILENAME, CAPTURE_JPEG_QUALITY, CAPTURE_OUTPUT_DIR,
SFTP_URL, SFTP_HOST, SFTP_PORT, SFTP_USERNAME, SFTP_REMOTE_DIR, SFTP_REMOTE_FILENAME,
SFTP_TIMEOUT, SFTP_MKDIR, SFTP_ATOMIC, SFTP_FILE_MODE, SFTP_RETRY_ATTEMPTS, SFTP_RETRY_BACKOFF,
SFTP_HOST_KEY_FINGERPRINT, SFTP_INSECURE_IGNORE_HOST_KEY, HTTP_ADDR,
HTTP_READY_MAX_STALENESS, LOG_LEVEL, LOG_FORMAT`

Rules:
- All values `quote`d (ports and booleans must be strings in a ConfigMap).
- **Omit** keys whose value is empty, so the binary's own defaults stay in charge.
- `SFTP_URL`/`RTSP_URL` contain credentials when a user supplies the URL form — therefore put
  `RTSP_URL`/`SFTP_URL` in the **Secret**, not the ConfigMap. Document this in the chart README.
- `HTTP_ADDR` = `""` when `http.enabled: false`, else `:{{ .Values.http.port }}`.
- `SFTP_PRIVATE_KEY_PATH` / `SFTP_KNOWN_HOSTS_PATH` are set here (they are paths, not secrets)
  pointing at the mounted files: `/etc/rtsp-sftp-uploader/id` and `/etc/rtsp-sftp-uploader/known_hosts`.

### 10.5 `templates/secret.yaml`
Rendered only when `existingSecret` is empty **and** at least one secret value is set.
`type: Opaque`, `stringData` with (omitting empties):
`RTSP_URL, RTSP_PASSWORD, SFTP_URL, SFTP_PASSWORD, SFTP_PRIVATE_KEY_PASSPHRASE`,
plus file keys `id` (private key PEM) and `known_hosts`.

### 10.6 `templates/deployment.yaml`
Key points, each covered by a chart test:
- `{{- include "rtsp-sftp-uploader.validate" . }}` as the first line.
- `replicas: {{ .Values.replicaCount }}`, `strategy: {type: Recreate}` (never two pods writing
  the same remote file, not even during a rollout).
- Annotation `checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}`
  and the same for the secret — so a value change restarts the pod.
- `envFrom`: the ConfigMap, then the Secret (`optional: true` so a no-secret install works),
  then `.Values.extraEnvFrom`.
- `env`: `.Values.extraEnv`.
- Container port `http` = `.Values.http.port`, only when `http.enabled`.
- Probes on `/healthz` (liveness) and `/readyz` (readiness), only when `http.enabled` **and** the
  respective `probes.*.enabled`.
- Volumes: `tmp` (emptyDir with `sizeLimit`/`medium`) mounted at `.Values.capture.outputDir`;
  `secret-files` (projected from the secret, `defaultMode: 0400`, items for `id`/`known_hosts`)
  mounted read-only at `/etc/rtsp-sftp-uploader` — **only** when a private key or known_hosts
  is supplied.
- `securityContext`, `podSecurityContext`, `resources`, `nodeSelector`, `tolerations`,
  `affinity`, `topologySpreadConstraints`, `priorityClassName`,
  `terminationGracePeriodSeconds`, `imagePullSecrets`, `serviceAccountName`,
  `automountServiceAccountToken` all wired from values.
- Image ref: `{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}`.

### 10.7 `templates/service.yaml`, `templates/serviceaccount.yaml`, `templates/NOTES.txt`
- Service: only when `service.enabled` **and** `http.enabled`; single port named `http`.
- ServiceAccount: only when `serviceAccount.create`.
- `NOTES.txt`: how to check status —
  `kubectl port-forward svc/<name> 8080:8080 && curl localhost:8080/status`,
  plus a `WARNING: host key verification is disabled` block when `insecureIgnoreHostKey` is true.

### 10.8 `.helmignore`
Defaults plus `tests/` and `*.tgz` (so chart unit tests are not packaged).

### 10.9 `charts/rtsp-sftp-uploader/README.md`
A values table generated in the same shape as §3, plus three worked examples:
minimal install, install with a known_hosts file, install reusing an existing Secret.

### 10.10 Verification
```bash
helm lint charts/rtsp-sftp-uploader
# must FAIL with the host-key message:
helm template t charts/rtsp-sftp-uploader --set rtsp.host=cam --set sftp.host=s --set sftp.username=u --set sftp.password=p
# must SUCCEED:
helm template t charts/rtsp-sftp-uploader \
  --set rtsp.host=cam.lan --set rtsp.username=admin --set rtsp.password=s3cr3t \
  --set sftp.host=sftp.lan --set sftp.username=up --set sftp.password=pw \
  --set sftp.insecureIgnoreHostKey=true | kubectl apply --dry-run=client -f -
```

### 10.11 Acceptance criteria
- `helm lint` clean.
- Every failure case in §10.3 produces its intended message.
- Rendered manifests pass `kubectl apply --dry-run=client`.
- No secret value appears in the rendered ConfigMap (grep the rendered output for the password).

---

## Step 11 — Helm chart unit tests (`helm-unittest`)

**Goal:** the chart’s behaviour is asserted, not eyeballed.

Install the plugin:
```bash
helm plugin install https://github.com/helm-unittest/helm-unittest --version v1.1.2
helm unittest charts/rtsp-sftp-uploader
```
> If the plugin is incompatible with Helm v4.2.3 on this machine, fall back to golden-file tests:
> a `charts/rtsp-sftp-uploader/tests/golden_test.go`-style shell script that runs `helm template`
> with fixed values and `diff`s against checked-in expected output, wired into `make helm-test`.
> Record whichever path was taken in the process file.

Test files under `charts/rtsp-sftp-uploader/tests/`, each with the standard
`suite/templates/tests` structure and a minimal `set:` block providing the required values.

**`deployment_test.yaml`**
1. renders one Deployment with the expected name and `app.kubernetes.io/*` labels
2. `replicas: 1` and `strategy.type: Recreate`
3. image is `ghcr.io/danieldenktmit/rtsp-sftp-uploader:<AppVersion>` when `image.tag` is empty
4. `image.tag` override is honoured
5. `imagePullPolicy` from values
6. `envFrom` references both the ConfigMap and the Secret
7. `extraEnv` entries appear in `env`
8. liveness probe path `/healthz` on port 8080; readiness `/readyz`
9. `probes.liveness.enabled=false` ⇒ no `livenessProbe`
10. `http.enabled=false` ⇒ no `ports`, no probes, and `HTTP_ADDR: ""` in the ConfigMap
11. `securityContext` has `readOnlyRootFilesystem: true` and `capabilities.drop: [ALL]`
12. emptyDir volume is mounted at `capture.outputDir`, and follows a changed `outputDir`
13. `tmpVolume.medium=Memory` ⇒ `emptyDir.medium: Memory`
14. secret-file volume **absent** when no key/known_hosts, **present** at
    `/etc/rtsp-sftp-uploader` with `defaultMode: 256` (0400) when `sftp.privateKey` is set
15. `checksum/config` annotation exists
16. `nodeSelector`/`tolerations`/`affinity`/`priorityClassName` pass through
17. `resources` pass through
18. `serviceAccountName` is the created SA; `serviceAccount.name` override honoured
19. `imagePullSecrets` pass through
20. `fullnameOverride` honoured
21. failure: no `rtsp.host`/`rtsp.url` ⇒ `failedTemplate` with the documented message
22. failure: no host-key strategy ⇒ documented message
23. failure: two host-key strategies ⇒ documented message
24. failure: `replicaCount: 2` ⇒ documented message
25. failure: no SFTP credential ⇒ documented message

**`configmap_test.yaml`**
26. `CAPTURE_INTERVAL: "60s"` by default; `"30s"` when overridden
27. `RTSP_PORT: "554"` quoted as a string
28. `SFTP_REMOTE_DIR` from values
29. `SFTP_REMOTE_FILENAME` defaults to `capture.filename`
30. empty optional keys are **absent** (e.g. no `RTSP_USERNAME` key when unset)
31. `SFTP_PRIVATE_KEY_PATH` present only when `sftp.privateKey` is set
32. `SFTP_KNOWN_HOSTS_PATH` present only when `sftp.knownHosts` is set
33. **no password appears anywhere in the ConfigMap** (assert with `notMatchRegex` on the
    rendered document)
34. `HTTP_ADDR: ":8080"`, and `":9090"` when `http.port=9090`
35. `SFTP_INSECURE_IGNORE_HOST_KEY: "true"` when set

**`secret_test.yaml`**
36. Secret rendered with `SFTP_PASSWORD` and `RTSP_PASSWORD` in `stringData`
37. not rendered at all when `existingSecret` is set
38. `existingSecret` name appears in the Deployment's `envFrom`
39. `id` key contains the PEM when `sftp.privateKey` is set
40. `known_hosts` key present when set
41. `RTSP_URL` lands in the Secret (not the ConfigMap) when supplied

**`service_test.yaml`**
42. Service rendered with port 8080, name `http`, type `ClusterIP`
43. `service.type=LoadBalancer` honoured
44. not rendered when `service.enabled=false`
45. not rendered when `http.enabled=false`
46. annotations pass through

### 11.1 Verification
```bash
helm unittest charts/rtsp-sftp-uploader     # expect all suites PASS
```

### 11.2 Acceptance criteria
- ≥ 40 chart assertions passing.
- Every `fail` branch in `_helpers.tpl` has a test.

---

## Step 12 — CI workflow (`.github/workflows/ci.yml`)

**Goal:** every push and PR is linted, tested with race detection and a coverage gate, and the
image + chart are built (not pushed).

Action versions below were verified current on 2026-09-26. Pin to the major tag shown.

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

env:
  GO_VERSION: "1.26"

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: ${{ env.GO_VERSION }}
          cache: true
      - name: go mod tidy is a no-op
        run: |
          go mod tidy
          git diff --exit-code go.mod go.sum
      - uses: golangci/golangci-lint-action@v9
        with:
          version: v2.14.0
          args: --timeout=5m

  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: ${{ env.GO_VERSION }}
          cache: true
      - name: Unit tests with race detector and coverage gate
        run: ./scripts/coverage.sh
        env:
          THRESHOLD: "85"
      - uses: actions/upload-artifact@v7
        if: always()
        with:
          name: coverage
          path: |
            coverage.out
            coverage.html

  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - { goos: linux,  goarch: amd64 }
          - { goos: linux,  goarch: arm64 }
          - { goos: linux,  goarch: arm, goarm: "7" }
          - { goos: darwin, goarch: arm64 }
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: ${{ env.GO_VERSION }}
          cache: true
      - run: CGO_ENABLED=0 GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} GOARM=${{ matrix.goarm }} go build -trimpath ./...

  helm:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: azure/setup-helm@v5
        with:
          version: v3.19.0       # pin; see note below
      - run: helm lint charts/rtsp-sftp-uploader
      - name: Install helm-unittest
        run: helm plugin install https://github.com/helm-unittest/helm-unittest --version v1.1.2
      - run: helm unittest charts/rtsp-sftp-uploader
      - name: Render and validate manifests
        run: |
          helm template t charts/rtsp-sftp-uploader \
            --set rtsp.host=cam.lan --set rtsp.username=admin --set rtsp.password=pw \
            --set sftp.host=sftp.lan --set sftp.username=up --set sftp.password=pw \
            --set sftp.insecureIgnoreHostKey=true > /tmp/rendered.yaml
          test -s /tmp/rendered.yaml
      - name: Assert no secret leaked into the ConfigMap
        run: |
          if awk '/kind: ConfigMap/,/^---/' /tmp/rendered.yaml | grep -q 'pw'; then
            echo "FAIL: secret value found in ConfigMap"; exit 1
          fi

  docker:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: docker/setup-buildx-action@v4
      - name: Build amd64 image into the local daemon
        uses: docker/build-push-action@v7
        with:
          context: .
          load: true
          tags: rtsp-sftp-uploader:ci
          build-args: |
            VERSION=ci-${{ github.sha }}
            COMMIT=${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
      - name: Smoke test the image
        run: IMAGE=rtsp-sftp-uploader:ci ./scripts/docker-smoke.sh
      - name: Verify it runs with a read-only root filesystem
        run: docker run --rm --read-only --tmpfs /tmp rtsp-sftp-uploader:ci --version

  docker-multiarch:
    # Cross-arch builds are slow; only on main and on demand.
    if: github.event_name != 'pull_request'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: docker/setup-qemu-action@v4
      - uses: docker/setup-buildx-action@v4
      - uses: docker/build-push-action@v7
        with:
          context: .
          platforms: linux/amd64,linux/arm64,linux/arm/v7
          push: false
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

**Notes for the implementer**
- `azure/setup-helm@v5` defaults to the latest Helm. Pin an explicit version so a Helm release
  cannot break CI unannounced. If `helm-unittest` v1.1.2 turns out not to support Helm 4, pin
  `v3.19.0` here (as written) and note it in the process file.
- `cache: true` on `setup-go` caches the module and build caches keyed on `go.sum`.
- The "no secret in ConfigMap" check is deliberately crude (`grep`). Keep the password used in
  the render distinctive (e.g. `zzSECRETzz`) so the grep cannot false-positive.

### 12.1 Verification
Push a branch and open a PR; all jobs must be green. Locally, approximate with:
```bash
make verify && make docker-smoke
```

---

## Step 13 — Release workflow (`.github/workflows/release.yml`)

**Goal:** a git tag produces a signed, multi-arch image in GHCR plus an OCI Helm chart, ready
to `helm install` immediately. Pushes to `main` produce a moving `edge` tag.

```yaml
name: Release

on:
  push:
    branches: [main]
    tags: ["v*.*.*"]
  workflow_dispatch:

permissions:
  contents: write        # create GitHub Releases
  packages: write        # push to GHCR
  id-token: write        # keyless cosign signing
  attestations: write    # build provenance attestation

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}      # danieldenktmit/rtsp-sftp-uploader
  CHART_DIR: charts/rtsp-sftp-uploader

jobs:
  image:
    runs-on: ubuntu-latest
    outputs:
      version: ${{ steps.meta.outputs.version }}
      digest: ${{ steps.build.outputs.digest }}
    steps:
      - uses: actions/checkout@v7
        with: { fetch-depth: 0 }

      - uses: docker/setup-qemu-action@v4
      - uses: docker/setup-buildx-action@v4

      - uses: docker/login-action@v4
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - id: meta
        uses: docker/metadata-action@v6
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}},enable=${{ !startsWith(github.ref, 'refs/tags/v0.') }}
            type=raw,value=edge,enable={{is_default_branch}}
            type=sha,format=short
          labels: |
            org.opencontainers.image.title=rtsp-sftp-uploader
            org.opencontainers.image.description=Captures a frame from an RTSP camera and uploads it to SFTP on an interval

      - id: build
        uses: docker/build-push-action@v7
        with:
          context: .
          platforms: linux/amd64,linux/arm64,linux/arm/v7
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          build-args: |
            VERSION=${{ steps.meta.outputs.version }}
            COMMIT=${{ github.sha }}
            DATE=${{ fromJSON(steps.meta.outputs.json).labels['org.opencontainers.image.created'] }}
          provenance: mode=max
          sbom: true
          cache-from: type=gha
          cache-to: type=gha,mode=max

      - uses: sigstore/cosign-installer@v4
      - name: Sign the image (keyless)
        run: |
          cosign sign --yes \
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}

      - uses: actions/attest-build-provenance@v3
        with:
          subject-name: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          subject-digest: ${{ steps.build.outputs.digest }}
          push-to-registry: true

  chart:
    needs: image
    if: startsWith(github.ref, 'refs/tags/v')
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: azure/setup-helm@v5
        with: { version: v3.19.0 }

      - name: Derive version from the tag
        id: v
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - name: Stamp chart version and appVersion
        run: |
          V=${{ steps.v.outputs.version }}
          helm package "$CHART_DIR" --version "$V" --app-version "$V" --destination dist

      - name: Log in to GHCR for OCI push
        run: echo "${{ secrets.GITHUB_TOKEN }}" | helm registry login ${{ env.REGISTRY }} -u ${{ github.actor }} --password-stdin

      - name: Push chart as an OCI artifact
        run: helm push dist/rtsp-sftp-uploader-${{ steps.v.outputs.version }}.tgz oci://${{ env.REGISTRY }}/${{ github.repository_owner }}/charts

      - uses: actions/upload-artifact@v7
        with:
          name: helm-chart
          path: dist/*.tgz

  release:
    needs: [image, chart]
    if: startsWith(github.ref, 'refs/tags/v')
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/download-artifact@v7
        with: { name: helm-chart, path: dist }
      - uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          files: dist/*.tgz
          body: |
            ## Container image
            ```
            docker pull ghcr.io/${{ github.repository }}:${{ needs.image.outputs.version }}
            ```
            Platforms: `linux/amd64`, `linux/arm64`, `linux/arm/v7`

            ## Helm chart
            ```
            helm install my-cam oci://ghcr.io/${{ github.repository_owner }}/charts/rtsp-sftp-uploader \
              --version ${{ needs.image.outputs.version }} \
              --set rtsp.host=cam.lan --set rtsp.username=admin --set rtsp.password=… \
              --set sftp.host=sftp.lan --set sftp.username=up --set sftp.password=… \
              --set sftp.hostKeyFingerprint=SHA256:…
            ```
```

**Repository settings the implementer must tell the user to apply once** (these cannot be done
from code):
1. **Package visibility:** after the first release, open
   `https://github.com/users/danieldenktmit/packages/container/rtsp-sftp-uploader/settings`
   and set visibility to **Public** if the image should be pullable without auth. Do the same
   for the `charts/rtsp-sftp-uploader` package.
2. **Link the package to the repo** (the `org.opencontainers.image.source` label does this
   automatically once pushed — verify it shows on the repo sidebar).
3. **Actions permissions:** Settings → Actions → General → Workflow permissions must allow
   `GITHUB_TOKEN` write access (or the per-job `permissions:` block above suffices — prefer the
   block, which is already least-privilege).
4. No `PAT` or extra secret is required; `secrets.GITHUB_TOKEN` is enough for GHCR.

### 13.1 Verification
```bash
# after implementing, dry-run the workflow syntax:
gh workflow list
gh act -n 2>/dev/null || echo "optional: use https://github.com/nektos/act for local dry runs"

# real verification:
git tag v0.1.0 && git push origin v0.1.0
gh run watch
docker pull ghcr.io/danieldenktmit/rtsp-sftp-uploader:0.1.0
docker run --rm ghcr.io/danieldenktmit/rtsp-sftp-uploader:0.1.0 --version
helm show chart oci://ghcr.io/danieldenktmit/charts/rtsp-sftp-uploader --version 0.1.0
cosign verify ghcr.io/danieldenktmit/rtsp-sftp-uploader:0.1.0 \
  --certificate-identity-regexp 'https://github.com/danieldenktmit/rtsp-sftp-uploader/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### 13.2 Acceptance criteria
- Tag `v0.1.0` produces GHCR tags `0.1.0`, `0.1`, `edge`(main only), `sha-…`.
- `docker manifest inspect` shows three platforms.
- `cosign verify` succeeds.
- `helm install` straight from the OCI URL works against a real cluster or `kind`.

---

## Step 14 — Documentation

**Goal:** someone who has never seen the repo can deploy it in five minutes.

### 14.1 `README.md` (root) — required sections
1. **What it does** — one paragraph plus a diagram:
   `RTSP camera → ffmpeg (1 frame) → image.jpg → SFTP (atomic rename) → repeat every 60s`.
2. **Quick start with Docker** — a complete `docker run` with every required env var.
3. **Quick start with Helm** — `helm install` from `oci://ghcr.io/danieldenktmit/charts/...`.
4. **Configuration** — the full table from §3 of this plan, verbatim.
5. **Host key verification** — explain the three strategies and how to obtain a fingerprint:
   `ssh-keyscan -p 22 sftp.host | ssh-keygen -lf -`. Explain why the chart refuses to install
   without a choice.
6. **Health endpoints** — `/healthz`, `/readyz`, `/status` with a sample `/status` JSON body.
7. **Kubernetes notes** — read-only rootfs, `emptyDir` at `/tmp`, why `replicaCount` is 1,
   why the strategy is `Recreate`.
8. **Troubleshooting** table:
   | Symptom | Likely cause | Fix |
   | `ffmpeg failed: … Connection refused` | wrong port/path | check `RTSP_PATH` per camera model |
   | `401 Unauthorized` in ffmpeg stderr | wrong credentials | verify with `ffplay` |
   | `ssh handshake … unable to authenticate` | wrong SFTP password/key | … |
   | `host key mismatch` | server rekeyed | update `knownHosts`/fingerprint |
   | `/readyz` 503 but `/healthz` 200 | captures failing | read `/status` `last_error` |
   | empty/black image | camera needs a warm-up | raise `CAPTURE_TIMEOUT`, consider `-ss` |
9. **Common RTSP paths** for Hikvision (`/Streaming/Channels/101`), Dahua
   (`/cam/realmonitor?channel=1&subtype=0`), Reolink (`/h264Preview_01_main`), ONVIF generic.
10. **Development** — `make verify`, how to run the local end-to-end setup from §8.3.
11. **Security notes** — secrets come from a k8s Secret; credentials are redacted from logs;
    the image runs as UID 65532 with no capabilities.
12. **License** — add a `LICENSE` file (MIT unless the user says otherwise; ask if unsure).

### 14.2 `charts/rtsp-sftp-uploader/README.md`
Values table + the three worked examples from §10.9.

### 14.3 Acceptance criteria
- Every env var in §3 appears in the README table.
- Every command in the README has been executed at least once and its output verified.

---

## Step 15 — Final verification and handover

Run the full gate:
```bash
cd /Users/danielroth/Documents/Workspace/flashpi/rtsp-ftp-upload
gofmt -l .                       # must print nothing
go vet ./...
golangci-lint run ./...
go test -race -count=1 ./...
./scripts/coverage.sh            # >= 85% over internal/
make docker docker-smoke
docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 -t rsu:multi .
helm lint charts/rtsp-sftp-uploader
helm unittest charts/rtsp-sftp-uploader
helm template t charts/rtsp-sftp-uploader --set rtsp.host=c --set sftp.host=s \
  --set sftp.username=u --set sftp.password=zzSECRETzz --set sftp.insecureIgnoreHostKey=true \
  | kubectl apply --dry-run=client -f -
grep -rn "zzSECRETzz" <(helm template t charts/rtsp-sftp-uploader ... ) | grep ConfigMap && echo LEAK
```

### 15.1 Completion checklist

| # | Requirement (from the original request) | Where satisfied | Done |
|---|---|---|---|
| 1 | Go application | Steps 2–8 | ☐ |
| 2 | Connect to RTSP cam | Step 4 | ☐ |
| 3 | Username configurable | `RTSP_USERNAME` | ☐ |
| 4 | Password configurable | `RTSP_PASSWORD` | ☐ |
| 5 | Port configurable | `RTSP_PORT` | ☐ |
| 6 | URL configurable | `RTSP_URL` / `RTSP_PATH` | ☐ |
| 7 | Save current frame as `image.jpg` | Step 4, `CAPTURE_FILENAME` | ☐ |
| 8 | Upload to SFTP | Step 5 | ☐ |
| 9 | SFTP username / password / url / folder configurable | `SFTP_*` | ☐ |
| 10 | **Runs every minute, interval configurable** | `CAPTURE_INTERVAL=60s`, Step 7 | ☐ |
| 11 | Docker container | Step 9 | ☐ |
| 12 | Helm chart | Step 10 | ☐ |
| 13 | GitHub Actions release to GHCR, deployable immediately | Step 13 | ☐ |
| 14 | Everything unit-tested | Steps 2–8 (Go, ≥85%), 11 (chart), 9/12 (image smoke) | ☐ |

### 15.2 Handover notes to write into the process file
- The exact coverage percentage achieved per package.
- Whether `linux/arm/v7` ffmpeg was available in Alpine 3.24 (Step 9 note).
- Whether `helm-unittest` worked with the installed Helm version, or the fallback was used.
- The list of manual repository settings still pending (Step 13 items 1–4).
- Anything deferred to v2 (see §0.5).

---

## Appendix A — Suggested commit sequence

One commit per step keeps the history bisectable:
```
chore: scaffold go module, linting, makefile        (Step 1)
feat(config): env+flag configuration with validation (Step 2)
feat(logging): slog setup with secret redaction      (Step 3)
feat(capture): ffmpeg-based RTSP frame grabber       (Step 4)
feat(uploader): atomic SFTP upload with retries      (Step 5)
feat(health): liveness/readiness/status endpoints    (Step 6)
feat(app): scheduled capture-and-upload loop         (Step 7)
feat(cmd): wire up the binary                        (Step 8)
build: multi-arch, non-root container image           (Step 9)
feat(chart): helm chart for kubernetes deployment     (Step 10)
test(chart): helm-unittest suites                     (Step 11)
ci: lint, test, coverage gate, image and chart checks  (Step 12)
ci: release multi-arch image to ghcr and chart as oci  (Step 13)
docs: readme, chart readme, troubleshooting            (Step 14)
```

## Appendix B — Risks and mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Camera credentials leak into pod logs via ffmpeg stderr | High | D6 + redaction tests (Step 4, test 8) |
| Consumer reads a half-written `image.jpg` | Medium | D7 atomic rename on both ends, tests 5/7 in Step 5 |
| Chart installs with host-key checking silently off | High | `_helpers.tpl` `fail` unless a strategy is chosen (Step 10.3) |
| Camera reboot crash-loops the pod | Medium | `Run` never returns on cycle errors; `/readyz` reports staleness (Step 7) |
| ffmpeg hangs forever on a half-open RTSP socket | Medium | ffmpeg `-timeout` **and** a context deadline (Step 4) |
| `helm-unittest` incompatible with Helm 4 | Low | Documented golden-file fallback (Step 11) |
| Alpine lacks ffmpeg for `arm/v7` | Low | Verify in Step 9; drop the platform and record it |
| Two replicas overwrite each other's upload | Medium | `replicaCount` validated to 1, `strategy: Recreate` |
| Read-only rootfs breaks local capture | Medium | `CAPTURE_OUTPUT_DIR=/tmp` + `emptyDir`, smoke-tested in Step 9 |
