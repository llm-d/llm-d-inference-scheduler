#!/usr/bin/env bash

# Exit on error
set -e

# Use the first argument as the base, default to upstream/main
BASE_BRANCH=${1:-upstream/main}

# Find the merge base to define the PR range
BASE=$(git merge-base HEAD "$BASE_BRANCH")
RANGE="$BASE..HEAD"

echo "Checking $(git rev-list --count "$RANGE") commits ($BASE..HEAD)..."

ERR_SIG=""
ERR_DCO=""

for commit in $(git rev-list "$RANGE"); do
    HASH=$(git rev-parse --short "$commit")
    
    # 1. Check for cryptographic signature header (gpgsig)
    # Using cat-file bypasses the SSH/GPG verification engine entirely
    if ! git cat-file commit "$commit" | grep -q "^gpgsig"; then
        ERR_SIG="$ERR_SIG $HASH"
    fi
    
    # 2. Check for DCO Sign-off text in the commit message
    if ! git log -1 --format="%b" "$commit" | grep -q "Signed-off-by:"; then
        ERR_DCO="$ERR_DCO $HASH"
    fi
done

# Report failures
if [ -z "$ERR_SIG" ] && [ -z "$ERR_DCO" ]; then
    echo "All commits are cryptographically signed and have DCO sign-offs."
    exit 0
else
    [ -n "$ERR_SIG" ] && echo "Missing Crypto Signature (gpgsig):$ERR_SIG"
    [ -n "$ERR_DCO" ] && echo "Missing DCO Sign-off (Signed-off-by):$ERR_DCO"
    echo ""
    echo "To fix, use: git rebase --exec 'git commit --amend --no-edit -S -s' -i $BASE_BRANCH"
    exit 1
fi
