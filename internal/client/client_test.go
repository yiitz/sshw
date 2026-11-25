package client

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestProgressWriterKeepsGoingWhenTotalIsExceeded covers downloading a file
// that grew after its size was measured (a live log, for example):
// progressbar reports "current number exceeds max" once the bar is full, and
// that error used to abort io.Copy, which restarted the transfer from scratch
// forever.
func TestProgressWriterKeepsGoingWhenTotalIsExceeded(t *testing.T) {
	const total = 10
	bar := newTransferBar(total, "test")
	if bar == nil {
		t.Fatal("newTransferBar returned nil")
	}

	var dst bytes.Buffer
	src := strings.NewReader(strings.Repeat("x", 2*total))
	n, err := io.Copy(io.MultiWriter(&dst, &progressWriter{bar: bar}), src)
	if err != nil {
		t.Fatalf("copy failed after the bar filled up: %v", err)
	}
	if n != 2*total {
		t.Fatalf("copied %d bytes, want %d", n, 2*total)
	}
	if dst.Len() != 2*total {
		t.Fatalf("wrote %d bytes to dst, want %d", dst.Len(), 2*total)
	}
	if err := bar.Finish(); err != nil {
		t.Fatalf("Finish after the bar overflowed: %v", err)
	}
}

// TestTransferBarForEmptyFile ensures an empty file gets no progress bar
// (progressbar rejects a max of 0) and that progress reporting tolerates it.
func TestTransferBarForEmptyFile(t *testing.T) {
	if bar := newTransferBar(0, "test"); bar != nil {
		t.Fatal("expected a nil bar for an empty file")
	}

	w := &progressWriter{}
	n, err := w.Write([]byte("abc"))
	if n != 3 || err != nil {
		t.Fatalf("Write = (%d, %v), want (3, nil)", n, err)
	}
}
