#!/usr/bin/env bash

set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
assets_dir="${1:-$project_dir/wakeword-micro}"
python_bin="${PYTHON_BIN:-python3}"
model_base="https://raw.githubusercontent.com/splastunov/microwakeword-ru-model-train/main"
openwakeword_base="https://github.com/dscripka/openWakeWord/releases/download/v0.5.1"

mkdir -p "$assets_dir"

if [[ ! -f "$assets_dir/Kuza.json" ]]; then
  echo "download: Kuza.json"
  curl -L --fail --show-error --output "$assets_dir/Kuza.json" "$model_base/Kuza.json"
fi

if [[ ! -f "$assets_dir/Kuza.tflite" ]]; then
  echo "download: Kuza.tflite"
  curl -L --fail --show-error --output "$assets_dir/Kuza.tflite" "$model_base/Kuza.tflite"
fi

for model in melspectrogram.tflite embedding_model.tflite; do
  if [[ ! -f "$assets_dir/$model" ]]; then
    echo "download: $model"
    curl -L --fail --show-error --output "$assets_dir/$model" "$openwakeword_base/$model"
  fi
done

if [[ ! -x "$assets_dir/venv/bin/python" && ! -x "$assets_dir/venv/Scripts/python.exe" ]]; then
  "$python_bin" -m venv "$assets_dir/venv"
fi

venv_python="$assets_dir/venv/bin/python"
if [[ ! -x "$venv_python" ]]; then
  venv_python="$assets_dir/venv/Scripts/python.exe"
fi

"$venv_python" -m pip install --disable-pip-version-check --quiet \
  "openwakeword==0.6.0" \
  "pymicro-wakeword==2.4.1"

echo "Russian wake word ready: Куза ($assets_dir)"
