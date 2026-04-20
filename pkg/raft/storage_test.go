package raft

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorageAppendLoadAndTruncate(t *testing.T) {
	st := NewStorage(filepath.Join(t.TempDir(), "raft.wal"))

	require.NoError(t, st.AppendEntries([]LogEntry{
		{Index: 1, Term: 1, Type: EntryTypeCommand, Data: []byte("a")},
		{Index: 2, Term: 1, Type: EntryTypeCommand, Data: []byte("b")},
		{Index: 3, Term: 2, Type: EntryTypeCommand, Data: []byte("c")},
	}))

	entries, err := st.LoadEntries()
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, uint64(3), entries[2].Index)

	require.NoError(t, st.TruncateFrom(3))
	entries, err = st.LoadEntries()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, uint64(2), entries[1].Index)
}

func TestStorageSaveAndLoadMeta(t *testing.T) {
	st := NewStorage(filepath.Join(t.TempDir(), "raft.wal"))

	meta := &Meta{
		CurrentTerm:   3,
		VotedFor:      "n1",
		CommitIndex:   8,
		Peers:         []string{"http://127.0.0.1:1"},
		SnapshotIndex: 6,
		SnapshotTerm:  2,
	}
	require.NoError(t, st.SaveMeta(meta))

	got, err := st.LoadMeta()
	require.NoError(t, err)
	require.Equal(t, meta.CurrentTerm, got.CurrentTerm)
	require.Equal(t, meta.VotedFor, got.VotedFor)
	require.Equal(t, meta.CommitIndex, got.CommitIndex)
	require.Equal(t, meta.Peers, got.Peers)
	require.Equal(t, meta.SnapshotIndex, got.SnapshotIndex)
	require.Equal(t, meta.SnapshotTerm, got.SnapshotTerm)
}

func TestStorageLoadEntriesSupportsLargePayload(t *testing.T) {
	st := NewStorage(filepath.Join(t.TempDir(), "raft.wal"))

	largeData := bytes.Repeat([]byte("x"), 128*1024)
	require.NoError(t, st.AppendEntry(LogEntry{
		Index: 1,
		Term:  1,
		Type:  EntryTypeCommand,
		Data:  largeData,
	}))

	entries, err := st.LoadEntries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, largeData, entries[0].Data)
}

func TestStorageSaveAndLoadSnapshot(t *testing.T) {
	st := NewStorage(filepath.Join(t.TempDir(), "raft.wal"))

	meta := SnapshotMeta{
		LastIncludedIndex: 100,
		LastIncludedTerm:  7,
	}
	data := []byte("snapshot-data")
	require.NoError(t, st.SaveSnapshot(meta, data))

	gotMeta, gotData, err := st.LoadSnapshot()
	require.NoError(t, err)
	require.NotNil(t, gotMeta)
	require.Equal(t, meta.LastIncludedIndex, gotMeta.LastIncludedIndex)
	require.Equal(t, meta.LastIncludedTerm, gotMeta.LastIncludedTerm)
	require.Equal(t, data, gotData)
	require.True(t, st.HasSnapshot())
}

func TestStorageCompactLog(t *testing.T) {
	st := NewStorage(filepath.Join(t.TempDir(), "raft.wal"))
	require.NoError(t, st.AppendEntries([]LogEntry{
		{Index: 1, Term: 1, Type: EntryTypeCommand, Data: []byte("1")},
		{Index: 2, Term: 1, Type: EntryTypeCommand, Data: []byte("2")},
		{Index: 3, Term: 2, Type: EntryTypeCommand, Data: []byte("3")},
		{Index: 4, Term: 2, Type: EntryTypeCommand, Data: []byte("4")},
	}))

	require.NoError(t, st.CompactLog(2))
	entries, err := st.LoadEntries()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, uint64(3), entries[0].Index)
	require.Equal(t, uint64(4), entries[1].Index)
}
