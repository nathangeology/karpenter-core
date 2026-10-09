#!/usr/bin/env bash
#
# karpenter-lint.sh -- Pre-review lint checks for Karpenter PRs
#
# Runs automated checks from the quality gates checklist against changed .go files.
# Checks are derived from recurring review feedback by jmdeal, DerekFrank,
# jonathan-innis, and njtran.
#
# Fork-local. This is not an upstream file. It encodes reviewer conventions
# observed on kubernetes-sigs/karpenter PRs, not project build steps, and
# nothing in the Makefile or in CI invokes it. Drop it from any branch opened
# against upstream.
#
# Usage:
#   ./karpenter-lint.sh              # diff against main
#   ./karpenter-lint.sh origin/main  # diff against specific base
#   BASE_SHA=<sha> ./karpenter-lint.sh   # same, for callers that set it in the env
#
# Pass an explicit base. The default 'main' is a local ref, so a stale local
# main widens the diff to every commit main is behind, and the three-dot diff
# cannot see uncommitted work. Prefer the parent SHA of the commit under
# review.
#
# Exit codes:
#   0  All checks passed, or the diff touched no .go files
#   1  Violations found, or the base does not resolve

set -uo pipefail

# Positional wins, then either env name, then 'main'. The env forms are read
# because callers reach for them and a base that is read as 'main' instead of
# as what the caller passed reports violations against files they never touched.
BASE_BRANCH="${1:-${BASE_BRANCH:-${BASE_SHA:-main}}}"
VIOLATIONS=0
WARNINGS=0

# Colors (disable if not a terminal)
if [ -t 1 ]; then
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    GREEN='\033[0;32m'
    BOLD='\033[1m'
    NC='\033[0m'
else
    RED='' YELLOW='' GREEN='' BOLD='' NC=''
fi

# An unresolvable base makes every git diff below fail, which reads as an empty
# file list and exits 0 with every check reported as passed. Refuse it instead,
# so "no .go files changed" and "the base could not be read" are distinguishable
# by exit code.
if ! git rev-parse --verify --quiet "${BASE_BRANCH}^{commit}" >/dev/null 2>&1; then
    echo -e "${RED}${BOLD}FAIL${NC} base '${BASE_BRANCH}' does not resolve to a commit in this worktree." >&2
    echo -e "Pass the parent SHA of the commit under review, e.g. ./karpenter-lint.sh HEAD~1" >&2
    exit 1
fi

# --- Helpers ---

fail() {
    echo -e "${RED}FAIL${NC} [$1] $2"
    VIOLATIONS=$((VIOLATIONS + 1))
}

warn() {
    echo -e "${YELLOW}WARN${NC} [$1] $2"
    WARNINGS=$((WARNINGS + 1))
}

pass() {
    echo -e "${GREEN}PASS${NC} [$1]"
}

# Get changed .go files (excludes deleted files)
changed_go_files() {
    git diff --name-only --diff-filter=d "${BASE_BRANCH}"... -- '*.go' 2>/dev/null || \
    git diff --name-only --diff-filter=d "${BASE_BRANCH}" -- '*.go' 2>/dev/null || true
}

# Get changed non-test .go files
changed_prod_files() {
    changed_go_files | grep -v '_test\.go$' || true
}

# Get changed test files
changed_test_files() {
    changed_go_files | grep '_test\.go$' || true
}

# Get only the added/modified lines in changed files (new code only)
# Usage: changed_lines <file>
changed_lines() {
    git diff "${BASE_BRANCH}"... -- "$1" 2>/dev/null | grep '^+' | grep -v '^+++' || \
    git diff "${BASE_BRANCH}" -- "$1" 2>/dev/null | grep '^+' | grep -v '^+++' || true
}

# Get the post-image line numbers of the added lines in a changed file.
# Needed by checks that read the whole file (to find a construct spanning
# several lines) but should only report what this diff touched.
# Usage: added_line_numbers <file>
added_line_numbers() {
    { git diff -U0 "${BASE_BRANCH}"... -- "$1" 2>/dev/null || \
      git diff -U0 "${BASE_BRANCH}" -- "$1" 2>/dev/null || true; } | awk '
        /^@@/ {
            if (match($0, /\+[0-9]+(,[0-9]+)?/)) {
                spec = substr($0, RSTART + 1, RLENGTH - 1)
                n = split(spec, a, ",")
                start = a[1] + 0
                count = (n > 1 ? a[2] + 0 : 1)
                for (i = 0; i < count; i++) print start + i
            }
        }'
}

# --- Gather files ---

PROD_FILES=$(changed_prod_files)
TEST_FILES=$(changed_test_files)
ALL_FILES=$(changed_go_files)

if [ -z "$ALL_FILES" ]; then
    echo -e "${GREEN}No changed .go files against ${BASE_BRANCH}. Nothing to check.${NC}"
    exit 0
