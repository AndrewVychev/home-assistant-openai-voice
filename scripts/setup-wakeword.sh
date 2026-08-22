#!/usr/bin/env bash

set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
assets_dir="${1:-$project_dir/wakeword}"
models_dir="$assets_dir/models"
runtime_dir="$assets_dir/runtime"
model_base="https://github.com/dscripka/openWakeWord/releases/download/v0.5.1"
runtime_version="1.26.0"
runtime_base="https://github.com/microsoft/onnxruntime/releases/download/v$runtime_version"

mkdir -p "$models_dir" "$runtime_dir"

for model in hey_jarvis_v0.1.onnx embedding_model.onnx melspectrogram.onnx silero_vad.onnx; do
  if [[ ! -f "$models_dir/$model" ]]; then
    echo "download: $model"
    curl -L --fail --show-error --output "$models_dir/$model" "$model_base/$model"
  fi
done

os="$(uname -s)"
arch="$(uname -m)"
case "$os:$arch" in
  Darwin:arm64)
    archive="onnxruntime-osx-arm64-$runtime_version.tgz"
    library="libonnxruntime.dylib"
    ;;
  Darwin:x86_64)
    archive="onnxruntime-osx-x86_64-$runtime_version.tgz"
    library="libonnxruntime.dylib"
    ;;
  Linux:x86_64)
    archive="onnxruntime-linux-x64-$runtime_version.tgz"
    library="libonnxruntime.so"
    ;;
  Linux:aarch64 | Linux:arm64)
    archive="onnxruntime-linux-aarch64-$runtime_version.tgz"
    library="libonnxruntime.so"
    ;;
  *)
    echo "unsupported platform: $os $arch" >&2
    exit 1
    ;;
esac

if [[ ! -f "$runtime_dir/$library" ]]; then
  temp_dir="$(mktemp -d)"
  trap 'rm -rf "$temp_dir"' EXIT
  echo "download: $archive"
  curl -L --fail --show-error --output "$temp_dir/$archive" "$runtime_base/$archive"
  tar -xzf "$temp_dir/$archive" -C "$temp_dir"
  found="$(find "$temp_dir" -type f -name "$library" | head -n 1)"
  if [[ -z "$found" ]]; then
    echo "runtime library not found: $library" >&2
    exit 1
  fi
  cp "$found" "$runtime_dir/$library"
fi

echo "Hey Jarvis assets ready: $assets_dir"
