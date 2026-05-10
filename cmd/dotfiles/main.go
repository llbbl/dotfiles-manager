package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "0.0.1-dev"

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: dotfiles <command>\n\ncommands:\n  version    print version\n\nflags:\n  -v, --version    print version\n")
	}
	showVersion := flag.Bool("version", false, "print version")
	flag.BoolVar(showVersion, "v", false, "print version")
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	switch flag.Arg(0) {
	case "version":
		printVersion()
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func printVersion() {
	fmt.Printf("dotfiles %s\n", version)
}
