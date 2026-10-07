package main

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// shapeRow is one line of a request-body or object table: the field, its
// type, whether the caller must send it, and the first sentence of its
// description.
type shapeRow struct {
	Field    string
	Type     string
	Required bool
	Meaning  string
}

// answerRow is one line of a responses table: the status and what the
// answer carries.
type answerRow struct {
	Status string
	Answer string
}

// objectRef names one entry of components.schemas or components.responses
// that the page has to describe in the objects section.
type objectRef struct {
	kind string
	name string
}

// shapes resolves references against the document's components and keeps
// the objects the page has referenced, in first-reference order, so the
// objects section lists exactly what the tables point at.
type shapes struct {
	schemas   *yaml.Node
	responses *yaml.Node
	order     []objectRef
	seen      map[objectRef]bool
}

func newShapes(doc *apiDoc) *shapes {
	return &shapes{schemas: &doc.Components.Schemas, responses: &doc.Components.Responses, seen: map[objectRef]bool{}}
}

// mapGet returns the value under key in a mapping node, or nil.
func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// mapKeys returns the keys of a mapping node in document order.
func mapKeys(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		keys = append(keys, n.Content[i].Value)
	}
	return keys
}

// seqValues returns the scalar items of a sequence node.
func seqValues(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		out = append(out, c.Value)
	}
	return out
}

// scalar returns a node's value, or "" for a missing or non-scalar node.
func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// refTarget splits a local reference into its components kind and name.
func refTarget(ref string) (objectRef, bool) {
	parts := strings.Split(strings.TrimPrefix(ref, "#/components/"), "/")
	if !strings.HasPrefix(ref, "#/components/") || len(parts) != 2 || parts[1] == "" {
		return objectRef{}, false
	}
	return objectRef{kind: parts[0], name: parts[1]}, true
}

// resolve returns the node a local reference points at and records the
// object for the objects section.
func (s *shapes) resolve(ref string) (*yaml.Node, objectRef, error) {
	target, ok := refTarget(ref)
	if !ok {
		return nil, objectRef{}, fmt.Errorf("reference %q is not a local components reference", ref)
	}
	var node *yaml.Node
	switch target.kind {
	case "schemas":
		node = mapGet(s.schemas, target.name)
	case "responses":
		node = mapGet(s.responses, target.name)
	}
	if node == nil {
		return nil, objectRef{}, fmt.Errorf("reference %q points at nothing in components", ref)
	}
	if !s.seen[target] {
		s.seen[target] = true
		s.order = append(s.order, target)
	}
	return node, target, nil
}

// firstSentence returns the first sentence of a description with its
// whitespace collapsed.
func firstSentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if i := sentenceEndRe.FindStringIndex(text); i != nil {
		return strings.TrimSpace(text[:i[1]])
	}
	return text
}

var sentenceEndRe = regexp.MustCompile(`[.!?](\s|$)`)

// meaningCell returns the first sentence of a description as a table cell,
// with the enum appended when the schema has one. It refuses a sentence over
// the cell cap, one carrying a version or decision token, and one with an
// em dash, so the fix lands in the document.
func meaningCell(who, description string, enum []string) (string, error) {
	cell := firstSentence(description)
	if strings.Contains(description, "\u2014") {
		return "", fmt.Errorf("%s: the description in %s carries an em dash, use a period, a comma, a colon or parentheses", who, openAPIPath)
	}
	if len(strings.Fields(cell)) > maxCellWords || noisySummaryRe.MatchString(cell) {
		return "", fmt.Errorf("%s: the description's first sentence in %s is over %d words or carries a version or decision token, open with one plain sentence", who, openAPIPath, maxCellWords)
	}
	if len(enum) > 0 {
		if cell != "" && !strings.HasSuffix(cell, ".") {
			cell += "."
		}
		cell = strings.TrimSpace(cell + " One of " + codeList(enum) + ".")
	}
	return strings.ReplaceAll(cell, "|", "\\|"), nil
}

