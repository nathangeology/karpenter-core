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
#
# Pass an explicit base. The default 'main' is a local ref, so a stale local
# main widens the diff to every commit main is behind, and the three-dot diff
# cannot see uncommitted work. Prefer the parent SHA of the commit under
# review.
#
# Exit codes:
#   0  All checks passed
#   1  Violations found

set -uo pipefail

BASE_BRANCH="${1:-main}"
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
