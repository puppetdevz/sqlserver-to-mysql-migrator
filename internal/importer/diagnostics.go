package importer

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
)

func recordDiagnosticFailure(r *diagnostics.Recorder, name string, err error) {
	if r == nil || err == nil {
		return
	}
	stage := diagnostics.Pipeline
	class := "other"
	var code uint16
	var dbErr *mysql.MySQLError
	var structure *CSVStructureError
	var staged *insertStageError
	if errors.As(err, &dbErr) {
		class = "database"
		code = dbErr.Number
	}
	if errors.As(err, &structure) {
		class = "structure"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		class = "cancelled"
	}
	if errors.Is(err, ErrUnknownCommit) {
		class = "unknown_commit"
	}
	if errors.As(err, &staged) {
		switch staged.Stage {
		case insertStagePrepare:
			stage = diagnostics.Prepare
		case insertStageExec:
			stage = diagnostics.Exec
		case insertStageResult:
			stage = diagnostics.ResultRead
		}
	}
	r.Failure(name, stage, class, code)
}
