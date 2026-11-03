#!/bin/sh
# Builds a sideloadable plugin release into OUT: OUT/<version>/ and OUT/latest/, each with rubi-plugin.json, one
# archive per target and SHA256SUMS. Core installs a sideloaded plugin from <source>/latest/ and updates it from
# <source>/<version>/, so both must exist. Signing SHA256SUMS (SHA256SUMS.sig) is a separate step with the
# publisher key, done by its holder; this script never touches a key.
#
#   scripts/build-dist.sh VERSION OUT [PUBLISHER_KEY]
set -eu
version=${1:?version, e.g. v0.1.0}
out=${2:?output folder}
key=${3:-}
case "$version" in v[0-9]*) ;; *) echo "version must look like v1.2.3" >&2; exit 2 ;; esac
plugin=t3agents
here=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
ldflags="-s -w -buildid= -X github.com/Deikus-LXXVII/rubi/sdk/rubiplugin.Version=$version"
[ -n "$key" ] && ldflags="$ldflags -X main.PublisherKey=$key"
dir="$out/$version"
rm -rf "$dir" "$out/latest"
mkdir -p "$dir"
cd "$here"
for target in ${TARGETS:-linux/amd64 linux/arm64 darwin/arm64}; do
	os=${target%/*}; arch=${target#*/}
	mkdir -p "$work/$os-$arch"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$work/$os-$arch/$plugin" .
	tar -C "$work/$os-$arch" -czf "$dir/$plugin-$os-$arch.tar.gz" "$plugin"
done
go build -trimpath -ldflags "$ldflags" -o "$work/host" .
"$work/host" --manifest > "$dir/rubi-plugin.json"
(cd "$dir" && if command -v sha256sum >/dev/null; then sha256sum rubi-plugin.json "$plugin"-*.tar.gz; else shasum -a 256 rubi-plugin.json "$plugin"-*.tar.gz; fi > SHA256SUMS)
cp -R "$dir" "$out/latest"
echo "built $plugin $version into $dir and $out/latest (unsigned)"
