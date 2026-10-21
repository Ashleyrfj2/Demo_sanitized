#!/usr/bin/env bash
set -euo pipefail

# Guarded Integration Test Runner Script
# Establishes a fresh disposable integration database, applies migrations,
# runs the Go backend test suite, and cleans up the disposable database.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
cd "$REPO_DIR"

: "${TEST_DATABASE_URL:?Set an isolated, disposable test database URL before integration tests}"

echo "============================================================"
echo "1. Initializing fresh disposable test database..."
echo "============================================================"
"$SCRIPT_DIR/test-db-reset.sh"

cleanup() {
  local exit_code=$?
  echo "============================================================"
  echo "Cleaning up disposable test database (qa_platform_test)..."
  echo "============================================================"
  docker compose exec -T postgres psql -U qa_platform -d qa_platform -c "DROP DATABASE IF EXISTS qa_platform_test WITH (FORCE);" > /dev/null 2>&1 || true
  exit $exit_code
}

trap cleanup EXIT

echo "============================================================"
echo "2. Running Go backend integration test suite..."
echo "============================================================"

# Run test suite inside golang container against 127.0.0.1:54332 via host networking
docker run --rm --net=host -e TEST_DATABASE_URL="$TEST_DATABASE_URL" -v "$REPO_DIR":/app -w /app golang:1.23 go test -v ./backend/internal/...

echo "============================================================"
echo "ALL BACKEND INTEGRATION TESTS PASSED CLEANLY"
echo "============================================================"
