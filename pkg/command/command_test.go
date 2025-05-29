package command

import (
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/log"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
)

func TestSetCommand(t *testing.T) {
	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	c := cache.New(time.Second*3, logger)
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
	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	c := cache.New(time.Second*3, logger)
	err := c.Set("exist", "value", "")
	assert.Nil(t, err)

	cmd := &DelCommand{Key: "exist"}
	resp, _ := cmd.Apply(c)
	assert.True(t, resp.(*pb.DelResponse).Existed)

	cmd = &DelCommand{Key: "nonexistent"}
	resp, _ = cmd.Apply(c)
	assert.False(t, resp.(*pb.DelResponse).Existed)
}
