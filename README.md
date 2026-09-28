# rtsp-sftp-uploader

Captures a still frame from an RTSP camera and uploads it to an SFTP server, on a
configurable interval (default: every 60 seconds).

```
 RTSP camera  ──▶  ffmpeg (1 frame)  ──▶  image.jpg  ──▶  SFTP (atomic rename)
      ▲                                                          │
      └──────────────── every CAPTURE_INTERVAL ──────────────────┘
```

- **Single static binary** plus `ffmpeg`, in a ~127 MB container that runs as a
  non-root user with a read-only root filesystem.
- **Multi-architecture**: `linux/amd64`, `linux/arm64`, `linux/arm/v7` (Raspberry Pi).
- **Atomic on both ends** — no consumer ever reads a half-written JPEG.
- **Credentials are never logged**, not even when ffmpeg echoes the stream URL on failure.
- **Host key verification is mandatory** — the chart refuses to install until you choose
  a strategy.
- `/healthz`, `/readyz` and `/status` endpoints for Kubernetes.

---

## Quick start

### Docker

```bash
docker run -d --name camera-snapshot \
  --read-only --tmpfs /tmp \
  -e RTSP_HOST=192.168.1.50 \
  -e RTSP_PATH=/Streaming/Channels/101 \
  -e RTSP_USERNAME=admin \
  -e RTSP_PASSWORD='your-camera-password' \
  -e SFTP_HOST=files.example.com \
  -e SFTP_USERNAME=uploader \
  -e SFTP_PASSWORD='your-sftp-password' \
  -e SFTP_REMOTE_DIR=/photos/cam1 \
  -e SFTP_HOST_KEY_FINGERPRINT='SHA256:...' \
  -e CAPTURE_INTERVAL=60s \
  -p 8080:8080 \
  ghcr.io/danieldenktmit/rtsp-sftp-uploader:latest
```

### Helm

```bash
helm install cam1 oci://ghcr.io/danieldenktmit/charts/rtsp-sftp-uploader \
  --set rtsp.host=192.168.1.50 \
  --set rtsp.path=/Streaming/Channels/101 \
  --set rtsp.username=admin \
  --set rtsp.password='your-camera-password' \
  --set sftp.host=files.example.com \
  --set sftp.username=uploader \
  --set sftp.password='your-sftp-password' \
  --set sftp.remoteDir=/photos/cam1 \
  --set sftp.hostKeyFingerprint='SHA256:...' \
  --set capture.interval=60s
```

See [`charts/rtsp-sftp-uploader/README.md`](charts/rtsp-sftp-uploader/README.md) for all
chart values.

### Locally

```bash
go build -o bin/rtsp-sftp-uploader ./cmd/rtsp-sftp-uploader
./bin/rtsp-sftp-uploader --help
```

`ffmpeg` must be on `PATH` (`brew install ffmpeg`, `apt install ffmpeg`).

---

## Configuration

Every setting is available as an environment variable and as a command-line flag.
The flag name is the variable lowercased with `_` replaced by `-`
(`RTSP_HOST` → `--rtsp-host`). **Precedence: flag > environment > default.**

Durations accept Go syntax (`90s`, `2m`) or a bare integer meaning seconds (`60` = `60s`).

### RTSP source

| Variable | Default | Description |
|---|---|---|
| `RTSP_URL` | — | Full URL, e.g. `rtsp://cam.lan:554/Streaming/Channels/101`. Takes precedence over host/port/path. |
| `RTSP_HOST` | — | Camera hostname or IP, bare: no scheme, port or path (use `RTSP_URL` for a full URL). Required unless `RTSP_URL` is set. |
| `RTSP_PORT` | `554` | |
| `RTSP_PATH` | `/` | Stream path; a query string is preserved. |
| `RTSP_USERNAME` | — | |
| `RTSP_PASSWORD` | — | Percent-encoded into the URL, so `@ : / # ? &` are all safe. |
| `RTSP_TRANSPORT` | `tcp` | `tcp` or `udp`. TCP avoids packet-loss artefacts. |
| `RTSP_TIMEOUT` | `15s` | Socket timeout passed to ffmpeg. |

