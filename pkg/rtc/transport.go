package rtc

import (
	"sync"
	"time"

	"github.com/bep/debounce"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/livekit-server/pkg/logger"

	livekit "github.com/livekit/livekit-server/proto"
)

const (
	negotiationFrequency = 150 * time.Millisecond
)

const (
	negotiationStateNone = iota
	// waiting for client answer
	negotiationStateClient
	// need to Negotiate again
	negotiationRetry
)

// PCTransport is a wrapper around PeerConnection, with some helper methods
type PCTransport struct {
	pc *webrtc.PeerConnection
	me *webrtc.MediaEngine

	lock sync.Mutex
	// map of mid => []codecs for the transceiver
	transceiverCodecs     map[string][]webrtc.RTPCodecCapability
	pendingCandidates     []webrtc.ICECandidateInit
	debouncedNegotiate    func(func())
	onOffer               func(offer webrtc.SessionDescription)
	restartAfterGathering bool
	negotiationState      int
}

type TransportParams struct {
	Target livekit.SignalTarget
	Config *WebRTCConfig
	Stats  *RoomStatsReporter
}

func newPeerConnection(params TransportParams) (*webrtc.PeerConnection, *webrtc.MediaEngine, error) {
	var me *webrtc.MediaEngine
	var err error
	if params.Target == livekit.SignalTarget_PUBLISHER {
		me, err = createPubMediaEngine()
	} else {
		me, err = createSubMediaEngine()
	}
	if err != nil {
		return nil, nil, err
	}
	se := params.Config.SettingEngine
	se.DisableMediaEngineCopy(true)
	if params.Stats != nil && se.BufferFactory != nil {
		wrapper := &StatsBufferWrapper{
			createBufferFunc: se.BufferFactory,
			stats:            params.Stats.incoming,
		}
		se.BufferFactory = wrapper.CreateBuffer
	}

	ir := &interceptor.Registry{}
	if params.Stats != nil && params.Target == livekit.SignalTarget_SUBSCRIBER {
		// only capture subscriber for outbound streams
		ir.Add(NewStatsInterceptor(params.Stats))
	}
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(me),
		webrtc.WithSettingEngine(se),
		webrtc.WithInterceptorRegistry(ir),
	)
	pc, err := api.NewPeerConnection(params.Config.Configuration)
	return pc, me, err
}

func NewPCTransport(params TransportParams) (*PCTransport, error) {
	pc, me, err := newPeerConnection(params)
	if err != nil {
		return nil, err
	}

	t := &PCTransport{
		pc:                 pc,
		me:                 me,
		transceiverCodecs:  make(map[string][]webrtc.RTPCodecCapability),
		debouncedNegotiate: debounce.New(negotiationFrequency),
		negotiationState:   negotiationStateNone,
	}
	t.pc.OnICEGatheringStateChange(func(state webrtc.ICEGathererState) {
		if state == webrtc.ICEGathererStateComplete {
			t.lock.Lock()
			defer t.lock.Unlock()
			if t.restartAfterGathering {
				logger.Debugw("restarting ICE after ICE gathering")
				if err := t.createAndSendOffer(&webrtc.OfferOptions{ICERestart: true}); err != nil {
					logger.Warnw("could not restart ICE", err)
				}
			}
		}
	})

	return t, nil
}

func (t *PCTransport) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if t.pc.RemoteDescription() == nil {
		t.lock.Lock()
		t.pendingCandidates = append(t.pendingCandidates, candidate)
		t.lock.Unlock()
		return nil
	}

	return t.pc.AddICECandidate(candidate)
}

func (t *PCTransport) PeerConnection() *webrtc.PeerConnection {
	return t.pc
}

func (t *PCTransport) Close() {
	_ = t.pc.Close()
}

