# rtsp-sftp-uploader

Captures a frame from an RTSP camera and uploads it to an SFTP server on an interval.

```bash
helm install cam1 oci://ghcr.io/danieldenktmit/charts/rtsp-sftp-uploader \
  --set rtsp.host=192.168.1.50 \
  --set rtsp.username=admin --set rtsp.password='...' \
  --set sftp.host=files.example.com \
  --set sftp.username=uploader --set sftp.password='...' \
  --set sftp.hostKeyFingerprint='SHA256:...'
```

The chart **refuses to install** until host key verification is configured, an RTSP source
and an SFTP destination are set, and a credential is supplied. Each refusal names the
value to set.

## Values

### Image and workload

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Must be 1: a second pod would overwrite the same remote file. |
| `image.repository` | `ghcr.io/danieldenktmit/rtsp-sftp-uploader` | |
| `image.tag` | `""` | Defaults to the chart `appVersion`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `nameOverride` / `fullnameOverride` | `""` | |

### RTSP source

| Key | Default | Description |
|---|---|---|
| `rtsp.url` | `""` | Full URL; wins over host/port/path. Stored in the Secret. |
| `rtsp.host` | `""` | Required unless `rtsp.url` is set. |
| `rtsp.port` | `554` | |
| `rtsp.path` | `/` | e.g. `/Streaming/Channels/101`. A query string is preserved. |
| `rtsp.username` | `""` | |
| `rtsp.password` | `""` | Stored in the Secret. |
| `rtsp.transport` | `tcp` | `tcp` or `udp`. |
| `rtsp.timeout` | `15s` | |

### Capture

| Key | Default | Description |
|---|---|---|
| `capture.interval` | `60s` | How often to capture and upload. |
| `capture.timeout` | `30s` | Must exceed `rtsp.timeout`. |
| `capture.filename` | `image.jpg` | |
| `capture.jpegQuality` | `2` | 2 (best) to 31 (worst). |
| `capture.outputDir` | `/tmp` | An `emptyDir` is mounted here. |

### SFTP destination

| Key | Default | Description |
|---|---|---|
| `sftp.url` | `""` | `sftp://user:pass@host:port/folder`. Stored in the Secret. |
| `sftp.host` | `""` | Required unless `sftp.url` is set. |
| `sftp.port` | `22` | |
| `sftp.username` | `""` | Required. |
| `sftp.password` | `""` | Stored in the Secret. |
| `sftp.privateKey` | `""` | PEM contents; mounted at `/etc/rtsp-sftp-uploader/id` mode 0400. |
| `sftp.privateKeyPassphrase` | `""` | Stored in the Secret. |
| `sftp.remoteDir` | `/upload` | |
| `sftp.remoteFilename` | `""` | Defaults to `capture.filename`. |
| `sftp.timeout` | `30s` | |
| `sftp.mkdir` | `true` | |
| `sftp.atomic` | `true` | Upload to a temporary name, then rename. |
| `sftp.fileMode` | `"0644"` | |
| `sftp.retryAttempts` | `3` | |
| `sftp.retryBackoff` | `2s` | |
| `sftp.knownHosts` | `""` | known_hosts contents; mounted at `/etc/rtsp-sftp-uploader/known_hosts`. |
| `sftp.hostKeyFingerprint` | `""` | `SHA256:...` |
| `sftp.insecureIgnoreHostKey` | `false` | |
| `existingSecret` | `""` | Reuse an existing Secret instead of creating one. |

Exactly one of `sftp.knownHosts`, `sftp.hostKeyFingerprint` and
`sftp.insecureIgnoreHostKey` must be set.

### Runtime, probes and scheduling

