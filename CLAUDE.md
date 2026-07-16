# CLAUDE.md
- The spec is ./wirelog_frd_phase1.md. It is the single source of truth.
  If anything conflicts with it, stop and ask — do not silently choose.
- PLAN.md tracks all work. Update task statuses IN THE SAME COMMIT as the
  work itself. Never mark a task done if its tests don't pass.
- Work stops at the end of each stage for human review. Do not start the
  next stage without explicit approval.
- Run ./quality.sh and `go test -race ./...` before declaring any task done.
- Conventional Commits. No dependencies beyond those the FRD allows.
