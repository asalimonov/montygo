package main

import (
	"context"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"

	"github.com/asalimonov/montygo/examples/internal/pyargs"
)

func dict(kv ...any) *sandbox.Dict {
	d := sandbox.NewDict()
	for i := 0; i < len(kv); i += 2 {
		d.Set(kv[i], kv[i+1])
	}
	return d
}

func member(id int64, name string) *sandbox.Dict {
	return dict("id", id, "name", name)
}

func item(date string, amount float64, description string) *sandbox.Dict {
	return dict("date", date, "amount", amount, "description", description)
}

var teamMembers = []any{
	member(1, "Alice Chen"),
	member(2, "Bob Smith"),
	member(3, "Carol Jones"),
	member(4, "David Kim"),
	member(5, "Eve Wilson"),
}

// Simulated expense data (multiple line items per person to bloat traditional context).
var expenses = map[int64][]any{
	// Alice - under budget
	1: {
		item("2024-07-15", 450.00, "Flight to NYC"),
		item("2024-07-16", 200.00, "Hotel NYC"),
		item("2024-07-17", 85.00, "Meals NYC"),
		item("2024-08-20", 380.00, "Flight to Chicago"),
		item("2024-08-21", 175.00, "Hotel Chicago"),
		item("2024-09-05", 520.00, "Flight to Seattle"),
		item("2024-09-06", 225.00, "Hotel Seattle"),
		item("2024-09-07", 95.00, "Meals Seattle"),
	},
	// Bob - over standard budget but has custom budget
	2: {
		item("2024-07-01", 850.00, "Flight to London"),
		item("2024-07-02", 450.00, "Hotel London"),
		item("2024-07-03", 125.00, "Meals London"),
		item("2024-07-04", 450.00, "Hotel London"),
		item("2024-07-05", 120.00, "Meals London"),
		item("2024-08-10", 780.00, "Flight to Tokyo"),
		item("2024-08-11", 380.00, "Hotel Tokyo"),
		item("2024-08-12", 380.00, "Hotel Tokyo"),
		item("2024-08-13", 150.00, "Meals Tokyo"),
		item("2024-09-15", 920.00, "Flight to Singapore"),
		item("2024-09-16", 320.00, "Hotel Singapore"),
		item("2024-09-17", 320.00, "Hotel Singapore"),
		item("2024-09-18", 180.00, "Meals Singapore"),
	},
	// Carol - way over budget (no custom budget)
	3: {
		item("2024-07-08", 1200.00, "Flight to Paris"),
		item("2024-07-09", 550.00, "Hotel Paris"),
		item("2024-07-10", 550.00, "Hotel Paris"),
		item("2024-07-11", 550.00, "Hotel Paris"),
		item("2024-07-12", 200.00, "Meals Paris"),
		item("2024-08-25", 1100.00, "Flight to Sydney"),
		item("2024-08-26", 480.00, "Hotel Sydney"),
		item("2024-08-27", 480.00, "Hotel Sydney"),
		item("2024-08-28", 480.00, "Hotel Sydney"),
		item("2024-08-29", 220.00, "Meals Sydney"),
		item("2024-09-20", 650.00, "Flight to Denver"),
		item("2024-09-21", 280.00, "Hotel Denver"),
	},
	// David - slightly under budget
	4: {
		item("2024-07-22", 420.00, "Flight to Boston"),
		item("2024-07-23", 190.00, "Hotel Boston"),
		item("2024-07-24", 75.00, "Meals Boston"),
		item("2024-08-05", 510.00, "Flight to Austin"),
		item("2024-08-06", 210.00, "Hotel Austin"),
		item("2024-08-07", 90.00, "Meals Austin"),
		item("2024-09-12", 480.00, "Flight to Portland"),
		item("2024-09-13", 195.00, "Hotel Portland"),
		item("2024-09-14", 85.00, "Meals Portland"),
	},
	// Eve - over standard budget (no custom budget)
	5: {
		item("2024-07-03", 680.00, "Flight to Miami"),
		item("2024-07-04", 320.00, "Hotel Miami"),
		item("2024-07-05", 320.00, "Hotel Miami"),
		item("2024-07-06", 145.00, "Meals Miami"),
		item("2024-08-18", 750.00, "Flight to San Diego"),
		item("2024-08-19", 290.00, "Hotel San Diego"),
		item("2024-08-20", 290.00, "Hotel San Diego"),
		item("2024-08-21", 130.00, "Meals San Diego"),
		item("2024-09-08", 820.00, "Flight to Las Vegas"),
		item("2024-09-09", 380.00, "Hotel Las Vegas"),
		item("2024-09-10", 380.00, "Hotel Las Vegas"),
		item("2024-09-11", 175.00, "Meals Las Vegas"),
	},
}

type customBudget struct {
	Amount float64
	Reason string
}

// Custom budgets (only Bob has one).
var customBudgets = map[int64]customBudget{
	2: {Amount: 7000.00, Reason: "International travel required"},
}

func resolved(v any) *host.Future {
	return host.Async(func() (any, error) { return v, nil })
}

// getTeamMembers gets the list of team members for a department.
func getTeamMembers(_ context.Context, args []any, kwargs host.Kwargs) (any, error) {
	bound, err := pyargs.Bind("get_team_members", args, kwargs, pyargs.Required("department"))
	if err != nil {
		return nil, err
	}
	department, err := pyargs.String("department", bound[0])
	if err != nil {
		return nil, err
	}
	return resolved(dict("department", department, "members", teamMembers)), nil
}

// getExpenses gets expense line items for a user.
func getExpenses(_ context.Context, args []any, kwargs host.Kwargs) (any, error) {
	bound, err := pyargs.Bind("get_expenses", args, kwargs,
		pyargs.Required("user_id"), pyargs.Required("quarter"), pyargs.Required("category"))
	if err != nil {
		return nil, err
	}
	userID, err := pyargs.Int("user_id", bound[0])
	if err != nil {
		return nil, err
	}
	quarter, err := pyargs.String("quarter", bound[1])
	if err != nil {
		return nil, err
	}
	category, err := pyargs.String("category", bound[2])
	if err != nil {
		return nil, err
	}
	items := expenses[userID]
	if items == nil {
		items = []any{}
	}
	return resolved(dict("user_id", userID, "quarter", quarter, "category", category, "expenses", items)), nil
}

// getCustomBudget gets the custom budget for a user if they have one.
func getCustomBudget(_ context.Context, args []any, kwargs host.Kwargs) (any, error) {
	bound, err := pyargs.Bind("get_custom_budget", args, kwargs, pyargs.Required("user_id"))
	if err != nil {
		return nil, err
	}
	userID, err := pyargs.Int("user_id", bound[0])
	if err != nil {
		return nil, err
	}
	budget, ok := customBudgets[userID]
	if !ok {
		return resolved(nil), nil
	}
	return resolved(dict("user_id", userID, "budget", budget.Amount, "reason", budget.Reason)), nil
}
