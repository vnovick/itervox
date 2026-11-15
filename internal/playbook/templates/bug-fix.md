---
description: Reproduce first, fix the root cause, prove the fix with a test.
keywords: [bug, crash, crashes, error, errors, broken, regression, fails, failing, failure, exception, panic, incorrect, wrong, fix]
require_evidence: []
---
This issue is a bug fix. Work in this order:

1. **Reproduce first.** Write a failing test, or a command whose output shows the bug, before changing any code. If you cannot reproduce it, stop and say what you tried.
2. **Find the root cause.** Explain why the failure happens. Fix the code that causes it, not the place where it shows up.
3. **Make the smallest change** that fixes the cause. Leave unrelated cleanups for another issue.
4. **Prove it works.** Run the reproduction again and show it now passes, then run the surrounding test suite.
5. In your handoff, name the root cause, the reproduction and the evidence.
