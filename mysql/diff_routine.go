package mysql

import (
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

func diffRoutines(current, desired schemigo.Schema) ([]schemigo.Statement, error) {
	var statements []schemigo.Statement
	for _, old := range current.Functions {
		next, found := findFunction(desired.Functions, old.Name)
		if !found || !sameFunction(old, next) {
			kind := "FUNCTION"
			if old.Returns == "" {
				kind = "PROCEDURE"
			}
			statements = append(statements, ddl("DROP "+kind+" IF EXISTS "+Qualified(current.Name, old.Name), true))
		}
	}
	for _, next := range desired.Functions {
		old, found := findFunction(current.Functions, next.Name)
		if found && sameFunction(old, next) {
			continue
		}
		kind := "PROCEDURE"
		returns := ""
		if next.Returns != "" {
			kind = "FUNCTION"
			returns = " RETURNS " + next.Returns
		}
		body := strings.TrimSpace(next.Body)
		if body == "" {
			return nil, fmt.Errorf("routine %q: body is empty", next.Name)
		}
		statements = append(statements, ddl("CREATE "+kind+" "+Qualified(desired.Name, next.Name)+"("+next.Parameters+")"+returns+" "+body, false))
	}
	return statements, nil
}
