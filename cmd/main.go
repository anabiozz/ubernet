package main

import "ubernet"

import "fmt"

func main() {
	fd, err := ubernet.CreateSocket()
	if err != nil {
		panic(err)
	}

	fmt.Println("fd", fd)

}
