package db

import "errors"

// Postgres error codes we react to (https://www.postgresql.org/docs/current/errcodes-appendix.html).
const (
	CodeUniqueViolation     = "23505" // a UNIQUE constraint or unique index was broken
	CodeForeignKeyViolation = "23503" // a row points at a parent row that does not exist
)

// SQLState returns the Postgres error code inside err, or "" if there is none.
//
// Both pgx (*pgconn.PgError) and lib/pq (*pq.Error) have a SQLState() method,
// so checking for that method keeps this code independent of the driver.
func SQLState(err error) string {
	var e interface{ SQLState() string }
	if errors.As(err, &e) {
		return e.SQLState()
	}
	return ""
}
