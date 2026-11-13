//go:build !nativemlow

package session

// Pure-Go builds keep the existing PCM data-channel fallback. Production uses
// the nativemlow build so the browser receives a real WebRTC audio track.
func newRemoteAudioEncoder() (remoteAudioEncoder, error) { return nil, nil }
