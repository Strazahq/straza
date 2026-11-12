package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// unpairedBodies names each /v1/admin operation with a request body that
// its handler reads without a function bodyDecoders names, and why.
// TestOpenAPIRequestBodiesMatchHandlers compares every other operation, and
// fails on an entry whose operation now pairs, declares no request body or
// is no longer a route.
var unpairedBodies = map[string]string{
	"POST /v1/admin/apps":                   "the body is an app.yaml manifest that manager.Parse reads, so its fields are the manifest's and no JSON request is decoded",
	"POST /v1/admin/apps/import":            "the body is an MCP registry server.json that manager.Import converts, so its fields are the registry's and not this route's",
	"PUT /v1/admin/policies":                "the body is one PolicySet document in YAML that policy.ParseAll reads, so its fields are the PolicySet schema's",
	"POST /v1/admin/policies/validate":      "the body is PolicySet YAML that policy.Parse reads, so its fields are the PolicySet schema's",
	"POST /v1/admin/approvers/enroll-token": "the route answers 403 to every caller and reads the body leniently, only to name a user in the refusal's record",
}

// bodyDecoders names the functions that decode an admin request body, each
// with the index of the argument it decodes into.
var bodyDecoders = map[string]int{"decodeAdmin": 1, "decodeDraftBody": 2}

// TestOpenAPIRequestBodiesMatchHandlers fails when the request schema of a
// /v1/admin operation in pkg/api/openapi.yaml and the JSON fields its
// handler decodes differ, in either direction. decodeAdmin answers 400 to a
// field the schema declares and the handler's type lacks, and a field the
// type takes and the schema lacks is missing from the contract. The top
// level is compared, and one level down where the schema declares the
// properties of an object and the Go field is a struct.
func TestOpenAPIRequestBodiesMatchHandlers(t *testing.T) {
	t.Parallel()
	doc := loadOpenAPIDoc(t)
	src := loadGoSource(t)
	routes := src.bodyRoutes(t)
	paired, skipped := 0, 0
	for _, rt := range routes {
		op := rt.method + " " + rt.path
		body, hasBody := doc.requestBody(rt.method, rt.path)
		schema, isJSON := doc.jsonSchema(body)
		site, decodes := src.findDecode(rt.handler, map[string]bool{})
		_, named := unpairedBodies[op]
		switch {
		case named && !hasBody:
			t.Errorf("unpairedBodies names %s, which declares no request body in pkg/api/openapi.yaml. Remove the entry.", op)
		case named && decodes:
			t.Errorf("unpairedBodies names %s, but %s now decodes its body with %s at %s, so this test compares it. Remove the entry.", op, site.handler, site.decoder, site.at)
		case named:
			skipped++
		case decodes && !isJSON:
			t.Errorf("%s: %s decodes a JSON body with %s at %s, and pkg/api/openapi.yaml declares no application/json request body for it. Add the request body and its schema.", op, site.handler, site.decoder, site.at)
		case hasBody && !decodes:
			t.Errorf("%s: pkg/api/openapi.yaml declares a request body, and this test finds no call to decodeAdmin or decodeDraftBody in %s or in a function it hands the request to. "+
				"Decode the body with decodeAdmin, add a new decoding helper to bodyDecoders, or name the operation in unpairedBodies with the reason.", op, rt.handler)
		case decodes:
			paired++
			src.compareBody(t, doc, op, site, schema)
		}
	}
	for op := range unpairedBodies {
		if !slices.ContainsFunc(routes, func(rt bodyRoute) bool { return rt.method+" "+rt.path == op }) {
			t.Errorf("unpairedBodies names %s, which is not a /v1/admin route in routeTable. Remove the entry.", op)
		}
	}
	t.Logf("request bodies: %d /v1/admin operations compared, %d named in unpairedBodies", paired, skipped)
}

// compareBody reports each field name that one side of an operation's
// request has and the other lacks: the properties of schema against the
// fields of the type site decodes into.
func (s *goSource) compareBody(t *testing.T, doc openAPIDoc, op string, site decodeSite, schema map[string]any) {
	t.Helper()
	if site.typ.expr == nil {
		t.Errorf("%s: %s at %s decodes into a value this test cannot trace to its type. Decode into a variable declared with var name Type before the call.", op, site.decoder, site.at)
		return
	}
	fields, ok := s.objectFields(site.typ)
	if !ok {
		t.Errorf("%s: %s at %s decodes into a type whose fields this test cannot read, such as a map or a type with its own UnmarshalJSON. "+
			"Decode into a struct, or name the operation in unpairedBodies with the reason.", op, site.decoder, site.at)
		return
	}
	props, _ := doc.properties(schema)
	reportDiff(t, op, "", props, fields, site)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		field, both := fields[name]
		if !both {
			continue
		}
		innerProps, declared := doc.properties(props[name])
		innerFields, isStruct := s.objectFields(field)
		if declared && isStruct {
			reportDiff(t, op, name+".", innerProps, innerFields, site)
		}
	}
}

