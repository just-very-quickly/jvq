package shop

import (
	"os"
	"testing"
)

func TestA(t *testing.T) {
	if Total(1, 2) != 3 {
		t.Fatal("Total(1, 2) != 3")
	}
}

var attempts int

func TestFlaky(t *testing.T) {
	attempts++
	switch os.Getenv("JVQ_DEMO") {
	case "flaky":
		if attempts == 1 {
			t.Fatal("failed on the first attempt, as planted")
		}
	case "crash":
		panic("crashed, as planted")
	}
}

func TestB(t *testing.T) {
	if Total() != 0 {
		t.Fatal("Total() != 0")
	}
}
