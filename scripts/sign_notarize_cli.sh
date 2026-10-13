#!/usr/bin/env bash
# Sign the GoReleaser macOS binaries, notarize their exact bytes, then rebuild
# the release archives. This runs before any public asset is uploaded.
set -euo pipefail
set +x

version=${1:?release version required}
dist=${2:?archive directory required}
[[ "$(uname -s)" == Darwin ]] || { echo 'Developer ID signing requires macOS' >&2; exit 1; }
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'invalid release version' >&2; exit 1; }
[[ "${APPLE_TEAM_ID:-}" =~ ^[A-Z0-9]{10}$ ]] || { echo 'missing or invalid Apple team ID' >&2; exit 1; }
: "${APPLE_DEVELOPER_ID_P12_BASE64:?missing Developer ID certificate}"
: "${APPLE_DEVELOPER_ID_P12_PASSWORD:?missing certificate password}"
: "${APPLE_NOTARY_APPLE_ID:?missing notary Apple ID}"
: "${APPLE_NOTARY_PASSWORD:?missing notary password}"

dist=$(cd "$dist" && pwd)
bash "$(dirname "$0")/verify_release_archives.sh" "$version" "$dist" --names-only
umask 077
temp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/caelis-cli-signing.XXXXXX")
keychain="$temp/release.keychain-db"
cleanup() {
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$temp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s' "$APPLE_DEVELOPER_ID_P12_BASE64" | base64 --decode > "$temp/identity.p12"
unset APPLE_DEVELOPER_ID_P12_BASE64
keychain_password=$(openssl rand -hex 32)
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 9000 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$temp/identity.p12" -k "$keychain" \
  -P "$APPLE_DEVELOPER_ID_P12_PASSWORD" -T /usr/bin/codesign >/dev/null
unset APPLE_DEVELOPER_ID_P12_PASSWORD
rm "$temp/identity.p12"
security set-key-partition-list -S apple-tool:,apple:,codesign: -s \
  -k "$keychain_password" "$keychain" >/dev/null
unset keychain_password

# Include the public Developer ID intermediate on runners without it.
curl --fail --silent --show-error --location \
  https://www.apple.com/certificateauthority/DeveloperIDG2CA.cer -o "$temp/AppleG2.cer"
printf '%s  %s\n' f16cd3c54c7f83cea4bf1a3e6a0819c8aaa8e4a1528fd144715f350643d2df3a \
  "$temp/AppleG2.cer" | shasum -a 256 --check
security import "$temp/AppleG2.cer" -k "$keychain" >/dev/null
identities=$(security find-identity -v -p codesigning "$keychain")
identity=$(printf '%s\n' "$identities" | sed -n 's/^[[:space:]]*[0-9][0-9]*) \([A-Fa-f0-9]\{40\}\) "Developer ID Application: .*"$/\1/p')
[[ -n "$identity" && "$identity" != *$'\n'* ]] || { echo 'expected exactly one valid Developer ID Application identity' >&2; exit 1; }

mkdir -p "$temp/notary"
for arch in amd64 arm64; do
  archive="$dist/caelis_${version}_darwin_${arch}.tar.gz"
  target="$temp/$arch"
  mkdir -p "$target" "$temp/notary/$arch"
  tar -xzf "$archive" -C "$target"
  binary="$target/caelis"
  [[ -f "$binary" && ! -L "$binary" ]] || { echo "missing regular macOS CLI in $archive" >&2; exit 1; }
  expected_arch=$arch
  [[ "$arch" != amd64 ]] || expected_arch=x86_64
  [[ "$(lipo -archs "$binary")" == "$expected_arch" ]] || { echo "wrong Mach-O architecture in $archive" >&2; exit 1; }
  codesign --force --sign "$identity" --keychain "$keychain" \
    --options runtime --timestamp "$binary"
  codesign --verify --strict --verbose=2 "$binary"
  codesign -dvvv "$binary" 2> "$temp/$arch-signature.txt"
  grep -F "TeamIdentifier=$APPLE_TEAM_ID" "$temp/$arch-signature.txt" >/dev/null
  grep -E '^CodeDirectory .*flags=.*runtime' "$temp/$arch-signature.txt" >/dev/null
  grep -E '^Timestamp=' "$temp/$arch-signature.txt" >/dev/null
  cp "$binary" "$temp/notary/$arch/caelis"
done

ditto -c -k --keepParent "$temp/notary" "$temp/notary.zip"
xcrun notarytool store-credentials caelis-cli-release --keychain "$keychain" \
  --apple-id "$APPLE_NOTARY_APPLE_ID" --team-id "$APPLE_TEAM_ID" \
  --password "$APPLE_NOTARY_PASSWORD" >/dev/null
unset APPLE_NOTARY_APPLE_ID APPLE_NOTARY_PASSWORD
mkdir -p "$dist/notarization"
xcrun notarytool submit "$temp/notary.zip" \
  --keychain-profile caelis-cli-release --keychain "$keychain" \
  --wait --timeout 60m --output-format json > "$dist/notarization/result.json"
submission=$(/usr/bin/python3 - "$dist/notarization/result.json" <<'PY'
import json, sys
result = json.load(open(sys.argv[1]))
if result.get('status') != 'Accepted':
    raise SystemExit('Apple did not accept the macOS CLI submission: ' + str(result.get('status')))
print(result['id'])
PY
)
xcrun notarytool log "$submission" \
  --keychain-profile caelis-cli-release --keychain "$keychain" \
  "$dist/notarization/log.json"
notary_digest=$(shasum -a 256 "$temp/notary.zip" | cut -d ' ' -f 1)
/usr/bin/python3 - "$dist/notarization/log.json" "$submission" "$notary_digest" <<'PY'
import json, sys
log = json.load(open(sys.argv[1]))
if (log.get('status'), log.get('jobId'), log.get('sha256')) != ('Accepted', sys.argv[2], sys.argv[3]):
    raise SystemExit('Apple notarization log does not match the submitted ZIP bytes')
PY

# The ZIP is a submission container. Apple cannot staple ZIPs or bare Mach-O
# executables; Gatekeeper retrieves the accepted ticket online by code hash.
for arch in amd64 arm64; do
  archive="$dist/caelis_${version}_darwin_${arch}.tar.gz"
  tar -czf "$archive" -C "$temp/$arch" .
done
(
  cd "$dist"
  shasum -a 256 "caelis_${version}_"*.tar.gz > checksums.txt
)
bash "$(dirname "$0")/verify_release_archives.sh" "$version" "$dist"
echo 'Both Darwin CLIs are Developer ID signed, hardened, timestamped and Apple notarized; final archives and checksums are ready.'