func (t *PCTransport) GetTransceiverForSending(track webrtc.TrackLocal, codec webrtc.RTPCodecCapability) *webrtc.RTPTransceiver {
	t.lock.Lock()
	defer t.lock.Unlock()
	for _, transceiver := range t.pc.GetTransceivers() {
		if transceiver.Kind() != track.Kind() {
			continue
		}
		sender := transceiver.Sender()
		if sender == nil || sender.Track() != nil {
			continue
		}

		// only use if there's matching codec
		codecMatches := false
		for _, params := range t.transceiverCodecs[transceiver.Mid()] {
			if params.MimeType == codec.MimeType {
				codecMatches = true
				break
			}
		}

		if codecMatches {
			transceiver.Mid()
			return transceiver
		}
	}

	return nil
}

func (t *PCTransport) SetRemoteDescription(sd webrtc.SessionDescription) error {
	t.lock.Lock()
	defer t.lock.Unlock()

	if err := t.pc.SetRemoteDescription(sd); err != nil {
		return err
	}

	// negotiated, reset flag
	lastState := t.negotiationState
	t.negotiationState = negotiationStateNone

	for _, c := range t.pendingCandidates {
		if err := t.pc.AddICECandidate(c); err != nil {
			return err
		}
	}
	t.pendingCandidates = nil

	// only initiate when we are the offerer
	if lastState == negotiationRetry && sd.Type == webrtc.SDPTypeAnswer {
		logger.Debugw("re-negotiate after answering")
		if err := t.createAndSendOffer(nil); err != nil {
			logger.Errorw("could not negotiate", err)
		}
	}
	return nil
}

// OnOffer is called when the PeerConnection starts negotiation and prepares an offer
func (t *PCTransport) OnOffer(f func(sd webrtc.SessionDescription)) {
	t.onOffer = f
}

func (t *PCTransport) Negotiate() {
	t.debouncedNegotiate(func() {
		if err := t.CreateAndSendOffer(nil); err != nil {
			logger.Errorw("could not negotiate", err)
		}
	})
}

func (t *PCTransport) CreateAndSendOffer(options *webrtc.OfferOptions) error {
	t.lock.Lock()
	defer t.lock.Unlock()
	return t.createAndSendOffer(options)
}

// creates and sends offer assuming lock has been acquired
func (t *PCTransport) createAndSendOffer(options *webrtc.OfferOptions) error {
	if t.onOffer == nil {
		return nil
	}
	if t.pc.ConnectionState() == webrtc.PeerConnectionStateClosed {
		return nil
	}

	iceRestart := options != nil && options.ICERestart

	// if restart is requested, and we are not ready, then continue afterwards
	if iceRestart {
		if t.pc.ICEGatheringState() == webrtc.ICEGatheringStateGathering {
			logger.Debugw("restart ICE after gathering")
			t.restartAfterGathering = true
			return nil
		}
		logger.Debugw("restarting ICE")
	}

	// when there's an ongoing negotiation, let it finish and not disrupt its state
	if t.negotiationState == negotiationStateClient {
		currentSD := t.pc.CurrentRemoteDescription()
		if iceRestart && currentSD != nil {
			logger.Debugw("recovering from client negotiation state")
			if err := t.pc.SetRemoteDescription(*currentSD); err != nil {
				return err
			}
		} else {
			logger.Debugw("skipping negotiation, trying again later")
			t.negotiationState = negotiationRetry
			return nil
		}
	} else if t.negotiationState == negotiationRetry {
		// already set to retry, we can safely skip this attempt
		return nil
	}

	offer, err := t.pc.CreateOffer(options)
	if err != nil {
		logger.Errorw("could not create offer", err)
		return err
	}

	err = t.pc.SetLocalDescription(offer)
	if err != nil {
		logger.Errorw("could not set local description", err)
		return err
	}

	// indicate waiting for client
	t.negotiationState = negotiationStateClient
	t.restartAfterGathering = false

	// record any transceiver and their codec types
	for _, transceiver := range t.pc.GetTransceivers() {
		sender := transceiver.Sender()
		if sender == nil || sender.Track() == nil {
			continue
		}
		var capabilities []webrtc.RTPCodecCapability
		for _, codec := range sender.GetParameters().Codecs {
			capabilities = append(capabilities, codec.RTPCodecCapability)
		}
		t.transceiverCodecs[transceiver.Mid()] = capabilities
	}

	go t.onOffer(offer)
	return nil
}
