# rtsp-sftp-uploader — Implementation Process Log

> Companion to `rtsp-sftp-uploader-plan.md`. One section per step, appended as the work was
> done. This file is the resume point: read it plus the plan to know exactly where things stand.

**Status: all 15 plan steps complete.** The full verification gate passes and the whole
pipeline has been proven against a live RTSP source and a real SFTP server.

---

## Status

| Step | Title | Status | Date | Notes |
|---|---|---|---|---|
| 1 | Repository scaffold and tooling | ☑ DONE | 2026-09-27 | Go toolchain moved 1.26.5 → 1.27.1 (see D-1) |
| 2 | `internal/config` | ☑ DONE | 2026-09-27 | 97.6% coverage |
| 3 | `internal/logging` | ☑ DONE | 2026-09-27 | 100% coverage |
| 4 | `internal/capture` (ffmpeg) | ☑ DONE | 2026-09-27 | 98.9% coverage |
| 5 | `internal/uploader` (SFTP) | ☑ DONE | 2026-09-27 | 91.3%; **found 2 real bugs** |
| 6 | `internal/health` | ☑ DONE | 2026-09-27 | 98.8% coverage |
| 7 | `internal/app` (loop) | ☑ DONE | 2026-09-27 | 100% coverage |
| 8 | `cmd/rtsp-sftp-uploader` | ☑ DONE | 2026-09-27 | E2E verified against live services |
| 9 | Docker image | ☑ DONE | 2026-09-27 | 127 MB, 3 architectures |
| 10 | Helm chart | ☑ DONE | 2026-09-27 | 5 objects, 7 fail-fast validations |
| 11 | Helm chart unit tests | ☑ DONE | 2026-09-27 | 63 assertions (plan asked for 46) |
| 12 | CI workflow | ☑ DONE | 2026-09-27 | 6 jobs, actionlint clean; fixed after the first real run (see B-3) |
| 13 | Release workflow (GHCR) | ☑ DONE | 2026-09-27 | Fixed after the first real run (see B-4); chart path and cosign CLI verified locally |
| 14 | Documentation | ☑ DONE | 2026-09-27 | README, chart README, LICENSE |
| 15 | Final verification and handover | ☑ DONE | 2026-09-27 | Gate passes |

---

## Coverage achieved

| Package | Coverage | Target | Met |
|---|---|---|---|
| `internal/config` | 97.6% | 95% | ☑ |
| `internal/logging` | 100.0% | 90% | ☑ |
| `internal/capture` | 98.9% | 90% | ☑ |
| `internal/capture/capturetest` | 97.1% | — | ☑ |
| `internal/uploader` | 91.3% | 88% | ☑ |
| `internal/uploader/uploadertest` | 100.0% | — | ☑ |
| `internal/health` | 98.8% | 95% | ☑ |
| `internal/app` | 100.0% | 95% | ☑ |
| `cmd/` (excluded from the gate) | 62.2% | — | — |
| **total over `internal/`** | **96.4%** | **85%** | ☑ |

Go test count: 9 packages, all passing under `-race`. Helm: 63 chart assertions.

---

## Bugs the tests, the end-to-end run, CI and the release run caught

These are the reason the work took the shape it did; all five are fixed.

### B-1 — The SSH handshake could hang forever (found by a unit test)

`ssh.ClientConfig.Timeout` is only honoured by `ssh.Dial`. This code dials itself and calls
`ssh.NewClientConn`, which ignores both that field **and** the context. A server that
accepts TCP but never speaks SSH would hang the upload indefinitely — `SFTP_TIMEOUT` would
have been silently ineffective.

The `handshake_timeout` test hung the whole suite, which is how it surfaced.

*Fix:* `internal/uploader/sftp.go` sets the context deadline on the raw connection before
the handshake, which also bounds the transfer that follows.

### B-2 — Host key verification failed for correct configurations (found by the E2E run)

`ssh-keyscan -t ed25519` is the recommended way to pin a host, but most servers also offer
an RSA key. Go negotiated the RSA key, `known_hosts` had no RSA entry, and verification
failed with `knownhosts: key mismatch` — on a configuration that was entirely correct.
Anyone following standard practice would have hit this.

x/crypto/ssh/knownhosts exposes no accessor for "which algorithms do I know for this host",
so `knownHostAlgorithms` probes the callback with a key that cannot match and reads the
resulting `KeyError.Want`. `ssh-rsa` entries are expanded to the `rsa-sha2-*` variants that
modern servers negotiate.