fi

echo -e "${BOLD}Karpenter Lint -- checking $(echo "$ALL_FILES" | wc -l | tr -d ' ') changed .go files against ${BASE_BRANCH}${NC}"
echo ""

# =============================================================================
# P0 CHECKS -- Block every PR
# =============================================================================

echo -e "${BOLD}--- P0: Critical checks ---${NC}"

# A1: Error + requeue anti-pattern
# Flag: returning RequeueAfter alongside a non-nil error
check_error_requeue() {
    local found=0
    if [ -z "$PROD_FILES" ]; then
        pass "A1: error+requeue"
        return
    fi
    while IFS= read -r file; do
        # Look for lines returning both RequeueAfter and err in the same return
        results=$(changed_lines "$file" | grep -n 'RequeueAfter:' || true)
        if [ -n "$results" ]; then
            # Check if any of these lines also return an error (not nil)
            while IFS= read -r line; do
                # Get surrounding context -- look for return statements with both RequeueAfter and non-nil error
                if echo "$line" | grep -qE 'RequeueAfter:.*,\s*(err|fmt\.Errorf|errors\.)'; then
                    fail "A1: error+requeue" "$file: returning error alongside RequeueAfter (controller-runtime will warn)"
                    echo "    $line"
                    found=1
                fi
            done <<< "$results"
        fi
    done <<< "$PROD_FILES"
    [ "$found" -eq 0 ] && pass "A1: error+requeue"
}

# A2: Missing optimistic lock on status patches
check_optimistic_lock() {
    local found=0
    if [ -z "$PROD_FILES" ]; then
        pass "A2: optimistic-lock"
        return
    fi
    while IFS= read -r file; do
        # Find Status().Patch() calls in changed lines
        status_patches=$(changed_lines "$file" | grep -n 'Status()\.Patch\|Status().Patch' || true)
        if [ -n "$status_patches" ]; then
            # Check if OptimisticLock appears anywhere in the changed lines of this file
            has_lock=$(changed_lines "$file" | grep -c 'OptimisticLock' || true)
            if [ "$has_lock" -eq 0 ]; then
                fail "A2: optimistic-lock" "$file: Status().Patch() without OptimisticLock"
                echo "    Review: ensure MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}) is used"
                found=1
            fi
        fi
    done <<< "$PROD_FILES"
    [ "$found" -eq 0 ] && pass "A2: optimistic-lock"
}

# A3: time.Now() in production code
check_time_now() {
    local found=0
    if [ -z "$PROD_FILES" ]; then
        pass "A3: time.Now()"
        return
    fi
    while IFS= read -r file; do
        results=$(changed_lines "$file" | grep -n 'time\.Now()' || true)
        if [ -n "$results" ]; then
            fail "A3: time.Now()" "$file: use injected clock interface instead of time.Now()"
            echo "$results" | head -5 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$PROD_FILES"
    [ "$found" -eq 0 ] && pass "A3: time.Now()"
}

# A4: fmt.Errorf with colon before %w instead of comma-space
check_errorf_style() {
    local found=0
    if [ -z "$ALL_FILES" ]; then
        pass "A4: errorf-style"
        return
    fi
    while IFS= read -r file; do
        # Match fmt.Errorf("...: %w" -- colon before %w
        results=$(changed_lines "$file" | grep -n 'fmt\.Errorf(' | grep ': %w' || true)
        if [ -n "$results" ]; then
            fail "A4: errorf-style" "$file: use comma-space before %%w, not colon (convention: fmt.Errorf(\"context, %%w\", err))"
            echo "$results" | head -5 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$ALL_FILES"
    [ "$found" -eq 0 ] && pass "A4: errorf-style"
}

echo ""

# =============================================================================
# P1 CHECKS -- Common issues
# =============================================================================

echo -e "${BOLD}--- P1: Common issues ---${NC}"

# A5: fmt.Printf / fmt.Println in test files
check_printf_tests() {
    local found=0
    if [ -z "$TEST_FILES" ]; then
        pass "A5: printf-in-tests"
        return
    fi
    while IFS= read -r file; do
        results=$(changed_lines "$file" | grep -n 'fmt\.Printf\|fmt\.Println' || true)
        if [ -n "$results" ]; then
            fail "A5: printf-in-tests" "$file: use Expect() assertions or structured logging, not Printf/Println"
            echo "$results" | head -5 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$TEST_FILES"
    [ "$found" -eq 0 ] && pass "A5: printf-in-tests"
}

