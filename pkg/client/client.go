package client

import (
	"context"
	"time"

	"github.com/lushenle/simple-cache/pkg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.CacheServiceClient
}

// New creates a new client instance
func New(addr string, opts ...grpc.DialOption) (*Client, error) {
	defaultOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	opts = append(defaultOpts, opts...)

	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}

	return &Client{
		conn:   conn,
		client: pb.NewCacheServiceClient(conn),
	}, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// Get gets a value by key
func (c *Client) Get(ctx context.Context, key string) (string, bool, error) {
	resp, err := c.client.Get(ctx, &pb.GetRequest{Key: key})
	if err != nil {
		return "", false, err
	}
	return resp.Value, resp.Found, nil
}

// Set sets a key-value pair
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	_, err := c.client.Set(ctx, &pb.SetRequest{
		Key:    key,
		Value:  value,
		Expire: formatTTL(ttl),
	})
	return err
}

// Del deletes a key
func (c *Client) Del(ctx context.Context, key string) (bool, error) {
	resp, err := c.client.Del(ctx, &pb.DelRequest{Key: key})
	if err != nil {
		return false, err
	}
	return resp.Existed, nil
}

// Search searches for keys matching the pattern
func (c *Client) Search(ctx context.Context, pattern string, isRegex bool) ([]string, error) {
	resp, err := c.client.Search(ctx, &pb.SearchRequest{
		Pattern: pattern,
	})
	if err != nil {
		return nil, err
	}
	return resp.Keys, nil
}

// ExpireKey expires a key
func (c *Client) ExpireKey(ctx context.Context, key string) (bool, error) {
	resp, err := c.client.ExpireKey(ctx, &pb.ExpireKeyRequest{Key: key})
	if err != nil {
		return false, err
	}

	return resp.Existed, nil
}

// Reset is reset the cache
func (c *Client) Reset(ctx context.Context) (int, error) {
	resp, err := c.client.Reset(ctx, &pb.ResetRequest{})
	if err != nil {
		return 0, err
	}
	return int(resp.KeysCleared), nil
}

// formatTTL converts time.Duration to string
func formatTTL(d time.Duration) string {
	if d <= 0 {
		return ""
	}

	return d.String()
}

func (c *Client) BatchSet(ctx context.Context, items map[string]string, ttl time.Duration) error {
	// TODO: implement batch set
	return nil
}
