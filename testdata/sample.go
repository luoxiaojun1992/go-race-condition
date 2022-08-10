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

	l.Lock()
	i := 1
	l.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()

		l.Lock()
		i = i + 3
		l.Unlock()
	}()

	l.Lock()
	Foo()
	i = i + 2
	fmt.Println(i)
	l.Unlock()

	wg.Wait()
}