// reportDiff reports each name in fields and not in props, and each name
// in props and not in fields, prefixed with the path of their object.
func reportDiff(t *testing.T, op, prefix string, props map[string]map[string]any, fields map[string]goType, site decodeSite) {
	t.Helper()
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: %s takes the field %q in the body it decodes at %s, and the request schema in pkg/api/openapi.yaml does not declare it. "+
				"Add %q to the schema, described by what the route does with it.", op, site.handler, prefix+name, site.at, prefix+name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(props)) {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s: the request schema in pkg/api/openapi.yaml declares the field %q, and %s answers 400 to it, because the type its call at %s decodes into has no such field. "+
				"Remove %q from the schema, or take it in the handler.", op, prefix+name, site.handler, site.at, prefix+name)
		}
	}
}

// openAPIDoc is pkg/api/openapi.yaml read as plain YAML maps.
type openAPIDoc map[string]any

// loadOpenAPIDoc reads pkg/api/openapi.yaml.
func loadOpenAPIDoc(t *testing.T) openAPIDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	return doc
}

// requestBody answers the request body of an operation, and whether the
// operation declares one.
func (d openAPIDoc) requestBody(method, path string) (map[string]any, bool) {
	op := asMap(asMap(asMap(d["paths"])[path])[strings.ToLower(method)])
	body, ok := op["requestBody"]
	return d.deref(asMap(body)), ok
}

// jsonSchema answers the application/json schema of a request body, and
// whether the body declares one.
func (d openAPIDoc) jsonSchema(body map[string]any) (map[string]any, bool) {
	media, ok := asMap(body["content"])["application/json"]
	return asMap(asMap(media)["schema"]), ok
}

// deref follows node's local $ref, such as #/components/schemas/Name, to
// the node it points to.
func (d openAPIDoc) deref(node map[string]any) map[string]any {
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		var cur any = map[string]any(d)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = asMap(cur)[part]
		}
		node = asMap(cur)
	}
}

// properties answers the property schemas of an object schema, or of the
// items of an array schema, with $ref and allOf resolved. ok is false when
// the schema declares no properties.
func (d openAPIDoc) properties(schema map[string]any) (map[string]map[string]any, bool) {
	schema = d.deref(schema)
	if schema["type"] == "array" {
		schema = d.deref(asMap(schema["items"]))
	}
	props := map[string]map[string]any{}
	parts, _ := schema["allOf"].([]any)
	for _, part := range parts {
		inner, _ := d.properties(asMap(part))
		maps.Copy(props, inner)
	}
	for name, prop := range asMap(schema["properties"]) {
		props[name] = asMap(prop)
	}
	return props, len(props) > 0
}

// asMap answers v as a YAML mapping, or nil when it is none.
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// goSource is the parsed non-test Go of this package, and of the module
// packages its request types name, parsed when first named.
type goSource struct {
	t      *testing.T
	fset   *token.FileSet
	module string
	server *goPackage
	pkgs   map[string]*goPackage
}

// goPackage indexes one parsed package: its functions and the methods of
// App by name, its type declarations by name, and the names of the types
// that declare their own UnmarshalJSON.
type goPackage struct {
	funcs  map[string]goFunc
	types  map[string]goType
	custom map[string]bool
}

// goFunc is a function declaration with the imports of its file.
type goFunc struct {
	decl    *ast.FuncDecl
	imports map[string]string
}

// goType is a type expression with its package and the imports of its
// file, so a qualified name in it resolves.
type goType struct {
	expr    ast.Expr
	pkg     *goPackage
	imports map[string]string
}

// loadGoSource parses this package and reads the module path from go.mod.
func loadGoSource(t *testing.T) *goSource {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	s := &goSource{t: t, fset: token.NewFileSet(), pkgs: map[string]*goPackage{}}
	for line := range strings.Lines(string(raw)) {
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			s.module = strings.TrimSpace(rest)
		}
	}
	s.server = s.parsePackage(".")
	return s
}

// parsePackage parses the non-test Go files in dir.
func (s *goSource) parsePackage(dir string) *goPackage {
	s.t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.t.Fatal(err)
	}
	pkg := &goPackage{funcs: map[string]goFunc{}, types: map[string]goType{}, custom: map[string]bool{}}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(s.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			s.t.Fatal(err)
		}
		imports := importNames(file)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				recv := receiverType(d)
				switch {
				case d.Name.Name == "UnmarshalJSON" && recv != "":
					pkg.custom[recv] = true
				case recv == "" || recv == "App":
					pkg.funcs[d.Name.Name] = goFunc{d, imports}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						pkg.types[ts.Name.Name] = goType{ts.Type, pkg, imports}
					}
				}
			}
		}
	}
	return pkg
}

