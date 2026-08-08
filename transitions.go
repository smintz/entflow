package entflow

import "entgo.io/ent/schema"

// TransitionsAnnotation declares the legal status transitions for a status
// enum field. It ships in Phase 1 as declaration-only (D-14): the enforcing
// hook (SM-02) and the bidirectional cross-validator against steps' claimed
// Transition() values (SM-03/SM-04) are codegen and land in Phase 6. Phase 1
// makes the annotation readable from a loaded schema graph so Phase 6 has
// something to validate against.
//
// The struct is a plain JSON-marshalable shape (an exported map field, no
// closures) because ent round-trips annotations through encoding/json at
// graph-load time — the same Annotations[Name()] -> json.Marshal ->
// json.Unmarshal idiom ent's own internal code uses for its built-in
// annotations.
type TransitionsAnnotation struct {
	Transitions map[string][]string
}

// Name identifies this annotation to ent's codegen. The name is namespaced
// to avoid colliding with other extensions' annotations attached to the same
// field. schema.Annotation is a one-method interface — this is the entire
// contract (entgo.io/ent@v0.14.6/schema/schema.go:12-15).
func (TransitionsAnnotation) Name() string {
	return "EntflowTransitions"
}

// Transitions constructs a TransitionsAnnotation from a map of status value
// to its legal successor values. Attach it to a status enum field:
//
//	field.Enum("status").
//		Values("draft", "pending", "cancelled").
//		Annotations(entflow.Transitions(map[string][]string{
//			"draft": {"pending", "cancelled"},
//		}))
func Transitions(m map[string][]string) TransitionsAnnotation {
	return TransitionsAnnotation{Transitions: m}
}

// compile-time assertion that TransitionsAnnotation satisfies schema.Annotation.
var _ schema.Annotation = TransitionsAnnotation{}
