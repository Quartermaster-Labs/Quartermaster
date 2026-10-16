package server

// WAV handling for the music routes: just enough decoding to get HTDemucs its
// one hard input contract (44.1 kHz), and a check that an upload is a WAV at
// all before it is written to disk for audio.cpp to open.
//
// Everything else audio.cpp reads itself (its wav_reader takes PCM 8/16/24/32,
// float32/64, mu-law, A-law and the EXTENSIBLE header), so an upload that needs
// no resampling is passed through byte for byte rather than re-encoded. A
// round trip through float here would make the ACE-Step edits differ from the
// CLI fed the same file.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	wavFormatPCM        = 1
	wavFormatFloat      = 3
	wavFormatExtensible = 0xFFFE
)

// wavInfo is a parsed WAV header plus the location of its sample data.
type wavInfo struct {
	format        int
	channels      int
	sampleRate    int
	bitsPerSample int
	data          []byte
}

// parseWAV walks the RIFF chunks of b and returns the fmt and data chunks. It
// does not decode samples: callers that only need to validate an upload stop
// here.
func parseWAV(b []byte) (wavInfo, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return wavInfo{}, errors.New("audio is not a WAV file (RIFF/WAVE header missing); send WAV")
	}
	var info wavInfo
	haveFmt := false
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		end := body + size
		if size < 0 || end > len(b) {
			// A streaming writer leaves the data size at 0 or 0xFFFFFFFF; take
			// what is actually there rather than refusing the file.
			if id == "data" {
				end = len(b)
			} else {
				return wavInfo{}, fmt.Errorf("WAV chunk %q runs past the end of the file", id)
			}
		}
		switch id {
		case "fmt ":
			if end-body < 16 {
				return wavInfo{}, errors.New("WAV fmt chunk too short")
			}
			f := b[body:end]
			info.format = int(binary.LittleEndian.Uint16(f[0:2]))
			info.channels = int(binary.LittleEndian.Uint16(f[2:4]))
			info.sampleRate = int(binary.LittleEndian.Uint32(f[4:8]))
			info.bitsPerSample = int(binary.LittleEndian.Uint16(f[14:16]))
			if info.format == wavFormatExtensible && len(f) >= 26 {
				// The real format code is the first two bytes of the SubFormat GUID.
				info.format = int(binary.LittleEndian.Uint16(f[24:26]))
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return wavInfo{}, errors.New("WAV data chunk precedes its fmt chunk")
			}
			info.data = b[body:end]
			if info.channels < 1 || info.sampleRate < 1 {
				return wavInfo{}, errors.New("WAV header has no channels or no sample rate")
			}
			return info, nil
		}
		// Chunks are word-aligned: an odd size carries one pad byte.
		off = end + size&1
	}
	return wavInfo{}, errors.New("WAV file has no data chunk")
}

// decodeWAV returns the interleaved samples of info as float32 in [-1, 1].
func decodeWAV(info wavInfo) ([]float32, error) {
	d := info.data
	switch {
	case info.format == wavFormatPCM && info.bitsPerSample == 8:
		out := make([]float32, len(d))
		for i, v := range d {
			out[i] = (float32(v) - 128) / 128
		}
		return out, nil
	case info.format == wavFormatPCM && info.bitsPerSample == 16:
		out := make([]float32, len(d)/2)
		for i := range out {
			out[i] = float32(int16(binary.LittleEndian.Uint16(d[2*i:]))) / 32768
		}
		return out, nil
	case info.format == wavFormatPCM && info.bitsPerSample == 24:
		out := make([]float32, len(d)/3)
		for i := range out {
			v := int32(uint32(d[3*i]) | uint32(d[3*i+1])<<8 | uint32(d[3*i+2])<<16)
			out[i] = float32(v<<8>>8) / 8388608
		}
		return out, nil
	case info.format == wavFormatPCM && info.bitsPerSample == 32:
		out := make([]float32, len(d)/4)
		for i := range out {
			out[i] = float32(float64(int32(binary.LittleEndian.Uint32(d[4*i:]))) / 2147483648)
		}
		return out, nil
	case info.format == wavFormatFloat && info.bitsPerSample == 32:
		out := make([]float32, len(d)/4)
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(d[4*i:]))
		}
		return out, nil
	case info.format == wavFormatFloat && info.bitsPerSample == 64:
		out := make([]float32, len(d)/8)
		for i := range out {
			out[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(d[8*i:])))
		}
		return out, nil
	}
	return nil, fmt.Errorf("WAV encoding (format %d, %d-bit) cannot be resampled here; send PCM or float WAV", info.format, info.bitsPerSample)
}

