#!/usr/bin/env bash
set -euo pipefail

# Metrics Validation
#
# Two checks, one per way the NodePool dimension reaches a metric.
#
# Declared in this repository. Any metric carrying the NodePool dimension must be wrapped in
# metrics.NodePoolScoped, so that metrics.DeleteNodePool drops its children once a deleted
# NodePool has no NodeClaims left. Unwrapped, those children outlive the NodePool for the process
# lifetime.
#
# A registry sweep cannot enforce this. prometheus.Registry exposes Gather() but not its
# collectors, and Gather() only returns families that already have a child, so a metric that has
# never been written is invisible at runtime. Hence a source-shape check.
#
# Built by an operatorpkg status controller. status.New*Controller declares its own metrics and
# derives their dimensions from the label keys given to status.WithLabels and the field keys given
# to status.WithFields, so a NodePool dimension arrives with no declaration in this repository for
# the first check to read. operatorpkg's own cleanupNotFound drops three of the six, and keys them
# on the object's namespace and name, so the condition duration, condition transitions and
# termination duration series keyed on a deleted NodePool need
# metrics.NodePoolScoped(Generic)StatusMetrics to go with it.
#
# WithGaugeLabels and WithGaugeFields are deliberately not checked. They feed only the three
# gauges cleanupNotFound already drops, and the wrapper panics on a dimension the three reaped
# metrics do not carry, so requiring it there would turn a non-leak into a startup crash.
#
# Four classes are reported:
#
#   UNSCOPED     a declared metric's label list is an inline []opmetrics.Label{...} that names the
#                NodePool dimension, and the constructor is not wrapped in NodePoolScoped(.
#   OPAQUE       a declared metric's label list is not an inline slice, so the dimension cannot be
#                decided from source. Treated as a finding rather than a pass: an opaque label
#                list evades a source check in exactly the shape this script exists to close.
#   UNREAPED     a status controller is given the NodePool label key and the construction is not
#                wrapped in metrics.NodePoolScoped(Generic)StatusMetrics.
#   OPAQUE_KEYS  an unwrapped status controller's options cannot be read from source, so its
#                NodePool dimension cannot be decided. Three shapes: WithLabels or WithFields
#                names no *LabelKey constant, it spells a key out as a string literal, or an
#                option past the client and recorder is not spelled at the call at all -- a
#                variable, or a slice of options spread with ... -- which would otherwise read as
#                a construction with no object-derived dimension and pass. Same treatment as
#                OPAQUE. Reported only when unwrapped, since a wrapper verifies its own dimension
#                by panicking at construction when the reaped metrics do not carry it.
#
# String literals, rune literals and comments are stripped before the call is parsed. Both
# hazards are live in the tree: Help strings contain the word NodePool, and Help strings contain
# parentheses that would otherwise desync the paren matcher. Stripping is load-bearing, not
# defensive: fed raw lines the parser loses every declaration that follows an unbalanced paren.
#
# Both wrappers are recognised on the constructor's own line only. Spread over two lines they read
# as unwrapped, which is a false FAIL rather than a false pass, so the check stays fail-closed and
# the fix is to join the lines. Only the status wrapper's presence is checked, never its label key
# argument: a wrapper given a key the reaped metrics do not carry panics at construction, which
# needs no gate.
#
# Exemptions are keyed "<path>:<name>", where name is a declared metric's Name literal or a status
# controller's constructor with its type parameter, and each carries its reason. An exemption that
# matches no finding is itself an error, so the list cannot rot into a blanket.

# --census prints the class of every metric declaration the parser saw, including the ones it
# passed, so that a green run can be audited rather than trusted. The gate itself prints only
# findings.
CENSUS=0
[[ "${1:-}" == "--census" ]] && CENSUS=1

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

