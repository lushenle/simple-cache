package raft

import (
	"os"
)

type Storage struct {
	path string
}

func NewStorage(path string) *Storage { return &Storage{path: path} }

func (s *Storage) Append(b []byte) error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}
