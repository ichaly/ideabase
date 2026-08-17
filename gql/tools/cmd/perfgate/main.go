package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ichaly/ideabase/gql/tools/perfgate"
)

func main() {
	budget := flag.Float64("budget", 0.10, "maximum significant ns/op regression")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: perfgate [-budget 0.10] BASE HEAD")
		os.Exit(2)
	}
	base, err := os.Open(flag.Arg(0))
	if err != nil { fatal(err) }
	defer base.Close()
	head, err := os.Open(flag.Arg(1))
	if err != nil { fatal(err) }
	defer head.Close()
	if err = perfgate.Compare(base, head, *budget); err != nil { fatal(err) }
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
