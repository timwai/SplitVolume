#!/bin/sh
# Usage: ./extract.sh backup.tar.gz.001 ./output
set -eu
if [ "$#" -lt 1 ]; then
  printf '%s\n' 'Usage: ./extract.sh /path/backup.tar.gz.001 [output-directory]' >&2
  exit 2
fi
BASE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$(uname -m)" in
  x86_64|amd64) EXTRACTOR="$BASE/extract-linux-amd64" ;;
  aarch64|arm64) EXTRACTOR="$BASE/extract-linux-arm64" ;;
  armv7l|armv7*) EXTRACTOR="$BASE/extract-linux-armv7" ;;
  *) printf '%s\n' 'Unsupported Linux architecture (use cat + tar if installed).' >&2; exit 3 ;;
esac
if [ ! -f "$EXTRACTOR" ]; then
  printf 'Extractor is missing: %s\n' "$EXTRACTOR" >&2
  exit 4
fi
exec "$EXTRACTOR" "$1" "${2:-.}"
