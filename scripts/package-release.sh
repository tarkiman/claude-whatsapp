#!/usr/bin/env bash
# Cross-compiles bridge + admin and packs one tarball per architecture into
# ./dist — the layout scripts/quick-install.sh expects. Used by
# .github/workflows/release.yml, and runnable locally to test an install
# without publishing a release.
#
# Usage:
#   scripts/package-release.sh <version> [arch...]    # arch: arm64 armv7 amd64
#   scripts/package-release.sh v0.1.0                 # all three
#   scripts/package-release.sh dev arm64              # just one, for local testing
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: $0 <version> [arch...]}"
shift || true
ARCHES=("$@")
[ "${#ARCHES[@]}" -gt 0 ] || ARCHES=(arm64 armv7 amd64)

OUT="dist"
rm -rf "$OUT"
mkdir -p "$OUT"

for arch in "${ARCHES[@]}"; do
	case "$arch" in
	arm64) goarch=arm64 goarm="" ;;
	armv7) goarch=arm goarm=7 ;;
	amd64) goarch=amd64 goarm="" ;;
	*)
		echo "unknown arch: $arch (choose from: arm64 armv7 amd64)" >&2
		exit 1
		;;
	esac

	name="claude-whatsapp-${VERSION}-${arch}"
	pkg="$OUT/$name"
	echo "==> $name"
	mkdir -p "$pkg/bin" "$pkg/deploy" "$pkg/scripts" "$pkg/docs"

	for cmd in bridge admin; do
		GOOS=linux GOARCH="$goarch" GOARM="$goarm" CGO_ENABLED=0 \
			go build -trimpath -ldflags="-s -w" -o "$pkg/bin/$cmd" "./cmd/$cmd"
	done

	cp docker-compose.yml .env.example README.md README.id.md LICENSE "$pkg/"
	cp deploy/*.template "$pkg/deploy/"
	cp scripts/install.sh scripts/deploy.sh scripts/setup-whisper.sh "$pkg/scripts/"
	cp docs/ARCHITECTURE.md docs/ARCHITECTURE.id.md "$pkg/docs/"
	chmod +x "$pkg"/bin/* "$pkg"/scripts/*.sh
	echo "$VERSION" >"$pkg/VERSION"

	tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
	rm -rf "$pkg"
done

ls -lh "$OUT"
