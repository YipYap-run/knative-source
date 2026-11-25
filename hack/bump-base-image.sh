#!/usr/bin/env bash

# Copyright 2026 The YipYap Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Repins the ko base image in .ko.yaml to the current digest of
# cgr.dev/chainguard/static:latest. Every pinned reference to that image
# moves together, and the "Current pin" comment gets today's date.
#
# Usage: hack/bump-base-image.sh [KO_YAML]
#
# Prints the new digest on stdout when the file changed and nothing when it
# was already current. The digest comes from `crane digest` (crane on PATH
# or $CRANE); BASE_IMAGE_DIGEST overrides the lookup for tests.

set -euo pipefail

ko_yaml=${1:-.ko.yaml}
image=cgr.dev/chainguard/static
crane=${CRANE:-crane}

if [ ! -f "$ko_yaml" ]; then
  echo "error: $ko_yaml does not exist" >&2
  exit 1
fi

digest=${BASE_IMAGE_DIGEST:-$("$crane" digest "$image:latest")}
if [[ ! "$digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  echo "error: $image:latest digest is not sha256:<64 hex>: '$digest'" >&2
  exit 1
fi

pins=$(grep -o "$image@sha256:[a-f0-9]\{64\}" "$ko_yaml" | sort -u || true)
if [ -z "$pins" ]; then
  echo "error: no $image@sha256 pin found in $ko_yaml" >&2
  exit 1
fi
if [ "$pins" = "$image@$digest" ]; then
  exit 0
fi

today=$(date -u +%Y-%m-%d)
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
sed -e "s|$image@sha256:[a-f0-9]\{64\}|$image@$digest|g" \
  -e "s|^\(# Current pin: $image:latest as of \)[0-9-]*\.|\1$today.|" \
  "$ko_yaml" >"$tmp"
cat "$tmp" >"$ko_yaml"
echo "$digest"
