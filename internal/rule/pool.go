//file: internal/rule/pool.go

package rule

import (
    "sync"
    "sync/atomic"

    "rule-router/internal/logger"
)

type ProcessingMessage struct {
    Topic   string
    Payload []byte
    Values  map[string]interface{}
    Rules   []*Rule
    Actions []*Action
}

type MessagePool struct {
    pool   sync.Pool
    logger *logger.Logger
    stats  poolStats
}

type poolStats struct {
    gets   uint64
    puts   uint64
    misses uint64
}

func NewMessagePool(log *logger.Logger) *MessagePool {
    mp := &MessagePool{
        logger: log,
    }
    
    mp.pool = sync.Pool{
        New: func() interface{} {
            // Update stats first
            atomic.AddUint64(&mp.stats.misses, 1)
            // Log the miss
            if mp.logger != nil {
                mp.logger.Debug("creating new message object due to pool miss")
            }
            // Create new message object
            return &ProcessingMessage{
                Values:  make(map[string]interface{}, 10),
                Rules:   make([]*Rule, 0, 5),
                Actions: make([]*Action, 0, 5),
            }
        },
    }
    return mp
}

func (p *MessagePool) Get() *ProcessingMessage {
    atomic.AddUint64(&p.stats.gets, 1)
    msg := p.pool.Get().(*ProcessingMessage)
    
    p.logger.Debug("retrieved message from pool",
        "totalGets", atomic.LoadUint64(&p.stats.gets),
        "poolMisses", atomic.LoadUint64(&p.stats.misses))
    
    return msg
}

func (p *MessagePool) Put(msg *ProcessingMessage) {
    if msg == nil {
        p.logger.Error("attempted to return nil message to pool")
        return
    }

    atomic.AddUint64(&p.stats.puts, 1)

    p.logger.Debug("returning message to pool",
        "topic", msg.Topic,
        "valuesLen", len(msg.Values),
        "rulesLen", len(msg.Rules),
        "actionsLen", len(msg.Actions))

    // Clear message data before returning to pool
    msg.Topic = ""
    msg.Payload = msg.Payload[:0]
    for k := range msg.Values {
        delete(msg.Values, k)
    }
    msg.Rules = msg.Rules[:0]
    msg.Actions = msg.Actions[:0]

    p.pool.Put(msg)
}

type ProcessingResult struct {
    Message *ProcessingMessage
    Error   error
}

type ResultPool struct {
    pool   sync.Pool
    logger *logger.Logger
    stats  poolStats
}

func NewResultPool(log *logger.Logger) *ResultPool {
    rp := &ResultPool{
        logger: log,
    }
    
    rp.pool = sync.Pool{
        New: func() interface{} {
            // Update stats first
            atomic.AddUint64(&rp.stats.misses, 1)
            // Log the miss
            if rp.logger != nil {
                rp.logger.Debug("creating new result object due to pool miss")
            }
            // Create new result object
            return &ProcessingResult{}
        },
    }
    return rp
}

func (p *ResultPool) Get() *ProcessingResult {
    atomic.AddUint64(&p.stats.gets, 1)
    result := p.pool.Get().(*ProcessingResult)

    p.logger.Debug("retrieved result from pool",
        "totalGets", atomic.LoadUint64(&p.stats.gets),
        "poolMisses", atomic.LoadUint64(&p.stats.misses))

    return result
}

func (p *ResultPool) Put(result *ProcessingResult) {
    if result == nil {
        p.logger.Error("attempted to return nil result to pool")
        return
    }

    atomic.AddUint64(&p.stats.puts, 1)

    p.logger.Debug("returning result to pool",
        "hadError", result.Error != nil)

    result.Message = nil
    result.Error = nil
    p.pool.Put(result)
}

// GetPoolStats returns current statistics for either pool type
func getPoolStats(stats *poolStats) map[string]uint64 {
    return map[string]uint64{
        "gets":   atomic.LoadUint64(&stats.gets),
        "puts":   atomic.LoadUint64(&stats.puts),
        "misses": atomic.LoadUint64(&stats.misses),
    }
}
