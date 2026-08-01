#!/usr/bin/env bash
#
# 2.git-push-dev.sh — run the gates, then commit and push.
#
# The gates run first on purpose: nothing reaches the remote that has not just
# passed lint and the coverage gate locally.
#
# Usage: scripts/2.git-push-dev.sh "commit message"
set -euo pipefail
cd "$(dirname "$0")/.."

if [ $# -lt 1 ]; then
  echo "usage: scripts/2.git-push-dev.sh \"commit message\"" >&2
  exit 2
fi
MESSAGE="$1"

# A tracked .env would be a leaked private key, so this is checked before
# anything is committed rather than after it is pushed.
if git ls-files --error-unmatch .env >/dev/null 2>&1; then
  echo "refusing to push: .env is tracked" >&2
  exit 1
fi

./scripts/1.lint.sh
./scripts/4.coverage-gate.sh

# --show-current rather than rev-parse HEAD, which has no revision to resolve
# on a repository whose first commit has not been made yet.
BRANCH="$(git branch --show-current)"
git add -A
if git diff --cached --quiet; then
  echo "nothing to commit"
else
  git commit -m "$MESSAGE"
fi
git push -u origin "$BRANCH"
echo "pushed $BRANCH"
