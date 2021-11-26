package congestion

import (
	"fmt"
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
	"github.com/lucas-clemente/quic-go/logging"
)

const (
	// maxDatagramSize is the default maximum packet size used in the Linux TCP implementation.
	// Used in QUIC for congestion window computations in bytes.
	initialMaxDatagramSize     = protocol.ByteCount(protocol.InitialPacketSizeIPv4)
	maxBurstPackets            = 3
	renoBeta                   = 0.7 // Reno backoff factor.
	minCongestionWindowPackets = 2
	initialCongestionWindow    = 32
)

type cubicSender struct {
	hybridSlowStart HybridSlowStart
	hybridSlowStartpp HybridSlowStartpp
	rttStats        *utils.RTTStats
	cubic           *Cubic
	pacer           *pacer
	clock           Clock

	chosenStartAlgo utils.StartAlgo
	chosenCongestionAlgo utils.CongestionAlgo

	// Track the largest packet that has been sent.
	largestSentPacketNumber protocol.PacketNumber

	// Track the largest packet that has been acked.
	largestAckedPacketNumber protocol.PacketNumber

	// Track the largest packet number outstanding when a CWND cutback occurs.
	largestSentAtLastCutback protocol.PacketNumber

	// Whether the last loss event caused us to exit slowstart.
	// Used for stats collection of slowstartPacketsLost
	lastCutbackExitedSlowstart bool

	// Congestion window in packets.
	congestionWindow protocol.ByteCount

	// Slow start congestion window in bytes, aka ssthresh.
	slowStartThreshold protocol.ByteCount

	// ACK counter for the Reno implementation.
	numAckedPackets uint64

	initialCongestionWindow    protocol.ByteCount
	initialMaxCongestionWindow protocol.ByteCount

	maxDatagramSize protocol.ByteCount

	lastState logging.CongestionState
	tracer    logging.ConnectionTracer
}

var (
	_ SendAlgorithm               = &cubicSender{}
	_ SendAlgorithmWithDebugInfos = &cubicSender{}
)

// NewCubicSender makes a new cubic sender
func NewCubicSender(
	clock Clock,
	rttStats *utils.RTTStats,
	initialMaxDatagramSize protocol.ByteCount,
	chosenStartAlgo utils.StartAlgo,
	chosenCongestionAlgo utils.CongestionAlgo,
	tracer logging.ConnectionTracer,
) *cubicSender {
	return newCubicSender(
		clock,
		rttStats,
		chosenStartAlgo,
		chosenCongestionAlgo,
		initialMaxDatagramSize,
		initialCongestionWindow*initialMaxDatagramSize,
		protocol.MaxCongestionWindowPackets*initialMaxDatagramSize,
		tracer,
	)
}

func newCubicSender(
	clock Clock,
	rttStats *utils.RTTStats,
	chosenStartAlgo utils.StartAlgo,
	chosenCongestionAlgo utils.CongestionAlgo,
	initialMaxDatagramSize,
	initialCongestionWindow,
	initialMaxCongestionWindow protocol.ByteCount,
	tracer logging.ConnectionTracer,
) *cubicSender {
	c := &cubicSender{
		rttStats:                   rttStats,
		largestSentPacketNumber:    protocol.InvalidPacketNumber,
		largestAckedPacketNumber:   protocol.InvalidPacketNumber,
		largestSentAtLastCutback:   protocol.InvalidPacketNumber,
		initialCongestionWindow:    initialCongestionWindow,
		initialMaxCongestionWindow: initialMaxCongestionWindow,
		congestionWindow:           initialCongestionWindow,
		slowStartThreshold:         protocol.MaxByteCount,
		cubic:                      NewCubic(clock),
		clock:                      clock,
		chosenStartAlgo:			chosenStartAlgo,
		chosenCongestionAlgo:		chosenCongestionAlgo,
		tracer:                     tracer,
		maxDatagramSize:            initialMaxDatagramSize,
	}
	c.pacer = newPacer(c.BandwidthEstimate)
	if c.tracer != nil {
		c.lastState = logging.CongestionStateSlowStart
		c.tracer.UpdatedCongestionState(logging.CongestionStateSlowStart)
	}
	return c
}

