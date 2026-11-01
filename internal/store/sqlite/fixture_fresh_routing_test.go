// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The pinned subjects include shared setup helpers as well as tests. Keep
// initialization coverage on a genuinely fresh database when fixtures evolve.
type freshFixtureSubject struct{ file, name string }

var freshFixtureSubjects = []freshFixtureSubject{
	{"attach_columns_test.go", "TestStoreInstanceID_IndependentCreations_UseDifferentIDs"},
	{"backup_test.go", "TestBackup_CreatesValidCopy"},
	{"backup_test.go", "TestBackup_EmptyDB_Succeeds"},
	{"backup_test.go", "TestBackup_EmptyDestPath_ReturnsError"},
	{"backup_test.go", "TestBackup_InvalidPath_ReturnsError"},
	{"backup_test.go", "TestBackup_MultipleNodes_PreservesAll"},
	{"backup_test.go", "TestBackup_PreservesData"},
	{"backup_test.go", "TestBackup_VerificationPasses"},
	{"export_base_compat_test.go", "exportCompatStore"},
	{"export_checksum_utf8_test.go", "seedInvalidUTF8Store"},
	{"export_node_columns_test.go", "TestVerifyExportChecksum_SchemaV100File_VerifiesAndImports"},
	{"export_schema100_form_test.go", "TestSchema100Form_Checksum_ComputedAs053"},
	{"export_schema100_form_test.go", "TestSchema100Form_EveryColumnNode_KeepsOnlyThe100Fields"},
	{"export_schema100_form_test.go", "TestSchema100Form_StoreWithoutNodes_NodesStayNull"},
	{"export_test.go", "TestExport_IncludesSchemaVersion"},
	{"hook_ledger_test.go", "TestHookLedger_Prune"},
	{"import_replace_diff_test.go", "TestDiffReplace_SameTaskAtUpgradeWithOtherTitle_ListedAsLoss"},
	{"node_list_multifilter_test.go", "TestListNodes_MultiUnder_SQLInjectionResistance"},
	{"node_undelete_cascade_test.go", "seedUndeleteTree"},
	{"relay_cursor_test.go", "TestLookupRelayJournalSeqs"},
	{"relay_cursor_test.go", "TestRepublishRelayFrom_Refusals"},
	{"scan_helpers_test.go", "TestQueryRow_NoRows_ReturnsErrNoRows"},
	{"scan_helpers_test.go", "TestQueryRow_SelectsFromReadDB"},
	{"scan_helpers_test.go", "TestQuery_MultipleRows_ReturnsRows"},
	{"scan_helpers_test.go", "TestWriteDB_CanInsertAndSelect"},
	{"scan_helpers_test.go", "TestWriteDB_ReturnsWriteConnection"},
	{"store_advanced_test.go", "TestBackup_InvalidDestination_ReturnsError"},
	{"store_advanced_test.go", "TestBackup_WithNodes_VerifiesIntegrity"},
	{"store_advanced_test.go", "TestVerify_EmptyStore_AllFieldsTrue"},
	{"store_data_integrity_test.go", "TestBackup_EmptyDatabase_Succeeds"},
	{"store_data_integrity_test.go", "TestBackup_EmptyPath_ReturnsError"},
	{"store_data_integrity_test.go", "TestBackup_WithData_Readable"},
	{"store_data_integrity_test.go", "TestVerify_AfterHierarchy_AllPass"},
	{"store_data_integrity_test.go", "TestVerify_AfterImportCycle_AllPass"},
	{"store_data_integrity_test.go", "TestVerify_AfterTransitions_ProgressConsistent"},
	{"store_data_integrity_test.go", "TestVerify_EmptyDatabase_AllPass"},
	{"store_operations_test.go", "TestBackup_PathWithSingleQuote_Succeeds"},
	{"store_operations_test.go", "TestVerify_ProgressInconsistency_MultipleChildren_Detected"},
	{"store_operations_test.go", "TestVerify_WithMultipleNodes_AllPass"},
	{"store_test.go", "TestNew_CreatesDB"},
	{"sync_backfill_dryrun_test.go", "backfillBehaviorStore"},
	{"verify_test.go", "TestVerify_CleanDB_AllChecksPassing"},
	{"verify_test.go", "TestVerify_EmptyDB_AllChecksPassing"},
	{"verify_test.go", "TestVerify_FTSConsistent_AfterOperations"},
	{"verify_test.go", "TestVerify_ForeignKeyViolation_Detected"},
	{"verify_test.go", "TestVerify_IntegrityAndFKChecksWork"},
	{"verify_test.go", "TestVerify_MissingSequence_Detected"},
	{"verify_test.go", "TestVerify_ProgressConsistent_WithCancelledChildren"},
	{"verify_test.go", "TestVerify_ProgressInconsistency_Detected"},
	{"verify_test.go", "TestVerify_SequenceInconsistency_Detected"},
}

