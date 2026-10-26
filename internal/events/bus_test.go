// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"sync"
	"testing"
)

// drain returns what is already waiting on ch without blocking.
func drain(ch <-chan Msg) []Msg {
	var out []Msg
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// A subscriber gets the messages of the topics it asked for, with their
// topic and data, and "*" gets every topic.
func TestBusDeliversByTopic(t *testing.T) {
	b := NewBus()
	jobs, c1 := b.Subscribe("jobs")
	defer c1()
	both, c2 := b.Subscribe("jobs", "events")
	defer c2()
	all, c3 := b.Subscribe("*")
	defer c3()
	none, c4 := b.Subscribe()
	defer c4()
	b.Publish("jobs", 1)
	b.Publish("events", "e")
	b.Publish("health", true)
	if got := drain(jobs); len(got) != 1 || got[0].Topic != "jobs" || got[0].Data != 1 {
		t.Errorf("jobs subscriber got %+v", got)
	}
	if got := drain(both); len(got) != 2 || got[0].Topic != "jobs" || got[1].Topic != "events" || got[1].Data != "e" {
		t.Errorf("jobs and events subscriber got %+v", got)
	}
	if got := drain(all); len(got) != 3 || got[2].Topic != "health" || got[2].Data != true {
		t.Errorf("wildcard subscriber got %+v", got)
	}
	if got := drain(none); len(got) != 0 {
		t.Errorf("subscriber without topics got %+v", got)
	}
}

// Cancelling closes the channel and stops deliveries to it; cancelling twice
// and publishing afterwards are harmless, and other subscribers go on.
func TestBusCancelClosesTheChannel(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe("jobs")
	other, cancelOther := b.Subscribe("jobs")
	defer cancelOther()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("the channel is open after cancel")
	}
	cancel()
	b.Publish("jobs", "after")
	if got := drain(other); len(got) != 1 || got[0].Data != "after" {
		t.Fatalf("the other subscriber got %+v", got)
	}
}

// Publish never blocks: a subscriber that does not read keeps the first 64
// messages and misses the rest, while one that keeps up gets them all.
func TestBusSlowSubscriberMissesUpdates(t *testing.T) {
	b := NewBus()
	slow, c1 := b.Subscribe("jobs")
	defer c1()
	fast, c2 := b.Subscribe("jobs")
	defer c2()
	var kept []Msg
	for i := 0; i < 100; i++ {
		b.Publish("jobs", i)
		kept = append(kept, drain(fast)...)
	}
	if len(kept) != 100 {
		t.Fatalf("the fast subscriber got %d of 100", len(kept))
	}
	got := drain(slow)
	if len(got) != 64 || got[0].Data != 0 || got[63].Data != 63 {
		t.Fatalf("the slow subscriber got %d messages, want the first 64", len(got))
	}
}

// Subscribing, publishing and cancelling from many goroutines at once is
// safe (run with -race).
func TestBusConcurrentUse(t *testing.T) {
	b := NewBus()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe("jobs")
			for j := 0; j < 50; j++ {
				drain(ch)
			}
			cancel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b.Publish("jobs", j)
			}
		}()
	}
	wg.Wait()
}