EXEMPTIONS=$(
  cat <<'EOF'
pkg/controllers/metrics/nodepool/controller.go:limit|metricStore-owned: the controller's metrics.Store ReplaceAll drops stale children every reconcile
pkg/controllers/metrics/nodepool/controller.go:usage|metricStore-owned: the controller's metrics.Store ReplaceAll drops stale children every reconcile
pkg/controllers/metrics/nodepool/controller.go:cost_total|metricStore-owned: the controller's metrics.Store ReplaceAll drops stale children every reconcile
pkg/controllers/pod/deletioncost/metrics.go:nodes_with_pending_annotation_writes|Reset at the top of every cycle, so no child survives a NodePool deletion
pkg/controllers/metrics/node/controller.go:allocatable|metricStore-owned and node-keyed: the label list is nodeLabelNamesWithResourceType(), whose nodepool dimension comes from v1.WellKnownLabels, and metrics.Store ReplaceAll drops stale children
pkg/controllers/metrics/node/controller.go:total_pod_requests|metricStore-owned and node-keyed: see allocatable
pkg/controllers/metrics/node/controller.go:total_pod_limits|metricStore-owned and node-keyed: see allocatable
pkg/controllers/metrics/node/controller.go:total_daemon_requests|metricStore-owned and node-keyed: see allocatable
pkg/controllers/metrics/node/controller.go:total_daemon_limits|metricStore-owned and node-keyed: see allocatable
pkg/controllers/metrics/node/controller.go:system_overhead|metricStore-owned and node-keyed: see allocatable
pkg/controllers/metrics/node/controller.go:current_lifetime_seconds|metricStore-owned and node-keyed: the label list is nodeLabelNames(), same WellKnownLabels source, same metrics.Store
pkg/controllers/metrics/pod/controller.go:state|metricStore-owned and pod-keyed: the label list is labelNames(), which names metrics.NodePool, and metrics.Store Delete drops the child when the pod goes
EOF
)

