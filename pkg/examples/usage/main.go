package main

import (
	"context"
	"fmt"
	"time"

	"github.com/lushenle/simple-cache/pkg/client"
	"google.golang.org/grpc"
)

func main() {
	// Initialize the client
	cli, err := client.New(context.Background(), "localhost:5051", grpc.WithDefaultCallOptions(
		grpc.MaxCallRecvMsgSize(1024*1024*10),
	))
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Set a key-value pair
	err = cli.Set(ctx, "greeting", "Hello World!", 10*time.Minute)
	if err != nil {
		panic(err)
	}

	// Get a value by key
	val, found, err := cli.Get(ctx, "greeting")
	if err != nil {
		panic(err)
	}
	if found {
		fmt.Println("Value:", val)
	}

	// Search for keys matching a pattern
	keys, err := cli.Search(ctx, "greet*", false)
	if err != nil {
		panic(err)
	}
	fmt.Println("Matching keys:", keys)
}