// TimeUntilSend returns when the next packet should be sent.
func (c *cubicSender) TimeUntilSend(_ protocol.ByteCount) time.Time {
	return c.pacer.TimeUntilSend()
}

func (c *cubicSender) HasPacingBudget() bool {
	return c.pacer.Budget(c.clock.Now()) >= c.maxDatagramSize
}

func (c *cubicSender) maxCongestionWindow() protocol.ByteCount {
	return c.maxDatagramSize * protocol.MaxCongestionWindowPackets
}

func (c *cubicSender) minCongestionWindow() protocol.ByteCount {
	return c.maxDatagramSize * minCongestionWindowPackets
}

func (c *cubicSender) OnPacketSent(
	sentTime time.Time,
	_ protocol.ByteCount,
	packetNumber protocol.PacketNumber,
	bytes protocol.ByteCount,
	isRetransmittable bool,
) {
	c.pacer.SentPacket(sentTime, bytes)
	if !isRetransmittable {
		return
	}
	c.largestSentPacketNumber = packetNumber
	switch(c.chosenStartAlgo){
	case utils.ChooseHystart:
		c.hybridSlowStart.OnPacketSent(packetNumber)
	case utils.ChooseHystartpp:
		c.hybridSlowStartpp.OnPacketSent(packetNumber)
	}
}

func (c *cubicSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	return bytesInFlight < c.GetCongestionWindow()
}

func (c *cubicSender) InRecovery() bool {
	return c.largestAckedPacketNumber != protocol.InvalidPacketNumber && c.largestAckedPacketNumber <= c.largestSentAtLastCutback
}

func (c *cubicSender) InSlowStart() bool {
	return c.GetCongestionWindow() < c.slowStartThreshold
}

func (c *cubicSender) InLowSlowStart() bool {
	return c.chosenStartAlgo == utils.ChooseHystartpp && c.hybridSlowStartpp.IsInLSS()
}

func (c *cubicSender) GetCongestionWindow() protocol.ByteCount {
	return c.congestionWindow
}

func (c *cubicSender) MaybeExitSlowStart() {
	if c.InSlowStart(){
		switch c.chosenStartAlgo {
		case utils.ChooseSlowStart:
			// do not exit slow start
			break
		case utils.ChooseHystart:
			if c.hybridSlowStart.ShouldExitSlowStart(c.rttStats.LatestRTT(), c.rttStats.MinRTT(), c.GetCongestionWindow()/c.maxDatagramSize) {
				// exit slow start
				c.slowStartThreshold = c.congestionWindow
				c.maybeTraceStateChange(logging.CongestionStateCongestionAvoidance)
			}
			break
		case utils.ChooseHystartpp:
			if c.hybridSlowStartpp.ShouldExitSlowStart(c.rttStats.LatestRTT(), c.rttStats.MinRTT(), c.GetCongestionWindow()/c.maxDatagramSize){
				c.slowStartThreshold = c.congestionWindow
				c.maybeTraceStateChange(logging.CongestionStateLowSlowStart)
			}
			break
		} 
	}
}

func (c *cubicSender) OnPacketAcked(
	ackedPacketNumber protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	c.largestAckedPacketNumber = utils.MaxPacketNumber(ackedPacketNumber, c.largestAckedPacketNumber)
	if c.InRecovery() {
		return
	}
	c.maybeIncreaseCwnd(ackedPacketNumber, ackedBytes, priorInFlight, eventTime)
	if c.InSlowStart() {
		switch c.chosenStartAlgo {
		case utils.ChooseSlowStart, utils.ChooseHystart:
			c.hybridSlowStart.OnPacketAcked(ackedPacketNumber)
		case utils.ChooseHystartpp:
			c.hybridSlowStartpp.OnPacketAcked(ackedPacketNumber)
		}
	}
}

