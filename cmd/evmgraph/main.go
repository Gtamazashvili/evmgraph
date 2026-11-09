// Command evmgraph holds simulated EVM addresses, their balances and the
// addresses connected to them. `evmgraph run` generates and saves the dataset
// (or loads the saved one) and runs until it is told to stop; balance,
// connections and search answer lookups from the saved dataset, and update
// changes a balance without rebuilding it.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

const usage = `evmgraph holds simulated EVM addresses, their balances and their connections.

usage:
  evmgraph run [flags]                     generate and save the dataset (or load it), run until stopped
  evmgraph balance [-data folder] <address>
                                           print an address's balance
  evmgraph connections [-data folder] [-limit N | -all] [-timeout D] <address>
                                           print its connections, 2 levels deep
  evmgraph search [-data folder] [-limit N]
                                           a prompt: paste an address, see its balance and neighbours
  evmgraph update [-data folder] balance <address> <wei>
  evmgraph update [-data folder] link <address> <address>
                                           change a balance, or connect two addresses, without rebuilding
  evmgraph help                            this help

Each command takes -h for its flags. The data lives in ./cache unless -data says otherwise.
`

func main() {
	os.Exit(guardFaults(func() int { return dispatch(os.Args[1:]) }))
}

// guardFaults runs a command and turns a memory fault into an error. The
// dataset's files are mapped into memory (decision 002); if another program
// shrinks one while a command reads it, the next read past the new end
// faults, which would kill the process with a bus error. With panic-on-fault
// set, the fault is a panic instead, caught here: the command says what
// happened and exits 1. The program itself never shrinks a file in place.
// Only the calling goroutine is covered, and the reads happen on it.
func guardFaults(command func() int) (code int) {
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if fault, ok := r.(interface{ Addr() uintptr }); ok {
			ui.fail("a dataset file changed while it was being read (fault at address %#x): another program "+
				"may have shrunk or replaced it; run the command again, or evmgraph run to check the dataset", fault.Addr())
			code = exitNotFound
			return
		}
		panic(r)
	}()
	return command()
}

// dispatch runs the command args name and returns the exit code.
func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, usage)
		return exitFound
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "run":
		return runRun(rest)
	case "balance":
		return runBalance(rest)
	case "connections":
		return runConnections(rest)
	case "search":
		return runSearch(rest)
	case "update":
		return runUpdate(rest)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(os.Stdout, usage)
		return exitFound
	default:
		if strings.HasPrefix(cmd, "-") {
			ui.fail("%q is a flag, and the command comes first: to generate or load the dataset, use evmgraph run %s",
				cmd, strings.Join(args, " "))
		} else {
			ui.fail("unknown command %q; the commands are run, balance, connections, search and update (evmgraph help)", cmd)
		}
		return exitUsage
	}
}
