#!/usr/bin/env bash
#
# Run the perf iteration loop for a single suite. On each iteration,
# invoke `make e2etests` and move the per-iteration performance-report
# JSONs into $OUTPUT_DIR/iter_${i}/. Extracted from .github/workflows/e2e.yaml
# so the loop can be invoked from any runner (GitHub Actions or otherwise).
#
# The workflow calls this with ITERATIONS=1 and ITER_INDEX set to the matrix
# leg's index, so one runner produces one sample. The loop is kept because it
# is how the script is driven outside CI, where one runner has to produce a
# whole batch.
#
# Required env:
#   OUTPUT_DIR   Directory the caller has created; make e2etests writes
#                *_performance_report.json here and this script moves them
#                into per-iteration subdirectories.
#   SUITE        Ginkgo suite passed through to make e2etests via TEST_SUITE.
#   ITERATIONS   Positive integer; number of times to run the suite.
#
# Optional env:
#   FOCUS        Ginkgo focus pattern; forwarded through the environment to
#                make e2etests (which passes it to the underlying test binary).
#   ITER_INDEX   Label for the single sample this invocation produces. Set by
#                the workflow to the matrix leg index so the sample
#                directories of a batch spread across runners do not collide.
#                Only meaningful with ITERATIONS=1.
#   ATTEMPTS     Positive integer, default 2. Number of tries per iteration
#                before giving up on it.
#
# Exit codes:
#   Non-zero if any iteration exhausts its attempts, or a required var is unset.
#
# Why the retry exists. The upstream per-job failure rate on this workflow is
# 19% (12 of 63 matrix jobs over the 9 non-skipped runs), and Wide Deployments
# alone is 44%. With the batch spread one-sample-per-runner, an unretried leg
# failure rate of 19% leaves a 10-sample batch complete only 12% of the time.
# The retry plus the aggregator's MIN_ITERATIONS quorum are what make a
# distributed batch land. Under the previous single-runner loop a failure also
# forfeited every good iteration already collected and the baseline advance
# with them, because the caller's `actions/cache` save is gated on success.

set -euo pipefail

: "${OUTPUT_DIR:?OUTPUT_DIR must be set by caller}"
: "${SUITE:?SUITE must be set}"
: "${ITERATIONS:?ITERATIONS must be set}"

export OUTPUT_DIR
export FOCUS="${FOCUS:-}"
attempts="${ATTEMPTS:-2}"

# run_iteration executes the suite once and moves its reports into $2. It
# returns non-zero if the suite failed, having still moved whatever reports
# were written, so a partial failure leaves evidence rather than nothing.
run_iteration() {
  local label="$1" iter_dir="$2" rc=0
  TEST_SUITE="$SUITE" make e2etests || rc=$?
  mkdir -p "$iter_dir"
  find "$OUTPUT_DIR" -maxdepth 1 -name "*_performance_report.json" \
    -exec mv {} "$iter_dir/" \;
  if [[ $rc -ne 0 ]]; then
    echo "iteration ${label}: make e2etests exited ${rc}"
  fi
  return $rc
}

for i in $(seq 1 "$ITERATIONS"); do
  label="${ITER_INDEX:-$i}"
  echo "=== Iteration ${label} (local ${i} / ${ITERATIONS}) ==="
  iter_dir="$OUTPUT_DIR/iter_${label}"
  ok=0
  for attempt in $(seq 1 "$attempts"); do
    echo "--- attempt ${attempt} / ${attempts} for iteration ${label} ---"
    # Each attempt starts from an empty sample directory so a retry does not
    # mix a failed attempt's partial reports into the one that succeeded.
    rm -rf "$iter_dir"
    if run_iteration "$label" "$iter_dir"; then
      ok=1
      break
    fi
  done
  if [[ $ok -ne 1 ]]; then
    echo "iteration ${label} failed ${attempts} attempt(s)"
    exit 1
  fi
done