func (c *cubicSender) OnPacketLost(packetNumber protocol.PacketNumber, lostBytes, priorInFlight protocol.ByteCount) {
	// TCP NewReno (RFC6582) says that once a loss occurs, any losses in packets
	// already sent should be treated as a single loss event, since it's expected.
	if c.InLowSlowStart() {
		//hystart++ should only be used once. After getting in congestion avoidance, we switch to standard Slow Start
		c.hybridSlowStartpp.QuitLSS()
		c.chosenStartAlgo = utils.ChooseSlowStart
		c.maybeTraceStateChange(logging.CongestionStateCongestionAvoidance)
	}
	switch c.chosenCongestionAlgo {
	case utils.ChooseNewReno:
		if packetNumber <= c.largestSentAtLastCutback {
			return
		}
		c.lastCutbackExitedSlowstart = c.InSlowStart()
		c.maybeTraceStateChange(logging.CongestionStateRecovery)

		c.congestionWindow = protocol.ByteCount(float64(c.congestionWindow) * renoBeta)

		if minCwnd := c.minCongestionWindow(); c.congestionWindow < minCwnd {
			c.congestionWindow = minCwnd
		}
		c.slowStartThreshold = c.congestionWindow
		c.largestSentAtLastCutback = c.largestSentPacketNumber
		// reset packet count from congestion avoidance mode. We start
		// counting again when we're out of recovery.
		c.numAckedPackets = 0
		break
	
	case utils.ChooseCubic:
		if packetNumber <= c.largestSentAtLastCutback {
			return
		}
		c.lastCutbackExitedSlowstart = c.InSlowStart()
		c.maybeTraceStateChange(logging.CongestionStateRecovery)

		c.congestionWindow = c.cubic.CongestionWindowAfterPacketLoss(c.congestionWindow)

		if minCwnd := c.minCongestionWindow(); c.congestionWindow < minCwnd {
			c.congestionWindow = minCwnd
		}
		c.slowStartThreshold = c.congestionWindow
		c.largestSentAtLastCutback = c.largestSentPacketNumber
		// reset packet count from congestion avoidance mode. We start
		// counting again when we're out of recovery.
		c.numAckedPackets = 0
		break
	}
}

// Called when we receive an ack. Normal TCP tracks how many packets one ack
// represents, but quic has a separate ack for each packet.
func (c *cubicSender) maybeIncreaseCwnd(
	_ protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	// Do not increase the congestion window unless the sender is close to using
	// the current window.
	if !c.isCwndLimited(priorInFlight) {
		c.cubic.OnApplicationLimited()
		c.maybeTraceStateChange(logging.CongestionStateApplicationLimited)
		return
	}
	if c.congestionWindow >= c.maxCongestionWindow() {
		return
	}
	if c.InSlowStart() {
		c.maybeTraceStateChange(logging.CongestionStateSlowStart)
		// TCP slow start, exponential growth, increase by one for each ACK.
		switch c.chosenStartAlgo{
		case utils.ChooseSlowStart, utils.ChooseHystart:
			c.congestionWindow += c.maxDatagramSize
		case utils.ChooseHystartpp:
			c.congestionWindow = c.hybridSlowStartpp.UpdateCwndHystartppSlowStart(ackedBytes, c.congestionWindow, c.maxDatagramSize)		
		}
	
	} else if c.InLowSlowStart() {
		//RFC recommends to compare hystartpp Cwnd to Congestion Avoidance algorithm computed Cwnd
		c.maybeTraceStateChange(logging.CongestionStateLowSlowStart)
		caWindow := c.congestionWindow
		switch c.chosenCongestionAlgo{
		case utils.ChooseNewReno:
			if c.numAckedPackets >= uint64(c.congestionWindow/c.maxDatagramSize) {
				caWindow = c.congestionWindow + c.maxDatagramSize
			}
			//else: keep caWindow = c.congestionWindow
		case utils.ChooseCubic:
			caWindow = utils.MinByteCount(c.maxCongestionWindow(), c.cubic.CongestionWindowAfterAck(ackedBytes, c.congestionWindow, c.rttStats.MinRTT(), eventTime))
			//fallthrough
		default:
			//cubic window is used 
			caWindow = utils.MinByteCount(c.maxCongestionWindow(), c.cubic.CongestionWindowAfterAck(ackedBytes, c.congestionWindow, c.rttStats.MinRTT(), eventTime))
		}
		c.congestionWindow = c.hybridSlowStartpp.UpdateCwndHystartppLowSlowStart(ackedBytes, c.congestionWindow, c.maxDatagramSize, c.slowStartThreshold, caWindow)		
	
	} else {
		// Congestion avoidance
		c.maybeTraceStateChange(logging.CongestionStateCongestionAvoidance)
		switch c.chosenCongestionAlgo{
		case utils.ChooseNewReno:
			// Classic Reno congestion avoidance.
			c.numAckedPackets++
			if c.numAckedPackets >= uint64(c.congestionWindow/c.maxDatagramSize) {
				c.congestionWindow += c.maxDatagramSize
				c.numAckedPackets = 0
			}	
		case utils.ChooseCubic:
			c.congestionWindow = utils.MinByteCount(c.maxCongestionWindow(), c.cubic.CongestionWindowAfterAck(ackedBytes, c.congestionWindow, c.rttStats.MinRTT(), eventTime))
		}
	}	
}

