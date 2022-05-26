package main

import (
	"fmt"
	"sync"
)

func main() {
	var wg sync.WaitGroup

	i := 1

	wg.Add(1)
	go func() {
		defer wg.Done()
		i = i + 3
	}()

	i = i + 2

	fmt.Println(i)

	wg.Wait()
}