type freshFixtureProblem struct {
	file, name string
	line       int
	message    string
}

func (p freshFixtureProblem) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", p.file, p.line, p.name, p.message)
}

// This is a source policy for constructor reference paths, not a proof of
// arbitrary Go control flow or opaque interface dispatch. Known subjects need
// a recognizable fresh route. Initialization families additionally forbid image
// routes so a newly added test cannot choose the default helper by habit.
func freshFixtureRoutingProblems(sources map[string][]byte, required []freshFixtureSubject) []freshFixtureProblem {
	graph, problems := parseFixtureRoutingSources(sources)
	if len(problems) > 0 {
		return problems
	}
	for _, unit := range graph.units {
		graph.references(unit)
	}
	selected, problems := graph.selectSubjects(required)
	for unit, requireFresh := range selected {
		if problem := graph.subjectProblem(unit, requireFresh); problem != nil {
			problems = append(problems, *problem)
		}
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].String() < problems[j].String() })
	return problems
}

func parseFixtureRoutingSources(sources map[string][]byte) (*fixtureRoutingGraph, []freshFixtureProblem) {
	graph := &fixtureRoutingGraph{fset: token.NewFileSet(), byName: map[string][]*fixtureRoutingUnit{}, methods: map[string][]*fixtureRoutingUnit{}, functions: map[freshFixtureSubject][]*fixtureRoutingUnit{}}
	if len(sources) == 0 {
		return graph, []freshFixtureProblem{{message: "no test source files to check"}}
	}
	files := make([]string, 0, len(sources))
	for file := range sources {
		files = append(files, file)
	}
	sort.Strings(files)
	var problems []freshFixtureProblem
	for _, file := range files {
		parsed, err := parser.ParseFile(graph.fset, file, sources[file], parser.SkipObjectResolution|parser.AllErrors)
		if err != nil {
			problems = append(problems, freshFixtureProblem{file: file, message: "cannot parse test source: " + err.Error()})
			continue
		}
		imports, err := fixtureImportPaths(parsed.Imports)
		if err != nil {
			problems = append(problems, freshFixtureProblem{file: file, message: "cannot parse import path: " + err.Error()})
			continue
		}
		graph.addDeclarations(file, parsed, imports)
	}
	return graph, problems
}

func fixtureImportPaths(specs []*ast.ImportSpec) (map[string]string, error) {
	imports := map[string]string{}
	for _, item := range specs {
		path, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			return nil, err
		}
		alias := path[strings.LastIndex(path, "/")+1:]
		if item.Name != nil {
			alias = item.Name.Name
		}
		imports[alias] = path
	}
	return imports, nil
}

func (g *fixtureRoutingGraph) addDeclarations(file string, parsed *ast.File, imports map[string]string) {
	for _, decl := range parsed.Decls {
		switch item := decl.(type) {
		case *ast.FuncDecl:
			if item.Body == nil {
				continue
			}
			unit := &fixtureRoutingUnit{file: file, pkg: parsed.Name.Name, name: item.Name.Name, pos: item.Pos(), syntax: []ast.Node{item.Body}, signature: item.Type, receiver: item.Recv, imports: imports}
			g.add(unit)
			if item.Recv == nil {
				key := freshFixtureSubject{file, item.Name.Name}
				g.functions[key] = append(g.functions[key], unit)
			}
		case *ast.GenDecl:
			if item.Tok == token.VAR {
				g.addVariables(file, parsed.Name.Name, imports, item.Specs)
			}
		}
	}
}

