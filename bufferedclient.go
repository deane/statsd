package statsd

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/quipo/statsd/event"
)

// request to close the buffered statsd collector
type closeRequest struct {
	reply chan error
}

// StatsdBuffer is a client library to aggregate events in memory before
// flushing aggregates to StatsD, useful if the frequency of events is extremely high
// and sampling is not desirable
type StatsdBuffer struct {
	statsd        *StatsdClient
	flushInterval time.Duration
	eventChannel  chan event.Event
	events        map[string]event.Event
	closeChannel  chan closeRequest
	Logger        *log.Logger
}

// NewStatsdBuffer Factory
func NewStatsdBuffer(interval time.Duration, client *StatsdClient) *StatsdBuffer {
	sb := &StatsdBuffer{
		flushInterval: interval,
		statsd:        client,
		eventChannel:  make(chan event.Event, 100),
		events:        make(map[string]event.Event, 0),
		closeChannel:  make(chan closeRequest, 0),
		Logger:        log.New(os.Stdout, "[BufferedStatsdClient] ", log.Ldate|log.Ltime),
	}
	go sb.collector()
	return sb
}

// Incr - Increment a counter metric. Often used to note a particular event
func (sb *StatsdBuffer) Incr(stat string, count int64) error {
	if 0 != count {
		sb.eventChannel <- &event.Increment{Name: stat, Value: count}
	}
	return nil
}

// Decr - Decrement a counter metric. Often used to note a particular event
func (sb *StatsdBuffer) Decr(stat string, count int64) error {
	if 0 != count {
		sb.eventChannel <- &event.Increment{Name: stat, Value: -count}
	}
	return nil
}

// Timing - Track a duration event
func (sb *StatsdBuffer) Timing(stat string, delta int64) error {
	sb.eventChannel <- event.NewTiming(stat, delta)
	return nil
}

// Gauge - Gauges are a constant data type. They are not subject to averaging,
// and they don’t change unless you change them. That is, once you set a gauge value,
// it will be a flat line on the graph until you change it again
func (sb *StatsdBuffer) Gauge(stat string, value int64) error {
	sb.eventChannel <- &event.Gauge{Name: stat, Value: value}
	return nil
}

// Absolute - Send absolute-valued metric (not averaged/aggregated)
func (sb *StatsdBuffer) Absolute(stat string, value int64) error {
	sb.eventChannel <- &event.Absolute{Name: stat, Values: []int64{value}}
	return nil
}

// Total - Send a metric that is continously increasing, e.g. read operations since boot
func (sb *StatsdBuffer) Total(stat string, value int64) error {
	sb.eventChannel <- &event.Total{Name: stat, Value: value}
	return nil
}

// handle flushes and updates in one single thread (instead of locking the events map)
func (sb *StatsdBuffer) collector() {
	// on a panic event, flush all the pending stats before panicking
	defer func(sb *StatsdBuffer) {
		if r := recover(); r != nil {
			sb.Logger.Println("Caught panic, flushing stats before throwing the panic again")
			sb.flush()
			panic(r)
		}
	}(sb)

	ticker := time.NewTicker(sb.flushInterval)

	for {
		select {
		case <-ticker.C:
			//fmt.Println("Flushing stats")
			sb.flush()
		case e := <-sb.eventChannel:
			//fmt.Println("Received ", e.String())
			if e2, ok := sb.events[e.Key()]; ok {
				//fmt.Println("Updating existing event")
				e2.Update(e)
				sb.events[e.Key()] = e2
			} else {
				//fmt.Println("Adding new event")
				sb.events[e.Key()] = e
			}
		case c := <-sb.closeChannel:
			sb.Logger.Println("Asked to terminate. Flushing stats before returning.")
			c.reply <- sb.flush()
			break
		}
	}
}

// Close sends a close event to the collector asking to stop & flush pending stats
// and closes the statsd client
func (sb *StatsdBuffer) Close() (err error) {
	// 1. send a close event to the collector
	req := closeRequest{reply: make(chan error, 0)}
	sb.closeChannel <- req
	// 2. wait for the collector to drain the queue and respond
	err = <-req.reply
	// 3. close the statsd client
	err2 := sb.statsd.Close()
	if err != nil {
		return err
	}
	return err2
}

// send the events to StatsD and reset them.
// This function is NOT thread-safe, so it must only be invoked synchronously
// from within the collector() goroutine
func (sb *StatsdBuffer) flush() (err error) {
	var wg sync.WaitGroup
	wg.Add(len(sb.events))
	for k, v := range sb.events {
		go func(e event.Event) {
			err := sb.statsd.SendEvent(e)
			if nil != err {
				fmt.Println(err)
			}
			wg.Done()
		}(v)
		//fmt.Println("Sent", v.String())
		delete(sb.events, k)
	}
	wg.Wait()
	return nil
}
