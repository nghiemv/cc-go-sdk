#!/usr/bin/env bash
#
# Verify that every cc-sdk CLI command has a corresponding wrapper
# in the Python and Java clients.
#
# This catches drift: maintainer adds a command to cmd/cc-sdk/main.go
# but forgets to update the language clients → CI fails.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXIT_CODE=0

# ---------------------------------------------------------------------------
# 1. Extract serve-mode commands from the Go CLI dispatch
#    (these are the commands in dispatchServeRequest, excluding "shutdown")
# ---------------------------------------------------------------------------
CLI_COMMANDS=$(grep -oP '(?<=case ")[^"]+' "$REPO_ROOT/cmd/cc-sdk/main.go" \
    | sort -u \
    | grep -v -E '^(serve|shutdown)$')

echo "=== cc-sdk CLI commands ==="
echo "$CLI_COMMANDS"
echo

# ---------------------------------------------------------------------------
# 2. Check Python client (cc.py)
#    Each command should appear as a string in a _request() call
# ---------------------------------------------------------------------------
echo "=== Python client coverage ==="
MISSING_PY=""
for cmd in $CLI_COMMANDS; do
    if ! grep -q "\"$cmd\"" "$REPO_ROOT/clients/python/cc.py"; then
        MISSING_PY="$MISSING_PY  $cmd\n"
    fi
done

if [ -n "$MISSING_PY" ]; then
    echo "MISSING in clients/python/cc.py:"
    echo -e "$MISSING_PY"
    EXIT_CODE=1
else
    echo "OK — all commands covered"
fi
echo

# ---------------------------------------------------------------------------
# 3. Check Java client (CcSdk.java)
#    Each command should appear as a string in a request() call
# ---------------------------------------------------------------------------
echo "=== Java client coverage ==="
MISSING_JAVA=""
for cmd in $CLI_COMMANDS; do
    if ! grep -q "\"$cmd\"" "$REPO_ROOT/clients/java/src/main/java/mil/army/usace/cc/CcSdk.java"; then
        MISSING_JAVA="$MISSING_JAVA  $cmd\n"
    fi
done

if [ -n "$MISSING_JAVA" ]; then
    echo "MISSING in clients/java/.../CcSdk.java:"
    echo -e "$MISSING_JAVA"
    EXIT_CODE=1
else
    echo "OK — all commands covered"
fi
echo

# ---------------------------------------------------------------------------
if [ $EXIT_CODE -ne 0 ]; then
    echo "FAIL: Client coverage check found gaps. Update the clients above."
else
    echo "PASS: All clients cover all commands/methods."
fi

exit $EXIT_CODE