// typeCell renders a schema's type: a scalar type, a list of its items, or
// the referenced object's name in backticks. A referenced object is
// recorded for the objects section.
func (s *shapes) typeCell(who string, schema *yaml.Node) (string, error) {
	if ref := scalar(mapGet(schema, "$ref")); ref != "" {
		_, target, err := s.resolve(ref)
		if err != nil {
			return "", fmt.Errorf("%s: %w", who, err)
		}
		return "`" + target.name + "`", nil
	}
	switch t := scalar(mapGet(schema, "type")); t {
	case "array":
		items, err := s.typeCell(who, mapGet(schema, "items"))
		if err != nil {
			return "", err
		}
		return "list of " + items, nil
	case "":
		if mapGet(schema, "properties") != nil {
			return "object", nil
		}
		return "any", nil
	default:
		return t, nil
	}
}

// fieldRows renders one row per property of an object schema in document
// order. A property that is itself an inline object with properties renders
// as "object"; its fields are the document's to name under components.
func (s *shapes) fieldRows(who string, schema *yaml.Node) ([]shapeRow, error) {
	required := map[string]bool{}
	for _, name := range seqValues(mapGet(schema, "required")) {
		required[name] = true
	}
	props := mapGet(schema, "properties")
	var rows []shapeRow
	for _, name := range mapKeys(props) {
		prop := mapGet(props, name)
		typ, err := s.typeCell(who+" field "+name, prop)
		if err != nil {
			return nil, err
		}
		meaning, err := meaningCell(who+" field "+name, scalar(mapGet(prop, "description")), seqValues(mapGet(prop, "enum")))
		if err != nil {
			return nil, err
		}
		rows = append(rows, shapeRow{Field: name, Type: typ, Required: required[name], Meaning: meaning})
	}
	return rows, nil
}

// requestRows renders the request body of an operation: the fields of a JSON
// body, or one row for a document or form body, which then needs a
// description on the requestBody itself.
func (s *shapes) requestRows(op apiOperation) ([]shapeRow, error) {
	who := strings.ToUpper(op.Method) + " " + op.Path
	body := &op.RequestBody
	if body.Kind == 0 {
		return nil, nil
	}
	content := mapGet(body, "content")
	mediaTypes := mapKeys(content)
	if len(mediaTypes) == 0 {
		return nil, fmt.Errorf("%s: the request body in %s declares no content", who, openAPIPath)
	}
	mediaType := mediaTypes[0]
	schema := mapGet(mapGet(content, mediaType), "schema")
	if mediaType != "application/json" {
		meaning, err := meaningCell(who+" request body", scalar(mapGet(body, "description")), nil)
		if err != nil {
			return nil, err
		}
		if meaning == "" {
			return nil, fmt.Errorf("%s: the %s request body in %s needs a description sentence", who, mediaType, openAPIPath)
		}
		label := "the document"
		if strings.Contains(mediaType, "form") {
			label = "form fields"
		}
		return []shapeRow{{Field: label, Type: mediaType, Required: scalar(mapGet(body, "required")) == "true", Meaning: meaning}}, nil
	}
	if ref := scalar(mapGet(schema, "$ref")); ref != "" {
		node, _, err := s.resolve(ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", who, err)
		}
		schema = node
	}
	return s.fieldRows(who, schema)
}

// answerRows renders the responses of an operation in document order: a
// component response or a named object by name, a list by its items, an
// empty body as such, and an inline object by its field names.
func (s *shapes) answerRows(op apiOperation) ([]answerRow, error) {
	who := strings.ToUpper(op.Method) + " " + op.Path
	var rows []answerRow
	for _, status := range mapKeys(&op.Responses) {
		resp := mapGet(&op.Responses, status)
		answer, err := s.answerCell(who+" "+status, resp)
		if err != nil {
			return nil, err
		}
		rows = append(rows, answerRow{Status: status, Answer: answer})
	}
	return rows, nil
}

func (s *shapes) answerCell(who string, resp *yaml.Node) (string, error) {
	if ref := scalar(mapGet(resp, "$ref")); ref != "" {
		_, target, err := s.resolve(ref)
		if err != nil {
			return "", fmt.Errorf("%s: %w", who, err)
		}
		return "`" + target.name + "`", nil
	}
	content := mapGet(resp, "content")
	mediaTypes := mapKeys(content)
	if len(mediaTypes) == 0 {
		return "an empty body", nil
	}
	mediaType := mediaTypes[0]
	schema := mapGet(mapGet(content, mediaType), "schema")
	if mediaType != "application/json" {
		return "a " + mediaType + " body", nil
	}
	if scalar(mapGet(schema, "$ref")) != "" || scalar(mapGet(schema, "type")) == "array" {
		typ, err := s.typeCell(who, schema)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(typ, "list of ") {
			return "a " + typ, nil
		}
		return typ, nil
	}
	names := mapKeys(mapGet(schema, "properties"))
	if len(names) == 0 {
		return "an object", nil
	}
	cell := "an object with " + nameList(names)
	if len(strings.Fields(cell)) > maxCellWords {
		return "", fmt.Errorf("%s: the inline answer object in %s has too many fields for a cell, name it under components.schemas", who, openAPIPath)
	}
	return cell, nil
}