func (g *fixtureRoutingGraph) addVariables(file, pkg string, imports map[string]string, specs []ast.Spec) {
	for _, spec := range specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range value.Names {
			unit := &fixtureRoutingUnit{file: file, pkg: pkg, name: name.Name, pos: name.Pos(), imports: imports}
			values := value.Values
			if len(values) == len(value.Names) {
				values = values[i : i+1]
			}
			for _, expression := range values {
				unit.syntax = append(unit.syntax, expression)
			}
			g.add(unit)
		}
	}
}

func (g *fixtureRoutingGraph) selectSubjects(required []freshFixtureSubject) (map[*fixtureRoutingUnit]bool, []freshFixtureProblem) {
	selected := map[*fixtureRoutingUnit]bool{}
	seen := map[freshFixtureSubject]bool{}
	var problems []freshFixtureProblem
	for _, subject := range required {
		if seen[subject] {
			problems = append(problems, freshFixtureProblem{file: subject.file, name: subject.name, message: "duplicate pinned fresh subject"})
			continue
		}
		seen[subject] = true
		matches := g.functions[subject]
		if len(matches) != 1 {
			problems = append(problems, freshFixtureProblem{file: subject.file, name: subject.name, message: "missing or ambiguous pinned fresh declaration"})
			continue
		}
		selected[matches[0]] = true
	}
	for _, matches := range g.functions {
		for _, unit := range matches {
			if fixtureInitializationFamily(unit.name) {
				if _, exists := selected[unit]; !exists {
					selected[unit] = false
				}
			}
		}
	}
	return selected, problems
}

func (g *fixtureRoutingGraph) subjectProblem(unit *fixtureRoutingUnit, requireFresh bool) *freshFixtureProblem {
	fresh, images := g.route(unit)
	position := g.fset.Position(unit.pos)
	problem := freshFixtureProblem{file: unit.file, name: unit.name, line: position.Line}
	if len(images) > 0 {
		problem.message = "reaches copied-image constructor " + strings.Join(images, ", ") + "; use newFreshTestStore or sqlite.New for this subject"
		return &problem
	}
	if requireFresh && !fresh {
		problem.message = "no recognized fresh constructor route through sqlite.New"
		return &problem
	}
	return nil
}

const fixtureSQLitePackage = "github.com/hyper-swe/mtix/internal/store/sqlite"

type fixtureRoutingUnit struct {
	file, pkg, name string
	pos             token.Pos
	syntax          []ast.Node
	signature       *ast.FuncType
	receiver        *ast.FieldList
	imports         map[string]string
	fresh, image    bool
	refs            []*fixtureRoutingUnit
}

type fixtureRoutingGraph struct {
	fset      *token.FileSet
	units     []*fixtureRoutingUnit
	byName    map[string][]*fixtureRoutingUnit
	methods   map[string][]*fixtureRoutingUnit
	functions map[freshFixtureSubject][]*fixtureRoutingUnit
}

func (g *fixtureRoutingGraph) add(unit *fixtureRoutingUnit) {
	g.units = append(g.units, unit)
	key := unit.pkg + "\x00" + unit.name
	if unit.receiver != nil {
		g.methods[key] = append(g.methods[key], unit)
		return
	}
	g.byName[key] = append(g.byName[key], unit)
	// These helpers produce or consume the immutable image. Reaching New later
	// while opening that image must not turn the route into a fresh creation.
	unit.image = unit.pkg == "sqlite" && (unit.name == "FixtureStoreForTest" || unit.name == "cachedFixtureImage" || unit.name == "buildEmptyStoreImage")
}

