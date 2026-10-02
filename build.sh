#!/usr/bin/env bash
# Compila o WinBoost.exe (compilacao cruzada a partir de Linux/macOS)
set -e
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui -s -w" -o WinBoost.exe .
echo "Pronto: WinBoost.exe"
