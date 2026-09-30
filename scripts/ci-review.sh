#!/usr/bin/env bash
# Review a pull request with Larik and post the result as a comment.
# Used by the GitHub Action (action.yml).
#
#   REVIEW_COMMAND  review (default) or security-review
#   REVIEW_TARGET   what to review: a PR number, a branch, or a range such
#                   as origin/main...HEAD (default: $PR_NUMBER)
#   REVIEW_FOCUS    optional: what to concentrate on
#   PR_NUMBER       the pull request to comment on (no comment without it)
#   POST_COMMENT    true (default) to comment on the pull request
#   LARIK_MODEL     provider/model (default: Larik's, from the API key set)
#   LARIK_BIN       the larik binary (default: larik on PATH)
#   GH_TOKEN        token for gh: to read the pull request and to comment
#
# The model runs in plan mode: it can read the checked-out code, and
# nothing else. It can't run commands, edit files or use the token.
set -euo pipefail

die() {
  printf 'larik review: %s\n' "$*" >&2
  exit 1
}

command=${REVIEW_COMMAND:-review}
target=${REVIEW_TARGET:-${PR_NUMBER:-}}
focus=${REVIEW_FOCUS:-}
larik=${LARIK_BIN:-larik}

case $command in
  review | security-review) ;;
  *) die "unknown command: $command (review or security-review)" ;;
esac
command -v "$larik" >/dev/null 2>&1 || die "$larik not found; run scripts/ci-install.sh first"
[[ -n $target ]] || die 'nothing to review: set REVIEW_TARGET, or run on a pull_request event'
# The target becomes part of the prompt's first word group; keep it to what
# a PR number, branch or range can contain.
[[ $target =~ ^[#A-Za-z0-9._/~^@-]+$ ]] || die "unexpected characters in the review target: $target"

prompt="/$command $target"
[[ -z $focus ]] || prompt+=" $focus"
args=(-p --mode plan)
[[ -z ${LARIK_MODEL:-} ]] || args+=(--model "$LARIK_MODEL")

out=$(mktemp "${TMPDIR:-/tmp}/larik-review.XXXXXXXX")
body=$(mktemp "${TMPDIR:-/tmp}/larik-comment.XXXXXXXX")
trap 'rm -f "$out" "$body"' EXIT

printf 'larik review: running %s\n' "$prompt"
"$larik" "${args[@]}" "$prompt" >"$out" || die 'larik failed; see the log above'
[[ -s $out ]] || die 'larik returned no review'

title='Larik review'
[[ $command == review ]] || title='Larik security review'
marker="<!-- larik-$command -->"
{
  printf '%s\n## %s\n\n' "$marker" "$title"
  # GitHub caps a comment at 65,536 characters.
  head -c 60000 "$out"
  if [[ $(wc -c <"$out") -gt 60000 ]]; then
    printf '\n\n_The review was cut here; the full text is in the workflow log._\n'
  fi
  printf '\n\n<sub>%s · %s' "$("$larik" --version)" "$prompt"
  [[ -z ${GITHUB_SHA:-} ]] || printf ' · %s' "${GITHUB_SHA:0:7}"
  printf '</sub>\n'
} >"$body"

cat "$out"
if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  cat "$body" >>"$GITHUB_STEP_SUMMARY"
fi
if [[ -n ${GITHUB_OUTPUT:-} ]]; then
  delim="LARIK_REVIEW_$RANDOM$RANDOM"
  {
    printf 'review<<%s\n' "$delim"
    cat "$out"
    printf '\n%s\n' "$delim"
  } >>"$GITHUB_OUTPUT"
fi

if [[ ${POST_COMMENT:-true} != true ]]; then
  exit 0
fi
if [[ -z ${PR_NUMBER:-} || -z ${GITHUB_REPOSITORY:-} ]]; then
  printf 'larik review: not on a pull request, so no comment was posted\n'
  exit 0
fi
command -v gh >/dev/null 2>&1 || die 'the GitHub CLI (gh) is needed to post the comment'

# Update this command's earlier comment instead of adding one per push.
comments="repos/$GITHUB_REPOSITORY/issues/$PR_NUMBER/comments"
existing=$(gh api "$comments" --paginate --jq ".[] | select(.body | startswith(\"$marker\")) | .id" | tail -n1)
if [[ -n $existing ]]; then
  gh api -X PATCH "repos/$GITHUB_REPOSITORY/issues/comments/$existing" -F "body=@$body" --silent
  printf 'larik review: updated the comment on pull request #%s\n' "$PR_NUMBER"
else
  gh api -X POST "$comments" -F "body=@$body" --silent
  printf 'larik review: commented on pull request #%s\n' "$PR_NUMBER"
fi
