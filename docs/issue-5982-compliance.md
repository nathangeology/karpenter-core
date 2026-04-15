# Enhancement Issue #5982 — Template Compliance Checklist

Issue: https://github.com/kubernetes/enhancements/issues/5982
Template: `.github/ISSUE_TEMPLATE/enhancement.md`

## Template Required Fields

The kubernetes/enhancements issue template requires the following sections under
**"Enhancement Description"**:

| # | Required Field | Present? | Notes |
|---|---------------|----------|-------|
| 1 | One-line enhancement description (release-note style) | ❌ Missing | Issue has a multi-paragraph Summary instead of a single-line description |
| 2 | Kubernetes Enhancement Proposal link | ❌ Missing | Issue body says "KEP: TBD" — needs link to KEP file or PR in k/enhancements |
| 3 | Discussion Link (SIG mailing list thread, meeting, or recording) | ❌ Missing | No link to sig-apps discussion where this was presented |
| 4 | PRs by stage and milestone — Alpha checklist | ❌ Missing | Template requires Alpha checkbox block with KEP PR, Code PR, and Docs PR links |
| 5 | PRs by stage and milestone — Beta/Stable (commented out) | ❌ Missing | Template includes commented-out Beta/Stable sections to uncomment later |

## What the Issue Currently Has (not in template)

The issue body contains free-form sections that are **not part of the template**:
- `## Summary` — detailed description (template wants a one-liner)
- `## Motivation` — background context (belongs in the KEP document, not the tracking issue)
- `## Key Design Points` — design details (belongs in the KEP document)
- `## Related` — links to related issues/KEPs
- `/sig apps` slash command (correct, but should accompany the template body)

## What Needs to Be Added

1. **One-line enhancement description**: Write a single sentence suitable for release notes, e.g.:
   > Add a consolidation-aware pod deletion heuristic to the ReplicaSet controller that prefers removing pods from nodes with fewer active pods during scale-down.

2. **KEP link**: Once the KEP PR is filed in `keps/sig-apps/5982-replicaset-consolidation-scale-in/`, link it here. Until then, link the KEP PR.

3. **Discussion link**: Link to the sig-apps meeting, mailing list thread, or Slack discussion where this enhancement was presented.

4. **Alpha PRs checklist**: Add the standard checkbox block:
   ```
   - [ ] Alpha - v1.37
     - [ ] KEP (`k/enhancements`) update PR(s):
     - [ ] Code (`k/k`) update PR(s):
     - [ ] Docs (`k/website`) update PR(s):
   ```

5. **Move detailed content to KEP**: The Summary, Motivation, and Key Design Points sections belong in the KEP document (`kep.yaml` + `README.md` in the KEP directory), not in the tracking issue. The tracking issue should be lightweight per the template.

## Reviewer Comment Summary

A reviewer asked the author to:
- Reformat the issue body to match the enhancement tracking template
- Remove "KEP:" from the title (done — issue numbers are KEP identifiers)
- File the actual KEP as a PR following the standard process at https://github.com/kubernetes/enhancements/blob/master/README.md