// importNames maps the name each import of file is used by to its path.
func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = path
	}
	return names
}

// receiverType answers the name of fd's receiver type, empty for a
// function.
func receiverType(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	typ := fd.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// receiverName answers the name fd gives its receiver, empty for a
// function.
func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

// bodyRoute is one /v1/admin entry of routeTable as server.go writes it,
// with the function that serves it once its wrappers are peeled off.
type bodyRoute struct {
	method, path, handler string
}

// bodyRoutes reads the /v1/admin entries of routeTable from its source,
// and fails when they differ from the routes the table holds at run time
// or name a handler this package does not declare, so no route escapes
// the comparison.
func (s *goSource) bodyRoutes(t *testing.T) []bodyRoute {
	t.Helper()
	aliases := map[string]string{}
	var routes []bodyRoute
	read := map[string]bool{}
	ast.Inspect(s.server.funcs["routeTable"].decl, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			id, isIdent := x.Lhs[0].(*ast.Ident)
			sel, isSel := x.Rhs[0].(*ast.SelectorExpr)
			if isIdent && isSel {
				aliases[id.Name] = sel.Sel.Name
			}
		case *ast.CompositeLit:
			rt, ok := routeEntry(x, aliases)
			if ok && strings.HasPrefix(rt.path, "/v1/admin/") {
				routes = append(routes, rt)
				read[rt.method+" "+rt.path] = true
				if _, declared := s.server.funcs[rt.handler]; !declared {
					t.Errorf("%s %s: routeTable serves it with %q, which this test cannot find among the functions and App methods of this package.", rt.method, rt.path, rt.handler)
				}
			}
		}
		return true
	})
	for _, rt := range (&App{}).routeTable() {
		if strings.HasPrefix(rt.pattern, "/v1/admin/") && !read[rt.method+" "+rt.pattern] {
			t.Errorf("%s %s is in routeTable, and this test cannot read it from the source. Write it as a literal {method, pattern, handler} entry.", rt.method, rt.pattern)
		}
	}
	return routes
}

// routeEntry reads one {method, pattern, handler} literal of routeTable.
func routeEntry(lit *ast.CompositeLit, aliases map[string]string) (bodyRoute, bool) {
	if len(lit.Elts) != 3 {
		return bodyRoute{}, false
	}
	method, isMethod := stringLit(lit.Elts[0])
	path, isPath := stringLit(lit.Elts[1])
	return bodyRoute{method, path, handlerName(lit.Elts[2], aliases)}, isMethod && isPath
}

// stringLit answers the value of a string literal.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// handlerName answers the function that serves a route's handler
// expression. A call to a require method, or to a name routeTable gives
// one, wraps the handler it is passed. Any other call builds the handler,
// so the function it calls is the one that serves.
func handlerName(e ast.Expr, aliases map[string]string) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.CallExpr:
		fun := handlerName(x.Fun, aliases)
		if alias, ok := aliases[fun]; ok {
			fun = alias
		}
		if strings.HasPrefix(fun, "require") && len(x.Args) == 1 {
			return handlerName(x.Args[0], aliases)
		}
		return fun
	}
	return ""
}

// decodeSite is the call that decodes an operation's body: the function
// that holds it, the decoder it calls, where, and the type of the variable
// it decodes into. The type's expr is nil when no var declaration of the
// variable precedes the call.
type decodeSite struct {
	handler, decoder string
	at               token.Position
	typ              goType
}

// findDecode finds the first call in fn to a function bodyDecoders names.
// When fn has none, it searches the App methods and package functions fn
// calls that take the request. seen holds the functions already searched.
func (s *goSource) findDecode(fn string, seen map[string]bool) (decodeSite, bool) {
	f, ok := s.server.funcs[fn]
	if !ok || seen[fn] {
		return decodeSite{}, false
	}
	seen[fn] = true
	var call *ast.CallExpr
	var callees []string
	ast.Inspect(f.decl.Body, func(n ast.Node) bool {
		c, isCall := n.(*ast.CallExpr)
		if call != nil || !isCall {
			return call == nil
		}
		switch fun := c.Fun.(type) {
		case *ast.Ident:
			if _, decoder := bodyDecoders[fun.Name]; decoder {
				call = c
				return false
			}
			callees = append(callees, fun.Name)
		case *ast.SelectorExpr:
			if recv, isIdent := fun.X.(*ast.Ident); isIdent && recv.Name == receiverName(f.decl) {
				callees = append(callees, fun.Sel.Name)
			}
		}
		return true
	})
	if call == nil {
		for _, callee := range callees {
			if g, ok := s.server.funcs[callee]; ok && takesRequest(g.decl) {
				if site, found := s.findDecode(callee, seen); found {
					return site, true
				}
			}
		}
		return decodeSite{}, false
	}
	decoder := call.Fun.(*ast.Ident).Name
	site := decodeSite{handler: fn, decoder: decoder, at: s.fset.Position(call.Pos())}
	if i := bodyDecoders[decoder]; i < len(call.Args) {
		if addr, isAddr := call.Args[i].(*ast.UnaryExpr); isAddr && addr.Op == token.AND {
			if id, isIdent := addr.X.(*ast.Ident); isIdent {
				site.typ = s.varType(f, id.Name, call.Pos())
			}
		}
	}
	return site, true
}

