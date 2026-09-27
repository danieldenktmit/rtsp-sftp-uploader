#!/usr/bin/env bash
# Smoke-tests a built container image. Set IMAGE to the tag to test.
set -euo pipefail

IMAGE=${IMAGE:?set IMAGE to the image tag under test}
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "--- 1/5 binary runs and reports its version"
docker run --rm "$IMAGE" --version

echo "--- 2/5 ffmpeg is present in the runtime image"
# Capture first, then trim: piping into head would SIGPIPE under `set -o pipefail`.
ffmpeg_version=$(docker run --rm --entrypoint ffmpeg "$IMAGE" -version)
echo "${ffmpeg_version%%$'\n'*}"

echo "--- 3/5 container runs as non-root uid 65532"
uid=$(docker run --rm --entrypoint id "$IMAGE" -u)
[ "$uid" = "65532" ] || fail "expected uid 65532, got '$uid'"
echo "uid=$uid"

echo "--- 4/5 unconfigured run exits non-zero with an actionable message"
out=$(docker run --rm "$IMAGE" 2>&1) && fail "expected a non-zero exit with no configuration"
echo "$out" | grep -q 'RTSP_HOST' || fail "config error did not mention RTSP_HOST: $out"
echo "config error mentions RTSP_HOST"

echo "--- 5/5 works with a read-only root filesystem"
docker run --rm --read-only --tmpfs /tmp "$IMAGE" --version > /dev/null

echo "SMOKE OK"
