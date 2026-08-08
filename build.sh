#!/bin/bash

set -euo pipefail

IMAGE="cdmatta/go-bench-suite:latest"
RUN_IMAGE=false

[[ "${1:-}" == "run" ]] && RUN_IMAGE=true

docker build -t cdmatta/go-bench-suite:latest --no-cache .

[[ "$RUN_IMAGE" == true ]] && docker run --rm -it -p 8000:8000 "$IMAGE" upstream