func fixtureInitializationFamily(name string) bool {
	for _, prefix := range []string{"TestNew_", "TestOpen_", "TestSchema_", "TestBackup_", "TestVerify_", "TestIntegrity_", "TestStoreInstanceID_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

type fixtureBindingRange struct{ start, end token.Pos }

type fixtureReferenceBindings struct {
	locals map[string][]fixtureBindingRange
	skip   map[*ast.Ident]bool
}

func (b *fixtureReferenceBindings) define(id *ast.Ident, start, end token.Pos) {
	if id != nil {
		b.locals[id.Name] = append(b.locals[id.Name], fixtureBindingRange{start, end})
		b.skip[id] = true
	}
}

func (b *fixtureReferenceBindings) defineNames(names []*ast.Ident, start, end token.Pos) {
	for _, id := range names {
		b.define(id, start, end)
	}
}

func (b *fixtureReferenceBindings) bindFunction(signature *ast.FuncType, body *ast.BlockStmt) {
	if signature == nil || body == nil {
		return
	}
	for _, list := range []*ast.FieldList{signature.TypeParams, signature.Params, signature.Results} {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			b.defineNames(field.Names, body.Pos(), body.End())
		}
	}
}

func (b *fixtureReferenceBindings) assignment(expressions []ast.Expr, tok token.Token, start, end token.Pos) {
	for _, expression := range expressions {
		if id, ok := expression.(*ast.Ident); ok {
			b.skip[id] = true
			if tok == token.DEFINE {
				b.define(id, start, end)
			}
		}
	}
}

func (b *fixtureReferenceBindings) collect(n ast.Node, end token.Pos) {
	switch item := n.(type) {
	case *ast.FuncLit:
		b.bindFunction(item.Type, item.Body)
	case *ast.Field:
		for _, id := range item.Names {
			b.skip[id] = true
		}
	case *ast.ValueSpec:
		b.defineNames(item.Names, item.End(), end)
	case *ast.TypeSpec:
		b.define(item.Name, item.Name.Pos(), end)
	case *ast.AssignStmt:
		b.assignment(item.Lhs, item.Tok, item.End(), end)
	case *ast.RangeStmt:
		b.assignment([]ast.Expr{item.Key, item.Value}, item.Tok, item.Body.Pos(), item.Body.End())
	case *ast.KeyValueExpr:
		if id, ok := item.Key.(*ast.Ident); ok {
			b.skip[id] = true
		}
	case *ast.SelectorExpr:
		b.skip[item.Sel] = true
	case *ast.LabeledStmt:
		b.skip[item.Label] = true
	case *ast.BranchStmt:
		b.skip[item.Label] = true
	}
}

func fixtureBindingScopeEnd(n ast.Node, inherited token.Pos) token.Pos {
	switch n.(type) {
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
		*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.CaseClause, *ast.CommClause:
		return n.End()
	default:
		return inherited
	}
}

func (b *fixtureReferenceBindings) collectSyntax(syntax ast.Node) {
	// Each frame follows ast.Inspect's enter/leave pair. Bindings begin after
	// their initializer and stop at the enclosing explicit or implicit block.
	var frames []token.Pos
	ast.Inspect(syntax, func(n ast.Node) bool {
		if n == nil {
			frames = frames[:len(frames)-1]
			return false
		}
		end := syntax.End()
		if len(frames) > 0 {
			end = frames[len(frames)-1]
		}
		end = fixtureBindingScopeEnd(n, end)
		frames = append(frames, end)
		b.collect(n, end)
		return true
	})
}

func (b *fixtureReferenceBindings) shadowed(id *ast.Ident) bool {
	for _, scope := range b.locals[id.Name] {
		if scope.start <= id.Pos() && id.Pos() < scope.end {
			return true
		}
	}
	return false
}

func (g *fixtureRoutingGraph) references(unit *fixtureRoutingUnit) {
	bindings := &fixtureReferenceBindings{locals: map[string][]fixtureBindingRange{}, skip: map[*ast.Ident]bool{}}
	if unit.signature != nil {
		body, _ := unit.syntax[0].(*ast.BlockStmt)
		bindings.bindFunction(unit.signature, body)
		if unit.receiver != nil {
			for _, field := range unit.receiver.List {
				bindings.defineNames(field.Names, body.Pos(), body.End())
			}
		}
	}
	for _, syntax := range unit.syntax {
		bindings.collectSyntax(syntax)
	}
	refs := map[*fixtureRoutingUnit]bool{}
	for _, syntax := range unit.syntax {
		ast.Inspect(syntax, func(n ast.Node) bool {
			switch item := n.(type) {
			case *ast.SelectorExpr:
				g.selectorReference(unit, bindings, refs, item)
			case *ast.Ident:
				g.identifierReference(unit, bindings, refs, item)
			}
			return true
		})
	}
	for target := range refs {
		unit.refs = append(unit.refs, target)
	}
}

func (g *fixtureRoutingGraph) selectorReference(unit *fixtureRoutingUnit, bindings *fixtureReferenceBindings, refs map[*fixtureRoutingUnit]bool, selector *ast.SelectorExpr) {
	if id, ok := selector.X.(*ast.Ident); ok && !bindings.shadowed(id) {
		if path, imported := unit.imports[id.Name]; imported {
			bindings.skip[id] = true
			if path == fixtureSQLitePackage {
				switch selector.Sel.Name {
				case "New":
					unit.fresh = true
				case "FixtureStoreForTest":
					unit.image = true
				}
			}
			return
		}
	}
	// Without type checking, conservatively follow every same-package method
	// with this name, including method values and expressions. Different receiver
	// types may overapproximate a route; an image candidate must never be hidden.
	for _, target := range g.methods[unit.pkg+"\x00"+selector.Sel.Name] {
		refs[target] = true
	}
}

func (g *fixtureRoutingGraph) identifierReference(unit *fixtureRoutingUnit, bindings *fixtureReferenceBindings, refs map[*fixtureRoutingUnit]bool, id *ast.Ident) {
	if bindings.skip[id] || bindings.shadowed(id) {
		return
	}
	if _, imported := unit.imports[id.Name]; imported {
		return
	}
	sqliteScope := unit.pkg == "sqlite" || unit.imports["."] == fixtureSQLitePackage
	if sqliteScope && id.Name == "New" {
		unit.fresh = true
		return
	}
	if sqliteScope && id.Name == "FixtureStoreForTest" {
		unit.image = true
		return
	}
	for _, target := range g.byName[unit.pkg+"\x00"+id.Name] {
		refs[target] = true
	}
}

func (g *fixtureRoutingGraph) route(root *fixtureRoutingUnit) (bool, []string) {
	visited := map[*fixtureRoutingUnit]bool{}
	images := map[string]bool{}
	fresh := false
	pending := []*fixtureRoutingUnit{root}
	for len(pending) > 0 {
		unit := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if visited[unit] {
			continue
		}
		visited[unit] = true
		fresh = fresh || unit.fresh
		if unit.image {
			images[unit.name] = true
		}
		pending = append(pending, unit.refs...)
	}
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)
	return fresh, names
}

