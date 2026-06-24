package graphql

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
)

// Request is one incoming GraphQL execution request. Query is the document
// source; OperationName selects which operation to run when the document
// defines more than one (it may be empty for a single-operation document);
// Variables supplies values for the operation's declared variables; Root, when
// non-nil, is the parent value passed to top-level resolvers.
type Request struct {
	Query         string
	OperationName string
	Variables     map[string]any
	Root          any
}

// Response is the standard GraphQL result envelope. Data holds the resolved
// selection tree (nil when execution could not begin, e.g. a parse error);
// Errors collects request-level and per-field errors. It marshals to the
// {"data":...,"errors":[...]} JSON shape, omitting an empty errors array.
type Response struct {
	Data   any        `json:"data,omitempty"`
	Errors []GqlError `json:"errors,omitempty"`
}

// GqlError is a single GraphQL error entry: a human message and, for field
// errors, the response Path at which it occurred (field names and list
// indices, root-to-leaf).
type GqlError struct {
	Message string `json:"message"`
	Path    []any  `json:"path,omitempty"`
}

// Error implements the error interface so a GqlError can be returned directly.
func (e GqlError) Error() string { return e.Message }

// Execute parses Req.Query, selects the requested operation, and resolves its
// selection set against cs. Argument and variable values are coerced to their
// declared types; resolver errors are collected into the response with their
// path while sibling fields continue. A nil schema or a parse/validation
// failure yields a Response with Data nil and a single error.
func Execute(ctx context.Context, cs *CompiledSchema, req Request) Response {
	if cs == nil {
		return Response{Errors: []GqlError{{Message: "graphql: nil schema"}}}
	}
	doc, err := parseDocument(req.Query)
	if err != nil {
		return Response{Errors: []GqlError{{Message: err.Error()}}}
	}
	op, err := selectOperation(doc, req.OperationName)
	if err != nil {
		return Response{Errors: []GqlError{{Message: err.Error()}}}
	}
	// Reject fragment-spread cycles before execution: a self-referential or
	// mutually-recursive fragment would otherwise drive collectFields into
	// unbounded recursion and crash the process (stack overflow). Per the
	// GraphQL spec, "Fragments Must Not Form Cycles".
	if err := detectFragmentCycles(doc); err != nil {
		return Response{Errors: []GqlError{{Message: err.Error()}}}
	}

	root := cs.query
	if op.kind == opMutation {
		root = cs.mutation
		if root == nil {
			return Response{Errors: []GqlError{{Message: "graphql: schema has no Mutation type"}}}
		}
	}

	ex := &executor{cs: cs, doc: doc}
	vars, err := ex.coerceVariables(op, req.Variables)
	if err != nil {
		return Response{Errors: []GqlError{{Message: err.Error()}}}
	}
	ex.vars = vars

	data, _ := ex.executeSelectionSet(ctx, root, op.selSet, req.Root, nil)
	return Response{Data: data, Errors: ex.errs}
}

// executor carries the per-request state shared across the resolution walk: the
// compiled schema, parsed document (for fragment lookup), coerced variables and
// the accumulating error list.
type executor struct {
	cs   *CompiledSchema
	doc  *document
	vars map[string]any
	errs []GqlError
	// bubble is set while resolving a selection set when a non-null field in it
	// produced null; executeSelectionSet reads and scopes it to nullify the
	// enclosing object per GraphQL null-propagation.
	bubble bool
}

// selectOperation picks the operation to run. With a name it must match; with
// none, the document must contain exactly one operation.
func selectOperation(doc *document, name string) (*operation, error) {
	if len(doc.operations) == 0 {
		return nil, fmt.Errorf("graphql: document defines no operations")
	}
	if name == "" {
		if len(doc.operations) != 1 {
			return nil, fmt.Errorf("graphql: operationName required: document defines %d operations", len(doc.operations))
		}
		return doc.operations[0], nil
	}
	for _, op := range doc.operations {
		if op.name == name {
			return op, nil
		}
	}
	return nil, fmt.Errorf("graphql: no operation named %q", name)
}