*Fix:* `internal/uploader/sftp.go` sets `ClientConfig.HostKeyAlgorithms`. The test SSH
server now offers both RSA and ed25519, and three regression tests cover ed25519-only,
RSA-only and both.

### B-3 — CI validated manifests against a live cluster (found by the first CI run)

The `helm` job used `kubectl apply --dry-run=client`, which despite the name contacts the
API server to fetch the OpenAPI schema. It passed locally because a kind cluster was
running on the dev machine, and failed on the runner:

```
error validating "rendered.yaml": failed to download openapi:
Get "http://localhost:8080/openapi/v2?timeout=32s": dial tcp [::1]:8080: connect: connection refused
```

*Fix:* replaced with `kubeconform -strict`, which validates offline and additionally
rejects unknown fields. See D-11.

### B-4 — The release workflow referenced a tag that does not exist (found by the first release run)

`sigstore/cosign-installer@v4` failed to resolve before a single step ran:

```
Error: Unable to resolve action `sigstore/cosign-installer@v4`, unable to find version `v4`
```

The pin was derived from the latest *release* (`v4.1.2`) on the assumption that a floating
major tag exists. It does not: that repository publishes `v2` and `v3` bare tags but no
`v4`. GitHub only reports an unresolvable `uses:` ref when the workflow actually runs, so
neither actionlint nor any local check caught it.

*Fix:* pinned to `sigstore/cosign-installer@v4.1.2`, and added
`scripts/check-action-refs.sh`, which resolves every `uses: owner/repo@ref` against the
GitHub tags API. It runs in the CI lint job and in `make verify`. All 14 referenced actions
now verify; the script was confirmed to reject the original bad ref with exit code 1.

### B-5 — Duplicate validation error

`Config.Validate` reported a missing RTSP source twice: once from the explicit check and
once from `ResolvedURL`. *Fix:* the URL is only resolved once a source is present.

---

## Decisions log (deviations from the plan)

| # | Step | Deviation | Reason |
|---|---|---|---|
| D-1 | 1 | Go toolchain is 1.27.1, not 1.26.5; the Docker builder and CI use `1.27`. `go.mod` requires `go 1.26.0`. | `brew install golangci-lint` upgraded Go as a side effect. `go.mod` keeps 1.26 as the minimum, so nothing is forced onto 1.27. |
| D-2 | 2 | Added `config.LoadTo(lookup, args, out)` alongside `Load`. | `run()` receives its own stderr writer; without this, `--help` output went to the process's real stderr and could not be tested or redirected. |
| D-3 | 2 | `Load` treats a present-but-empty environment variable as explicitly set. | `HTTP_ADDR=""` must be able to disable the health server. This is why the chart's ConfigMap omits empty keys rather than emitting `""`. |
| D-4 | 2 | Credentials embedded in `RTSP_URL` / `SFTP_URL` are hoisted into the config struct. | The redactor can only scrub secrets it knows about. Without hoisting, a password supplied only via `RTSP_URL` would appear in logs. |
| D-5 | 3 | The log handler renders `time.Duration` as a string. | slog's default emits raw nanoseconds (`"interval":5000000000`), which is unreadable in pod logs. Now `"interval":"5s"`. |
| D-6 | 6 | `health.Server.Start` takes a `context.Context`. | `noctx` requires `net.ListenConfig.Listen` over `net.Listen`. |
| D-7 | 9 | Dockerfile copies `cmd/` and `internal/` explicitly rather than `COPY . .`. | Smaller build context and a layer cache that survives chart and docs edits. |
| D-8 | 11 | Whole-document secret assertions use `notMatchRegexRaw`, not `notMatchRegex`. | `notMatchRegex` requires a string path; `data` is a map. |
| D-9 | 12 | CI pins Helm 3.19 for `helm-unittest`; the dev machine runs Helm 4.2.3. | helm-unittest 1.1.2 works on both, but Helm 4 needs `--verify=false` when installing the plugin, which Helm 3 does not accept. |
| D-10 | — | `stretchr/testify` was dropped; tests use the standard library only. | Nothing needed it, and fewer dependencies is better. It remains an indirect dependency of `pkg/sftp`. |
| D-12 | 13 | `sigstore/cosign-installer` is pinned to the full `v4.1.2`, unlike the other actions which use floating major tags. | That repository publishes no `v4` tag. See B-4. |
| D-11 | 12 | Manifest validation uses `kubeconform -strict`, not `kubectl apply --dry-run=client`. | **This was a defect in the first CI run.** `--dry-run=client` still downloads the OpenAPI schema from a live API server; it passed on the dev machine only because a kind cluster happened to be running, and failed in CI with `dial tcp [::1]:8080: connect: connection refused`. kubeconform validates offline against bundled schemas and is strictly better: `-strict` also rejects unknown fields, which `kubectl` does not. |

