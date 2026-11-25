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

# Checks the tracked first-party text (Go, YAML, Markdown, shell, workflows)
# against the repo writing rules:
#   - no em or en dashes
#   - no internal planning markers such as "Phase-4"
#   - license headers name "The YipYap Authors" or the upstream
#     "The Knative Authors", never a person
#   - none of the overused words in the last check below
#
# vendor/, third_party/ and LICENSE are upstream text and are skipped.
# Run from anywhere inside the repo. Exits 1 and prints each offending line.

set -o errexit
set -o nounset
set -o pipefail

cd "$(git rev-parse --show-toplevel)"

self="hack/verify-text.sh"
paths=(
  .
  ":(exclude)vendor"
  ":(exclude)third_party"
  ":(exclude)LICENSE"
  ":(exclude)${self}"
  # The self-test writes bad lines on purpose as fixtures.
  ":(exclude)hack/verify-text-test.sh"
)

failed=0

# search [git grep args...]: print matching lines. git grep exits 1 when
# nothing matches, which is fine; any other failure (a bad pattern or
# pathspec) stops the script so a broken check never reports OK.
search() {
  local rc=0
  git grep -n -I "$@" -- "${paths[@]}" || rc=$?
  if [[ ${rc} -gt 1 ]]; then
    echo "hack/verify-text.sh: git grep failed (exit ${rc})" >&2
    exit 2
  fi
}

# report NAME OUTPUT: print the offending lines under a heading.
report() {
  if [[ -n "$2" ]]; then
    echo "$1:"
    awk '{ print "  " $0 }' <<<"$2"
    failed=1
  fi
}

# check NAME [git grep args...]: report every match and remember the failure.
check() {
  local name="$1"
  shift
  local out
  out=$(search "$@")
  report "${name}" "${out}"
}

em_dash=$(printf '\342\200\224')
en_dash=$(printf '\342\200\223')

check "em or en dash (use a comma, colon, period or parentheses)" \
  -e "${em_dash}" -e "${en_dash}" -e '&mdash;' -e '&ndash;'

check "planning marker" -i -E -e 'phase[- ]?[0-9]'

# Every "Copyright <year>" line must name one of the two project authors,
# whatever comment style or "(c)" mark it uses. The Apache License
# boilerplate ("copyright owner") lives only in LICENSE files, which are
# excluded above.
copyright_sign=$(printf '\302\251')
headers=$(search -i -E -e "copyright[[:space:]]+((\\(c\\)|${copyright_sign})[[:space:]]+)?[0-9]{4}")
report "license header that does not name The YipYap Authors or The Knative Authors" \
  "$(grep -v -E 'Copyright [0-9]{4} The (YipYap|Knative) Authors\.?$' <<<"${headers}" || true)"

check "word from the writing-style list" -i -w -E \
  -e 'delve[sd]?' -e 'delving' -e 'leverag(e|es|ed|ing)' -e 'robust(ly|ness)?' \
  -e 'seamless(ly)?' -e 'streamlin(e|es|ed|ing)'

if [[ ${failed} -ne 0 ]]; then
  echo
  echo "hack/verify-text.sh: fix the lines above."
  exit 1
fi
echo "hack/verify-text.sh: OK"