func requireFreshFixtureRoutes(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read SQLite test sources: %v", err)
	}
	sources := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read SQLite test source %s: %v", entry.Name(), err)
		}
		sources[entry.Name()] = raw
	}
	problems := requiredFreshFixtureRoutingProblems(sources, freshFixtureSubjects)
	for _, problem := range problems {
		t.Errorf("fresh fixture routing: %s", problem)
	}
	if len(problems) > 0 {
		t.FailNow()
	}
}

func fixtureRoutingExample(name, body, extra string) map[string][]byte {
	return map[string][]byte{"store_test.go": []byte(fmt.Sprintf(`package sqlite_test
import (
 "testing"
 sqlite "github.com/hyper-swe/mtix/internal/store/sqlite"
)
func newFreshTestStore(t *testing.T) *sqlite.Store {
 s, _ := sqlite.New("new.db", nil)
 return s
}
func newTestStore(t *testing.T) *sqlite.Store {
 return sqlite.FixtureStoreForTest(t, "image.db")
}
%s
func %s(t *testing.T) {
 %s
}
`, extra, name, body))}
}

type fixtureRoutingCase struct {
	name, subject, body, extra, want string
	unpinned                         bool
	rewrite                          func(string) string
}

func fixtureRoutingScopeCases() []fixtureRoutingCase {
	return []fixtureRoutingCase{
		{name: "for-initializer-shadow-ends-with-loop", body: "_ = newFreshTestStore; for newTestStore := 0; newTestStore < 1; newTestStore++ {}; _ = newTestStore(t)", want: "image"},
		{name: "switch-initializer-shadow-ends-with-switch", body: "_ = newFreshTestStore; switch newTestStore := 0; newTestStore { case 0: }; _ = newTestStore(t)", want: "image"},
		{name: "switch-case-binding-is-local-to-case", body: "_ = newFreshTestStore; switch 0 { case 0: newTestStore := 0; _ = newTestStore; case 1: _ = newTestStore(t) }", want: "image"},
		{name: "select-case-binding-is-local-to-case", body: "_ = newFreshTestStore; select { case <-make(chan int): newTestStore := 0; _ = newTestStore; default: _ = newTestStore(t) }", want: "image"},
		{name: "label-is-not-global-helper-reference", body: "_ = newFreshTestStore(t); newTestStore: for { break newTestStore }"},
		{name: "unrelated-import-shadows-global-helper", body: "_ = newFreshTestStore(t); _ = newTestStore.Sprint(1)", rewrite: func(s string) string {
			return strings.Replace(s, `"testing"`, "\"testing\"\n newTestStore \"fmt\"", 1)
		}},
		{name: "block-shadow-must-not-hide-outer-image", body: "_ = newFreshTestStore; { newTestStore := 0; _ = newTestStore }; _ = newTestStore(t)", want: "image"},
		{name: "later-local-must-not-hide-earlier-image", body: "_ = newFreshTestStore; _ = newTestStore(t); newTestStore := 0; _ = newTestStore", want: "image"},
		{name: "function-parameter-must-not-hide-outer-image", body: "_ = newFreshTestStore; _ = func(newTestStore int) {}; _ = newTestStore(t)", want: "image"},
		{name: "initializer-must-see-outer-image", body: "_ = newFreshTestStore; newTestStore := newTestStore; _ = newTestStore(t)", want: "image"},
		{name: "if-initializer-shadow-ends-with-if", body: "_ = newFreshTestStore; if newTestStore := 0; newTestStore == 0 {}; _ = newTestStore(t)", want: "image"},
		{name: "range-source-precedes-local-binding", body: "_ = newFreshTestStore; for newTestStore := range []func(*testing.T) *sqlite.Store{newTestStore} { _ = newTestStore }", want: "image"},
		{name: "block-import-shadow-must-not-hide-fresh", body: "{ sqlite := 0; _ = sqlite }; _, _ = sqlite.New(\"new.db\", nil)"},
		{name: "later-local-must-not-hide-earlier-fresh", body: "_, _ = sqlite.New(\"new.db\", nil); sqlite := 0; _ = sqlite"},
		{name: "function-parameter-must-not-hide-outer-fresh", body: "_ = func(sqlite int) { _ = sqlite }; _, _ = sqlite.New(\"new.db\", nil)"},
		{name: "local-image-helper-name-is-not-global", body: "_ = newFreshTestStore(t); { newTestStore := func(*testing.T) *sqlite.Store { return nil }; _ = newTestStore(t) }"},
		{name: "global-fresh-alias-next-to-image", body: "_ = freshFactory(t)", extra: "var imageFactory, freshFactory = newTestStore, newFreshTestStore"},
		{name: "struct-field-is-not-import-shadow", body: "var holder struct { sqlite int }; _ = holder; _, _ = sqlite.New(\"new.db\", nil)"},
		{name: "local-import-shadow-is-not-fresh", body: "sqlite := struct { New func(string, any) (any, error) }{}; _, _ = sqlite.New(\"fake.db\", nil)", want: "fresh"},
	}
}

