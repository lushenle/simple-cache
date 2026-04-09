package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lushenle/simple-cache/pkg/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// --- gRPC Client 示例 ---
	cli, err := client.New(context.Background(), "localhost:5051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1024*1024*10)),
	)
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Set a key-value pair with 10 minute TTL
	err = cli.Set(ctx, "greeting", "Hello World!", 10*time.Minute)
	if err != nil {
		panic(err)
	}
	fmt.Println("✓ Set greeting = Hello World!")

	// Get a value by key
	val, found, err := cli.Get(ctx, "greeting")
	if err != nil {
		panic(err)
	}
	if found {
		fmt.Printf("✓ Get greeting = %s\n", val)
	}

	// Search for keys matching a pattern
	keys, err := cli.Search(ctx, "greet*", false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ Search greet* = %v\n", keys)

	// Set more keys for demonstration
	_ = cli.Set(ctx, "user:1", "Alice", 30*time.Minute)
	_ = cli.Set(ctx, "user:2", "Bob", 30*time.Minute)
	_ = cli.Set(ctx, "config:debug", "true", 0) // no expiration
	fmt.Println("✓ Set user:1, user:2, config:debug")

	// --- REST API 示例：数据持久化 ---
	// Dump/Load 目前通过 REST API 调用（gRPC 客户端 SDK 暂未封装）
	baseURL := "http://localhost:8080"

	// Dump cache data to disk (binary format, default)
	fmt.Println("\n--- Persistence ---")
	dumpResp, err := postJSON(baseURL+"/v1/dump", map[string]string{"format": "json"})
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ Dump: %s\n", dumpResp)

	// Reset cache to demonstrate Load
	cleared, err := cli.Reset(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ Reset: cleared %d keys\n", cleared)

	// Verify cache is empty
	_, found, _ = cli.Get(ctx, "greeting")
	if !found {
		fmt.Println("✓ Cache is now empty")
	}

	// Load cache data from disk (auto-detect format)
	loadResp, err := postJSON(baseURL+"/v1/load", map[string]string{})
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ Load: %s\n", loadResp)

	// Verify data is restored
	val, found, _ = cli.Get(ctx, "greeting")
	if found {
		fmt.Printf("✓ Restored greeting = %s\n", val)
	}

	val, found, _ = cli.Get(ctx, "user:1")
	if found {
		fmt.Printf("✓ Restored user:1 = %s\n", val)
	}

	// Expire a key
	existed, err := cli.ExpireKey(ctx, "user:2", 5*time.Second)
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ ExpireKey user:2: existed=%v, TTL=5s\n", existed)

	// Batch set
	items := map[string]string{
		"batch:1": "alpha",
		"batch:2": "beta",
		"batch:3": "gamma",
	}
	err = cli.BatchSet(ctx, items, 1*time.Hour)
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ BatchSet: %d keys\n", len(items))

	// Search with regex
	keys, err = cli.Search(ctx, `^batch:\d+$`, true)
	if err != nil {
		panic(err)
	}
	fmt.Printf("✓ Regex search ^batch:\\d+$ = %v\n", keys)
}

// postJSON sends a JSON POST request and returns the response body as a string.
func postJSON(url string, body interface{}) (string, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(result))
	}

	return string(result), nil
}