// detectFragmentCycles reports an error if any named fragment reaches itself
// through a chain of fragment spreads. Without this guard collectFields would
// recurse forever on a cyclic spread (e.g. `fragment F on T { ...F }`), causing
// a stack-overflow DoS reachable from a single small request. It performs a DFS
// over the spread graph rooted at every fragment, tracking the active path.
func detectFragmentCycles(doc *document) error {
	state := make(map[string]int) // 0=unvisited, 1=on-stack, 2=done
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("graphql: fragment %q forms a cycle", name)
		case 2:
			return nil
		}
		frag, ok := doc.fragments[name]
		if !ok {
			// Unknown spread target; handled at execution time, not a cycle.
			state[name] = 2
			return nil
		}
		state[name] = 1
		if err := visitFragmentSpreads(frag.selSet, visit); err != nil {
			return err
		}
		state[name] = 2
		return nil
	}
	for name := range doc.fragments {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// visitFragmentSpreads walks a selection set and invokes visit for every named
// fragment spread reachable through nested fields and inline fragments.
func visitFragmentSpreads(sels []selection, visit func(string) error) error {
	for _, sel := range sels {
		switch {
		case sel.field != nil:
			if err := visitFragmentSpreads(sel.field.selSet, visit); err != nil {
				return err
			}
		case sel.inlineFrag != nil:
			if err := visitFragmentSpreads(sel.inlineFrag.selSet, visit); err != nil {
				return err
			}
		case sel.fragmentSpread != "":
			if err := visit(sel.fragmentSpread); err != nil {
				return err
			}
		}
	}
	return nil
}

// addError appends a field/request error with its response path.
func (ex *executor) addError(path []any, format string, args ...any) {
	cp := append([]any(nil), path...)
	ex.errs = append(ex.errs, GqlError{Message: fmt.Sprintf(format, args...), Path: cp})
}

// executeSelectionSet resolves every field reachable from sels against the
// object type obj on parent, flattening fragments. It returns an ordered-by-key
// map[string]any (response object) and a bubble flag: when a non-null field in
// this set produced null, the whole object is null and the flag is true so the
// enclosing position can propagate the null. parent may be nil at the root
// (top-level resolvers ignore it); nested object nullity is handled in
// completeValue before this is ever reached.
func (ex *executor) executeSelectionSet(ctx context.Context, obj *Object, sels []selection, parent any, path []any) (map[string]any, bool) {
	// Scope the bubble flag to this selection set so a sibling at an enclosing
	// level cannot leak in or be clobbered while this object is being built.
	saved := ex.bubble
	ex.bubble = false
	out := map[string]any{}
	ex.collectFields(ctx, obj, sels, parent, path, out)
	bubbled := ex.bubble
	ex.bubble = saved
	if bubbled {
		return nil, true
	}
	return out, false
}

// collectFields walks sels, resolving plain fields into out and recursing into
// applicable fragments. Fragment type conditions are matched against obj.Name.
func (ex *executor) collectFields(ctx context.Context, obj *Object, sels []selection, parent any, path []any, out map[string]any) {
	for _, sel := range sels {
		switch {
		case sel.field != nil:
			ex.resolveField(ctx, obj, sel.field, parent, path, out)
		case sel.inlineFrag != nil:
			if ex.fragmentApplies(obj, sel.inlineFrag.typeOn) {
				ex.collectFields(ctx, obj, sel.inlineFrag.selSet, parent, path, out)
			}
		case sel.fragmentSpread != "":
			frag, ok := ex.doc.fragments[sel.fragmentSpread]
			if !ok {
				key := sel.fragmentSpread
				out[key] = nil
				ex.addError(path, "graphql: unknown fragment %q", sel.fragmentSpread)
				continue
			}
			if ex.fragmentApplies(obj, frag.typeOn) {
				ex.collectFields(ctx, obj, frag.selSet, parent, path, out)
			}
		}
	}
}

// fragmentApplies reports whether a fragment's type condition matches obj. An
// empty condition (untyped inline fragment) always applies.
func (ex *executor) fragmentApplies(obj *Object, typeOn string) bool {
	return typeOn == "" || typeOn == obj.Name
}

// resolveField resolves a single field selection and writes its serialised
// value (under alias or name) into out, recording any error.
func (ex *executor) resolveField(ctx context.Context, obj *Object, fn *fieldNode, parent any, path []any, out map[string]any) {
	key := fn.name
	if fn.alias != "" {
		key = fn.alias
	}
	fpath := append(append([]any(nil), path...), key)

	// __typename meta field: available on every object, no schema entry.
	if fn.name == "__typename" {
		out[key] = obj.Name
		return
	}

	def, ok := obj.Fields[fn.name]
	if !ok {
		out[key] = nil
		ex.addError(fpath, "graphql: Cannot query field %q on type %q", fn.name, obj.Name)
		return
	}

	args, err := ex.coerceArguments(def.Args, fn.args)
	if err != nil {
		out[key] = nil
		ex.addError(fpath, "graphql: field %q: %v", fn.name, err)
		return
	}

	_, fieldNonNull := def.Type.(*NonNull)

	val, err := ex.resolveValue(ctx, def, fn.name, parent, args)
	if err != nil {
		out[key] = nil
		ex.addError(fpath, "%v", err)
		// A resolver error on a non-null field bubbles the null upward.
		if fieldNonNull {
			ex.bubble = true
		}
		return
	}

	res, bubbled := ex.completeValue(ctx, def.Type, fn, val, fpath)
	if bubbled {
		// A non-null violation occurred at or beneath this field; its value is
		// null. If the field itself is non-null the null keeps bubbling to the
		// enclosing object; if it is nullable the null is contained here (and
		// any sibling's bubble flag must be left untouched).
		out[key] = nil
		if fieldNonNull {
			ex.bubble = true
		}
		return
	}
	out[key] = res
}

// resolveValue invokes the field's resolver, or falls back to default
// resolution (struct field / map key) when Resolve is nil.
func (ex *executor) resolveValue(ctx context.Context, def *Field, name string, parent any, args ArgValues) (any, error) {
	if def.Resolve != nil {
		return def.Resolve(ctx, parent, args)
	}
	return defaultResolve(parent, name), nil
}

// completeValue serialises a resolved value against its declared type:
// validating non-null, mapping lists element-wise, recursing into objects via
// the field's selection set, and serialising leaves (scalars/enums).
//
// The second result reports whether a non-null violation occurred at or beneath
// this position that must bubble to the enclosing nullable field/object. When
// true the returned value is always nil.
func (ex *executor) completeValue(ctx context.Context, t Type, fn *fieldNode, val any, path []any) (any, bool) {
	switch tt := t.(type) {
	case *NonNull:
		res, bubbled := ex.completeValue(ctx, tt.OfType, fn, val, path)
		if bubbled {
			return nil, true
		}
		if res == nil {
			ex.addError(path, "graphql: Cannot return null for non-nullable field of type %q", tt.TypeName())
			return nil, true
		}
		return res, false
	case *List:
		if val == nil {
			return nil, false
		}
		rv := reflect.ValueOf(val)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			ex.addError(path, "graphql: expected list for type %q, got %T", tt.TypeName(), val)
			return nil, false
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			ipath := append(append([]any(nil), path...), i)
			elem, bubbled := ex.completeValue(ctx, tt.OfType, fn, rv.Index(i).Interface(), ipath)
			if bubbled {
				// A null bubbled out of a non-null element: the whole list
				// becomes null and the null continues to bubble upward.
				return nil, true
			}
			out[i] = elem
		}
		return out, false
	case *Object:
		if val == nil {
			return nil, false
		}
		obj, bubbled := ex.executeSelectionSet(ctx, tt, fn.selSet, val, path)
		if bubbled {
			return nil, true
		}
		return obj, false
	case *Enum:
		return serializeLeaf(val), false
	case *Scalar:
		if val == nil {
			return nil, false
		}
		return serializeScalar(tt, val), false
	default:
		return val, false
	}
}

