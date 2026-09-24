package linear

import "testing"

// These tests cover extractBlockers' handling of Linear sub-issues
// (raw["children"]), per the tracker-edge-widening design: a parent issue is
// blocked by its incomplete sub-issues, captured as hard edges alongside
// blocks-type inverseRelations. See
// docs/superpowers/specs/2026-08-05-tracker-edge-widening-design.md.

func TestExtractBlockersCapturesChildren(t *testing.T) {
	raw := map[string]any{
		"children": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":         "child-1",
					"identifier": "ENG-2",
					"url":        "https://linear.app/issue/ENG-2",
					"state":      map[string]any{"name": "Todo"},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 1 {
		t.Fatalf("expected 1 blocker ref from children, got %d: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.ID == nil || *ref.ID != "child-1" {
		t.Errorf("expected ID %q, got %v", "child-1", ref.ID)
	}
	if ref.Identifier == nil || *ref.Identifier != "ENG-2" {
		t.Errorf("expected Identifier %q, got %v", "ENG-2", ref.Identifier)
	}
	if ref.URL == nil || *ref.URL != "https://linear.app/issue/ENG-2" {
		t.Errorf("expected URL %q, got %v", "https://linear.app/issue/ENG-2", ref.URL)
	}
	if ref.State == nil || *ref.State != "Todo" {
		t.Errorf("expected State %q, got %v", "Todo", ref.State)
	}
}

func TestExtractBlockersChildTerminalStateCaptured(t *testing.T) {
	raw := map[string]any{
		"children": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":         "child-done",
					"identifier": "ENG-3",
					"state":      map[string]any{"name": "Done"},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 1 {
		t.Fatalf("expected Done child to still be captured, got %d refs: %+v", len(refs), refs)
	}
	if refs[0].State == nil || *refs[0].State != "Done" {
		t.Errorf("expected state Done to be carried through, got %v", refs[0].State)
	}
}

func TestExtractBlockersChildDedupedAgainstRelation(t *testing.T) {
	raw := map[string]any{
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"id":         "shared-id",
						"identifier": "ENG-9",
						"state":      map[string]any{"name": "Todo"},
					},
				},
			},
		},
		"children": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":         "shared-id",
					"identifier": "ENG-9",
					"state":      map[string]any{"name": "Todo"},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 1 {
		t.Fatalf("expected the same issue appearing as both a blocks-relation and a child to dedupe to 1 ref, got %d: %+v", len(refs), refs)
	}
}

