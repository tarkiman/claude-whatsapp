#!/usr/bin/env bash
# Builds whisper.cpp and installs the "base" multilingual model, so voice
# notes can be transcribed locally (internal/transcribe) — no cloud STT,
# no API key. Optional: the bridge runs fine without this, voice notes just
# fall back to the "can't listen, please retype" prompt.
#
# Benchmarked on a Raspberry Pi 5 (4 threads, CPU-only): the base model
# transcribes at roughly 2.3x real-time — a 30s voice note finishes in
# well under 15s. tiny is ~6x real-time but noticeably less accurate;
# small is more accurate but slower than real-time. base is the default
# because it's the best speed/accuracy tradeoff for short voice notes.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_DIR="$(pwd)"
MODEL="${WHISPER_MODEL_NAME:-base}"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

for bin in cmake git make; do
	if ! command -v "$bin" >/dev/null 2>&1; then
		echo "Missing '$bin' — install it first (e.g. sudo apt-get install -y $bin)." >&2
		exit 1
	fi
done
if ! command -v ffmpeg >/dev/null 2>&1; then
	echo "Missing 'ffmpeg' — install it first (e.g. sudo apt-get install -y ffmpeg)." >&2
	echo "Voice notes arrive as Opus-in-Ogg, which needs ffmpeg to normalize before whisper-cli can read it." >&2
	exit 1
fi

echo "Cloning whisper.cpp into a temp dir..."
git clone --depth 1 https://github.com/ggerganov/whisper.cpp.git "$BUILD_DIR/whisper.cpp"

echo "Building whisper-cli (this can take a few minutes on a Pi)..."
cmake -B "$BUILD_DIR/whisper.cpp/build" -S "$BUILD_DIR/whisper.cpp" -DCMAKE_BUILD_TYPE=Release
cmake --build "$BUILD_DIR/whisper.cpp/build" --config Release --parallel "$(nproc)"

mkdir -p "$REPO_DIR/bin" "$REPO_DIR/data/whisper"
cp "$BUILD_DIR/whisper.cpp/build/bin/whisper-cli" "$REPO_DIR/bin/whisper-cli"

echo "Downloading the '$MODEL' multilingual model..."
bash "$BUILD_DIR/whisper.cpp/models/download-ggml-model.sh" "$MODEL" "$BUILD_DIR/whisper.cpp/models"
cp "$BUILD_DIR/whisper.cpp/models/ggml-$MODEL.bin" "$REPO_DIR/data/whisper/ggml-$MODEL.bin"

echo "Done."
echo "  bin/whisper-cli"
echo "  data/whisper/ggml-$MODEL.bin"
echo
echo "If WHISPER_MODEL in .env doesn't already point at this file, set:"
echo "  WHISPER_MODEL=./data/whisper/ggml-$MODEL.bin"
echo "Then re-run scripts/deploy.sh (or restart the service) to pick it up."
