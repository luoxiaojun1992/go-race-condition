package main

import (
	"fmt"
	"sync"
)

func Foo() {

}

func main() {
	var wg sync.WaitGroup
	var l sync.Mutex

	i := 1

	wg.Add(1)
	go func() {
		defer wg.Done()

		l.Lock()
		i = i + 3
		l.Unlock()
	}()

	Foo()
	i = i + 2
	fmt.Println(i)

	wg.Wait()
}
