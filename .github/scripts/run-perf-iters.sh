#!/usr/bin/env bash
#
# Run the perf iteration loop for a single suite. On each iteration,
# invoke `make e2etests` and move the per-iteration performance-report
# JSONs into $OUTPUT_DIR/iter_${i}/. Extracted from .github/workflows/e2e.yaml
# so the loop can be invoked from any runner (GitHub Actions or otherwise).
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
#
# Exit codes:
#   Non-zero if any iteration fails (set -e) or a required var is unset.

set -euo pipefail

: "${OUTPUT_DIR:?OUTPUT_DIR must be set by caller}"
: "${SUITE:?SUITE must be set}"
: "${ITERATIONS:?ITERATIONS must be set}"

export OUTPUT_DIR
export FOCUS="${FOCUS:-}"

for i in $(seq 1 "$ITERATIONS"); do
  echo "=== Iteration $i / $ITERATIONS ==="
  TEST_SUITE="$SUITE" make e2etests
  iter_dir="$OUTPUT_DIR/iter_${i}"
  mkdir -p "$iter_dir"
  # Move reports from this iteration into a per-iteration subdirectory.
  find "$OUTPUT_DIR" -maxdepth 1 -name "*_performance_report.json" \
    -exec mv {} "$iter_dir/" \;
done
