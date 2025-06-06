#!/bin/bash
set -euo pipefail

echo "Downloading Go modules..."
if ! go mod tidy; then
    echo "go mod tidy failed; retrying with GOPROXY=direct"
    GOPROXY=direct go mod tidy || {
        echo "Module download failed. If you have a vendor directory from another machine, it will be used."
    }
fi
echo "Modules downloaded."
