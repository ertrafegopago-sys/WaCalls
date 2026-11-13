package session

import "time"

// remoteAudioEncoder converts the 16 kHz PCM produced by the WhatsApp call
// engine into an Opus packet suitable for a browser WebRTC audio track.
type remoteAudioEncoder interface {
	Encode(pcm []float32) ([]byte, time.Duration, error)
	Close()
}
