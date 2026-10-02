# Compila o WinBoost.exe (Windows, PowerShell)
$ErrorActionPreference = "Stop"
go build -trimpath -ldflags "-H windowsgui -s -w" -o WinBoost.exe .
Write-Host "Pronto: WinBoost.exe"