# A6: lo.ForEach usage (blanket ban)
check_lo_foreach() {
    local found=0
    if [ -z "$ALL_FILES" ]; then
        pass "A6: lo.ForEach"
        return
    fi
    while IFS= read -r file; do
        results=$(changed_lines "$file" | grep -n 'lo\.ForEach' || true)
        if [ -n "$results" ]; then
            fail "A6: lo.ForEach" "$file: use standard for loops instead of lo.ForEach"
            echo "$results" | head -5 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$ALL_FILES"
    [ "$found" -eq 0 ] && pass "A6: lo.ForEach"
}

# A7: lo.Values / lo.Filter usage (warn for review, may be hot path)
check_lo_hotpath() {
    local found=0
    if [ -z "$ALL_FILES" ]; then
        pass "A7: lo.Values/Filter"
        return
    fi
    while IFS= read -r file; do
        results=$(changed_lines "$file" | grep -n 'lo\.Values\|lo\.Filter' || true)
        if [ -n "$results" ]; then
            warn "A7: lo.Values/Filter" "$file: review for unnecessary allocations (iterate maps directly, use explicit loops)"
            echo "$results" | head -5 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$ALL_FILES"
    [ "$found" -eq 0 ] && pass "A7: lo.Values/Filter"
}

# A8: Double-logging (log.Error + return err in same function)
check_double_log() {
    local found=0
    if [ -z "$PROD_FILES" ]; then
        pass "A8: double-log"
        return
    fi
    while IFS= read -r file; do
        # Check if changed lines contain both .Error(err and return.*err patterns
        has_log_err=$(changed_lines "$file" | grep -c '\.Error(err' || true)
        has_return_err=$(changed_lines "$file" | grep -c 'return.*fmt\.Errorf\|return.*err$\|return.*err)' || true)
        if [ "$has_log_err" -gt 0 ] && [ "$has_return_err" -gt 0 ]; then
            warn "A8: double-log" "$file: may be logging AND returning errors (controller-runtime auto-logs returned errors)"
            found=1
        fi
    done <<< "$PROD_FILES"
    [ "$found" -eq 0 ] && pass "A8: double-log"
}

echo ""

# =============================================================================
# P2 CHECKS -- Nice to have
# =============================================================================

echo -e "${BOLD}--- P2: Style and hygiene ---${NC}"

# A9: time.Sleep in test files
check_sleep_tests() {
    local found=0
    if [ -z "$TEST_FILES" ]; then
        pass "A9: sleep-in-tests"
        return
    fi
    while IFS= read -r file; do
        results=$(changed_lines "$file" | grep -n 'time\.Sleep' || true)
        if [ -n "$results" ]; then
            warn "A9: sleep-in-tests" "$file: prefer Eventually/Consistently or fakeClock.Step() over time.Sleep"
            echo "$results" | head -5 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$TEST_FILES"
    [ "$found" -eq 0 ] && pass "A9: sleep-in-tests"
}

# A10: Exported test-only functions
# This is a best-effort check: finds exported funcs in prod files that are new
# and checks if they appear in test files only.
check_test_only_exports() {
    local found=0
    if [ -z "$PROD_FILES" ]; then
        pass "A10: test-only-exports"
        return
    fi
    while IFS= read -r file; do
        # Extract new exported function names from changed lines
        new_exports=$(changed_lines "$file" | grep -oE '^[+]func \([^)]+\) ([A-Z][a-zA-Z0-9]+)\(' | grep -oE '[A-Z][a-zA-Z0-9]+' || true)
        new_exports="$new_exports"$'\n'$(changed_lines "$file" | grep -oE '^[+]func ([A-Z][a-zA-Z0-9]+)\(' | grep -oE '[A-Z][a-zA-Z0-9]+' || true)
        new_exports=$(echo "$new_exports" | sort -u | grep -v '^$' || true)
        if [ -n "$new_exports" ]; then
            while IFS= read -r func_name; do
                [ -z "$func_name" ] && continue
                # Skip common names that are always fine
                case "$func_name" in
                    New|NewController|Name|Register|Reconcile|String) continue ;;
                esac
                # Check if the function is ONLY referenced in test files
                prod_refs=$(grep -rl --include='*.go' --exclude='*_test.go' "$func_name" . 2>/dev/null | wc -l | tr -d ' ')
                test_refs=$(grep -rl --include='*_test.go' "$func_name" . 2>/dev/null | wc -l | tr -d ' ')
                # If referenced in tests but not in other prod files (besides its definition), flag it
                if [ "$test_refs" -gt 0 ] && [ "$prod_refs" -le 1 ]; then
                    warn "A10: test-only-exports" "$file: exported func $func_name appears only in test files"
                    found=1
                fi
            done <<< "$new_exports"
        fi
    done <<< "$PROD_FILES"
    [ "$found" -eq 0 ] && pass "A10: test-only-exports"
}

