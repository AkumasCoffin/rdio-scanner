#!/usr/bin/env bash
# Fetches the Gradle wrapper jar that ships with a pinned Gradle release.
# Run this once after cloning the repo.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

VERSION="8.10.2"
JAR_PATH="gradle/wrapper/gradle-wrapper.jar"
URL="https://raw.githubusercontent.com/gradle/gradle/v${VERSION}/gradle/wrapper/gradle-wrapper.jar"

if [ -f "$JAR_PATH" ]; then
    echo "Wrapper jar already present at $JAR_PATH"
    exit 0
fi

echo "Downloading gradle-wrapper.jar from $URL"
mkdir -p "$(dirname "$JAR_PATH")"
if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 -o "$JAR_PATH" "$URL"
elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$JAR_PATH" "$URL"
else
    echo "error: need curl or wget to download the wrapper jar" >&2
    exit 1
fi

chmod +x gradlew 2>/dev/null || true
echo "Done. Run ./gradlew tasks to verify."
