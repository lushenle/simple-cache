package raft

import (
	"encoding/json"
	"os"
)

type Storage struct {
	path string
}

func NewStorage(path string) *Storage { return &Storage{path: path} }

func (s *Storage) Append(b []byte) error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

type Meta struct {
	CurrentTerm uint64 `json:"current_term"`
	VotedFor    string `json:"voted_for"`
}

func (s *Storage) metaPath() string { return s.path + ".meta" }

func (s *Storage) LoadMeta() (*Meta, error) {
	mp := s.metaPath()
	f, err := os.Open(mp)
	if err != nil {
		return &Meta{}, nil
	}
	defer f.Close()
	var m Meta
	dec := json.NewDecoder(f)
	if err := dec.Decode(&m); err != nil {
		return &Meta{}, nil
	}
	return &m, nil
}

func (s *Storage) SaveMeta(m *Meta) error {
	mp := s.metaPath()
	f, err := os.OpenFile(mp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(m)
}
