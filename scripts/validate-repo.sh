#!/bin/sh

set -eu

cd "$(dirname "$0")/.."

for path in README.md LICENSE .github/workflows/ci.yml; do
  if [ ! -f "$path" ]; then
    echo "Missing required file: $path" >&2
    exit 1
  fi
done

for forbidden in AGENTS.md .moltenhub-agents- prompt-images/; do
  if git ls-files --error-unmatch "$forbidden" >/dev/null 2>&1; then
    echo "Forbidden generated artifact is tracked: $forbidden" >&2
    exit 1
  fi
done

if git ls-files | grep -Eq '^prompt-images/'; then
  echo "Forbidden generated artifacts are tracked under prompt-images/" >&2
  exit 1
fi

echo "Repository validation passed."
