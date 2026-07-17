# CLAUDE.md

## Documentation style (mail-room analogy)
- `doc.go` holds the package overview: ONE unified mail-room analogy mapping
  every component explicitly, technical terms alongside every metaphor.
- Every `.go` file opens with ONE analogy anchor line —
  `// file.go — the <metaphor>: <technical statement>` — and all comments
  below it are purely technical.
- Exception: exactly three landmark lines may carry a short analogy fragment
  (one clause, technical fact first): the non-blocking enqueue in
  transport.go, the body swap in body.go, the drain-on-close in writer.go.
- Tests, the example demo, and README stay literal (README may link to
  doc.go's overview for the narrative).
- Every analogy comment must still read as a complete technical statement
  with the metaphor deleted. The analogy annotates; it never replaces.

## Code style
- Every function — exported, unexported, and test helpers — carries at least
  one sentence describing what it does.
- Descriptive variable names: no cryptic locals (`lk`, `ne`); idiomatic Go
  short names stay (single-letter receivers, `i`/`j` indices, `p []byte` in
  Read, `rc` for a ReadCloser).

- The spec is ./wirelog_frd_phase1.md. It is the single source of truth.
  If anything conflicts with it, stop and ask — do not silently choose.
- PLAN.md tracks all work. Update task statuses IN THE SAME COMMIT as the
  work itself. Never mark a task done if its tests don't pass.
- Work stops at the end of each stage for human review. Do not start the
  next stage without explicit approval.
- Run ./quality.sh and `go test -race ./...` before declaring any task done.
- Conventional Commits. No dependencies beyond those the FRD allows.
