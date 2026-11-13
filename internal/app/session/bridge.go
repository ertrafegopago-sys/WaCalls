package session

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"wacalls/internal/voip/media"

	"github.com/pion/webrtc/v4"
	webrtcmedia "github.com/pion/webrtc/v4/pkg/media"
)

// pcmChannelLabel is the data channel the browser opens to carry raw 16 kHz mono
// Int16 LE PCM in both directions. The browser side must create it with this label.
const pcmChannelLabel = "pcm"

// Bridge is the browser-leg adapter: it carries raw PCM between the browser and
// the CallManager over a WebRTC data channel. The call core only ever sees
// []float32 PCM, so it stays unaware of the transport (no Opus here anymore).
type Bridge struct {
	pc    *webrtc.PeerConnection
	dc    atomic.Pointer[webrtc.DataChannel]
	track *webrtc.TrackLocalStaticSample
	enc   remoteAudioEncoder
	encMu sync.Mutex
	log   *slog.Logger

	// OnBrowserPCM is invoked with decoded 16 kHz mono PCM captured from the browser mic.
	OnBrowserPCM func(pcm []float32)
	// OnTerminalICE fires when the peer connection fails or closes.
	OnTerminalICE func()
}

func NewBridge(api *webrtc.API, offerSDP string, log *slog.Logger) (*Bridge, string, error) {
	enc, err := newRemoteAudioEncoder()
	if err != nil {
		return nil, "", err
	}
	return newBridge(api, offerSDP, log, enc)
}

func newBridge(api *webrtc.API, offerSDP string, log *slog.Logger, enc remoteAudioEncoder) (*Bridge, string, error) {
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	br := &Bridge{pc: pc, enc: enc, log: log}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case pcmChannelLabel:
			br.dc.Store(dc)
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				if cb := br.OnBrowserPCM; cb != nil && len(msg.Data) > 0 {
					cb(media.PCMInt16LEToFloat32(msg.Data))
				}
			})
		}
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Debug("browser ice state", "state", s.String())
		if s == webrtc.ICEConnectionStateFailed || s == webrtc.ICEConnectionStateClosed {
			if br.OnTerminalICE != nil {
				br.OnTerminalICE()
			}
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		_ = pc.Close()
		return nil, "", err
	}

	// Edvaldo/Codex: add the track after SetRemoteDescription so Pion can
	// match it against the Opus codec actually offered by this browser.
	// Android routes a real WebRTC audio track as call media; playback of PCM
	// through Web Audio was classified as ordinary media and used the speaker.
	if enc != nil {
		track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		}, "wacalls-remote", "wacalls")
		if err != nil {
			_ = pc.Close()
			return nil, "", err
		}
		if _, err := pc.AddTrack(track); err != nil {
			_ = pc.Close()
			return nil, "", err
		}
		br.track = track
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	<-gatherComplete

	return br, pc.LocalDescription().SDP, nil
}

// WritePCM sends 16 kHz mono float32 PCM to the browser. Native builds encode it
// as Opus on a real WebRTC audio track; pure-Go builds retain the PCM data-channel
// fallback for compatibility.
func (b *Bridge) WritePCM(pcm []float32) error {
	if len(pcm) == 0 {
		return nil
	}
	if b.track != nil && b.enc != nil {
		b.encMu.Lock()
		packet, duration, err := b.enc.Encode(pcm)
		if err == nil && len(packet) > 0 {
			err = b.track.WriteSample(webrtcmedia.Sample{Data: packet, Duration: duration})
		}
		b.encMu.Unlock()
		if err != nil {
			b.log.Warn("remote WebRTC audio write failed", "err", err)
		}
		return err
	}
	// Compatibility fallback for pure-Go builds without libopus. Production
	// uses the native build and never takes this path.
	dc := b.dc.Load()
	if dc == nil {
		return nil
	}
	return dc.Send(media.PCMFloat32ToInt16LE(pcm))
}

func (b *Bridge) Close() {
	if b.enc != nil {
		b.encMu.Lock()
		b.enc.Close()
		b.encMu.Unlock()
	}
	if b.pc != nil {
		_ = b.pc.Close()
	}
}