---

## Verification evidence

### Full gate (2026-09-27)

```
──── gofmt            OK: all files formatted
──── go vet           OK
──── go mod tidy      OK: no-op
──── golangci-lint    0 issues.
──── race tests       internal coverage: 96.4% (threshold 85%) — PASS
──── flake check      go test -race -count=3 ./internal/app/ ./internal/health/ — ok
──── helm lint        1 chart(s) linted, 0 chart(s) failed
──── helm unittest    Test Suites: 4 passed, 4 total | Tests: 63 passed, 63 total
──── actionlint       0 problems
```

### Container

```
$ IMAGE=rtsp-sftp-uploader:final ./scripts/docker-smoke.sh
--- 1/5 binary runs and reports its version
rtsp-sftp-uploader 0.1.0 (commit 89774c4, built unknown, linux/arm64)
--- 2/5 ffmpeg is present in the runtime image
ffmpeg version 8.1.2
--- 3/5 container runs as non-root uid 65532
uid=65532
--- 4/5 unconfigured run exits non-zero with an actionable message
config error mentions RTSP_HOST
--- 5/5 works with a read-only root filesystem
SMOKE OK
```

Image size 127 MB (ffmpeg is ~90 MB of it). Docker HEALTHCHECK observed reaching `healthy`
at t=32s (start-period 20s + one interval).

Multi-architecture build verified by exporting the binaries:

```
linux_amd64:  ELF 64-bit LSB executable, x86-64, statically linked
linux_arm64:  ELF 64-bit LSB executable, ARM aarch64, statically linked
linux_arm_v7: ELF 32-bit LSB executable, ARM, EABI5
```

`ffmpeg` confirmed available in Alpine 3.24 for `linux/arm/v7`, so no platform was dropped.

### End-to-end against live services

Test rig: `bluenviron/mediamtx` as the RTSP server with an ffmpeg-published test pattern
(`-g 15`), `atmoz/sftp` as the SFTP server.

Host-process run, with real `known_hosts` verification:

```
level=INFO msg="starting the capture loop" interval=5s
level=DEBUG msg="frame captured" url=rtsp://127.0.0.1:8554/cam1 bytes=17104 duration=2.146065417s
level=INFO msg="image uploaded" host=127.0.0.1:2222 remote=/upload/image.jpg bytes=17104 duration=716.017083ms
level=INFO msg="cycle complete" bytes=17104 duration=2.863722s
... (3 cycles, byte counts differ each time: genuinely fresh frames)
level=INFO msg="shutting down"
```

`/status` → `{"successes":3,"failures":0,...}`; `/readyz` → 200; `/healthz` → 200.
The uploaded file was pulled back and decoded: a valid 640x480 JPEG showing the live test
pattern with its frame counter. No `.part` files left behind locally or remotely.

Container run (read-only rootfs, non-root, `SFTP_MKDIR`, custom filename, `SFTP_FILE_MODE`):

```
-rw-------    1 cam      users        17412 Sep 27 08:32 snapshot.jpg
```

Nested remote directory `/upload/nested/dir` was created; mode `0600` applied; SIGTERM
produced exit code 0.

### Chart

```
$ helm template t charts/rtsp-sftp-uploader ... > rendered.yaml
$ kubeconform -strict -summary -kubernetes-version 1.29.0 rendered.yaml
Summary: 5 resources found in 1 file - Valid: 5, Invalid: 0, Errors: 0, Skipped: 0
```

The validator has been shown to have teeth: a misspelled field (`replicas` → `replica`)
and a wrong type (`containerPort: "eighty-eighty"`) are both rejected with exit code 1.

No secret value appears in the rendered ConfigMap; both appear in the Secret.
All seven fail-fast validations produce their intended message:

```
no-hostkey   -> configure host key verification: set sftp.knownHosts or sftp.hostKeyFingerprint, or explicitly set sftp.insecureIgnoreHostKey=true
two-hostkey  -> sftp.knownHosts, sftp.hostKeyFingerprint and sftp.insecureIgnoreHostKey are mutually exclusive; choose one
replicas     -> replicaCount must be 1; multiple replicas would overwrite the same remote file
no-rtsp      -> set either rtsp.url or rtsp.host
no-sftp      -> set either sftp.url or sftp.host
no-user      -> set sftp.username
no-cred      -> provide a credential: set sftp.password, sftp.privateKey, or existingSecret
```

### Release path (verified as far as is possible without pushing a tag)

The chart job was run end to end against a local OCI registry:

