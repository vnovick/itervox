---
description: Measure a baseline first, change one thing, explain the number.
keywords: [perf, performance, slow, slower, latency, speed, faster, memory, cpu, throughput, benchmark, optimize, optimise, timeout]
require_evidence: []
---
This issue is performance work. Measure before you change anything.

1. **Measure a baseline.** Write or find a benchmark, or a repeatable command, that shows the cost. Record the numbers and how you got them.
2. **Find where the time or memory goes** (a profile, not a guess).
3. **Change one thing at a time** and measure again after each change.
4. **Explain the number.** Report before and after with the same method, and say why the change moved it.
5. Keep the behaviour the same; run the tests.
6. In your handoff, include the benchmark command and the before/after numbers.
