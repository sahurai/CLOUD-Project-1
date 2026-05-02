#!/usr/bin/env sh
#
# Prepare local model files for Docker Compose and Kubernetes deployments.
#
# The application expects model weights in ./models, while large weights are
# usually shipped as models.zip to keep the working tree easier to move around.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
MODEL_DIR="$ROOT/models"
MODEL_ZIP="$ROOT/models.zip"

mkdir -p "$MODEL_DIR"

if find "$MODEL_DIR" -maxdepth 1 -type f \( -name '*.h5' -o -name '*.keras' -o -name '*.pb' \) | grep -q .; then
    echo "Models already present in $MODEL_DIR"
    exit 0
fi

if [ ! -f "$MODEL_ZIP" ]; then
    echo "ERROR: models.zip not found and no models are present in $MODEL_DIR" >&2
    exit 1
fi

echo "Extracting models from $MODEL_ZIP into $MODEL_DIR..."
unzip -o "$MODEL_ZIP" -d "$MODEL_DIR"
echo "Models ready:"
find "$MODEL_DIR" -maxdepth 1 -type f \( -name '*.h5' -o -name '*.keras' -o -name '*.pb' \) -print
