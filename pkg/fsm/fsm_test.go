package fsm

import (
	"testing"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/stretchr/testify/assert"
)

type invalidCommand struct{}

func TestFSMApply(t *testing.T) {
	c := cache.New()
	fsm := New(c)

	t.Run("ValidCommand", func(t *testing.T) {
		cmd := &command.SetCommand{Key: "k1", Value: "v1"}
		resp, err := fsm.Apply(cmd)
		assert.Nil(t, err)
		assert.True(t, resp.(*pb.SetResponse).Success)
	})

	t.Run("InvalidCommandType", func(t *testing.T) {
		_, err := fsm.Apply(&invalidCommand{})
		assert.ErrorContains(t, err, "invalid command type")
	})

	t.Run("NonPointerCommand", func(t *testing.T) {
		_, err := fsm.Apply("invalid-type")
		assert.ErrorContains(t, err, "invalid command type")
	})
}
