# Outcome

Correct Work Definition structural clarification so it asks only about genuinely non-inferable workflow topology. Remove the fixed review-gate question and the heuristic that treats a short intent as a reason to ask.

# Scope

- `internal/work/ports.go`
- `internal/work/definition_planning_attention.go`
- `internal/work/collaboration_recovery.go`
- `internal/boot/definition_planner.go`
- their focused tests
- Frontend only if the typed response shape requires a small compatibility update.

# Required behavior

1. Extend planner output with a typed `structuralQuestions` collection. Keep old valid four-field JSON parse-compatible if practical, while the new model prompt must always emit the collection (usually `[]`).
2. The prompt must default to `[]`. A question is allowed only when:
   - the intent/base leave at least two materially different, reasonable workflow structures;
   - the choice changes node boundaries/membership, dependencies, parallel-vs-sequential topology, or input/artifact slot ownership;
   - no safe, reversible default can be inferred;
   - only the user can decide.
3. Explicitly forbid content questions, searchable information, output wording/quality preferences, generic approval/confirmation policy, and anything the planner can safely infer. A recommended/default option is evidence the planner should normally choose it and not ask.
4. Service pauses only for a validated unanswered planner question. Empty questions commit normally, including sparse prompts such as “translate this PDF”.
5. Replanning with `StructuralAnswers` must tell the planner not to repeat answered questions.
6. Validate question impact against the existing structure-only scope, require stable ID/question and 2–4 distinct valid options, cap questions to a small bound, and expose failures as retryable planner errors.
7. Preserve current raw JSON progress events and idempotent/recoverable behavior.

# Acceptance

- Focused test: sparse but inferable prompt commits without clarification.
- Focused test: planner-emitted non-inferable structure question pauses before commit.
- Focused test: answered question replans and commits without repeating it.
- Prompt/schema tests prove the default-empty and forbidden-question rules.
- Existing focused planner/recovery tests pass.
- `gofmt`, related Go tests, and `go vet` pass.

# Constraints

Shared dirty worktree: do not touch unrelated files, create/switch branches, commit, stage, push, or release. Do not weaken strict JSON repair or leak secrets.
