package mountfs

import (
	"os"
	"sort"
)

const (
	entryMemoryUsage          uint64 = 256
	overlayEntrySize          uint64 = 48
	realDescendantMemoryUsage uint64 = 512
)

type entryKind uint8

const (
	entFile entryKind = iota
	entRealFileRef
	entDirectory
	entDeleted
)

type overlayEntry struct {
	kind     entryKind
	content  []byte
	mtime    float64
	relative string
	size     int64
}

func deletedEntry() *overlayEntry { return &overlayEntry{kind: entDeleted} }

func directoryEntry(mtime float64) *overlayEntry {
	return &overlayEntry{kind: entDirectory, mtime: mtime}
}

func fileEntry(content []byte, mtime float64) *overlayEntry {
	return &overlayEntry{kind: entFile, content: content, mtime: mtime}
}

// fileRefFromRelative captures a lazy reference only when the final component is a regular file.
func fileRefFromRelative(dir *os.Root, rel string) *overlayEntry {
	fi, err := dir.Lstat(rel)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	return &overlayEntry{kind: entRealFileRef, relative: rel, mtime: mtimeSecs(fi), size: fi.Size()}
}

// overlayState is an ordered map of mount-relative keys ("" is the root).
type overlayState struct {
	entries map[string]*overlayEntry
	keys    []string
	usage   uint64
}

func newOverlayState() *overlayState {
	return &overlayState{entries: map[string]*overlayEntry{}}
}

func (s *overlayState) get(key string) *overlayEntry { return s.entries[key] }

func (s *overlayState) memoryUsage() uint64 { return s.usage }

func (s *overlayState) put(key string, e *overlayEntry) {
	if _, ok := s.entries[key]; !ok {
		i := sort.SearchStrings(s.keys, key)
		s.keys = append(s.keys, "")
		copy(s.keys[i+1:], s.keys[i:])
		s.keys[i] = key
	}
	s.entries[key] = e
}

func (s *overlayState) remove(key string) *overlayEntry {
	e, ok := s.entries[key]
	if !ok {
		return nil
	}
	delete(s.entries, key)
	i := sort.SearchStrings(s.keys, key)
	s.keys = append(s.keys[:i], s.keys[i+1:]...)
	s.usage = satSub(s.usage, entryUsage(key, e))
	return e
}

func (s *overlayState) insert(key string, e *overlayEntry, limit uint64) *mountError {
	projected := s.projectedUsage(key, e)
	if projected > limit {
		return memoryLimitExceeded(limit)
	}
	s.put(key, e)
	s.usage = projected
	return nil
}

func (s *overlayState) insertUnchecked(key string, e *overlayEntry) {
	s.usage = s.projectedUsage(key, e)
	s.put(key, e)
}

func (s *overlayState) projectedUsage(key string, e *overlayEntry) uint64 {
	var old uint64
	if prev := s.entries[key]; prev != nil {
		old = entryUsage(key, prev)
	}
	return satAdd(satSub(s.usage, old), entryUsage(key, e))
}

func (s *overlayState) appendFile(key string, data []byte, mtime float64, limit uint64) (bool, *mountError) {
	e := s.entries[key]
	if e == nil || e.kind != entFile {
		return false, nil
	}
	projected := satAdd(s.usage, uint64(len(data)))
	if projected > limit {
		return false, memoryLimitExceeded(limit)
	}
	e.content = append(e.content, data...)
	e.mtime = mtime
	s.usage = projected
	return true, nil
}

func (s *overlayState) checkFileReplacement(key string, contentLen, limit uint64) *mountError {
	var old uint64
	if prev := s.entries[key]; prev != nil {
		old = entryUsage(key, prev)
	}
	if satAdd(satSub(s.usage, old), satAdd(baseEntryUsage(key), contentLen)) > limit {
		return memoryLimitExceeded(limit)
	}
	return nil
}

type replacement struct {
	key   string
	entry *overlayEntry
}

// checkReplacements projects a batch as one update; later writes to a key win.
func (s *overlayState) checkReplacements(batch []replacement, limit uint64) *mountError {
	projected := s.usage
	replaced := map[string]uint64{}
	for _, r := range batch {
		old, seen := replaced[r.key]
		if !seen {
			if prev := s.entries[r.key]; prev != nil {
				old = entryUsage(r.key, prev)
			}
		}
		next := entryUsage(r.key, r.entry)
		projected = satAdd(satSub(projected, old), next)
		replaced[r.key] = next
	}
	if projected > limit {
		return memoryLimitExceeded(limit)
	}
	return nil
}

// prefixKeys returns keys in [prefix, prefix[:-1]+"0"); prefix is "" or ends with '/'.
func (s *overlayState) prefixKeys(prefix string) []string {
	if prefix == "" {
		return append([]string(nil), s.keys...)
	}
	start := sort.SearchStrings(s.keys, prefix)
	end := sort.SearchStrings(s.keys, prefix[:len(prefix)-1]+"0")
	return append([]string(nil), s.keys[start:end]...)
}

func entryUsage(key string, e *overlayEntry) uint64 {
	var variable uint64
	switch e.kind {
	case entFile:
		variable = uint64(len(e.content))
	case entRealFileRef:
		variable = uint64(len(e.relative))
	}
	return satAdd(baseEntryUsage(key), variable)
}

func baseEntryUsage(key string) uint64 {
	return satAdd(satAdd(entryMemoryUsage, uint64(len(key))), overlayEntrySize)
}

func satSub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}
