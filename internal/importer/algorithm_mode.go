//go:build !migration_baseline

package importer

// BaselineAlgorithms is a compile-time choice, never an automatic write fallback.
const BaselineAlgorithms = false