func fixtureRoutingConstructorCases() []fixtureRoutingCase {
	return []fixtureRoutingCase{

		{name: "fresh-helper", body: "_ = newFreshTestStore(t)"},
		{name: "real-constructor", body: "_, _ = sqlite.New(\"new.db\", nil)"},
		{name: "fresh-local-alias", body: "ctor := newFreshTestStore; _ = ctor(t)"},
		{name: "fresh-helper-wrapper", body: "_ = freshWrapper(t)", extra: "func freshWrapper(t *testing.T) *sqlite.Store { return newFreshTestStore(t) }"},
		{name: "aliased-import", body: "_, _ = sqlite.New(\"new.db\", nil)", rewrite: func(s string) string {
			return strings.ReplaceAll(strings.ReplaceAll(s, "sqlite.", "db."), "sqlite \"", "db \"")
		}},
		{name: "image-helper", body: "_ = newTestStore(t)", want: "image"},
		{name: "image-local-alias", body: "ctor := newTestStore; _ = ctor(t)", want: "image"},
		{name: "image-global-alias", body: "_ = imageFactory(t)", extra: "var imageFactory = newTestStore", want: "image"},
		{name: "image-wrapper", body: "_ = imageWrapper(t)", extra: "func imageWrapper(t *testing.T) *sqlite.Store { return newTestStore(t) }", want: "image"},
		{name: "direct-image-factory", body: "_ = sqlite.FixtureStoreForTest(t, \"image.db\")", want: "image"},
		{name: "image-factory-function-value", body: "ctor := sqlite.FixtureStoreForTest; _ = ctor(t, \"image.db\")", want: "image"},
		{name: "fresh-decoy-does-not-hide-image", body: "_ = newFreshTestStore; _ = newTestStore(t)", want: "image"},
		{name: "fresh-helper-implementation-cannot-become-image", body: "_ = newFreshTestStore(t)", want: "image", rewrite: func(s string) string {
			return strings.Replace(s, "s, _ := sqlite.New(\"new.db\", nil)\n return s", "return newTestStore(t)", 1)
		}},
		{name: "comment-string-not-a-constructor", body: "_ = \"newFreshTestStore(t)\" // sqlite.New", want: "fresh"},
		{name: "missing-fresh-route", body: "_ = t", want: "fresh"},
		{name: "alias-cycle-fails-closed", body: "_ = first(t)", extra: "var first = second; var second = first", want: "fresh"},
		{name: "ordinary-operation-image-is-permitted", subject: "TestCreateNode_Operation", body: "_ = newTestStore(t)", unpinned: true},
		{name: "future-creation-image-rejected", subject: "TestNew_Future", body: "_ = newTestStore(t)", unpinned: true, want: "image"},
		{name: "future-schema-image-rejected", subject: "TestSchema_Future", body: "ctor := newTestStore; _ = ctor(t)", unpinned: true, want: "image"},
		{name: "future-backup-image-rejected", subject: "TestBackup_Future", body: "_ = newTestStore(t)", unpinned: true, want: "image"},
		{name: "future-verify-image-rejected", subject: "TestVerify_Future", body: "_ = newTestStore(t)", unpinned: true, want: "image"},
		{name: "future-open-image-rejected", subject: "TestOpen_Future", body: "_ = newTestStore(t)", unpinned: true, want: "image"},
		{name: "future-integrity-image-rejected", subject: "TestIntegrity_Future", body: "_ = newTestStore(t)", unpinned: true, want: "image"},
		{name: "future-error-test-is-not-liveness-checked", subject: "TestNew_InvalidPath", body: "_ = t", unpinned: true},
	}
}

