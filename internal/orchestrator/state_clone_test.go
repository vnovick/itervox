package orchestrator

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// cloneAllowlist names reference-typed fields (as "Type.Field") that
// State.Clone intentionally does NOT deep-copy. Every entry needs a reason.
// Adding a map, slice or pointer field anywhere under State without either
// deep-copying it in Clone or listing it here fails
// TestStateCloneDeepCopiesAllReferenceFields. Same pattern as
// cfg_mu_audit_test.go::AllowedMutableCfgFields.
var cloneAllowlist = map[string]string{
	// Cancel handles are process-local capabilities, not state: Clone
	// clears them so a snapshot reader can never cancel a live worker.
	"RunEntry.WorkerCancel": "cleared to nil by Clone (snapshot readers must not cancel workers)",

	// Immutable pointer-to-scalar values: replaced wholesale, never written
	// through. Evidence: grep -rnE '^\s*\*[A-Za-z_.]*\.(LastEventAt|RetryAttempt|Error|Priority|BranchName|Description|URL|CreatedAt|UpdatedAt|ID|Identifier|State)\s*=' internal/ cmd/ → 0 non-test hits.
	"RunEntry.LastEventAt":  "immutable *time.Time (reassigned, never written through)",
	"RunEntry.RetryAttempt": "immutable *int (reassigned, never written through)",
	"RunEntry.CostUSD":      "immutable *float64 (reassigned from a fresh copy, never written through)",
	"RetryEntry.Error":      "immutable *string",
	"Issue.Priority":        "immutable *int from the tracker adapter",
	"Issue.BranchName":      "immutable *string from the tracker adapter",
	"Issue.Description":     "immutable *string from the tracker adapter",
	"Issue.URL":             "immutable *string from the tracker adapter",
	"Issue.CreatedAt":       "immutable *time.Time from the tracker adapter",
	"Issue.UpdatedAt":       "immutable *time.Time from the tracker adapter",
	"Comment.CreatedAt":     "immutable *time.Time from the tracker adapter",
	"BlockerRef.ID":         "immutable *string from the tracker adapter",
	"BlockerRef.Identifier": "immutable *string from the tracker adapter",
	"BlockerRef.State":      "immutable *string from the tracker adapter",
	"BlockerRef.URL":        "immutable *string from the tracker adapter",
	"BlockerRef.BranchName": "immutable *string from the tracker adapter",
}

// TestStateCloneDeepCopiesAllReferenceFields fills every settable field of
// State — recursively, through maps, slices, pointers and nested structs —
// with non-zero data, clones it, and fails on any map, slice or pointer that
// the clone still shares with the original (or any func handle left intact),
// unless the field is in cloneAllowlist. CORE-034.
func TestStateCloneDeepCopiesAllReferenceFields(t *testing.T) {
	var orig State
	fillForCloneGuard(reflect.ValueOf(&orig).Elem(), 0)
	cp := orig.Clone()

	var shared []string
	findSharedRefs(reflect.ValueOf(orig), reflect.ValueOf(cp), "State", "State", &shared)
	sort.Strings(shared)
	if len(shared) > 0 {
		t.Fatalf("State.Clone shares %d reference field(s) with the original — deep-copy them in "+
			"state_clone.go or add them to cloneAllowlist with a reason:\n  - %s",
			len(shared), strings.Join(shared, "\n  - "))
	}

	// The allowlisted func handle must actually be cleared, not shared.
	for id, e := range cp.Running {
		if e != nil && e.WorkerCancel != nil {
			t.Fatalf("Clone kept RunEntry.WorkerCancel for %s", id)
		}
	}
}

// TestStateCloneMutationDoesNotLeak is the behavioural half: mutating every
// review map through the clone must not change the original. CORE-034.
func TestStateCloneMutationDoesNotLeak(t *testing.T) {
	s := State{
		ReviewVerdicts:   map[string][]ReviewVerdict{"ENG-1": {{Profile: "r1"}}},
		ReviewChainIndex: map[string]int{"ENG-1": 1},
		ReviewOutcomes:   map[string]ReviewOutcome{"ENG-1": {}},
	}
	cp := s.Clone()
	cp.ReviewVerdicts["ENG-1"][0].Profile = "mutated"
	cp.ReviewVerdicts["ENG-2"] = nil
	cp.ReviewChainIndex["ENG-1"] = 7
	delete(cp.ReviewOutcomes, "ENG-1")
	if s.ReviewVerdicts["ENG-1"][0].Profile != "r1" || len(s.ReviewVerdicts) != 1 ||
		s.ReviewChainIndex["ENG-1"] != 1 || len(s.ReviewOutcomes) != 1 {
		t.Fatalf("mutating the clone changed the original: %+v", s)
	}
}