// objectRows renders the fields of every referenced object, in
// first-reference order, resolving the objects those fields reference in
// turn. The first row of an object carries its own description.
func (s *shapes) objectRows() ([]objectRows, error) {
	var out []objectRows
	for i := 0; i < len(s.order); i++ {
		ref := s.order[i]
		node := mapGet(s.schemas, ref.name)
		description := scalar(mapGet(node, "description"))
		if ref.kind == "responses" {
			node = mapGet(s.responses, ref.name)
			description = scalar(mapGet(node, "description"))
			if content := mapGet(node, "content"); content != nil {
				node = mapGet(mapGet(content, mapKeys(content)[0]), "schema")
				if d := scalar(mapGet(node, "description")); d != "" {
					description = d
				}
			}
		}
		who := ref.kind + "." + ref.name
		meaning, err := meaningCell(who, description, nil)
		if err != nil {
			return nil, err
		}
		rows, err := s.fieldRows(who, node)
		if err != nil {
			return nil, err
		}
		out = append(out, objectRows{Name: ref.name, Meaning: meaning, Fields: rows})
	}
	return out, nil
}

// objectRows is one object of the objects section.
type objectRows struct {
	Name    string
	Meaning string
	Fields  []shapeRow
}

// writeShapeTables appends the request-body and responses tables of one
// section to the page.
func (s *shapes) writeShapeTables(b *strings.Builder, ops []apiOperation) error {
	var requests []string
	var answers []string
	for _, op := range ops {
		who := "`" + strings.ToUpper(op.Method) + " " + op.Path + "`"
		rows, err := s.requestRows(op)
		if err != nil {
			return err
		}
		for _, r := range rows {
			requests = append(requests, fmt.Sprintf("| %s | `%s` | %s | %s | %s |", who, r.Field, r.Type, yesOrBlank(r.Required), r.Meaning))
		}
		arows, err := s.answerRows(op)
		if err != nil {
			return err
		}
		for _, r := range arows {
			answers = append(answers, fmt.Sprintf("| %s | %s | %s |", who, r.Status, r.Answer))
		}
	}
	if len(requests) > 0 {
		b.WriteString("\nRequest bodies in this section, one row per field.\n\n| Operation | Field | Type | Required | Meaning |\n|---|---|---|---|---|\n")
		b.WriteString(strings.Join(requests, "\n") + "\n")
	}
	if len(answers) > 0 {
		b.WriteString("\nAnswers in this section, by status.\n\n| Operation | Status | Answer |\n|---|---|---|\n")
		b.WriteString(strings.Join(answers, "\n") + "\n")
	}
	return nil
}

// writeObjects appends the objects section: every object the tables above
// named, with its fields.
func (s *shapes) writeObjects(b *strings.Builder) error {
	objects, err := s.objectRows()
	if err != nil {
		return err
	}
	if len(objects) == 0 {
		return nil
	}
	b.WriteString("\n## Objects {#api-objects}\n\nEvery object the tables above name, in the order they first name it, with its fields. A row without a field carries the object's own meaning.\n\n| Object | Field | Type | Meaning |\n|---|---|---|---|\n")
	for _, o := range objects {
		if o.Meaning != "" {
			fmt.Fprintf(b, "| `%s` | | | %s |\n", o.Name, o.Meaning)
		}
		for _, f := range o.Fields {
			required := ""
			if f.Required {
				required = " Required."
			}
			fmt.Fprintf(b, "| `%s` | `%s` | %s | %s |\n", o.Name, f.Field, f.Type, strings.TrimSpace(f.Meaning+required))
		}
	}
	return nil
}

// nameList joins field names in backticks with commas and a final "and".
func nameList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

func yesOrBlank(required bool) string {
	if required {
		return "yes"
	}
	return ""
}