func TestFreshFixtureRouting_Policy(t *testing.T) {
	cases := append(fixtureRoutingScopeCases(), fixtureRoutingConstructorCases()...)
	reviewed := []string{"TestNew_CreatesDB", "TestStoreInstanceID_IndependentCreations_UseDifferentIDs", "TestVerify_EmptyDB_AllChecksPassing", "TestVerify_IntegrityAndFKChecksWork", "TestVerify_EmptyDatabase_AllPass", "TestBackup_EmptyDB_Succeeds", "TestBackup_VerificationPasses", "TestExport_IncludesSchemaVersion"}
	for _, name := range reviewed {
		cases = append(cases, fixtureRoutingCase{name: "reviewed-" + name, subject: name, body: "_ = newTestStore(t)", want: "image"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkFixtureRoutingCase(t, tc) })
	}
}

func checkFixtureRoutingCase(t *testing.T, tc fixtureRoutingCase) {
	t.Helper()
	subject := tc.subject
	if subject == "" {
		subject = "TestPinnedSubject"
	}
	sources := fixtureRoutingExample(subject, tc.body, tc.extra)
	if tc.rewrite != nil {
		sources["store_test.go"] = []byte(tc.rewrite(string(sources["store_test.go"])))
	}
	var required []freshFixtureSubject
	if !tc.unpinned {
		required = []freshFixtureSubject{{"store_test.go", subject}}
	}
	assertFixtureRoutingDiagnostic(t, sources, required, subject, tc.want)
}

func TestFreshFixtureRouting_InvalidInputs(t *testing.T) {
	cases := []struct {
		name    string
		sources map[string][]byte
		want    string
	}{
		{"empty", nil, "no test source"},
		{"parse", map[string][]byte{"store_test.go": []byte("package sqlite_test; func {")}, "parse"},
		{"missing", fixtureRoutingExample("TestOtherSubject", "_ = newFreshTestStore(t)", ""), "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := freshFixtureRoutingProblems(tc.sources, []freshFixtureSubject{{"store_test.go", "TestPinnedSubject"}})
			if len(got) == 0 {
				t.Fatalf("expected %s error", tc.want)
			}
			if !strings.Contains(fmt.Sprint(got), tc.want) {
				t.Fatalf("expected %s diagnostic, got %v", tc.want, got)
			}
		})
	}
}