// serializeScalar coerces a resolver-returned value into the JSON shape of the
// built-in scalar t. Unknown/custom scalars pass the value through unchanged.
func serializeScalar(t *Scalar, val any) any {
	switch t.Name {
	case "Int":
		switch v := val.(type) {
		case int:
			return int64(v)
		case int32:
			return int64(v)
		case int64:
			return v
		case float64:
			return int64(v)
		}
		return val
	case "Float":
		switch v := val.(type) {
		case float64:
			return v
		case float32:
			return float64(v)
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
		return val
	case "Boolean":
		if b, ok := val.(bool); ok {
			return b
		}
		return val
	case "String", "ID":
		switch v := val.(type) {
		case string:
			return v
		case fmt.Stringer:
			return v.String()
		}
		return serializeLeaf(val)
	}
	return val
}

// serializeLeaf renders a non-object leaf value (enum/ID) to a JSON-friendly
// form, stringifying where the underlying value is not already a scalar.
func serializeLeaf(val any) any {
	switch v := val.(type) {
	case nil, string, bool, int, int32, int64, float32, float64:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// defaultResolve implements struct-field / map-key resolution for fields with
// no Resolve function: a map[string]any lookup or a struct field matching name
// (exact, then case-insensitive, then a `graphql` / `json` tag).
func defaultResolve(parent any, name string) any {
	if parent == nil {
		return nil
	}
	if m, ok := parent.(map[string]any); ok {
		return m[name]
	}
	rv := reflect.ValueOf(parent)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	rt := rv.Type()
	// Exact field name.
	if f := rv.FieldByName(name); f.IsValid() {
		return f.Interface()
	}
	// Tag or case-insensitive match.
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		if tagMatches(sf.Tag.Get("graphql"), name) || tagMatches(sf.Tag.Get("json"), name) {
			return rv.Field(i).Interface()
		}
		if equalFold(sf.Name, name) {
			return rv.Field(i).Interface()
		}
	}
	return nil
}

// tagMatches reports whether a struct tag's first comma-segment equals name.
func tagMatches(tag, name string) bool {
	if tag == "" {
		return false
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			tag = tag[:i]
			break
		}
	}
	return tag == name
}

// equalFold is a small ASCII case-insensitive compare (avoids strings import
// churn and matches GraphQL's ASCII identifier rules).
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// coerceArguments coerces the parsed argument literals against their declared
// definitions, applying defaults for omitted args and validating required
// (NonNull) arguments.
func (ex *executor) coerceArguments(defs Args, raw map[string]value) (ArgValues, error) {
	out := ArgValues{}
	for name, def := range defs {
		lit, present := raw[name]
		if !present {
			if def.Default != nil {
				out[name] = def.Default
				continue
			}
			if _, isNonNull := def.Type.(*NonNull); isNonNull {
				return nil, fmt.Errorf("missing required argument %q", name)
			}
			continue
		}
		cv, err := ex.coerceValue(def.Type, lit)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %w", name, err)
		}
		out[name] = cv
	}
	// Reject arguments not declared on the field.
	for name := range raw {
		if _, ok := defs[name]; !ok {
			return nil, fmt.Errorf("unknown argument %q", name)
		}
	}
	return out, nil
}

