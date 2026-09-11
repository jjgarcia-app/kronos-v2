package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// placeholderWhitelist documenta, por nombre de función/método, los casos en
// los que una llamada directa a s.db.Query*/Exec*/Prepare* con un literal SQL
// que contiene "?" es legítima. Si agregás una entrada acá, dejá explícito
// por qué esa función nunca corre contra Postgres (o por qué ya traduce el
// placeholder a mano).
var placeholderWhitelist = map[string]string{
	"migrate": `bootstrap de SQLite invocado únicamente desde store.New(), que ` +
		`fija s.driver = "sqlite3" antes de llamarlo — nunca corre contra ` +
		`Postgres (ese camino usa store_postgres.go:migratePostgres, con ` +
		`placeholders "$N" propios), así que "?" es el placeholder correcto ` +
		`y no necesita pasar por rebind().`,
}

var placeholderLintMethods = map[string]bool{
	"QueryContext":    true,
	"QueryRowContext": true,
	"ExecContext":     true,
	"PrepareContext":  true,
}

// TestNoRawPlaceholdersAgainstDB es el test de regresión de la clase de bug
// documentada en docs/architecture.md: una llamada directa a
// s.db.QueryContext/QueryRowContext/ExecContext/PrepareContext con un
// string SQL que usa "?" como placeholder nunca pasa por rebind() (el que
// traduce "?" a "$1, $2, ..."). Contra SQLite "?" es el placeholder nativo y
// no pasa nada, pero contra Postgres "?" es el operador de existencia de
// jsonb/hstore: el parser no espera un placeholder ahí, y en cuanto la query
// tiene más de una condición rompe con "syntax error at or near AND" (o
// "OR"). Ese fue exactamente el bug real en CountObservations y
// ListRelations: corrían en cada arranque de sesión, Postgres las rechazaba,
// DualStore marcaba el primary como caído y la memoria quedaba leyendo el
// buffer SQLite congelado.
//
// Las queries de *Store deben pasar por s.query/s.queryRow/s.exec (que
// aplican rebind), o envolver la query en s.rebind(...) explícitamente si
// necesitan llamar a s.db.* directo (ver el caso de SaveObservation en
// observation.go). Cualquier función nueva que llame a s.db.* directo con un
// literal "?" y no siga ese patrón debe corregirse o, si el caso es
// legítimo, agregarse a placeholderWhitelist con la razón.
func TestNoRawPlaceholdersAgainstDB(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			funcName := fn.Name.Name

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				checked++

				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !placeholderLintMethods[sel.Sel.Name] {
					return true
				}
				recv, ok := sel.X.(*ast.SelectorExpr)
				if !ok || recv.Sel.Name != "db" {
					return true
				}
				if ident, ok := recv.X.(*ast.Ident); !ok || ident.Name != "s" {
					return true
				}

				// Firma esperada: (ctx, query, args...) — el query es el
				// segundo argumento.
				if len(call.Args) < 2 {
					return true
				}
				queryArg := call.Args[1]

				// s.rebind(...) envolviendo la query ya traduce "?" a "$N" a
				// mano — seguro aunque el literal interno tenga "?".
				if inner, ok := queryArg.(*ast.CallExpr); ok {
					if innerSel, ok := inner.Fun.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "rebind" {
						return true
					}
				}

				lit, ok := queryArg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					// No es un literal (variable, concatenación, etc.) — no
					// podemos verificar estáticamente si contiene "?", así
					// que no lo marcamos (evita falsos positivos sobre los
					// propios helpers query/queryRow/exec, que reciben la
					// query como parámetro).
					return true
				}
				if !strings.Contains(lit.Value, "?") {
					return true
				}
				if _, ok := placeholderWhitelist[funcName]; ok {
					return true
				}

				pos := fset.Position(call.Pos())
				t.Errorf(
					"%s:%d: %s() llama a s.db.%s con un literal SQL que contiene \"?\" "+
						"sin pasar por rebind (usá s.query/s.queryRow/s.exec, o envolvé la "+
						"query en s.rebind(...) si necesitás llamar a s.db.* directo). "+
						"Contra Postgres esto rompe con \"syntax error at or near AND\" (o "+
						"\"OR\") apenas la query tiene más de una condición, porque pgx no "+
						"traduce \"?\" a \"$N\". Si esta llamada es legítima (el driver está "+
						"fijo de antemano, como en migrate()), agregala a placeholderWhitelist "+
						"en este archivo con la razón.",
					pos.Filename, pos.Line, funcName, sel.Sel.Name,
				)
				return true
			})
		}
	}

	if checked == 0 {
		t.Fatal("no se inspeccionó ninguna llamada — el glob de archivos probablemente está vacío, revisar el test")
	}
}