func (c *cubicSender) isCwndLimited(bytesInFlight protocol.ByteCount) bool {
	congestionWindow := c.GetCongestionWindow()
	if bytesInFlight >= congestionWindow {
		return true
	}
	availableBytes := congestionWindow - bytesInFlight
	slowStartLimited := c.InSlowStart() && bytesInFlight > congestionWindow/2
	return slowStartLimited || availableBytes <= maxBurstPackets*c.maxDatagramSize
}

// BandwidthEstimate returns the current bandwidth estimate
func (c *cubicSender) BandwidthEstimate() Bandwidth {
	srtt := c.rttStats.SmoothedRTT()
	if srtt == 0 {
		// If we haven't measured an rtt, the bandwidth estimate is unknown.
		return infBandwidth
	}
	return BandwidthFromDelta(c.GetCongestionWindow(), srtt)
}

// OnRetransmissionTimeout is called on an retransmission timeout
func (c *cubicSender) OnRetransmissionTimeout(packetsRetransmitted bool) {
	c.largestSentAtLastCutback = protocol.InvalidPacketNumber
	if !packetsRetransmitted {
		return
	}
	switch c.chosenStartAlgo{
	case utils.ChooseHystartpp:
		c.hybridSlowStartpp.Restart()
	case utils.ChooseHystart:
		c.hybridSlowStart.Restart()
	}
	
	c.cubic.Reset()
	c.slowStartThreshold = c.congestionWindow / 2
	c.congestionWindow = c.minCongestionWindow()
}

// OnConnectionMigration is called when the connection is migrated (?)
func (c *cubicSender) OnConnectionMigration() {
	switch c.chosenStartAlgo {
	case utils.ChooseHystart:
		c.hybridSlowStart.Restart()
	case utils.ChooseHystartpp:
		c.hybridSlowStartpp.Restart()
	}
	
	c.largestSentPacketNumber = protocol.InvalidPacketNumber
	c.largestAckedPacketNumber = protocol.InvalidPacketNumber
	c.largestSentAtLastCutback = protocol.InvalidPacketNumber
	c.lastCutbackExitedSlowstart = false
	c.cubic.Reset()
	c.numAckedPackets = 0
	c.congestionWindow = c.initialCongestionWindow
	c.slowStartThreshold = c.initialMaxCongestionWindow
}

func (c *cubicSender) maybeTraceStateChange(new logging.CongestionState) {
	if c.tracer == nil || new == c.lastState {
		return
	}
	c.tracer.UpdatedCongestionState(new)
	c.lastState = new
}

func (c *cubicSender) SetMaxDatagramSize(s protocol.ByteCount) {
	if s < c.maxDatagramSize {
		panic(fmt.Sprintf("congestion BUG: decreased max datagram size from %d to %d", c.maxDatagramSize, s))
	}
	cwndIsMinCwnd := c.congestionWindow == c.minCongestionWindow()
	c.maxDatagramSize = s
	if cwndIsMinCwnd {
		c.congestionWindow = c.minCongestionWindow()
	}
	c.pacer.SetMaxDatagramSize(s)
}