// varType answers the type that the last var declaration of name in f
// before pos gives it, with expr nil when there is none.
func (s *goSource) varType(f goFunc, name string, pos token.Pos) goType {
	var typ goType
	ast.Inspect(f.decl.Body, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if ok && spec.Type != nil && spec.Pos() < pos && slices.ContainsFunc(spec.Names, func(id *ast.Ident) bool { return id.Name == name }) {
			typ = goType{spec.Type, s.server, f.imports}
		}
		return true
	})
	return typ
}

// takesRequest reports whether fd has a parameter of type *http.Request.
func takesRequest(fd *ast.FuncDecl) bool {
	for _, p := range fd.Type.Params.List {
		star, isStar := p.Type.(*ast.StarExpr)
		if !isStar {
			continue
		}
		if sel, isSel := star.X.(*ast.SelectorExpr); isSel && sel.Sel.Name == "Request" {
			return true
		}
	}
	return false
}

// objectFields answers the JSON names encoding/json decodes into t, each
// with its Go type, when t is a struct, or a slice of structs, that
// resolve can read. An embedded struct without a json name lends its
// fields, and a field tagged "-" is left out.
func (s *goSource) objectFields(t goType) (map[string]goType, bool) {
	t, ok := s.resolve(t)
	if arr, isArr := t.expr.(*ast.ArrayType); ok && isArr {
		t, ok = s.resolve(goType{arr.Elt, t.pkg, t.imports})
	}
	st, isStruct := t.expr.(*ast.StructType)
	if !ok || !isStruct {
		return nil, false
	}
	fields := map[string]goType{}
	for _, f := range st.Fields.List {
		ft := goType{f.Type, t.pkg, t.imports}
		name, skip := jsonName(f)
		switch {
		case skip:
		case name != "":
			fields[name] = ft
		case len(f.Names) == 0:
			// A field of the outer struct wins over a promoted one.
			inner, _ := s.objectFields(ft)
			for k, v := range inner {
				if _, outer := fields[k]; !outer {
					fields[k] = v
				}
			}
		default:
			for _, id := range f.Names {
				if id.IsExported() {
					fields[id.Name] = ft
				}
			}
		}
	}
	return fields, true
}

// jsonName answers the name a struct field's json tag gives it, empty for
// a field without one, and whether the tag leaves the field out.
func jsonName(f *ast.Field) (string, bool) {
	if f.Tag == nil {
		return "", false
	}
	raw, _ := strconv.Unquote(f.Tag.Value)
	tag := reflect.StructTag(raw).Get("json")
	if tag == "-" {
		return "", true
	}
	name, _, _ := strings.Cut(tag, ",")
	return name, false
}

// resolve follows t through pointers and declared names to the type they
// stand for. It answers false for a type with its own UnmarshalJSON and
// for a type from outside this module, such as json.RawMessage, whose
// fields this test does not read. A predeclared name resolves to itself.
func (s *goSource) resolve(t goType) (goType, bool) {
	for {
		switch x := t.expr.(type) {
		case *ast.StarExpr:
			t.expr = x.X
		case *ast.Ident:
			decl, declared := t.pkg.types[x.Name]
			if !declared {
				return t, true
			}
			if t.pkg.custom[x.Name] {
				return t, false
			}
			t = decl
		case *ast.SelectorExpr:
			pkg := s.imported(t, x.X)
			if pkg == nil || pkg.custom[x.Sel.Name] {
				return t, false
			}
			decl, declared := pkg.types[x.Sel.Name]
			if !declared {
				return t, false
			}
			t = decl
		default:
			return t, true
		}
	}
}

// imported answers the module package that qualifier names in t's file,
// parsing it on first use, or nil for a package outside this module.
func (s *goSource) imported(t goType, qualifier ast.Expr) *goPackage {
	id, ok := qualifier.(*ast.Ident)
	if !ok {
		return nil
	}
	rel, inModule := strings.CutPrefix(t.imports[id.Name], s.module+"/")
	if !inModule {
		return nil
	}
	if s.pkgs[rel] == nil {
		s.pkgs[rel] = s.parsePackage(filepath.Join("..", "..", filepath.FromSlash(rel)))
	}
	return s.pkgs[rel]
}
