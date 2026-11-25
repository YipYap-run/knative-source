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

# Writes published image digests into the chart's values.yaml so the
# packaged chart deploys repository@digest.
#
# Usage: hack/set-image-digests.sh VALUES_FILE KEY=sha256:HEX [KEY=sha256:HEX ...]
#
# KEY is an entry under images: (controller, webhook, adapter). Each key's
# `digest: ""` line must exist exactly once, else the script fails and
# leaves the file untouched. Edits in place, keeping comments and layout.

set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 VALUES_FILE KEY=sha256:HEX [KEY=sha256:HEX ...]" >&2
  exit 2
fi

values=$1
shift
if [ ! -f "$values" ]; then
  echo "error: $values does not exist" >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
cp "$values" "$tmp"

for pair in "$@"; do
  key=${pair%%=*}
  digest=${pair#*=}
  case "$key" in
    controller | webhook | adapter) ;;
    *)
      echo "error: unknown image key '$key' (want controller, webhook or adapter)" >&2
      exit 1
      ;;
  esac
  if [[ ! "$digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    echo "error: digest for $key is not sha256:<64 hex>: '$digest'" >&2
    exit 1
  fi

  out=$(mktemp)
  # Track the top-level images: block and the two-space-indented key
  # inside it, then rewrite the four-space-indented digest line.
  if ! awk -v key="$key" -v digest="$digest" '
    BEGIN { in_images = 0; in_key = 0; done = 0 }
    /^[^ #]/ { in_images = ($0 ~ /^images:[ \t]*$/); in_key = 0 }
    in_images && /^  [^ #]/ { in_key = ($0 ~ ("^  " key ":[ \t]*$")) }
    in_key && /^    digest:[ \t]*""[ \t]*$/ {
      print "    digest: \"" digest "\""
      done++
      next
    }
    { print }
    END { if (done != 1) exit 1 }
  ' "$tmp" >"$out"; then
    rm -f "$out"
    echo "error: images.$key.digest: \"\" not found exactly once in $values" >&2
    exit 1
  fi
  mv "$out" "$tmp"
done

cat "$tmp" >"$values"
