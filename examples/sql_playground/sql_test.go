package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	monty "github.com/asalimonov/montygo"
)

const customersFixture = "First,Last,Email,Total Purchased,Score,Note\n" +
	"Al,Fresco,afresco@dayrep.com,45,1.5,\n" +
	"Bill,Melator,bmelator@einrot.com,6090,2,vip\n" +
	"Barb ,Barion,bbarion@superrito.com,950,-0.25,\"a, b\"\n" +
	"Dan,Delyons,ddelyons@dayrep.com,,3e2,x\n"

func TestBindParameters(t *testing.T) {
	query, args, err := bindParameters(`SELECT * FROM data WHERE "Email" IN $emails AND n > $min`, map[string]any{
		"emails": []any{"a@x", "b@x"},
		"min":    int64(3),
	})
	require.NoError(t, err)
	require.Equal(t, `SELECT * FROM data WHERE "Email" IN (?, ?) AND n > ?`, query)
	require.Equal(t, []any{"a@x", "b@x", int64(3)}, args)

	query, args, err = bindParameters("SELECT '$a', \"$b\", 'it''s $c' -- $d\n/* $e */ FROM data WHERE x = $f", map[string]any{"f": monty.Path("/p")})
	require.NoError(t, err)
	require.Equal(t, "SELECT '$a', \"$b\", 'it''s $c' -- $d\n/* $e */ FROM data WHERE x = ?", query)
	require.Equal(t, []any{"/p"}, args)

	query, args, err = bindParameters("SELECT 1 WHERE x IN $empty", map[string]any{"empty": monty.Tuple{}})
	require.NoError(t, err)
	require.Equal(t, "SELECT 1 WHERE x IN ()", query)
	require.Empty(t, args)

	_, _, err = bindParameters("SELECT $missing", nil)
	require.EqualError(t, err, "Invalid Input Error: Values were not provided for the following prepared statement parameters: missing")
	_, _, err = bindParameters("SELECT $1", map[string]any{})
	require.ErrorContains(t, err, `positional parameter "$1" is not supported`)
	_, _, err = bindParameters("SELECT $d", map[string]any{"d": monty.NewDict()})
	require.EqualError(t, err, "TypeError: parameter 'd' has unsupported type dict")
}

func TestQueryCSVNumericTyping(t *testing.T) {
	rows, err := queryCSV(t.Context(), []byte(customersFixture), `
        SELECT "First", "Email", "Total Purchased" as TotalPurchased, "Score", "Note"
        FROM data
        ORDER BY "Total Purchased"
        DESC LIMIT 10
        `, nil)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	first := rows[0].(*monty.Dict)
	require.Equal(t, []any{"First", "Email", "TotalPurchased", "Score", "Note"}, first.Keys())
	require.Equal(t, []any{"Bill", "bmelator@einrot.com", int64(6090), float64(2), "vip"}, first.Values())
	require.Equal(t, []any{"Barb ", "bbarion@superrito.com", int64(950), -0.25, "a, b"}, rows[1].(*monty.Dict).Values())
	require.Equal(t, []any{"Al", "afresco@dayrep.com", int64(45), 1.5, nil}, rows[2].(*monty.Dict).Values())
	require.Equal(t, []any{"Dan", "ddelyons@dayrep.com", nil, 300.0, "x"}, rows[3].(*monty.Dict).Values())
}

func TestQueryCSVListParameter(t *testing.T) {
	rows, err := queryCSV(t.Context(), []byte(customersFixture), `
        SELECT "Email", "First" as Name
        FROM data
        WHERE "Email" IN $emails
        `, map[string]any{"emails": []any{"bmelator@einrot.com", "ddelyons@dayrep.com", "nobody@x"}})
	require.NoError(t, err)
	require.Equal(t, []any{
		monty.NewDict(monty.Pair{Key: "Email", Value: "bmelator@einrot.com"}, monty.Pair{Key: "Name", Value: "Bill"}),
		monty.NewDict(monty.Pair{Key: "Email", Value: "ddelyons@dayrep.com"}, monty.Pair{Key: "Name", Value: "Dan"}),
	}, rows)
}

func TestColumnNamesAndTypes(t *testing.T) {
	require.Equal(t, []string{"a", "A_1", "column2", "b"}, columnNames([]string{"a", "A", "", "b"}))
	types := inferColumnTypes([][]string{{"1", "1", "x", ""}, {"-2", "2.5", "3", ""}, {"+3", "1e3", "", ""}}, 4)
	require.Equal(t, []columnType{columnInteger, columnReal, columnText, columnText}, types)
}

func TestQueryCSVErrors(t *testing.T) {
	_, err := queryCSV(t.Context(), []byte("a,b\n1\n"), "SELECT * FROM data", nil)
	require.ErrorContains(t, err, "read CSV")
	_, err = queryCSV(t.Context(), []byte("a\n1\n"), "SELECT nope FROM data", nil)
	require.ErrorContains(t, err, "no such column: nope")
}
