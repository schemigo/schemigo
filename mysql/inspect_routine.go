package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/schemigo/schemigo"
)

func inspectRoutines(ctx context.Context, conn *sql.Conn, schema string) ([]schemigo.Function, error) {
	rows, err := conn.QueryContext(ctx, `SELECT routine_name, specific_name, routine_type, dtd_identifier, routine_definition, is_deterministic, sql_data_access
FROM information_schema.routines WHERE routine_schema=? ORDER BY routine_name`, schema)
	if err != nil {
		return nil, fmt.Errorf("inspect routines: %w", err)
	}
	type routine struct {
		name, specific, kind, returns, body, deterministic, dataAccess string
	}
	var routines []routine
	for rows.Next() {
		var name, specific, kind, deterministic, dataAccess string
		var returns, body sql.NullString
		if err := rows.Scan(&name, &specific, &kind, &returns, &body, &deterministic, &dataAccess); err != nil {
			rows.Close()
			return nil, err
		}
		if !body.Valid || strings.TrimSpace(body.String) == "" {
			rows.Close()
			return nil, fmt.Errorf("routine %q: body is unavailable", name)
		}
		routines = append(routines, routine{name, specific, kind, returns.String, body.String, deterministic, dataAccess})
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	var functions []schemigo.Function
	for _, routine := range routines {
		function := schemigo.Function{Name: routine.name, Body: routine.body}
		if routine.dataAccess != "CONTAINS SQL" {
			function.Body = routine.dataAccess + " " + function.Body
		}
		if routine.deterministic == "YES" {
			function.Body = "DETERMINISTIC " + function.Body
		}
		switch routine.kind {
		case "FUNCTION":
			function.Returns = routine.returns
		case "PROCEDURE":
		default:
			return nil, fmt.Errorf("routine %q: unsupported type %q", routine.name, routine.kind)
		}
		rows, err := conn.QueryContext(ctx, `SELECT parameter_mode, parameter_name, dtd_identifier FROM information_schema.parameters
WHERE specific_schema=? AND specific_name=? AND ordinal_position>0 ORDER BY ordinal_position`, schema, routine.specific)
		if err != nil {
			return nil, fmt.Errorf("routine %q parameters: %w", routine.name, err)
		}
		var parameters []string
		for rows.Next() {
			var mode, parameter sql.NullString
			var typ string
			if err := rows.Scan(&mode, &parameter, &typ); err != nil {
				rows.Close()
				return nil, err
			}
			part := ""
			if mode.Valid && mode.String != "IN" {
				part += mode.String + " "
			}
			if parameter.Valid && parameter.String != "" {
				part += QuoteIdent(parameter.String) + " "
			}
			parameters = append(parameters, part+typ)
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
		function.Parameters = strings.Join(parameters, ", ")
		functions = append(functions, function)
	}
	return functions, nil
}

func inspectTriggers(ctx context.Context, conn *sql.Conn, schema string) ([]schemigo.Trigger, error) {
	rows, err := conn.QueryContext(ctx, `SELECT trigger_name, event_object_table, action_timing, event_manipulation, action_statement, action_orientation
FROM information_schema.triggers WHERE trigger_schema=? ORDER BY trigger_name`, schema)
	if err != nil {
		return nil, fmt.Errorf("inspect triggers: %w", err)
	}
	var triggers []schemigo.Trigger
	for rows.Next() {
		var name, table, timing, event, action, orientation string
		if err := rows.Scan(&name, &table, &timing, &event, &action, &orientation); err != nil {
			rows.Close()
			return nil, err
		}
		if orientation != "ROW" {
			rows.Close()
			return nil, fmt.Errorf("trigger %q: unsupported orientation %q", name, orientation)
		}
		function, err := calledProcedure(action)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("trigger %q: %w", name, err)
		}
		triggers = append(triggers, schemigo.Trigger{Name: name, Table: table, When: timing, Event: event, Function: function})
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return triggers, nil
}

func calledProcedure(action string) (string, error) {
	action = strings.TrimSpace(action)
	if len(action) < 5 || !strings.EqualFold(action[:5], "CALL ") || !strings.HasSuffix(strings.TrimSpace(action), ")") {
		return "", fmt.Errorf("only a zero-argument CALL action is supported")
	}
	target := strings.TrimSpace(action[5:])
	open := strings.LastIndexByte(target, '(')
	if open < 0 || strings.TrimSpace(target[open+1:len(target)-1]) != "" {
		return "", fmt.Errorf("only a zero-argument CALL action is supported")
	}
	name := strings.TrimSpace(target[:open])
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	name = strings.Trim(name, "` ")
	if name == "" {
		return "", fmt.Errorf("procedure name is empty")
	}
	return strings.ReplaceAll(name, "``", "`"), nil
}
