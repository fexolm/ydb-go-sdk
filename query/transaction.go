package query

import (
	"context"

	"github.com/ydb-platform/ydb-go-sdk/v3/internal/query/options"
	"github.com/ydb-platform/ydb-go-sdk/v3/internal/query/tx"
)

type (
	TxIdentifier interface {
		ID() string
	}
	TxActor interface {
		TxIdentifier

		// Execute executes query.
		//
		// Execute used by default:
		// - DefaultTxControl
		// - flag WithKeepInCache(true) if params is not empty.
		Execute(ctx context.Context, query string, opts ...options.TxExecuteOption) (r Result, err error)

		// ReadRow is a helper which read only one row from first result set in result
		//
		// ReadRow returns error if result contains more than one result set or more than one row
		//
		// Experimental: https://github.com/ydb-platform/ydb-go-sdk/blob/master/VERSIONING.md#experimental
		ReadRow(ctx context.Context, query string, opts ...options.TxExecuteOption) (Row, error)

		// ReadResultSet is a helper which read all rows from first result set in result
		//
		// ReadRow returns error if result contains more than one result set
		//
		// Experimental: https://github.com/ydb-platform/ydb-go-sdk/blob/master/VERSIONING.md#experimental
		ReadResultSet(ctx context.Context, query string, opts ...options.TxExecuteOption) (ResultSet, error)
	}
	Transaction interface {
		TxActor

		CommitTx(ctx context.Context) (err error)
		Rollback(ctx context.Context) (err error)
	}
	TransactionControl  = tx.Control
	TransactionSettings = tx.Settings
	TransactionOption   = tx.Option
)

// BeginTx returns selector transaction control option
func BeginTx(opts ...tx.Option) tx.ControlOption {
	return tx.BeginTx(opts...)
}

func WithTx(t tx.Identifier) tx.ControlOption {
	return tx.WithTx(t)
}

func WithTxID(txID string) tx.ControlOption {
	return tx.WithTxID(txID)
}

// CommitTx returns commit transaction control option
func CommitTx() tx.ControlOption {
	return tx.CommitTx()
}

// TxControl makes transaction control from given options
func TxControl(opts ...tx.ControlOption) *TransactionControl {
	return tx.NewControl(opts...)
}

func NoTx() *TransactionControl {
	return nil
}

// DefaultTxControl returns default transaction control for use default tx control on server-side
func DefaultTxControl() *TransactionControl {
	return NoTx()
}

// SerializableReadWriteTxControl returns transaction control with serializable read-write isolation mode
func SerializableReadWriteTxControl(opts ...tx.ControlOption) *TransactionControl {
	return tx.SerializableReadWriteTxControl(opts...)
}

// OnlineReadOnlyTxControl returns online read-only transaction control
func OnlineReadOnlyTxControl(opts ...tx.OnlineReadOnlyOption) *TransactionControl {
	return TxControl(
		BeginTx(WithOnlineReadOnly(opts...)),
		CommitTx(), // open transactions not supported for OnlineReadOnly
	)
}

// StaleReadOnlyTxControl returns stale read-only transaction control
func StaleReadOnlyTxControl() *TransactionControl {
	return TxControl(
		BeginTx(WithStaleReadOnly()),
		CommitTx(), // open transactions not supported for StaleReadOnly
	)
}

// SnapshotReadOnlyTxControl returns snapshot read-only transaction control
func SnapshotReadOnlyTxControl() *TransactionControl {
	return TxControl(
		BeginTx(WithSnapshotReadOnly()),
		CommitTx(), // open transactions not supported for StaleReadOnly
	)
}

// TxSettings returns transaction settings
func TxSettings(opts ...tx.Option) TransactionSettings {
	return opts
}

func WithDefaultTxMode() tx.Option {
	return tx.WithDefaultTxMode()
}

func WithSerializableReadWrite() tx.Option {
	return tx.WithSerializableReadWrite()
}

func WithSnapshotReadOnly() tx.Option {
	return tx.WithSnapshotReadOnly()
}

func WithStaleReadOnly() tx.Option {
	return tx.WithStaleReadOnly()
}

func WithInconsistentReads() tx.OnlineReadOnlyOption {
	return tx.WithInconsistentReads()
}

func WithOnlineReadOnly(opts ...tx.OnlineReadOnlyOption) tx.Option {
	return tx.WithOnlineReadOnly(opts...)
}
