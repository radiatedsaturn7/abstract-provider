#!/bin/bash
set -euo pipefail

echo "Downloading Go modules..."
go mod tidy

echo "Modules downloaded."
