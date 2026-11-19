#!/usr/bin/env bash
# Writes the number of Go tests and the statement coverage into README.md (the badges and
# the line in the header, "Project status") and docs/development.md (the table per package).
# Usage: make coverage-docs, or bash scripts/coverage.sh <report>
#   <report>: the output of "go test -coverprofile=<file> ./..." followed by the "total:"
#   line of "go tool cover -func=<file>".
# Run it after every change of the Go code, so the numbers in the documents stay true.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
report="${1:?usage: coverage.sh <report>}"
total="$(awk '/^total:/ { sub("%", "", $NF); print $NF }' "$report")"
tests="$(find "$root/cmd" "$root/internal" -name '*_test.go' -exec cat {} + | grep -c '^func Test')"
packages="$(awk '$1 == "ok" && /coverage:/ { sub("^proximity/", "", $2); for (i = 3; i <= NF; i++)
    if ($i == "coverage:") { sub("%", "", $(i + 1)); print $2, $(i + 1) } }' "$report")"
if [ -z "$total" ] || [ -z "$packages" ] || grep -q '^FAIL' "$report"; then
    echo "coverage: the report '$report' has no numbers or has failed tests" >&2
    exit 1
fi
lowest="$(echo "$packages" | awk '{ print $2 }' | sort -n | head -n 1)"
highest="$(echo "$packages" | awk '{ print $2 }' | sort -n | tail -n 1)"
rules=(-e "s/tests-[0-9]*%20passing/tests-$tests%20passing/"
    -e "s/coverage-[0-9.]*%25/coverage-$total%25/"
    -e "s/<b>[0-9]*<\/b> Go tests, <b>[0-9.]*%<\/b> statement coverage/<b>$tests<\/b> Go tests, <b>$total%<\/b> statement coverage/"
    -e "s/[0-9]* Go tests pass, total statement coverage [0-9.]*% ([0-9.]*–[0-9.]*% per/$tests Go tests pass, total statement coverage $total% ($lowest–$highest% per/"
    -e "s/\*\*[0-9]* tests, all passing, [0-9.]*% total statement coverage\.\*\*/**$tests tests, all passing, $total% total statement coverage.**/")
while read -r name value; do
    rules+=(-e "s#^| \`$name\` | [0-9.]*% |#| \`$name\` | $value% |#")
done <<< "$packages"
for file in README.md docs/development.md docs/status.md; do
    sed -i.bak "${rules[@]}" "$root/$file"
    rm -f "$root/$file.bak"
done
echo "coverage: $tests tests, $total% of statements ($lowest–$highest% per package)"
