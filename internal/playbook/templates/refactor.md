---
description: Preserve behaviour; tests green before and after every step.
keywords: [refactor, refactoring, cleanup, clean up, restructure, rename, extract, simplify, tech debt, reorganize, split]
require_evidence: []
---
This issue is a refactor: the behaviour must not change.

1. **Run the tests first** and record that they pass. If coverage of the code you will move is thin, add characterisation tests before touching it.
2. **Change structure in small steps;** run the tests after each one.
3. **Subtract before you add.** Prefer deleting duplicated or dead code over adding layers.
4. **Do not change behaviour** — no new features and no bug fixes mixed in. Note any bug you find for a separate issue.
5. In your handoff, say what moved where and show the same tests passing before and after.
