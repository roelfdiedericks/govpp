package statsclient

import (
	"testing"
	"unsafe"

	"go.fd.io/govpp/adapter"
)

// testSegmentV2 builds a version 2 stats segment in memory: the shared
// header, then a directory vector (length word + entries). base is 0, so
// segment offsets are the stored pointer values.
func testSegmentV2(t *testing.T, entries []statSegDirectoryEntryV2) *statSegmentV2 {
	t.Helper()
	hdrSize := unsafe.Sizeof(sharedHeaderV2{})
	entSize := unsafe.Sizeof(statSegDirectoryEntryV2{})
	size := hdrSize + 8 + uintptr(len(entries))*entSize + 64
	words := make([]uint64, (size+7)/8)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)

	vecOff := hdrSize + 8
	*(*uint64)(unsafe.Pointer(&buf[hdrSize])) = uint64(len(entries))
	for i, e := range entries {
		*(*statSegDirectoryEntryV2)(unsafe.Pointer(&buf[vecOff+uintptr(i)*entSize])) = e
	}
	h := (*sharedHeaderV2)(unsafe.Pointer(&buf[0]))
	h.version = 2
	*(*uint64)(unsafe.Pointer(&h.dirVector)) = uint64(vecOff)
	return newStatSegmentV2(buf, int64(len(buf)))
}

func dirEntry(typ dirType, value uint64, name string) statSegDirectoryEntryV2 {
	e := statSegDirectoryEntryV2{directoryType: typ, unionData: value}
	copy(e.name[:], name)
	return e
}

// A VPP 26.10 gauge (type 9) reads as a scalar with its inline value,
// next to an ordinary scalar; before, it came back as UnknownStatType and
// was dropped.
func TestStatSegmentV2Gauge(t *testing.T) {
	ss := testSegmentV2(t, []statSegDirectoryEntryV2{
		dirEntry(9, 12345, "/buffer-pools/default/used"),
		dirEntry(1, 7, "/sys/vector_rate"),
	})
	dv := ss.GetDirectoryVector()
	if dv == nil {
		t.Fatal("directory vector not found")
	}
	cases := []struct {
		name  string
		value adapter.ScalarStat
	}{
		{"/buffer-pools/default/used", 12345},
		{"/sys/vector_rate", 7},
	}
	for i, c := range cases {
		seg, name, typ := ss.GetStatDirOnIndex(dv, uint32(i))
		if string(name) != c.name || typ != adapter.ScalarIndex {
			t.Fatalf("entry %d: %q type %s, want %q ScalarIndex", i, name, typ, c.name)
		}
		got := ss.CopyEntryData(seg, ^uint32(0))
		if got != c.value {
			t.Fatalf("%s copied as %#v, want ScalarStat(%v)", c.name, got, c.value)
		}
	}

	// A later read of the same entry follows the gauge's new value
	seg, _, _ := ss.GetStatDirOnIndex(dv, 0)
	(*statSegDirectoryEntryV2)(seg).unionData = 999
	var st adapter.Stat = adapter.ScalarStat(0)
	if err := ss.UpdateEntryData(seg, &st); err != nil {
		t.Fatal(err)
	}
	if st != adapter.ScalarStat(999) {
		t.Fatalf("gauge updated to %#v, want ScalarStat(999)", st)
	}
}