var timeType = reflect.TypeOf(time.Time{})

// fillForCloneGuard sets v (settable) to non-zero data at every reference
// depth. Unexported fields are left alone (not reachable from outside the
// type's package and not part of State's contract).
func fillForCloneGuard(v reflect.Value, depth int) {
	if depth > 8 {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillForCloneGuard(p.Elem(), depth+1)
		v.Set(p)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillForCloneGuard(k, depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		fillForCloneGuard(e, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillForCloneGuard(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillForCloneGuard(v.Index(i), depth+1)
		}
	case reflect.Struct:
		if v.Type() == timeType {
			v.Set(reflect.ValueOf(time.Unix(1_700_000_000, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() {
				fillForCloneGuard(f, depth+1)
			}
		}
	case reflect.Func:
		v.Set(reflect.MakeFunc(v.Type(), func([]reflect.Value) []reflect.Value {
			out := make([]reflect.Value, v.Type().NumOut())
			for i := range out {
				out[i] = reflect.Zero(v.Type().Out(i))
			}
			return out
		}))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	}
}

// findSharedRefs walks a and b in parallel and records every reference that
// b still shares with a. owner/field name the enclosing "Type.Field" used for
// allowlist lookups; path is the human-readable location.
func findSharedRefs(a, b reflect.Value, path, owner string, out *[]string) {
	allowed := func() bool { _, ok := cloneAllowlist[owner]; return ok }
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() {
			return
		}
		if b.IsNil() {
			if !allowed() {
				*out = append(*out, fmt.Sprintf("%s (%s): dropped by Clone", path, owner))
			}
			return
		}
		if a.Pointer() == b.Pointer() && !allowed() {
			*out = append(*out, fmt.Sprintf("%s (%s): pointer shared", path, owner))
			return
		}
		findSharedRefs(a.Elem(), b.Elem(), path, owner, out)
	case reflect.Map:
		if a.Len() == 0 {
			return
		}
		if b.IsNil() || b.Len() != a.Len() {
			*out = append(*out, fmt.Sprintf("%s (%s): map not copied (len %d -> %d)", path, owner, a.Len(), b.Len()))
			return
		}
		if a.Pointer() == b.Pointer() {
			if !allowed() {
				*out = append(*out, fmt.Sprintf("%s (%s): map shared", path, owner))
			}
			return
		}
		iter := a.MapRange()
		for iter.Next() {
			findSharedRefs(iter.Value(), b.MapIndex(iter.Key()), path+"[]", owner, out)
		}
	case reflect.Slice:
		if a.Len() == 0 {
			return
		}
		if b.Len() != a.Len() {
			*out = append(*out, fmt.Sprintf("%s (%s): slice not copied (len %d -> %d)", path, owner, a.Len(), b.Len()))
			return
		}
		if a.Pointer() == b.Pointer() {
			if !allowed() {
				*out = append(*out, fmt.Sprintf("%s (%s): slice backing array shared", path, owner))
			}
			return
		}
		for i := 0; i < a.Len(); i++ {
			findSharedRefs(a.Index(i), b.Index(i), path+"[]", owner, out)
		}
	case reflect.Array:
		for i := 0; i < a.Len(); i++ {
			findSharedRefs(a.Index(i), b.Index(i), path+"[]", owner, out)
		}
	case reflect.Struct:
		if a.Type() == timeType {
			return
		}
		for i := 0; i < a.NumField(); i++ {
			f := a.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			findSharedRefs(a.Field(i), b.Field(i), path+"."+f.Name, a.Type().Name()+"."+f.Name, out)
		}
	case reflect.Func:
		if !a.IsNil() && !b.IsNil() && !allowed() {
			*out = append(*out, fmt.Sprintf("%s (%s): func handle kept", path, owner))
		}
	}
}
