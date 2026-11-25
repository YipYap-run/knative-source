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

# Fails unless none of the given image references exist in the registry.
# The release runs this before pushing so a published tag is never
# overwritten, whether by a re-run or a workflow_dispatch of an old tag.
#
# Usage: hack/release-tag-absent.sh REPO:TAG [REPO:TAG ...]
#
# Needs crane on PATH (or $CRANE). Only a registry answer of
# MANIFEST_UNKNOWN or NAME_UNKNOWN counts as absent; any other error
# (auth, network, rate limit) fails closed.

set -uo pipefail

crane=${CRANE:-crane}

if [ "$#" -lt 1 ]; then
  echo "usage: $0 REPO:TAG [REPO:TAG ...]" >&2
  exit 2
fi

status=0
for ref in "$@"; do
  if out=$("$crane" digest "$ref" 2>&1); then
    echo "::error::$ref already exists ($out); refusing to overwrite a published tag. Cut a new version instead."
    status=1
    continue
  fi
  if grep -q -e MANIFEST_UNKNOWN -e NAME_UNKNOWN <<<"$out"; then
    echo "$ref is not published yet"
    continue
  fi
  echo "::error::could not tell whether $ref exists, refusing to publish:"
  echo "$out"
  status=1
done
exit "$status"