# A11: make(map[...]) for empty initialization
check_make_map() {
    local found=0
    if [ -z "$ALL_FILES" ]; then
        pass "A11: make-map"
        return
    fi
    while IFS= read -r file; do
        # Match make(map[...]) but not make(map[...], size) -- only flag zero-size init
        results=$(changed_lines "$file" | grep -n 'make(map\[' | grep -v 'make(map\[.*\].*,' || true)
        if [ -n "$results" ]; then
            warn "A11: make-map" "$file: prefer map[K]V{} over make(map[K]V) for empty initialization"
            echo "$results" | head -3 | while IFS= read -r line; do
                echo "    $line"
            done
            found=1
        fi
    done <<< "$ALL_FILES"
    [ "$found" -eq 0 ] && pass "A11: make-map"
}

# A12: hardcoded metric value literal at an emission site
#
# Convention, AGENTS.md "Metric Labels": a value that exists only as a metric
# value should be a first-class metrics.Value var that its emission site
# references by .Name. The shape that breaks it is an accessor named after a
# declared metric dimension returning a bare string literal. No value-equality
# test can catch that one: when the literal equals the var's Name, pointing the
# return at the var is an equivalent mutant, so a linter is the only gate.
#
# Candidate accessor names are read out of the tree rather than hardcoded. A
# `<X>Label = "<dimension>"` const means the value-producing accessor is named
# `<X>`: ConsolidationTypeLabel -> ConsolidationType(), ReasonLabel ->
# Reason(). Measured on upstream/main 637bceba: 18 such consts, 4 hits over
# every .go file in pkg/ and test/, 3 of them real (the consolidation_type
# empty-string sites) and 1 a nil guard whose literal reaches no series.
#
# WARN, not FAIL. A nil guard returning "" is this pattern and is legitimate,
# so a reviewer has to make the call.
check_metric_value_literal() {
    local found=0
    if [ -z "$PROD_FILES" ]; then
        pass "A12: metric-value-literal"
        return
    fi
    local names
    names=$(git grep -hoE '[A-Za-z_][A-Za-z0-9_]*Label[[:space:]]*=[[:space:]]*"' -- '*.go' ':!*_test.go' 2>/dev/null \
            | sed -E 's/[[:space:]]*=[[:space:]]*"$//; s/Label$//' | sort -u | paste -sd'|' - || true)
    if [ -z "$names" ]; then
        pass "A12: metric-value-literal"
        return
    fi
    while IFS= read -r file; do
        [ -z "$file" ] && continue
        [ -f "$file" ] || continue
        # Report the literal returns inside a dimension-named accessor's body.
        # The body can span lines, so this reads the file, not the diff.
        hits=$(awk -v names="$names" '
            !inside && $0 ~ ("^func \\([^)]*\\) (" names ")\\(\\)") { inside = 1; depth = 0 }
            inside {
                if ($0 ~ "return[[:space:]]+\"") print FNR ":" $0
                n = gsub(/\{/, "{"); m = gsub(/\}/, "}")
                depth += n - m
                if (depth <= 0 && (n + m) > 0) inside = 0
            }' "$file" || true)
        [ -z "$hits" ] && continue
        added=$(added_line_numbers "$file")
        [ -z "$added" ] && continue
        while IFS= read -r hit; do
            [ -z "$hit" ] && continue
            lineno=${hit%%:*}
            if echo "$added" | grep -qx "$lineno"; then
                warn "A12: metric-value-literal" "$file:$lineno: accessor named after a metric dimension returns a bare string literal; declare a metrics.Value and return its .Name"
                echo "    ${hit#*:}"
                found=1
            fi
        done <<< "$hits"
    done <<< "$PROD_FILES"
    [ "$found" -eq 0 ] && pass "A12: metric-value-literal"
}

echo ""

# =============================================================================
# Run all checks
# =============================================================================

check_error_requeue
check_optimistic_lock
check_time_now
check_errorf_style
check_printf_tests
check_lo_foreach
check_lo_hotpath
check_double_log
check_sleep_tests
check_test_only_exports
check_make_map
check_metric_value_literal

# =============================================================================
# Summary
# =============================================================================

echo ""
echo -e "${BOLD}--- Summary ---${NC}"
echo -e "Violations (FAIL): ${VIOLATIONS}"
echo -e "Warnings  (WARN):  ${WARNINGS}"

if [ "$VIOLATIONS" -gt 0 ]; then
    echo ""
    echo -e "${RED}${BOLD}${VIOLATIONS} violation(s) found. Fix before submitting for review.${NC}"
    exit 1
elif [ "$WARNINGS" -gt 0 ]; then
    echo ""
    echo -e "${YELLOW}${WARNINGS} warning(s). Review these before submitting -- they may be fine but reviewers will ask about them.${NC}"
    exit 0
else
    echo ""
    echo -e "${GREEN}${BOLD}All checks passed.${NC}"
    exit 0
fi