// coerceValue coerces a single parsed literal against a schema type, resolving
// variable references, validating NonNull, mapping lists, and descending into
// input objects. It delegates leaf coercion to coerceScalar / enum lookup.
func (ex *executor) coerceValue(t Type, v value) (any, error) {
	switch tt := t.(type) {
	case *NonNull:
		if v.kind == valNull {
			return nil, fmt.Errorf("expected non-null value of type %q", tt.TypeName())
		}
		if v.kind == valVariable {
			rv, err := ex.resolveVar(v.enumVal)
			if err != nil {
				return nil, err
			}
			if rv == nil {
				return nil, fmt.Errorf("variable $%s is null for non-null type %q", v.enumVal, tt.TypeName())
			}
			return ex.coerceRuntime(tt.OfType, rv)
		}
		return ex.coerceValue(tt.OfType, v)
	case *List:
		if v.kind == valNull {
			return nil, nil
		}
		if v.kind == valVariable {
			rv, err := ex.resolveVar(v.enumVal)
			if err != nil {
				return nil, err
			}
			return ex.coerceRuntime(tt, rv)
		}
		if v.kind != valList {
			// A single value coerces to a one-element list per the spec.
			elem, err := ex.coerceValue(tt.OfType, v)
			if err != nil {
				return nil, err
			}
			return []any{elem}, nil
		}
		out := make([]any, len(v.listVal))
		for i, item := range v.listVal {
			cv, err := ex.coerceValue(tt.OfType, item)
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	}

	if v.kind == valVariable {
		rv, err := ex.resolveVar(v.enumVal)
		if err != nil {
			return nil, err
		}
		return ex.coerceRuntime(t, rv)
	}
	if v.kind == valNull {
		return nil, nil
	}

	switch tt := t.(type) {
	case *Scalar:
		return coerceScalarLiteral(tt, v)
	case *Enum:
		if v.kind != valEnum {
			return nil, fmt.Errorf("expected enum value for %q", tt.Name)
		}
		ev, ok := tt.value(v.enumVal)
		if !ok {
			return nil, fmt.Errorf("%q is not a valid value for enum %q", v.enumVal, tt.Name)
		}
		return ev, nil
	case *InputObject:
		if v.kind != valObject {
			return nil, fmt.Errorf("expected input object for %q", tt.Name)
		}
		return ex.coerceInputObject(tt, v.objVal)
	}
	return nil, fmt.Errorf("cannot coerce value to type %q", t.TypeName())
}

// coerceInputObject coerces a parsed object literal against an InputObject's
// declared fields, applying defaults and validating required fields and unknown
// keys.
func (ex *executor) coerceInputObject(io *InputObject, fields map[string]value) (map[string]any, error) {
	out := map[string]any{}
	for name, fdef := range io.Fields {
		lit, present := fields[name]
		if !present {
			if fdef.Default != nil {
				out[name] = fdef.Default
				continue
			}
			if _, isNonNull := fdef.Type.(*NonNull); isNonNull {
				return nil, fmt.Errorf("input %q missing required field %q", io.Name, name)
			}
			continue
		}
		cv, err := ex.coerceValue(fdef.Type, lit)
		if err != nil {
			return nil, fmt.Errorf("input %q field %q: %w", io.Name, name, err)
		}
		out[name] = cv
	}
	for name := range fields {
		if _, ok := io.Fields[name]; !ok {
			return nil, fmt.Errorf("input %q has unknown field %q", io.Name, name)
		}
	}
	return out, nil
}

// coerceScalarLiteral coerces a parsed literal into the Go value of a built-in
// scalar. It is the literal-side companion to coerceScalar.
func coerceScalarLiteral(s *Scalar, v value) (any, error) {
	switch s.Name {
	case "Int":
		switch v.kind {
		case valInt:
			n, err := strconv.ParseInt(v.intVal, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid Int literal %q", v.intVal)
			}
			return n, nil
		}
		return nil, fmt.Errorf("expected Int, got %s", v.describe())
	case "Float":
		switch v.kind {
		case valFloat:
			f, err := strconv.ParseFloat(v.floatVal, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid Float literal %q", v.floatVal)
			}
			return f, nil
		case valInt:
			f, err := strconv.ParseFloat(v.intVal, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid Float literal %q", v.intVal)
			}
			return f, nil
		}
		return nil, fmt.Errorf("expected Float, got %s", v.describe())
	case "String":
		if v.kind == valString {
			return v.strVal, nil
		}
		return nil, fmt.Errorf("expected String, got %s", v.describe())
	case "Boolean":
		if v.kind == valBool {
			return v.boolVal, nil
		}
		return nil, fmt.Errorf("expected Boolean, got %s", v.describe())
	case "ID":
		switch v.kind {
		case valString:
			return v.strVal, nil
		case valInt:
			return v.intVal, nil
		}
		return nil, fmt.Errorf("expected ID, got %s", v.describe())
	}
	// Custom scalar: pass through a best-effort runtime value.
	return coerceScalar(s, literalRuntime(v))
}

// describe returns a short human label for a parsed value's kind (error text).
func (v value) describe() string {
	switch v.kind {
	case valNull:
		return "null"
	case valInt:
		return "Int"
	case valFloat:
		return "Float"
	case valString:
		return "String"
	case valBool:
		return "Boolean"
	case valEnum:
		return "Enum"
	case valVariable:
		return "$" + v.enumVal
	case valList:
		return "List"
	case valObject:
		return "Object"
	}
	return "value"
}

// literalRuntime converts a parsed literal to a plain Go runtime value, used as
// the input to custom-scalar coercion.
func literalRuntime(v value) any {
	switch v.kind {
	case valInt:
		if n, err := strconv.ParseInt(v.intVal, 10, 64); err == nil {
			return n
		}
		return v.intVal
	case valFloat:
		if f, err := strconv.ParseFloat(v.floatVal, 64); err == nil {
			return f
		}
		return v.floatVal
	case valString:
		return v.strVal
	case valBool:
		return v.boolVal
	case valEnum:
		return v.enumVal
	case valList:
		out := make([]any, len(v.listVal))
		for i, it := range v.listVal {
			out[i] = literalRuntime(it)
		}
		return out
	case valObject:
		out := map[string]any{}
		for k, it := range v.objVal {
			out[k] = literalRuntime(it)
		}
		return out
	}
	return nil
}

// coerceScalar coerces an already-runtime value (from variables or custom
// scalars) into the Go representation of a built-in scalar. It is the public
// hook referenced by the package doc; variable coercion routes through it.
func coerceScalar(s *Scalar, val any) (any, error) {
	if val == nil {
		return nil, nil
	}
	switch s.Name {
	case "Int":
		switch v := val.(type) {
		case int:
			return int64(v), nil
		case int32:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			if v != float64(int64(v)) {
				return nil, fmt.Errorf("Int cannot represent non-integer %v", v)
			}
			return int64(v), nil
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("Int cannot represent %q", v)
			}
			return n, nil
		}
		return nil, fmt.Errorf("Int cannot represent %T", val)
	case "Float":
		switch v := val.(type) {
		case float64:
			return v, nil
		case float32:
			return float64(v), nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("Float cannot represent %q", v)
			}
			return f, nil
		}
		return nil, fmt.Errorf("Float cannot represent %T", val)
	case "String":
		switch v := val.(type) {
		case string:
			return v, nil
		case fmt.Stringer:
			return v.String(), nil
		}
		return nil, fmt.Errorf("String cannot represent %T", val)
	case "Boolean":
		if b, ok := val.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("Boolean cannot represent %T", val)
	case "ID":
		switch v := val.(type) {
		case string:
			return v, nil
		case int:
			return strconv.Itoa(v), nil
		case int64:
			return strconv.FormatInt(v, 10), nil
		case float64:
			return strconv.FormatInt(int64(v), 10), nil
		}
		return nil, fmt.Errorf("ID cannot represent %T", val)
	}
	// Custom scalar: pass through unchanged.
	return val, nil
}

