package main

import (
	"flag"
	"github.com/luoxiaojun1992/go-race-condition/pkg"
)

func main() {
	var filePath string
	flag.StringVar(&filePath, "file", "", "")
	flag.Parse()

	l, err := pkg.NewLinter(filePath)
	if err != nil {
		panic(err)
	}

	l.Analysis()
}
