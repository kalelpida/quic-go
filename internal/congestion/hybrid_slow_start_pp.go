package congestion

import (
	"time"
	"math"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
)

// Note(pwestin): the magic clamping numbers come from the original code in
// tcp_cubic.c.
const hybridStartppLowWindow = protocol.ByteCount(16)

// Number of delay samples for detecting the increase of delay.
//N_RTT_SAMPLE
const hybridStartppNRttSample = uint32(8)

// cwnd increase limit, recommended value in RFC3465
const hybridStartppL = 2

// Limited Slow Start from RFC 3742, advised value 0.25 < 0.5
const hybridStartppLSS_DIVISOR = 0.25  

// The original paper specifies 2 and 8ms, but those have changed over time.
//MIN et MAX RTT_THRESH
const (
	hybridStartppDelayMinThresholdUs = 4000
	hybridStartppDelayMaxThresholdUs = 16000
)

// HybridSlowStartpp implements the TCP hybrid slow start algorithm
type HybridSlowStartpp struct {
	endPacketNumber      protocol.PacketNumber
	lastSentPacketNumber protocol.PacketNumber
	started              bool
	currentRoundMinRTT   time.Duration
	lastRoundMinRTT		 time.Duration
	rttSampleCount       uint32
	inLSS				 bool
}

// StartReceiveRound is called for the start of each receive round (burst) in the slow start phase.
func (s *HybridSlowStartpp) StartReceiveRound(lastSent protocol.PacketNumber) {
	s.endPacketNumber = lastSent
	s.lastRoundMinRTT = s.currentRoundMinRTT
	s.currentRoundMinRTT = 0
	s.rttSampleCount = 0
	s.inLSS = false
	s.started = true
}

func (s *HybridSlowStartpp) IsInLSS() bool {
	return s.inLSS
}
// IsEndOfRound returns true if this ack is the last packet number of our current slow start round.
func (s *HybridSlowStartpp) IsEndOfRound(ack protocol.PacketNumber) bool {
	return s.endPacketNumber < ack
}

func (s *HybridSlowStartpp) UpdateCwndHystartpp(ackedBytes protocol.ByteCount, cwnd protocol.ByteCount, maxDatagramSize protocol.ByteCount, ssthresh protocol.ByteCount, predictedCAcwnd protocol.ByteCount) protocol.ByteCount {
	// maxDatagramSize : SMSS, ackedBytes:N
	if s.inLSS {
		K := float64(cwnd) / (hybridStartppLSS_DIVISOR * float64(ssthresh))
		return protocol.ByteCount(math.Max(float64(cwnd) + (math.Min(float64(ackedBytes), hybridStartppL*float64(maxDatagramSize)) / K), float64(predictedCAcwnd)))
	} else {
		return cwnd + protocol.ByteCount(math.Min(float64(ackedBytes), hybridStartppL*float64(maxDatagramSize)))
	}
}
// ShouldExitSlowStart should be called on every new ack frame, since a new
// RTT measurement can be made then.
// rtt: the RTT for this ack packet.
// minRTT: is the lowest delay (RTT) we have seen during the session.
// congestionWindow: the congestion window in packets.

func (s *HybridSlowStartpp) ShouldExitSlowStart(latestRTT time.Duration, minRTT time.Duration, congestionWindow protocol.ByteCount, maxSegmentSize protocol.ByteCount) bool {
	if !s.started {
		// Time to start the hybrid slow start.
		s.StartReceiveRound(s.lastSentPacketNumber)
	}
	//keep track of minimum observed RTT, 
	s.currentRoundMinRTT = utils.MinDuration(s.currentRoundMinRTT, latestRTT)
	s.rttSampleCount += 1

	if (congestionWindow >= (hybridStartppLowWindow * maxSegmentSize) && s.rttSampleCount >= hybridStartppNRttSample) {
		rttThresh := utils.MaxDuration(hybridStartppDelayMaxThresholdUs, utils.MinDuration(s.lastRoundMinRTT / 8, hybridStartppDelayMaxThresholdUs))
		if (s.currentRoundMinRTT >= (s.lastRoundMinRTT + rttThresh)){
			s.inLSS = true
			//ssthresh = cwnd
			return true
		}
	}
	return false
}

// OnPacketSent is called when a packet was sent
func (s *HybridSlowStartpp) OnPacketSent(packetNumber protocol.PacketNumber) {
	s.lastSentPacketNumber = packetNumber
}

// OnPacketAcked gets invoked after ShouldExitSlowStart, so it's best to end
// the round when the final packet of the burst is received and start it on
// the next incoming ack.
func (s *HybridSlowStartpp) OnPacketAcked(ackedPacketNumber protocol.PacketNumber) {
	if s.IsEndOfRound(ackedPacketNumber) {
		s.started = false
	}
}

// Restart the slow start phase
func (s *HybridSlowStartpp) Restart() {
	s.started = false
}