```
$ helm package charts/rtsp-sftp-uploader --version 0.1.0 --app-version 0.1.0 --destination dist
Successfully packaged chart and saved it to: dist/rtsp-sftp-uploader-0.1.0.tgz
$ helm push dist/rtsp-sftp-uploader-0.1.0.tgz oci://localhost:5001/danieldenktmit/charts
Pushed: localhost:5001/danieldenktmit/charts/rtsp-sftp-uploader:0.1.0
$ helm show chart oci://localhost:5001/danieldenktmit/charts/rtsp-sftp-uploader --version 0.1.0
name: rtsp-sftp-uploader   version: 0.1.0   appVersion: 0.1.0
```

Templating straight from the registry yields `image: "ghcr.io/danieldenktmit/rtsp-sftp-uploader:0.1.0"`,
confirming the tag flows from the git tag through `--app-version` into the Deployment.

cosign CLI surface checked against v3.0.6, the version `cosign-installer@v4.1.2` installs:
`cosign sign <IMAGE DIGEST>` is the documented form and `-y, --yes` exists. The workflow
passes `${REGISTRY}/${IMAGE_NAME}@${DIGEST}`, the digest form cosign wants — it warns when
given a tag instead, which this does not do.

Still unverified: the GHCR push itself, keyless OIDC signing and the provenance
attestation. These need a real tag push; see open item 4.

---

## Requirement traceability

| # | Requirement | Where satisfied | Verified |
|---|---|---|---|
| 1 | Go application | `cmd/`, `internal/` | ☑ builds, 9 packages |
| 2 | Connect to an RTSP camera | `internal/capture` | ☑ live stream captured |
| 3 | Username configurable | `RTSP_USERNAME` | ☑ |
| 4 | Password configurable | `RTSP_PASSWORD` | ☑ incl. `@ : / # ? &` |
| 5 | Port configurable | `RTSP_PORT` | ☑ tested on 8554 |
| 6 | URL configurable | `RTSP_URL` / `RTSP_PATH` | ☑ query strings preserved |
| 7 | Save the current frame as `image.jpg` | `CAPTURE_FILENAME` | ☑ valid JPEG verified |
| 8 | Upload to SFTP | `internal/uploader` | ☑ real server |
| 9 | SFTP username/password/url/folder configurable | `SFTP_*` | ☑ |
| 10 | **Runs every minute, interval configurable** | `CAPTURE_INTERVAL=60s` | ☑ tested at 4s/5s |
| 11 | Docker container | `Dockerfile` | ☑ smoke test, 3 arches |
| 12 | Helm chart | `charts/rtsp-sftp-uploader` | ☑ lint + dry-run apply |
| 13 | GitHub Actions release to GHCR | `.github/workflows/release.yml` | ◐ authored and actionlint-clean; **needs a tag push to exercise** |
| 14 | Everything unit-tested | 96.4% Go, 63 chart assertions, image smoke test | ☑ |

---

## Open items for the user

| # | Item | Status |
|---|---|---|
| 1 | The local folder is `rtsp-ftp-upload` but the git remote is `rtsp-sftp-uploader`; the module, image and chart all use the remote's name. | assumed — confirm |
| 2 | License set to MIT. | assumed — confirm |
| 3 | After the first release, set the GHCR package visibility to Public at `https://github.com/users/danieldenktmit/packages/container/rtsp-sftp-uploader/settings` (and the same for the `charts/rtsp-sftp-uploader` package) if the image should be pullable without auth. | pending, manual |
| 4 | The release workflow has never run. Push `v0.1.0` to exercise it; the per-job `permissions:` blocks mean no extra secret or repo setting is needed beyond `GITHUB_TOKEN`. | pending |
| 5 | Nothing is committed — all work is in the working tree, per the plan's convention. Suggested commit sequence is in Appendix A of the plan. | pending |

## Deferred to a future version

Motion detection, video recording, several cameras per pod, Prometheus metrics, S3/HTTP
upload targets, ONVIF discovery, a web UI. The interfaces (`capture.Grabber`,
`uploader.Uploader`) leave room for all of them.

---

## Environment at implementation time

```
go          go1.27.1 darwin/arm64  (go.mod requires go 1.26.0)
helm        v4.2.3+g43e8b7f, helm-unittest 1.1.2
golangci    2.14.0
docker      Docker Desktop, buildx container driver "rsu-multi" for multi-arch
ffmpeg      8.1.2 (host, for manual testing only — unit tests need no ffmpeg)
git         branch main, remote git@github.com:danieldenktmit/rtsp-sftp-uploader.git
```
