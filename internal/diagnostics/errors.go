package diagnostics

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
)

// Error records classifications only; raw errors may contain business values.
func (r *Recorder) Error(name string, stage Metric, err error) {
	if r == nil || err == nil {
		return
	}
	class := "other"
	var code uint16
	var databaseError *mysql.MySQLError
	if errors.As(err, &databaseError) {
		class = "database"
		code = databaseError.Number
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		class = "cancelled"
	}
	r.Failure(name, stage, class, code)
}
