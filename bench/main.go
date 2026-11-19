// Command bench is the load generator for bench/run.sh.
//
//	bench [flags] setup   create (or reset) the BENCH_IN / BENCH_OUT streams
//	bench [flags] run     send load, measure what arrives on bench.out
//
// -mode nats (default) publishes to bench.in for the router; -mode http POSTs
// to the gateway. Either way the rule publishes to bench.out, where this
// program listens.
//
// Latency is "receive time minus the `sent` field the message was sent
// with", so the rule must carry `seq` and `sent` through (see bench/rules/).
// Generator and router share a clock because they run on the same host.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	inSubject  = "bench.in"
	outSubject = "bench.out"
)

type options struct {
	mode    string
	httpURL string
	n       int
	rate    int
	pad     int
	conc    int
}

func main() {
	var o options
	url := flag.String("url", nats.DefaultURL, "NATS server URL")
	flag.StringVar(&o.mode, "mode", "nats", "what to load: nats (router) or http (gateway)")
	flag.StringVar(&o.httpURL, "http-url", "http://127.0.0.1:8099/bench", "gateway endpoint for -mode http")
	flag.IntVar(&o.n, "n", 100_000, "messages to send")
	flag.IntVar(&o.rate, "rate", 0, "messages/sec to send (0 = as fast as possible)")
	flag.IntVar(&o.pad, "pad", 0, "extra bytes of padding per message")
	flag.IntVar(&o.conc, "c", 64, "concurrent HTTP clients in -mode http")
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
		err = run(nc, js, o)
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

// sender delivers one message body to the system under test.
type sender func(body []byte) error

func natsSender(js jetstream.JetStream) sender {
	return func(body []byte) error {
		for {
			_, err := js.PublishAsync(inSubject, body)
			if !errors.Is(err, jetstream.ErrTooManyStalledMsgs) {
				return err
			}
			// The server is acking slower than we publish (it is busy serving
			// the router too). Back off until the window drains, then retry.
			select {
			case <-js.PublishAsyncComplete():
			case <-time.After(30 * time.Second):
				return errors.New("publish window never drained")
			}
		}
	}
}

func httpSender(url string, conc int) sender {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: conc},
	}
	return func(body []byte) error {
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reused
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

type message struct {
	Seq  int64 `json:"seq"`
	Sent int64 `json:"sent"`
}

func run(nc *nats.Conn, js jetstream.JetStream, o options) error {
	send, conc := natsSender(js), 1 // PublishAsync never blocks, one sender is enough
	if o.mode == "http" {
		send, conc = httpSender(o.httpURL, o.conc), o.conc
	} else if o.mode != "nats" {
		return fmt.Errorf("unknown -mode %q", o.mode)
	}

	// latencies[seq] is filled once per seq; a redelivered duplicate is ignored.
	latencies := make([]time.Duration, o.n)
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
		if msg.Seq >= int64(o.n) || latencies[msg.Seq] != 0 {
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

	if err := warmup(send, warm); err != nil {
		return err
	}

	padding := strings.Repeat("x", o.pad)
	body := func(seq int64) []byte {
		return fmt.Appendf(nil, `{"seq":%d,"sent":%d,"device":{"id":"dev-42","temp":31.5},"pad":%q}`,
			seq, time.Now().UnixNano(), padding)
	}

	// The pacer hands out sequence numbers; `conc` senders deliver them. A send
	// that fails is counted as rejected (e.g. a 503 from a full gateway queue)
	// rather than ending the run, since rejection under load is a result.
	var rejected atomic.Int64
	var firstErr atomic.Value
	seqs := make(chan int64, conc)
	var wg sync.WaitGroup
	for range conc {
		wg.Go(func() {
			for seq := range seqs {
				if err := send(body(seq)); err != nil {
					rejected.Add(1)
					firstErr.CompareAndSwap(nil, err.Error())
				}
			}
		})
	}

	fmt.Printf("sending %d messages via %s (rate: %s, pad: %d bytes)\n", o.n, o.mode, rateLabel(o.rate), o.pad)
	start := time.Now()
	for i := range o.n {
		if o.rate > 0 {
			// Pace against the schedule, not the previous send, so a slow
			// send is caught up instead of drifting the rate down.
			if wait := time.Until(start.Add(time.Duration(i) * time.Second / time.Duration(o.rate))); wait > 0 {
				time.Sleep(wait)
			}
		}
		seqs <- int64(i)
	}
	close(seqs)
	wg.Wait()
	if o.mode == "nats" {
		select {
		case <-js.PublishAsyncComplete():
		case <-time.After(30 * time.Second):
			return errors.New("timed out waiting for publish acks")
		}
	}
	sendDur := time.Since(start)

	// Wait until everything accepted arrives or nothing new has for 5s.
	want := int64(o.n) - rejected.Load()
	last, lastChange := int64(-1), time.Now()
	for received.Load() < want && time.Since(lastChange) < 5*time.Second {
		if r := received.Load(); r != last {
			last, lastChange = r, time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Println()
	fmt.Printf("sent        %d in %s (%.0f msg/s offered)\n", o.n, sendDur.Round(time.Millisecond), float64(o.n)/sendDur.Seconds())
	if r := rejected.Load(); r > 0 {
		fmt.Printf("rejected    %d (first error: %v)\n", r, firstErr.Load())
	}
	report(latencies, received.Load(), want, time.Unix(0, lastRecv.Load()).Sub(start))
	return nil
}

// warmup sends seq=-1 messages until one comes back, so the run does not
// start before the router's consumers (or the gateway's listener) exist.
func warmup(send sender, warm <-chan struct{}) error {
	deadline := time.After(30 * time.Second)
	for {
		// Errors are expected while the target starts up; just try again.
		_ = send(fmt.Appendf(nil, `{"seq":-1,"sent":%d,"device":{"id":"warmup","temp":99}}`, time.Now().UnixNano()))
		select {
		case <-warm:
			return nil
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			return errors.New("target never answered a warmup message within 30s")
		}
	}
}

func report(latencies []time.Duration, received, accepted int64, total time.Duration) {
	got := make([]time.Duration, 0, received)
	for _, l := range latencies {
		if l != 0 {
			got = append(got, l)
		}
	}
	slices.Sort(got)

	fmt.Printf("received    %d of %d accepted (missing %d)\n", received, accepted, accepted-received)
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
