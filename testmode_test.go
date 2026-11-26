package main

import (
	"testing"
	"time"
)

func TestTestColorSequence(t *testing.T) {
	e := NewEngine(defaultConfig(), nil)
	now := time.Now()
	if _, ok := e.testColor(now); ok {
		t.Fatal("test active before StartTest")
	}
	e.StartTest()
	start := time.Unix(0, e.testFrom.Load())
	for i, want := range testColors {
		got, ok := e.testColor(start.Add(time.Duration(i)*testStep + time.Millisecond))
		if !ok || got != want {
			t.Fatalf("step %d = %+v %v, want %+v", i, got, ok, want)
		}
	}
	if _, ok := e.testColor(start.Add(time.Duration(len(testColors)) * testStep)); ok {
		t.Fatal("test still active after sequence")
	}
	if e.testFrom.Load() != 0 {
		t.Fatal("test not cleared")
	}
	th := solidThumb(rgb{255, 0, 0})
	if c := regionColor(th, 0, th.W, false); c != (rgb{255, 0, 0}) {
		t.Fatalf("solid thumb = %+v", c)
	}
}