// coerceRuntime coerces a plain runtime value (from a JSON variable) against a
// schema type, validating NonNull/List and descending into input objects.
func (ex *executor) coerceRuntime(t Type, val any) (any, error) {
	switch tt := t.(type) {
	case *NonNull:
		if val == nil {
			return nil, fmt.Errorf("expected non-null value of type %q", tt.TypeName())
		}
		return ex.coerceRuntime(tt.OfType, val)
	case *List:
		if val == nil {
			return nil, nil
		}
		rv := reflect.ValueOf(val)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			elem, err := ex.coerceRuntime(tt.OfType, val)
			if err != nil {
				return nil, err
			}
			return []any{elem}, nil
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			cv, err := ex.coerceRuntime(tt.OfType, rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	case *Scalar:
		return coerceScalar(tt, val)
	case *Enum:
		name, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("expected enum string for %q, got %T", tt.Name, val)
		}
		ev, ok := tt.value(name)
		if !ok {
			return nil, fmt.Errorf("%q is not a valid value for enum %q", name, tt.Name)
		}
		return ev, nil
	case *InputObject:
		if val == nil {
			return nil, nil
		}
		m, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object for input %q, got %T", tt.Name, val)
		}
		return ex.coerceRuntimeInput(tt, m)
	}
	return val, nil
}