# -exec {} + rather than a pipe into xargs: GNU xargs runs the command once even on empty input,
# which would leave awk reading stdin forever, while find skips the exec entirely.
FINDINGS=$(
  find pkg -name '*.go' -not -name '*_test.go' -exec awk -v census="${CENSUS}" '
    BEGIN { Q = sprintf("%c", 39) }

    function sanitize(s,   out, i, c, d, n) {
      out = ""; n = length(s); i = 1
      while (i <= n) {
        c = substr(s, i, 1); d = substr(s, i + 1, 1)
        if (inblock) { if (c == "*" && d == "/") { inblock = 0; i += 2 } else { i++ } ; continue }
        if (inraw)   { if (c == "`")  { inraw = 0 } ; i++ ; continue }
        if (instr)   { if (c == "\\") { i += 2; continue } ; if (c == "\"") { instr = 0 } ; i++ ; continue }
        if (inrune)  { if (c == "\\") { i += 2; continue } ; if (c == Q) { inrune = 0 } ; i++ ; continue }
        if (c == "/" && d == "*")  { inblock = 1; i += 2; continue }
        if (c == "/" && d == "/")  { break }
        if (c == "\"")   { instr  = 1; out = out "\"\"";       i++; continue }
        if (c == "`")    { inraw  = 1; out = out "``";         i++; continue }
        if (c == Q)      { inrune = 1; out = out Q Q;          i++; continue }
        out = out c; i++
      }
      return out
    }

    function body(startln, startpos,   depth, ln, s, i, c, acc, opened) {
      depth = 0; opened = 0; acc = ""
      for (ln = startln; ln <= nlines; ln++) {
        s = san[ln]
        i = (ln == startln) ? startpos : 1
        for (; i <= length(s); i++) {
          c = substr(s, i, 1); acc = acc c
          if (c == "(") { depth++; opened = 1 }
          else if (c == ")") { depth--; if (opened && depth == 0) { endln = ln; return acc } }
        }
        acc = acc " "
      }
      endln = -1
      return acc
    }

    # Contents of the first inline []opmetrics.Label{...} in s, brace matched. Sets inline to
    # whether such a slice was present at all, so that an EMPTY slice -- a metric with no
    # dimensions, of which the tree has several -- is not mistaken for an unreadable one.
    function labelslice(s,   at, i, c, depth, out, n) {
      at = index(s, "[]opmetrics.Label{")
      if (at == 0) { inline = 0; return "" }
      inline = 1
      i = at + length("[]opmetrics.Label{") - 1
      depth = 0; out = ""; n = length(s)
      for (; i <= n; i++) {
        c = substr(s, i, 1)
        if (c == "{") { depth++; if (depth == 1) continue }
        else if (c == "}") { depth--; if (depth == 0) return out }
        if (depth >= 1) out = out c
      }
      return out
    }

    function names(s,   t) { t = "#" s "#"; return (t ~ /[^A-Za-z0-9_]NodePool[^A-Za-z0-9_]/) }

    function word(s, w,   t) { t = "#" s "#"; return (t ~ "[^A-Za-z0-9_]" w "[^A-Za-z0-9_]") }

    # Whether any paren matched argument list of a call to fn( in s names the identifier key.
    # Sets opaquekeys when some call names no *LabelKey constant at all, or carries a string
    # literal sanitize has already emptied, so that a key reached through a variable or spelled
    # out is a finding rather than a pass even alongside a readable one.
    function optionkeys(s, fn, key,   needle, from, at, i, c, depth, args, n, found) {
      needle = fn "("
      n = length(s); from = 1; found = 0
      while ((at = index(substr(s, from), needle)) > 0) {
        i = from + at + length(needle) - 2
        depth = 0; args = ""
        for (; i <= n; i++) {
          c = substr(s, i, 1)
          if (c == "(") { depth++; if (depth == 1) continue }
          else if (c == ")") { depth--; if (depth == 0) break }
          if (depth >= 1) args = args c
        }
        if (word(args, key)) found = 1
        else if (args !~ /LabelKey/ || index(args, "\"\"") > 0) opaquekeys = 1
        from = i + 1
        if (from > n) break
      }
      return found
    }

    # Whether every option the construction s takes past its client and its event recorder is
    # spelled out at the call as an <ident>.With*/<ident>.Emit* constant or call. An option
    # reached through a variable, or a slice of options spread with ..., is not readable, and
    # without this it would leave WithLabels( absent from the body and so read as a construction
    # with no object-derived dimension at all -- the shape the two live unwrapped constructions
    # legitimately have.
    function optsreadable(s,   i, c, depth, n, arg, idx, at) {
      at = index(s, "(")
      if (at == 0) return 1
      n = length(s); depth = 0; arg = ""; idx = 0
      for (i = at; i <= n; i++) {
        c = substr(s, i, 1)
        if (c == "(" || c == "[" || c == "{") { depth++; if (depth == 1) continue }
        else if (c == ")" || c == "]" || c == "}") {
          depth--
          if (depth == 0) { return argreadable(arg, idx + 1) }
        }
        if (depth == 1 && c == ",") { idx++; if (!argreadable(arg, idx)) return 0; arg = ""; continue }
        if (depth >= 1) arg = arg c
      }
      return 1
    }

    function argreadable(a, idx) {
      gsub(/^[ \t]+|[ \t]+$/, "", a)
      if (a == "") return 1
      if (idx <= 2) return 1
      if (a ~ /\.\.\.$/) return 0
      return (a ~ /^[A-Za-z_][A-Za-z0-9_]*\.(With|Emit)[A-Za-z0-9_]*/)
    }

    function controllername(s, at,   t) {
      t = substr(s, at)
      if (match(t, /^[A-Za-z_][A-Za-z0-9_]*\.New[A-Za-z]*Controller\[[^]]*\]/)) return substr(t, RSTART, RLENGTH)
      return "<unnamed>"
    }

    function metricname(startln, stopln,   ln, m) {
      for (ln = startln; ln <= stopln; ln++) {
        if (match(raw[ln], /Name: *"[^"]*"/)) {
          m = substr(raw[ln], RSTART, RLENGTH)
          sub(/Name: *"/, "", m); sub(/"$/, "", m)
          if (m != "") return m
        }
      }
      return "<unnamed>"
    }

    FNR == 1 {
      if (NR > 1) process()
      delete raw; delete san; nlines = 0
      instr = inraw = inrune = inblock = 0
      file = FILENAME
    }
    { nlines++; raw[nlines] = $0; san[nlines] = sanitize($0) }
    END { if (NR > 0) process() }

    function process(   ln, s, at, pre, b, slice, cls, nm, wrapped) {
      for (ln = 1; ln <= nlines; ln++) {
        s = san[ln]
        at = match(s, /opmetrics\.NewPrometheus(Counter|Gauge|Histogram|Summary)/)
        if (at > 0) {
          pre = substr(s, 1, at - 1)
          b = body(ln, at)
          if (endln == -1) {
            printf "UNPARSED\t%s\t%d\t%s\t%s\n", file, ln, "<unparsed>", "paren matching ran off the end of the file"
            continue
          }
          nm = metricname(ln, endln)
          slice = labelslice(b)
          if (!inline) {
            cls = "OPAQUE"
          } else if (names(slice)) {
            cls = (pre ~ /NodePoolScoped\([ \t]*$/) ? "OK" : "UNSCOPED"
          } else {
            cls = "OK"
          }
          if (cls != "OK" || census) printf "%s\t%s\t%d\t%s\t%s\n", cls, file, ln, nm, (pre ~ /NodePoolScoped\([ \t]*$/ ? "scoped" : "unwrapped")
          ln = endln
          continue
        }
        at = match(s, /[A-Za-z_][A-Za-z0-9_]*\.New[A-Za-z]*Controller\[/)
        if (at > 0) {
          pre = substr(s, 1, at - 1)
          b = body(ln, at)
          if (endln == -1) {
            printf "UNPARSED\t%s\t%d\t%s\t%s\n", file, ln, "<unparsed>", "paren matching ran off the end of the file"
            continue
          }
          nm = controllername(s, at)
          opaquekeys = 0
          wrapped = (pre ~ /NodePoolScoped(Generic)?StatusMetrics\([^()]*,[ \t]*$/)
          if (!optsreadable(b)) opaquekeys = 1
          if (optionkeys(b, "WithLabels", "NodePoolLabelKey") || optionkeys(b, "WithFields", "NodePoolLabelKey")) {
            cls = wrapped ? "OK" : "UNREAPED"
          } else if (opaquekeys && !wrapped) {
            cls = "OPAQUE_KEYS"
          } else {
            cls = "OK"
          }
          if (cls != "OK" || census) printf "%s\t%s\t%d\t%s\t%s\n", cls, file, ln, nm, (wrapped ? "wrapped" : "unwrapped")
          ln = endln
          continue
        }
      }
    }
  ' {} +
)

if [[ "${CENSUS}" == "1" ]]; then
  printf '%s\n' "${FINDINGS}" | sort
  printf -- '--- class totals ---\n'
  printf '%s\n' "${FINDINGS}" | cut -f1,5 | sort | uniq -c
  exit 0
fi

rc=0
FOUND_KEYS=""

while IFS=$'\t' read -r class path line name _detail; do
  [[ -z "${class}" ]] && continue
  key="${path}:${name}"
  FOUND_KEYS="${FOUND_KEYS}${key}"$'\n'
  if grep -qF -- "${key}|" <<<"${EXEMPTIONS}"; then
    continue
  fi
  case "${class}" in
  UNSCOPED)
    echo "FAIL ${path}:${line} metric \"${name}\" declares the NodePool dimension but is not wrapped in metrics.NodePoolScoped."
    echo "     Its children will outlive a deleted NodePool. Wrap the constructor, or add \"${key}\" to the exemption list in $0 with a reason."
    ;;
  OPAQUE)
    echo "FAIL ${path}:${line} metric \"${name}\" passes a label list this check cannot read, so its NodePool dimension cannot be decided from source."
    echo "     Use an inline []opmetrics.Label{...}, or add \"${key}\" to the exemption list in $0 with a reason."
    ;;
  UNREAPED)
    echo "FAIL ${path}:${line} ${name} gives the NodePool label key to status.WithLabels or status.WithFields but is not wrapped in metrics.NodePoolScopedStatusMetrics."
    echo "     operatorpkg reaps only its three gauges, and only on the object's own deletion, so its condition and termination series will outlive a deleted NodePool. Wrap the construction, or add \"${key}\" to the exemption list in $0 with a reason."
    ;;
  OPAQUE_KEYS)
    echo "FAIL ${path}:${line} ${name} passes options this check cannot read, so its NodePool dimension cannot be decided from source."
    echo "     Name every label key with its constant, as in v1.NodePoolLabelKey, and spell each option out at the call rather than reaching it through a variable or spreading a slice with \"...\". Or add \"${key}\" to the exemption list in $0 with a reason."
    ;;
  *)
    echo "FAIL ${path}:${line} metric \"${name}\": ${class}."
    ;;
  esac
  rc=1
done <<<"${FINDINGS}"

while IFS='|' read -r key reason; do
  [[ -z "${key}" ]] && continue
  if [[ -z "${reason}" ]]; then
    echo "FAIL exemption \"${key}\" in $0 carries no reason."
    rc=1
    continue
  fi
  if ! grep -qxF -- "${key}" <<<"${FOUND_KEYS}"; then
    echo "FAIL exemption \"${key}\" in $0 matches no metric. Remove it."
    rc=1
  fi
done <<<"${EXEMPTIONS}"

exit "${rc}"
