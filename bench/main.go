// Command bench is the load generator for bench/run.sh.
//
//	bench [flags] setup   create (or reset) the BENCH_IN / BENCH_OUT streams
//	bench [flags] run     publish to bench.in, measure what arrives on bench.out
//
// Latency is "receive time minus the `sent` field the message was published
// with", so the router's rule must carry `seq` and `sent` through (see
// bench/rules/bench.yaml). Generator and router share a clock because they
// run on the same host.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	inSubject  = "bench.in"
	outSubject = "bench.out"
)

func main() {
	url := flag.String("url", nats.DefaultURL, "NATS server URL")
	n := flag.Int("n", 100_000, "messages to publish")
	rate := flag.Int("rate", 0, "messages/sec to publish (0 = as fast as possible)")
	pad := flag.Int("pad", 0, "extra bytes of padding per message")
	flag.Parse()

	nc, err := nats.Connect(*url)
	if err != nil {
		fail("connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		fail("jetstream: %v", err)
	}

	switch flag.Arg(0) {
	case "setup":
		err = setup(js)
	case "run":
		err = run(nc, js, *n, *rate, *pad)
	default:
		fail("usage: bench [flags] setup|run")
	}
	if err != nil {
		fail("%s: %v", flag.Arg(0), err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bench: "+format+"\n", args...)
	os.Exit(1)
}

// setup creates both streams and purges them, so every run starts empty.
func setup(js jetstream.JetStream) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for name, subject := range map[string]string{"BENCH_IN": inSubject, "BENCH_OUT": outSubject} {
		s, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     name,
			Subjects: []string{subject},
			Storage:  jetstream.FileStorage,
		})
		if err != nil {
			return fmt.Errorf("stream %s: %w", name, err)
		}
		if err := s.Purge(ctx); err != nil {
			return fmt.Errorf("purge %s: %w", name, err)
		}
	}
	return nil
}

type message struct {
	Seq  int64 `json:"seq"`
	Sent int64 `json:"sent"`
}

func run(nc *nats.Conn, js jetstream.JetStream, n, rate, pad int) error {
	// latencies[seq] is filled once per seq; a redelivered duplicate is ignored.
	latencies := make([]time.Duration, n)
	var received, lastRecv atomic.Int64
	var badOnce atomic.Bool
	warm := make(chan struct{}, 1)

	sub, err := nc.Subscribe(outSubject, func(m *nats.Msg) {
		now := time.Now().UnixNano()
		var msg message
		if err := json.Unmarshal(m.Data, &msg); err != nil {
			if !badOnce.Swap(true) {
				fmt.Fprintf(os.Stderr, "bench: %s payload is not JSON with seq/sent (%v):\n%s\n", outSubject, err, m.Data)
			}
			return
		}
		if msg.Seq < 0 { // warmup message
			select {
			case warm <- struct{}{}:
			default:
			}
			return
		}
		if msg.Seq >= int64(n) || latencies[msg.Seq] != 0 {
			return
		}
		latencies[msg.Seq] = time.Duration(now - msg.Sent)
		received.Add(1)
		lastRecv.Store(now)
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	// Never drop on our side: a slow-consumer drop would read as router loss.
	_ = sub.SetPendingLimits(-1, -1)

	if err := warmup(js, warm); err != nil {
		return err
	}

	padding := strings.Repeat("x", pad)
	publish := func(seq int64) error {
		data := fmt.Sprintf(`{"seq":%d,"sent":%d,"device":{"id":"dev-42","temp":31.5},"pad":%q}`,
			seq, time.Now().UnixNano(), padding)
		_, err := js.PublishAsync(inSubject, []byte(data))
		return err
	}

	fmt.Printf("publishing %d messages to %s (rate: %s, pad: %d bytes)\n", n, inSubject, rateLabel(rate), pad)
	start := time.Now()
	for i := range n {
		if rate > 0 {
			// Pace against the schedule, not the previous send, so a slow
			// publish is caught up instead of drifting the rate down.
			if wait := time.Until(start.Add(time.Duration(i) * time.Second / time.Duration(rate))); wait > 0 {
				time.Sleep(wait)
			}
		}
		if err := publish(int64(i)); err != nil {
			return fmt.Errorf("publish %d: %w", i, err)
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(30 * time.Second):
		return errors.New("timed out waiting for publish acks")
	}
	pubDur := time.Since(start)

	// Wait until everything arrives or nothing new has arrived for 5s.
	last, lastChange := int64(-1), time.Now()
	for received.Load() < int64(n) && time.Since(lastChange) < 5*time.Second {
		if r := received.Load(); r != last {
			last, lastChange = r, time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}

	report(latencies, received.Load(), n, pubDur, time.Unix(0, lastRecv.Load()).Sub(start))
	return nil
}

// warmup sends seq=-1 messages until one comes back, so the run does not
// start before the router's consumers exist.
func warmup(js jetstream.JetStream, warm <-chan struct{}) error {
	deadline := time.After(30 * time.Second)
	for {
		data := fmt.Sprintf(`{"seq":-1,"sent":%d,"device":{"id":"warmup","temp":99}}`, time.Now().UnixNano())
		if _, err := js.PublishAsync(inSubject, []byte(data)); err != nil {
			return fmt.Errorf("warmup publish: %w", err)
		}
		select {
		case <-warm:
			return nil
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			return errors.New("router never answered a warmup message within 30s")
		}
	}
}

func report(latencies []time.Duration, received int64, n int, pubDur, total time.Duration) {
	got := make([]time.Duration, 0, received)
	for _, l := range latencies {
		if l != 0 {
			got = append(got, l)
		}
	}
	slices.Sort(got)

	fmt.Println()
	fmt.Printf("sent        %d in %s (%.0f msg/s offered)\n", n, pubDur.Round(time.Millisecond), float64(n)/pubDur.Seconds())
	fmt.Printf("received    %d (missing %d)\n", received, int64(n)-received)
	if len(got) == 0 {
		fmt.Println("no messages came back; check the router log")
		return
	}
	fmt.Printf("throughput  %.0f msg/s (first send to last receive: %s)\n", float64(received)/total.Seconds(), total.Round(time.Millisecond))
	fmt.Printf("latency     p50 %s  p90 %s  p99 %s  max %s\n",
		pct(got, 50), pct(got, 90), pct(got, 99), got[len(got)-1].Round(time.Microsecond))
}

func pct(sorted []time.Duration, p int) time.Duration {
	return sorted[(len(sorted)-1)*p/100].Round(time.Microsecond)
}

func rateLabel(rate int) string {
	if rate == 0 {
		return "max"
	}
	return fmt.Sprintf("%d/s", rate)
}