// coerceRuntimeInput coerces a runtime map against an InputObject's fields.
func (ex *executor) coerceRuntimeInput(io *InputObject, m map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for name, fdef := range io.Fields {
		raw, present := m[name]
		if !present {
			if fdef.Default != nil {
				out[name] = fdef.Default
				continue
			}
			if _, isNonNull := fdef.Type.(*NonNull); isNonNull {
				return nil, fmt.Errorf("input %q missing required field %q", io.Name, name)
			}
			continue
		}
		cv, err := ex.coerceRuntime(fdef.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("input %q field %q: %w", io.Name, name, err)
		}
		out[name] = cv
	}
	for name := range m {
		if _, ok := io.Fields[name]; !ok {
			return nil, fmt.Errorf("input %q has unknown field %q", io.Name, name)
		}
	}
	return out, nil
}

// resolveVar returns the coerced variable value for name (already coerced in
// coerceVariables), erroring when undeclared.
func (ex *executor) resolveVar(name string) (any, error) {
	v, ok := ex.vars[name]
	if !ok {
		return nil, fmt.Errorf("variable $%s is not defined", name)
	}
	return v, nil
}

// coerceVariables validates and coerces the request's raw variables against the
// operation's declared variable types, filling in declared defaults.
func (ex *executor) coerceVariables(op *operation, raw map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, def := range op.variables {
		t, err := ex.resolveTypeRef(def.typ)
		if err != nil {
			return nil, fmt.Errorf("variable $%s: %w", def.name, err)
		}
		rawVal, present := raw[def.name]
		if !present {
			if hasDefault(def) {
				cv, err := ex.coerceValue(t, def.defaultVal)
				if err != nil {
					return nil, fmt.Errorf("variable $%s default: %w", def.name, err)
				}
				out[def.name] = cv
				continue
			}
			if _, isNonNull := t.(*NonNull); isNonNull {
				return nil, fmt.Errorf("variable $%s of type %q is required", def.name, t.TypeName())
			}
			continue
		}
		cv, err := ex.coerceRuntime(t, rawVal)
		if err != nil {
			return nil, fmt.Errorf("variable $%s: %w", def.name, err)
		}
		out[def.name] = cv
	}
	return out, nil
}

