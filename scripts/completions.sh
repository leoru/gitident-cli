#!/bin/sh
# Generates shell completion files for release archives.
set -e
rm -rf completions
mkdir completions
go run ./cmd/gitident completion bash > completions/gitident.bash
go run ./cmd/gitident completion zsh > completions/_gitident
go run ./cmd/gitident completion fish > completions/gitident.fish