| Key | Default | Description |
|---|---|---|
| `log.level` / `log.format` | `info` / `json` | |
| `http.enabled` | `true` | Serves `/healthz`, `/readyz`, `/status`. |
| `http.port` | `8080` | |
| `http.readyMaxStaleness` | `""` | Empty means 3 × `capture.interval` (min 90s). |
| `service.enabled` / `.type` / `.port` / `.annotations` | `true` / `ClusterIP` / `8080` / `{}` | |
| `probes.liveness.*` / `probes.readiness.*` | enabled | `enabled`, `initialDelaySeconds`, `periodSeconds`, `timeoutSeconds`, `failureThreshold`. |
| `serviceAccount.create` / `.name` / `.annotations` | `true` / `""` / `{}` | |
| `serviceAccount.automountServiceAccountToken` | `false` | |
| `podSecurityContext` | non-root 65532, `RuntimeDefault` seccomp | |
| `securityContext` | read-only rootfs, all capabilities dropped | |
| `resources` | 25m/64Mi requests, 500m/256Mi limits | |
| `tmpVolume.sizeLimit` / `.medium` | `64Mi` / `""` | Set `medium: Memory` for a tmpfs. |
| `extraEnv` / `extraEnvFrom` | `[]` | |
| `podAnnotations` / `podLabels` | `{}` | |
| `nodeSelector` / `tolerations` / `affinity` / `topologySpreadConstraints` | empty | |
| `priorityClassName` | `""` | |
| `terminationGracePeriodSeconds` | `30` | |

## Examples

### Minimal, with a pinned fingerprint

```bash
ssh-keyscan -p 22 files.example.com 2>/dev/null | ssh-keygen -lf -

helm install cam1 oci://ghcr.io/danieldenktmit/charts/rtsp-sftp-uploader \
  --set rtsp.host=192.168.1.50 \
  --set rtsp.path=/Streaming/Channels/101 \
  --set rtsp.username=admin --set rtsp.password='camera-password' \
  --set sftp.host=files.example.com \
  --set sftp.username=uploader --set sftp.password='sftp-password' \
  --set sftp.remoteDir=/photos/cam1 \
  --set sftp.hostKeyFingerprint='SHA256:NeNTWOHz...'
```

### With a known_hosts file and a private key

```yaml
# values.yaml
rtsp:
  host: 192.168.1.50
  path: /Streaming/Channels/101
  username: admin
  password: camera-password

sftp:
  host: files.example.com
  username: uploader
  remoteDir: /photos/cam1
  privateKey: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    ...
    -----END OPENSSH PRIVATE KEY-----
  knownHosts: |
    files.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA...

capture:
  interval: 30s
```

```bash
helm install cam1 oci://ghcr.io/danieldenktmit/charts/rtsp-sftp-uploader -f values.yaml
```

### Reusing an existing Secret

```bash
kubectl create secret generic camera-credentials \
  --from-literal=RTSP_PASSWORD='camera-password' \
  --from-literal=SFTP_PASSWORD='sftp-password'

helm install cam1 oci://ghcr.io/danieldenktmit/charts/rtsp-sftp-uploader \
  --set existingSecret=camera-credentials \
  --set rtsp.host=192.168.1.50 --set rtsp.username=admin \
  --set sftp.host=files.example.com --set sftp.username=uploader \
  --set sftp.hostKeyFingerprint='SHA256:...'
```

Recognised keys in an existing Secret: `RTSP_URL`, `RTSP_PASSWORD`, `SFTP_URL`,
`SFTP_PASSWORD`, `SFTP_PRIVATE_KEY_PASSPHRASE`, and the files `id` and `known_hosts`.

### Raspberry Pi

```bash
helm install cam1 ... \
  --set nodeSelector."kubernetes\.io/arch"=arm64 \
  --set resources.limits.memory=192Mi
```

The image is published for `linux/arm64` and `linux/arm/v7`.

## Testing the chart

```bash
helm lint charts/rtsp-sftp-uploader --set rtsp.host=c --set sftp.host=s \
  --set sftp.username=u --set sftp.password=p --set sftp.insecureIgnoreHostKey=true
helm unittest charts/rtsp-sftp-uploader
```
