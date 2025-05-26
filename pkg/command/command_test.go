package command

import (
	"testing"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/stretchr/testify/assert"
)

func TestSetCommand(t *testing.T) {
	c := cache.New()
	cmd := &SetCommand{
		Key:    "test",
		Value:  "value",
		Expire: "1s",
	}

	resp, err := cmd.Apply(c)
	assert.Nil(t, err)
	assert.True(t, resp.(*pb.SetResponse).Success)

	val, found := c.Get("test")
	assert.True(t, found)
	assert.Equal(t, "value", val)
}

func TestDelCommand(t *testing.T) {
	c := cache.New()
	err := c.Set("exist", "value", "")
	assert.Nil(t, err)

	cmd := &DelCommand{Key: "exist"}
	resp, _ := cmd.Apply(c)
	assert.True(t, resp.(*pb.DelResponse).Existed)

	cmd = &DelCommand{Key: "nonexistent"}
	resp, _ = cmd.Apply(c)
	assert.False(t, resp.(*pb.DelResponse).Existed)
}
