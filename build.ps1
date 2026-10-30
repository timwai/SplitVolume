# Run at the project root with Go 1.22+ installed. PowerShell 5.1 / 7.
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED='0'
New-Item -ItemType Directory -Path dist -Force | Out-Null
& go test ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
$targets = @(
    @{OS='windows'; Arch='amd64'; Output='SplitVolume.exe'; Package='./cmd/split7z'},
    @{OS='windows'; Arch='arm64'; Output='SplitVolume-Windows-ARM64.exe'; Package='./cmd/split7z'},
    @{OS='linux'; Arch='amd64'; Output='extract-linux-amd64'; Package='./cmd/extract'},
    @{OS='linux'; Arch='arm64'; Output='extract-linux-arm64'; Package='./cmd/extract'},
    @{OS='linux'; Arch='arm'; Arm='7'; Output='extract-linux-armv7'; Package='./cmd/extract'}
)
foreach ($target in $targets) {
    $env:GOOS = $target.OS
    $env:GOARCH = $target.Arch
    if ($target.Arm) { $env:GOARM = $target.Arm } else { Remove-Item Env:GOARM -ErrorAction SilentlyContinue }
    $flags = '-s -w'
    if ($target.OS -eq 'windows') { $flags = '-H windowsgui -s -w' }
    & go build -ldflags $flags -o (Join-Path 'dist' $target.Output) $target.Package
    if ($LASTEXITCODE -ne 0) { throw "Build failed: $($target.Output)" }
}
Write-Host 'Compiled into ./dist/'