// hasDefault reports whether a variable definition declared a default literal.
func hasDefault(def *variableDef) bool {
	return def.defaultVal.kind != valNull ||
		def.defaultVal.intVal != "" || def.defaultVal.floatVal != "" ||
		def.defaultVal.strVal != "" || def.defaultVal.enumVal != "" ||
		def.defaultVal.listVal != nil || def.defaultVal.objVal != nil
}

// resolveTypeRef resolves a parsed typeRef into a schema Type using the
// compiled schema's named-type index plus list/non-null wrappers.
func (ex *executor) resolveTypeRef(tr typeRef) (Type, error) {
	var t Type
	if tr.list != nil {
		inner, err := ex.resolveTypeRef(*tr.list)
		if err != nil {
			return nil, err
		}
		t = NewList(inner)
	} else {
		named, ok := ex.cs.types[tr.name]
		if !ok {
			// Built-in scalars are always available even if unused elsewhere.
			switch tr.name {
			case "Int":
				named = Int
			case "Float":
				named = Float
			case "String":
				named = String
			case "Boolean":
				named = Boolean
			case "ID":
				named = ID
			default:
				return nil, fmt.Errorf("unknown type %q", tr.name)
			}
		}
		t = named
	}
	if tr.nonNull {
		t = NewNonNull(t)
	}
	return t, nil
}