// TestExtractBlockersChildDedupedAgainstRelationByIdentifier makes the
// identifier fallback tier of blockerRefKey load-bearing. Linear always
// sends `id` in practice, but blockerRefKey mirrors
// internal/orchestrator/dependency_audit.go::blockerKey's id > identifier >
// url priority and must be trustworthy standalone.
//
// A 2-node "both refs lack id, share identifier, differ in url, expect 1
// ref" fixture is NOT sufficient here: if the identifier (and url) tiers
// were deleted entirely, both refs would fall through to the same
// "unknown" default key and still collapse to 1 — the assertion would pass
// vacuously regardless of whether the identifier tier exists (this is
// exactly the gap the reviewer's mutation exposed in the first version of
// this test). Using 3 refs makes the tier's specific behavior observable:
//   - relation ref:  identifier=ENG-9, url=shared   (no id)
//   - child ref 1:   identifier=ENG-9, url=other    (no id) — same
//     identifier as the relation ref, MUST merge with it.
//   - child ref 2:   identifier=ENG-8, url=shared   (no id) — different
//     identifier from the relation ref (despite sharing its url), MUST
//     stay distinct: identifier takes priority over url.
//
// Correct behavior: 2 refs survive, with identifiers {ENG-9, ENG-8} both
// present. If the identifier tier is deleted, keying falls to url instead:
// the relation ref and child-2 (both url=shared) collapse together and
// child-1 (url=other) survives separately — still 2 refs by count, but
// ENG-8 is silently dropped and ENG-9 appears twice, so asserting on the
// actual identifiers present (not just len) catches it.
func TestExtractBlockersChildDedupedAgainstRelationByIdentifier(t *testing.T) {
	raw := map[string]any{
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"identifier": "ENG-9",
						"url":        "https://linear.app/issue/shared",
						"state":      map[string]any{"name": "Todo"},
					},
				},
			},
		},
		"children": map[string]any{
			"nodes": []any{
				map[string]any{
					"identifier": "ENG-9",
					"url":        "https://linear.app/issue/other",
					"state":      map[string]any{"name": "Todo"},
				},
				map[string]any{
					"identifier": "ENG-8",
					"url":        "https://linear.app/issue/shared",
					"state":      map[string]any{"name": "Todo"},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 2 {
		t.Fatalf("expected 2 distinct refs (ENG-9 merged, ENG-8 kept distinct despite sharing url), got %d: %+v", len(refs), refs)
	}
	gotIdentifiers := make(map[string]bool)
	for _, r := range refs {
		if r.Identifier != nil {
			gotIdentifiers[*r.Identifier] = true
		}
	}
	if !gotIdentifiers["ENG-9"] || !gotIdentifiers["ENG-8"] {
		t.Errorf("expected both ENG-9 (deduped merge) and ENG-8 (distinct, despite sharing url with the relation ref) to survive as separate blockers; got %+v", refs)
	}
}

// TestExtractBlockersChildDedupedAgainstRelationByURL makes the url
// fallback tier of blockerRefKey load-bearing, mirroring the identifier
// test's 3-node "distinctness must be preserved" design so a deleted url
// tier is observable rather than accidentally passing via the shared
// "unknown" default (see comment on the identifier-tier test above for why
// a plain 2-node "shared url -> 1 ref" fixture is insufficient):
//   - relation ref:  url=shared (no id, no identifier)
//   - child ref 1:   url=other  (no id, no identifier) — different url,
//     MUST stay distinct.
//   - child ref 2:   url=shared (no id, no identifier) — same url as the
//     relation ref, MUST merge with it.
func TestExtractBlockersChildDedupedAgainstRelationByURL(t *testing.T) {
	raw := map[string]any{
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"url":   "https://linear.app/issue/shared",
						"state": map[string]any{"name": "Todo"},
					},
				},
			},
		},
		"children": map[string]any{
			"nodes": []any{
				map[string]any{
					"url":   "https://linear.app/issue/other",
					"state": map[string]any{"name": "Todo"},
				},
				map[string]any{
					"url":   "https://linear.app/issue/shared",
					"state": map[string]any{"name": "Todo"},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 2 {
		t.Fatalf("expected 2 distinct refs (shared-url pair merged, other-url kept distinct), got %d: %+v", len(refs), refs)
	}
	gotURLs := make(map[string]bool)
	for _, r := range refs {
		if r.URL != nil {
			gotURLs[*r.URL] = true
		}
	}
	if !gotURLs["https://linear.app/issue/shared"] || !gotURLs["https://linear.app/issue/other"] {
		t.Errorf("expected both the shared url (deduped merge) and the other url (distinct) to survive as separate blockers; got %+v", refs)
	}
}

// TestExtractBlockersChildOriginTaggedSubIssue pins the provenance marker
// (Task 3, item 3 of the wave-2 polish plan): a BlockerRef sourced from
// raw["children"] must carry Origin == "sub_issue" so
// internal/orchestrator/dependency_audit.go::dependencySourceForBlocker can
// map it to the dedicated DependencySourceSubIssue audit label instead of
// the generic tracker_relation label used for explicit "blocks" relations.
func TestExtractBlockersChildOriginTaggedSubIssue(t *testing.T) {
	raw := map[string]any{
		"children": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":         "child-1",
					"identifier": "ENG-2",
					"state":      map[string]any{"name": "Todo"},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 1 {
		t.Fatalf("expected 1 blocker ref from children, got %d: %+v", len(refs), refs)
	}
	if refs[0].Origin != "sub_issue" {
		t.Errorf("expected Origin %q on a child-derived ref, got %q", "sub_issue", refs[0].Origin)
	}
}

// TestExtractBlockersRelationOriginUnset pins the negative case: a ref
// sourced purely from inverseRelations (an explicit "blocks" relation, not a
// sub-issue) must NOT carry the sub_issue Origin marker.
func TestExtractBlockersRelationOriginUnset(t *testing.T) {
	raw := map[string]any{
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"id":         "blocker-1",
						"identifier": "ENG-0",
						"state":      map[string]any{"name": "In Progress"},
					},
				},
			},
		},
	}

	refs := extractBlockers(raw)

	if len(refs) != 1 {
		t.Fatalf("expected 1 blocker ref from relation, got %d: %+v", len(refs), refs)
	}
	if refs[0].Origin != "" {
		t.Errorf("expected no Origin marker on a relation-sourced ref, got %q", refs[0].Origin)
	}
}

func TestExtractBlockersNoChildrenUnchanged(t *testing.T) {
	rawAbsent := map[string]any{
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"id":         "blocker-1",
						"identifier": "ENG-0",
						"state":      map[string]any{"name": "In Progress"},
					},
				},
			},
		},
	}
	rawEmpty := map[string]any{
		"inverseRelations": map[string]any{
			"nodes": []any{
				map[string]any{
					"type": "blocks",
					"issue": map[string]any{
						"id":         "blocker-1",
						"identifier": "ENG-0",
						"state":      map[string]any{"name": "In Progress"},
					},
				},
			},
		},
		"children": map[string]any{
			"nodes": []any{},
		},
	}

	refsAbsent := extractBlockers(rawAbsent)
	refsEmpty := extractBlockers(rawEmpty)

	if len(refsAbsent) != 1 {
		t.Fatalf("expected 1 ref from relation only (children key absent), got %d", len(refsAbsent))
	}
	if len(refsEmpty) != 1 {
		t.Fatalf("expected 1 ref from relation only (children nodes empty), got %d", len(refsEmpty))
	}
	if *refsAbsent[0].ID != *refsEmpty[0].ID {
		t.Errorf("absent vs empty children should produce identical results: %v vs %v", refsAbsent[0].ID, refsEmpty[0].ID)
	}
}
