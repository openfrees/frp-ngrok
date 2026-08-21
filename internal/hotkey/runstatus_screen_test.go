package hotkey

import (
	"fmt"
	"strings"
	"testing"
)

func TestRunStatusScreenCRLFCommitsLineInsteadOfWipingIt(t *testing.T) {
	s := newRunStatusScreen(200)
	s.feed([]byte("第一行\r\n第二行\r\n请输入选项 [1/2/3]: "))

	got := s.snapshot()
	if !strings.Contains(got, "第一行") || !strings.Contains(got, "第二行") {
		t.Fatalf("PTY ONLCR \\r\\n wiped real output: %q", got)
	}
	if !strings.Contains(got, "请输入选项 [1/2/3]:") {
		t.Fatalf("prompt missing: %q", got)
	}
	if strings.Count(got, "\n\n\n") > 0 {
		t.Fatalf("blank padding leaked into snapshot: %q", got)
	}
}

func TestRunStatusScreenCRLFSplitAcrossWritesStillCommits(t *testing.T) {
	s := newRunStatusScreen(200)
	s.feed([]byte("keep-me\r"))
	s.feed([]byte("\nnext\n"))

	got := s.snapshot()
	if !strings.Contains(got, "keep-me") {
		t.Fatalf("CR at chunk end plus LF should commit the line: %q", got)
	}
	if !strings.Contains(got, "next") {
		t.Fatalf("line after split CRLF missing: %q", got)
	}
}

func TestRunStatusScreenCarriageReturnOverwritesLine(t *testing.T) {
	s := newRunStatusScreen(200)
	s.feed([]byte("\rtransforming (1575/6530) 25%"))
	s.feed([]byte("\rtransforming (1714/6530) 25%"))

	got := s.snapshot()
	if strings.Contains(got, "1575") {
		t.Fatalf("old progress frame leaked into snapshot: %q", got)
	}
	if !strings.Contains(got, "1714/6530") {
		t.Fatalf("latest progress missing: %q", got)
	}
}

func TestRunStatusScreenThousandsOfProgressFramesStaySmall(t *testing.T) {
	s := newRunStatusScreen(200)
	for i := 0; i < 8000; i++ {
		s.feed([]byte(fmt.Sprintf("\r\x1b[32mBuilding\x1b[39m %d/6530", i)))
	}
	got := s.snapshot()
	if len(got) > 200 {
		t.Fatalf("snapshot too large after progress flood: %d bytes %q", len(got), got)
	}
	if !strings.Contains(got, "7999/6530") {
		t.Fatalf("latest frame missing: %q", got)
	}
}

func TestRunStatusScreenDropsOldCompletedLines(t *testing.T) {
	s := newRunStatusScreen(3)
	s.feed([]byte("a\nb\nc\nd\n"))
	got := s.snapshot()
	if strings.Contains(got, "a") {
		t.Fatalf("oldest line should have been dropped: %q", got)
	}
	for _, want := range []string{"b", "c", "d"} {
		if !strings.Contains(got, want) {
			t.Fatalf("snapshot %q missing %q", got, want)
		}
	}
}

func TestRunStatusScreenKeepsColorSGRButClearsLineOnEL(t *testing.T) {
	s := newRunStatusScreen(200)
	s.feed([]byte("\x1b[32mkeep-me\x1b[0m\n"))
	s.feed([]byte("stale\x1b[2Kfresh"))
	got := s.snapshot()
	if !strings.Contains(got, "\x1b[32mkeep-me") {
		t.Fatalf("color SGR should stay for the window parser: %q", got)
	}
	if strings.Contains(got, "stale") {
		t.Fatalf("CSI EL should clear the current line: %q", got)
	}
	if !strings.Contains(got, "fresh") {
		t.Fatalf("text after EL missing: %q", got)
	}
}

func TestRunStatusFeedFlushEmitsOnceAfterManyWrites(t *testing.T) {
	var got []string
	f := newRunStatusFeed(func(text string) {
		got = append(got, text)
	})
	for i := 0; i < 1000; i++ {
		_, _ = fmt.Fprintf(f, "\rBuilding %d/6530", i)
	}
	f.Flush()
	if len(got) != 1 {
		t.Fatalf("flush count = %d, want 1", len(got))
	}
	if !strings.Contains(got[0], "999/6530") {
		t.Fatalf("flushed snapshot = %q", got[0])
	}
}

func TestRunStatusFeedIgnoresWritesAfterClose(t *testing.T) {
	var n int
	f := newRunStatusFeed(func(string) { n++ })
	f.Close()
	_, _ = f.Write([]byte("still running\n"))
	f.Flush()
	if n != 0 {
		t.Fatalf("emits after close = %d, want 0", n)
	}
}

func TestMarkRunStatusUserClosedStopsFeed(t *testing.T) {
	id := 99
	feed := newRunStatusFeed(func(string) {
		t.Fatal("closed feed should not emit")
	})
	runStatusFeeds.Store(id, feed)
	t.Cleanup(func() {
		runStatusFeeds.Delete(id)
		runStatusUserClosed.Delete(id)
	})

	markRunStatusUserClosed(id)
	_, _ = feed.Write([]byte("x\n"))
	feed.Flush()
	if !takeRunStatusUserClosed(id) {
		t.Fatal("user-closed flag should be set while the command is still running")
	}
}

func TestMarkRunStatusUserClosedIgnoredAfterFeedGone(t *testing.T) {
	markRunStatusUserClosed(12345)
	if takeRunStatusUserClosed(12345) {
		t.Fatal("closing a finished window must not look like a user abort")
	}
}
