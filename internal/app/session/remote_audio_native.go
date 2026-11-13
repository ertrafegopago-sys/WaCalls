//go:build nativemlow

package session

/*
#cgo LDFLAGS: -lopus -lm
#include <opus.h>

static int remote_enc_ctl_int(OpusEncoder *st, int request, opus_int32 value) {
	return opus_encoder_ctl(st, request, value);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

const (
	remoteInputRate = 16000
	remoteMaxPacket = 4000
)

type nativeRemoteAudioEncoder struct {
	mu sync.Mutex
	st *C.OpusEncoder
}

func newRemoteAudioEncoder() (remoteAudioEncoder, error) {
	var cerr C.int
	st := C.opus_encoder_create(remoteInputRate, 1, C.OPUS_APPLICATION_VOIP, &cerr)
	if cerr != C.OPUS_OK {
		return nil, fmt.Errorf("remote opus encoder create: %d", int(cerr))
	}
	for _, ctl := range [...]struct {
		req C.int
		val C.opus_int32
	}{
		{C.OPUS_SET_BITRATE_REQUEST, 32000},
		{C.OPUS_SET_COMPLEXITY_REQUEST, 5},
		{C.OPUS_SET_DTX_REQUEST, 0},
	} {
		if rc := C.remote_enc_ctl_int(st, ctl.req, ctl.val); rc != C.OPUS_OK {
			C.opus_encoder_destroy(st)
			return nil, fmt.Errorf("remote opus encoder ctl %d: %d", int(ctl.req), int(rc))
		}
	}
	return &nativeRemoteAudioEncoder{st: st}, nil
}

func (e *nativeRemoteAudioEncoder) Encode(pcm []float32) ([]byte, time.Duration, error) {
	if len(pcm) == 0 {
		return nil, 0, errors.New("remote opus encoder: empty PCM")
	}
	clean := make([]float32, len(pcm))
	for i, sample := range pcm {
		switch {
		case math.IsNaN(float64(sample)):
			sample = 0
		case sample < -1:
			sample = -1
		case sample > 1:
			sample = 1
		}
		clean[i] = sample
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.st == nil {
		return nil, 0, errors.New("remote opus encoder closed")
	}
	var buf [remoteMaxPacket]byte
	n := C.opus_encode_float(e.st, (*C.float)(unsafe.Pointer(&clean[0])), C.int(len(clean)),
		(*C.uchar)(unsafe.Pointer(&buf[0])), remoteMaxPacket)
	runtime.KeepAlive(e)
	if n < 0 {
		return nil, 0, fmt.Errorf("remote opus encode: %d", int(n))
	}
	out := make([]byte, int(n))
	copy(out, buf[:n])
	return out, time.Duration(len(clean)) * time.Second / remoteInputRate, nil
}

func (e *nativeRemoteAudioEncoder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.st != nil {
		C.opus_encoder_destroy(e.st)
		e.st = nil
	}
}
