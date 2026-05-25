package main

import (
	"os"

	"github.com/TracqiTechnology/roboranch/internal/roboranch"
)

func main() {
	os.Exit(roboranch.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
