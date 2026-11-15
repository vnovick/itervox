---
description: Pin down the behaviour, build it in small steps, test what users see.
keywords: [feature, add, support, implement, new, allow, enable, introduce, option]
require_evidence: []
---
This issue adds a feature. Work in this order:

1. **Restate the behaviour.** List what the user will be able to do and the acceptance criteria. Ask (input required) when the issue leaves a choice that changes the outcome.
2. **Look for what exists.** Reuse the code, components and conventions already in the repository before adding new ones.
3. **Build in small steps,** each one compiling and tested.
4. **Test the behaviour, not the implementation:** one test per acceptance criterion, named after it.
5. **Update the docs** a user reads to find the feature.
6. In your handoff, map each acceptance criterion to the test that proves it.
