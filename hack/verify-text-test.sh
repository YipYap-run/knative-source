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

# Runs hack/verify-text.sh against throwaway git repos: a clean tree must
# pass, each kind of violation must fail under its own heading, and a git
# grep error must fail instead of reporting OK.

set -o errexit
set -o nounset
set -o pipefail

script="$(cd "$(dirname "$0")" && pwd)/verify-text.sh"
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT

fails=0

# repo NAME: create a git repo holding a copy of the script, print its path.
repo() {
  local dir="${work}/$1"
  mkdir -p "${dir}/hack"
  cp "${script}" "${dir}/hack/verify-text.sh"
  git -C "${dir}" init -q
  printf '%s\n' "$dir"
}

# run DIR: stage everything and run the script, print "exit=N" then output.
run() {
  git -C "$1" add -A
  local rc=0 out
  out=$(cd "$1" && bash hack/verify-text.sh 2>&1) || rc=$?
  printf 'exit=%s\n%s\n' "${rc}" "${out}"
}

# expect NAME WANT_EXIT WANT_TEXT DIR
expect() {
  local got
  got=$(run "$4")
  if ! grep -q "^exit=$2\$" <<<"${got}" || ! grep -qF -- "$3" <<<"${got}"; then
    echo "FAIL $1: want exit $2 and \"$3\", got:"
    awk '{ print "  " $0 }' <<<"${got}"
    fails=1
  else
    echo "ok   $1"
  fi
}

d=$(repo clean)
printf '// Copyright 2026 The YipYap Authors\n// Plain comment, no dashes.\n' >"${d}/a.go"
printf '/*\nCopyright 2019 The Knative Authors.\n*/\n' >"${d}/b.go"
expect clean 0 "OK" "${d}"

d=$(repo emdash)
printf '// one %s two\n' "$(printf '\342\200\224')" >"${d}/a.go"
expect em-dash 1 "em or en dash" "${d}"

d=$(repo entity)
printf 'a &mdash; b\n' >"${d}/a.md"
expect mdash-entity 1 "em or en dash" "${d}"

d=$(repo phase)
printf '# Used by the Phase-4 controller.\n' >"${d}/a.yaml"
expect phase 1 "planning marker" "${d}"

d=$(repo person)
printf '// Copyright 2026 Some Person.\n' >"${d}/a.go"
expect person-header 1 "license header" "${d}"

d=$(repo starred)
printf '/*\n * Copyright (c) 2026 Some Person\n */\n' >"${d}/a.go"
expect starred-c-header 1 "license header" "${d}"

d=$(repo words)
printf 'A robust design.\n' >"${d}/a.md"
expect word-list 1 "writing-style list" "${d}"

# A broken git grep (here an unknown pathspec magic) must not pass.
d=$(repo broken)
printf 'fine\n' >"${d}/a.md"
sed -i.bak 's/":(exclude)LICENSE"/":(exclude)LICENSE" ":(nosuchmagic)x"/' "${d}/hack/verify-text.sh"
rm -f "${d}/hack/verify-text.sh.bak"
expect grep-error 2 "git grep failed" "${d}"

if [[ ${fails} -ne 0 ]]; then
  exit 1
fi
echo "hack/verify-text-test.sh: OK"
