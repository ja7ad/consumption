#!/bin/bash

TAG=$(git describe --tags --abbrev=0 2>/dev/null)
if [ -z "$TAG" ]; then
    TAG="0.0.0-devel"
fi

if git rev-parse --short HEAD >/dev/null 2>&1; then
    COMMIT=$(git rev-parse --short HEAD)
else
    COMMIT="unknown"
fi

# Ensure single leading v
if [[ $TAG != v* ]]; then
  BASE="v${TAG}"
else
  BASE="$TAG"
fi

VERSION="${BASE}-${COMMIT}"
echo "$VERSION"
