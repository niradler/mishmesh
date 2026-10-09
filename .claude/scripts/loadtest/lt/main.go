package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "backend":
		err = runBackend(args)
	case "agents":
		err = runAgents(args)
	case "load":
		err = runLoad(args)
	case "lb":
		err = runLB(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lt backend|agents|load|lb [flags]")
	os.Exit(2)
}
