#!/bin/sh
# Stop hook: before the agent ends its turn, run the project's tests, unless
# they already passed for exactly these changes. Failing output goes back to
# the model (exit 2) so it keeps working; Larik allows five such rounds a turn.
input=$(cat)
session=$(printf '%s' "$input" | sed -n 's/.*"session_id" *: *"\([^"]*\)".*/\1/p' | head -1)
cd "${LARIK_PROJECT_DIR:-.}" || exit 0

if [ -n "${VERIFY_CMD:-}" ]; then cmd=$VERIFY_CMD
elif [ -f go.mod ]; then cmd='go test ./...'
elif [ -f package.json ]; then cmd='npm test --silent'
elif [ -f Cargo.toml ]; then cmd='cargo test'
elif [ -f pyproject.toml ] || [ -f pytest.ini ]; then cmd='pytest -q'
else exit 0; fi

# A fingerprint of the working tree, so a turn that changed nothing since the
# last green run doesn't pay for the tests again. Without git, always run.
state=
if git rev-parse --git-dir >/dev/null 2>&1; then
  state="${TMPDIR:-/tmp}/larik-verified-${session:-none}"
  now=$({ git status --porcelain; git diff HEAD; git ls-files -o --exclude-standard -z | xargs -0 cksum; } 2>/dev/null | cksum)
  [ "$(cat "$state" 2>/dev/null)" = "$now" ] && exit 0
fi

if out=$(sh -c "$cmd" 2>&1); then
  [ -n "$state" ] && printf '%s\n' "$now" > "$state"
  exit 0
fi
printf 'Tests failed (%s). Fix them before you finish:\n%s\n' "$cmd" "$(printf '%s\n' "$out" | tail -n 40)" >&2
exit 2
