package evm

// Account is one row of the dataset: an address and its balance, 36 bytes.
// The program keeps newly generated rows as []Account while it runs, so
// sorting moves an address and its balance together; on disk they are two
// column files (decision 009).
type Account struct {
	Address Address
	Balance Wei
}
