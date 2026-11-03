# deps-analyzer INSTRUCTIONS

## Required Reading
- Read the supplied "## Issues" block in your prompt. Each entry has identifier, title, body, and current state.
- Read the supplied "## Existing Tracker Edges" block; these are relations the tracker already declares.

## Workflow
1. Walk every issue. For each issue, look for natural-language references that imply a dependency:
   - "blocked by FOO-12"
   - "depends on FOO-34"
   - "sub-task of FOO-56"
   - "needs FOO-78 first"
2. Skip any relation already present in the tracker-edges block.
3. For each surviving relation, emit one edge object with:
   - "source": the blocking issue identifier
   - "target": the blocked issue identifier
   - "evidence": a short quotation or paraphrase showing why you inferred the edge
4. If you cannot identify any non-tracker relations, return {"edges":[]}.

## Done Criteria
- Output is a single JSON object on stdout matching the schema below.
- No prose before or after the JSON; no markdown code fences.
- Every emitted edge has all three fields populated.
- You never modified the tracker, the workspace, or any file.

## Output Schema
{
  "edges": [
    { "source": "FOO-12", "target": "FOO-34", "evidence": "issue body mentions \"blocked by FOO-12\"" }
  ]
}

## Ambiguity Policy
If the evidence is weak or contradictory, omit the edge. Operators prefer
the dashboard miss an inferred relation over showing a false one.