// encodeFloatWAV writes interleaved float32 samples as an IEEE-float WAV.
// Float rather than PCM16 so the resampled input loses nothing to a second
// quantization before the separator sees it.
func encodeFloatWAV(samples []float32, channels, rate int) []byte {
	dataLen := len(samples) * 4
	var b bytes.Buffer
	b.Grow(44 + dataLen)
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + dataLen))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(wavFormatFloat))
	w(uint16(channels))
	w(uint32(rate))
	w(uint32(rate * channels * 4))
	w(uint16(channels * 4))
	w(uint16(32))
	b.WriteString("data")
	w(uint32(dataLen))
	buf := make([]byte, dataLen)
	for i, v := range samples {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
	}
	b.Write(buf)
	return b.Bytes()
}

// resampleHalfTaps is the windowed-sinc half width in input samples at unity
// ratio. 32 keeps the passband flat to well past 20 kHz at 48k->44.1k, which is
// the conversion that matters here, at a cost that stays under a second for a
// whole song.
const resampleHalfTaps = 32

// resample converts interleaved samples from inRate to outRate with a
// Blackman-windowed sinc. When downsampling the cutoff drops to the output
// Nyquist so the content above it is filtered instead of aliased.
func resample(in []float32, channels, inRate, outRate int) []float32 {
	if inRate == outRate || len(in) == 0 {
		return in
	}
	frames := len(in) / channels
	outFrames := int(int64(frames) * int64(outRate) / int64(inRate))
	out := make([]float32, outFrames*channels)
	cutoff := math.Min(1, float64(outRate)/float64(inRate))
	half := int(math.Ceil(resampleHalfTaps / cutoff))
	taps := 2 * half

	// Polyphase table: an output frame's position falls on one of `phases`
	// fractional offsets between input frames, so its kernel is precomputed
	// once per phase instead of evaluating sin() per tap per sample (which is
	// tens of seconds on a full song). Exact for rational ratios with a small
	// denominator, 48k->44.1k is 147 phases; an odd ratio is quantized to
	// maxPhases, an error far below 16-bit resolution.
	const maxPhases = 4096
	g := gcd(inRate, outRate)
	inStep, phases := int64(inRate/g), int64(outRate/g)
	exact := phases <= maxPhases
	if !exact {
		phases = maxPhases
	}
	kernel := make([]float64, int(phases)*taps)
	for p := 0; p < int(phases); p++ {
		frac := float64(p) / float64(phases)
		row := kernel[p*taps : (p+1)*taps]
		var norm float64
		for t := range row {
			x := frac - float64(t-half+1)
			row[t] = cutoff * sinc(cutoff*x) * blackman(x, float64(half))
			norm += row[t]
		}
		// Unit DC gain per phase, so a constant input stays constant.
		for t := range row {
			row[t] /= norm
		}
	}

	for o := 0; o < outFrames; o++ {
		var center, phase int64
		if exact {
			num := int64(o) * inStep
			center, phase = num/phases, num%phases
		} else {
			pos := float64(o) * float64(inRate) / float64(outRate)
			center = int64(pos)
			phase = int64(math.Round((pos - float64(center)) * float64(phases)))
			if phase == phases {
				center, phase = center+1, 0
			}
		}
		row := kernel[int(phase)*taps : int(phase+1)*taps]
		first := int(center) - half + 1
		for c := 0; c < channels; c++ {
			var acc float64
			for t, wt := range row {
				k := first + t
				if k < 0 || k >= frames {
					continue
				}
				acc += wt * float64(in[k*channels+c])
			}
			out[o*channels+c] = float32(acc)
		}
	}
	return out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// blackman is the Blackman window over [-half, half], zero outside it.
func blackman(x, half float64) float64 {
	if x <= -half || x >= half {
		return 0
	}
	t := (x + half) / (2 * half)
	return 0.42 - 0.5*math.Cos(2*math.Pi*t) + 0.08*math.Cos(4*math.Pi*t)
}