### Capture

| Variable | Default | Description |
|---|---|---|
| `CAPTURE_INTERVAL` | `60s` | How often to capture and upload. **`0` captures once and exits.** |
| `CAPTURE_OUTPUT_DIR` | `/tmp` | Must be writable. |
| `CAPTURE_FILENAME` | `image.jpg` | |
| `CAPTURE_JPEG_QUALITY` | `2` | ffmpeg `-q:v`: 2 (best) to 31 (worst). |
| `CAPTURE_TIMEOUT` | `30s` | Hard limit for one capture. Must exceed `RTSP_TIMEOUT`. |
| `FFMPEG_PATH` | `ffmpeg` | |

### SFTP destination

| Variable | Default | Description |
|---|---|---|
| `SFTP_URL` | — | `sftp://user:pass@host:port/folder`. Supplies defaults for the settings below. |
| `SFTP_HOST` | — | Hostname or IP, bare: no scheme, port or path. Required unless `SFTP_URL` is set. |
| `SFTP_PORT` | `22` | |
| `SFTP_USERNAME` | — | Required. |
| `SFTP_PASSWORD` | — | Required unless a private key is given. |
| `SFTP_PRIVATE_KEY_PATH` | — | PEM private key for publickey auth. |
| `SFTP_PRIVATE_KEY_PASSPHRASE` | — | |
| `SFTP_REMOTE_DIR` | `/upload` | A path without a leading `/` is relative to the login directory. |
| `SFTP_REMOTE_FILENAME` | = `CAPTURE_FILENAME` | |
| `SFTP_TIMEOUT` | `30s` | Dial, handshake and transfer budget. |
| `SFTP_KNOWN_HOSTS_PATH` | — | See [Host key verification](#host-key-verification). |
| `SFTP_HOST_KEY_FINGERPRINT` | — | One or more SHA256 fingerprints, comma- or space-separated. Pin every key type the server publishes. |
| `SFTP_INSECURE_IGNORE_HOST_KEY` | `false` | |
| `SFTP_MKDIR` | `true` | Create the remote directory if missing. |
| `SFTP_ATOMIC` | `true` | Upload to a temporary name, then rename into place. |
| `SFTP_FILE_MODE` | `0644` | Octal. |
| `SFTP_RETRY_ATTEMPTS` | `3` | |
| `SFTP_RETRY_BACKOFF` | `2s` | Base for exponential backoff, capped at 30s. |

### Runtime

| Variable | Default | Description |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Empty string disables the health endpoints. |
| `HTTP_READY_MAX_STALENESS` | `0` | Age of the last success before `/readyz` fails. `0` means 3 × `CAPTURE_INTERVAL`, at least 90s. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. |

Misconfiguration is reported all at once, not one problem per restart:

```
$ rtsp-sftp-uploader
fatal: configuration:
rtsp: either RTSP_URL or RTSP_HOST must be set
sftp: either SFTP_URL or SFTP_HOST must be set
SFTP_USERNAME: must be set
sftp: provide a credential: set SFTP_PASSWORD or SFTP_PRIVATE_KEY_PATH
sftp: configure host key verification: set SFTP_KNOWN_HOSTS_PATH or SFTP_HOST_KEY_FINGERPRINT, or explicitly set SFTP_INSECURE_IGNORE_HOST_KEY=true
```

---

## Host key verification

Without it, anyone who can intercept the connection can collect your SFTP password. The
application refuses to start until you pick **exactly one** of three strategies.

**1. Pin a fingerprint (simplest):**

```bash
ssh-keyscan -p 22 files.example.com 2>/dev/null | ssh-keygen -lf -
# 256 SHA256:NeNTWOHzHNghhrcfk6JzeBEr9Rd2UjxaSMp3YdIlEuo files.example.com (ED25519)

export SFTP_HOST_KEY_FINGERPRINT='SHA256:NeNTWOHzHNghhrcfk6JzeBEr9Rd2UjxaSMp3YdIlEuo'
```

The `SHA256:` prefix is optional, and only the `SHA256:...` token is wanted — not
the whole `ssh-keygen` line.

> **Pin every key type the server publishes, not just one.** Most servers offer
> both an RSA and an ed25519 host key and the client negotiates exactly one of
> them. If you pin only the ed25519 fingerprint and RSA gets negotiated, you get
> `host key mismatch` on a fingerprint that is perfectly valid for that host.
> Separate several with commas or spaces:
>
> ```bash
> export SFTP_HOST_KEY_FINGERPRINT="$(ssh-keyscan -p 22 files.example.com 2>/dev/null \
>   | ssh-keygen -lf - | awk '{print $2}' | paste -sd, -)"
> ```
>
> Listing them all keeps the pin strict — an unknown key is still rejected.

> **Hosting panels often show MD5.** IONOS, among others, displays the legacy
> form — 32 hex digits such as `e5f04b35d161e4c14d6c764130fb53ff`, sometimes
> colon-separated. That is not accepted here, and the startup error will say so.
> To get the SHA256 while still checking it against the panel, print both formats
> for the same key and compare the MD5 line with what the panel shows:
>
> ```bash
> ssh-keyscan -p 22 files.example.com 2>/dev/null > /tmp/hk
> ssh-keygen -lf /tmp/hk -E md5      # compare this with the control panel
> ssh-keygen -lf /tmp/hk -E sha256   # use this value
> ```
>
> If the MD5 matches the panel, the SHA256 from the same scan is trustworthy.

**2. Use a `known_hosts` file:**

```bash
ssh-keyscan -p 22 files.example.com > known_hosts
export SFTP_KNOWN_HOSTS_PATH=./known_hosts
```

A file containing only one key type is fine even when the server offers several: the
client restricts negotiation to the algorithms your file records.

**3. Disable verification** (trusted networks only):

```bash
export SFTP_INSECURE_IGNORE_HOST_KEY=true   # logs a warning on every start
```

---

## Health endpoints

| Path | Meaning |
|---|---|
| `GET /healthz` | Liveness: 200 while the process runs. |
| `GET /readyz` | Readiness: 200 when a capture succeeded within `HTTP_READY_MAX_STALENESS`, otherwise 503. |
| `GET /status` | JSON counters, for humans. |

```json
{
  "successes": 42,
  "failures": 1,
  "consecutive_failures": 0,
  "last_success": "2026-09-27T09:59:40.588092+02:00",
  "last_failure": "2026-09-27T09:41:02.117418+02:00",
  "last_error": "upload: upload failed after 3 attempt(s): sftp: dialing files.example.com:22: connection refused"
}
```

`last_error` is redacted, like the logs — `/status` is an HTTP endpoint and must not leak
credentials either.

---

## Kubernetes notes

- **`replicaCount` must be 1.** Two pods would overwrite each other's upload. The chart
  refuses anything higher, and the rollout strategy is `Recreate` so two pods never
  overlap even briefly.
- **The root filesystem is read-only.** An `emptyDir` is mounted at `capture.outputDir`
  (default `/tmp`); set `tmpVolume.medium: Memory` for a tmpfs.
- **A camera outage does not crash-loop the pod.** Cycle failures are logged and counted;
  `/readyz` turns 503 once the last success is too old, so the pod leaves the Service
  without restarting.
- **Secrets stay in the Secret.** `rtsp.password`, `sftp.password`, `rtsp.url`, `sftp.url`,
  the private key and its passphrase are never rendered into the ConfigMap. CI asserts this.
- Changing any value restarts the pod, via `checksum/config` and `checksum/secret`
  annotations.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `ffmpeg failed: ... Connection refused` | Wrong port or path | Verify with `ffplay rtsp://host:554/path`; see [common paths](#common-rtsp-paths) |
| `401 Unauthorized` in the ffmpeg output | Wrong camera credentials | Check `RTSP_USERNAME` / `RTSP_PASSWORD` |
| `capture did not finish within 30s` | Long keyframe interval, or an unreachable camera | Raise `CAPTURE_TIMEOUT`; a capture cannot complete faster than the camera's GOP length (a 250-frame GOP at 15 fps is ~17 s). Shorten the camera's I-frame interval if you can. |
| `ffmpeg output is not a JPEG` | ffmpeg wrote a diagnostic instead of an image | Run with `LOG_LEVEL=debug` and read the captured stderr in the error |
| `ssh handshake ... unable to authenticate` | Wrong SFTP password or key | Test with `sftp -P <port> user@host` |
| `host key mismatch` | Server rekeyed, or only one of several key types was pinned | Pin **all** fingerprints from `ssh-keyscan <host> \| ssh-keygen -lf -`, comma-separated |
| `knownhosts: key mismatch` right after install | `known_hosts` has no entry for this host at all | `ssh-keyscan -p <port> <host> >> known_hosts` |
| `/readyz` 503 while `/healthz` is 200 | Captures or uploads are failing | `curl /status` and read `last_error` |
| Image is black or stale | Camera needs a warm-up | Raise `CAPTURE_TIMEOUT`; check the camera's substream |
| `sftp: creating remote directory` fails | No permission to create it | Pre-create the directory and set `SFTP_MKDIR=false` |

### Common RTSP paths

| Vendor | Path |
|---|---|
| Hikvision | `/Streaming/Channels/101` (main), `/Streaming/Channels/102` (sub) |
| Dahua | `/cam/realmonitor?channel=1&subtype=0` |
| Reolink | `/h264Preview_01_main`, `/h264Preview_01_sub` |
| Axis | `/axis-media/media.amp` |
| ONVIF generic | `/onvif1`, `/live`, `/stream1` |

The sub-stream is usually a better choice: lower resolution, shorter keyframe interval,
faster captures.

---

## Development

```bash
make verify          # tidy, lint, race tests, coverage gate, helm lint, helm unittest
make build           # ./bin/rtsp-sftp-uploader
make docker-smoke    # build the image and smoke-test it
make docker-multiarch
```

Requirements: Go 1.26+, Docker, Helm, `golangci-lint`, and the `helm-unittest` plugin:

```bash
helm plugin install https://github.com/helm-unittest/helm-unittest --version v1.1.2
# Helm 4 additionally needs --verify=false
```

The unit tests need no camera, no SFTP server and no ffmpeg binary: the SFTP tests run a
real SSH server in-process, and the ffmpeg tests re-execute the test binary as a
stand-in subprocess.

### End-to-end against real services

```bash
# A throwaway SFTP server and RTSP source
docker run -d --name sftp -p 2222:22 atmoz/sftp:alpine cam:campw:::upload
docker run -d --name rtsp -p 8554:8554 bluenviron/mediamtx:latest
ffmpeg -re -f lavfi -i "testsrc=size=640x480:rate=15" -c:v libx264 \
  -preset ultrafast -tune zerolatency -pix_fmt yuv420p -g 15 \
  -f rtsp rtsp://127.0.0.1:8554/cam1 &

ssh-keyscan -p 2222 127.0.0.1 > /tmp/known_hosts

RTSP_HOST=127.0.0.1 RTSP_PORT=8554 RTSP_PATH=/cam1 \
SFTP_HOST=127.0.0.1 SFTP_PORT=2222 SFTP_USERNAME=cam SFTP_PASSWORD=campw \
SFTP_REMOTE_DIR=/upload SFTP_KNOWN_HOSTS_PATH=/tmp/known_hosts \
CAPTURE_INTERVAL=5s LOG_FORMAT=text LOG_LEVEL=debug \
go run ./cmd/rtsp-sftp-uploader

curl -s localhost:8080/status
docker exec sftp ls -l /home/cam/upload/
```

---

## Security

- Secrets come from a Kubernetes Secret and are read as environment variables; they are
  never written to a ConfigMap, a command line, or a process argument.
- Known secrets are scrubbed from every log record and every error message, including the
  percent-encoded forms that appear inside URLs — ffmpeg echoes the full input URL when a
  connection fails.
- The container runs as UID 65532 with no capabilities, no privilege escalation and a
  read-only root filesystem.
- Release images are signed with cosign (keyless) and carry a build provenance attestation.

## License

MIT — see [LICENSE](LICENSE).
