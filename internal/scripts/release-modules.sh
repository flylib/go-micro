#!/bin/sh
# Prepares a release of every module in this repository at one version.
#
#   internal/scripts/release-modules.sh v1.0.0          # rewrite requires, build-check
#   internal/scripts/release-modules.sh v1.0.0 --tag    # ...and create the tags locally
#
# The plugins depend on the root module and on each other. Consumers can
# only fetch them when those requires name real versions: Go ignores the
# replace directives of a dependency's go.mod. So every in-repo require is
# set to the release version, and every module is tagged at the same
# commit: v1.0.0 for the root, <dir>/v1.0.0 for a module in <dir>.
#
# The replace directives stay: they keep builds inside the repository on
# the local code (with or without go.work) and do not affect consumers.
# examples/ modules are rewritten too but not tagged; nobody imports them.
#
# Pushing is left to the caller: git push origin main && git push origin --tags
set -eu

version="${1:?usage: $0 vX.Y.Z [--tag]}"
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "version must look like v1.2.3, got $version" >&2; exit 1 ;;
esac
tag=false
[ "${2:-}" = "--tag" ] && tag=true

root=$(git rev-parse --show-toplevel)
cd "$root"

if [ -n "$(git status --porcelain)" ]; then
  echo "working tree not clean; commit or stash first" >&2
  exit 1
fi

mods=$(git ls-files '*go.mod' | sed 's|/\{0,1\}go.mod$||; s|^$|.|' | sort)

# 1. every require of a module of this repo -> $version
for dir in $mods; do
  f="$dir/go.mod"
  [ "$dir" = "." ] && f=go.mod
  # rewrite "github.com/flylib/go-micro[/...] <any version>" in require lines,
  # leaving replace lines (which contain "=>") alone
  awk -v v="$version" '
    !/=>/ && $0 ~ /^[ \t]*(require[ \t]+)?github\.com\/flylib\/go-micro(\/[^ \t]+)?[ \t]+v/ {
      sub(/[ \t]v[^ \t]+/, " " v)
    }
    { print }
  ' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
done

# 2. each module must build on its own, without go.work
failed=""
for dir in $mods; do
  if ! (cd "$dir" && GOWORK=off go build ./... >/dev/null 2>&1); then
    failed="$failed $dir"
  fi
done
if [ -n "$failed" ]; then
  echo "build failed without go.work in:$failed" >&2
  exit 1
fi
echo "all $(echo "$mods" | wc -w | tr -d ' ') modules build at $version without go.work"

if [ -z "$(git status --porcelain)" ]; then
  echo "requires already at $version"
else
  git commit -q -am "chore: require $version for all in-repo modules"
  echo "committed: chore: require $version for all in-repo modules"
fi

# 3. tags: root and every non-example module
tags=""
for dir in $mods; do
  case "$dir" in examples/*) continue ;; esac
  if [ "$dir" = "." ]; then t="$version"; else t="$dir/$version"; fi
  tags="$tags $t"
done
echo "tags ($(echo "$tags" | wc -w | tr -d ' ')):$tags" | fold -w 120

if $tag; then
  for t in $tags; do
    git tag -a "$t" -m "$t"
  done
  echo "tags created locally; push with: git push origin main && git push origin --tags"
fi
