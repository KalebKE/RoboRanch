package main

import (
	"os"

	"github.com/KalebKE/RoboRanch/internal/roboranch"
)

func main() {
	os.Exit(roboranch.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
