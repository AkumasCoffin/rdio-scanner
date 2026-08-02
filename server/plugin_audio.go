// Copyright (C) 2019-2022 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// Audio a plugin can actually work with.
//
// Until now a call's audio was opaque bytes: readable and forwardable, never
// understood. There is no codec in a JavaScript runtime, so anything wanting to
// analyse or alter audio — tone detection, loudness, resampling — was not hard,
// it was impossible.
//
// This wraps the ffmpeg support the server already carries. Everything is
// promise-returning and runs on a goroutine, so an ffmpeg subprocess never
// occupies a plugin's event loop.

const (
	pluginAudioTimeout = 2 * time.Minute

	// pluginAudioMaxBytes bounds a single conversion's output.
	pluginAudioMaxBytes = 256 << 20
)

func (rt *PluginRuntime) bindAudio(vm *goja.Runtime, rdio *goja.Object, throw func(string, ...any)) {
	audio := vm.NewObject()

	requireFfmpeg := func() error {
		if rt.controller.FFMpeg == nil || !rt.controller.FFMpeg.Available() {
			return fmt.Errorf("ffmpeg is not installed, so audio cannot be decoded or converted. %s",
				rt.controller.FFMpeg.UnavailableMessage())
		}
		return nil
	}

	// probe reports what a blob actually contains, so a plugin can decide
	// whether it is worth doing anything with before paying to decode it.
	audio.Set("probe", func(data goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("audio.probe: %v", err)
		}

		return rt.promiseFrom(vm, func() (any, error) {
			if err := requireFfmpeg(); err != nil {
				return nil, err
			}
			return probeAudio(body)
		})
	})

	// decode turns audio into signed 16-bit PCM samples.
	//
	// The result is an Int16Array over an ArrayBuffer, never a JavaScript array
	// of numbers: thirty seconds at 8 kHz is 240,000 samples, which as boxed
	// values would be tens of megabytes for a single call.
	audio.Set("decode", func(data goja.Value, options goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("audio.decode: %v", err)
		}

		rate, channels := 8000, 1
		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if v, ok := numberFromMap(m, "sampleRate"); ok && v > 0 {
					rate = int(v)
				}
				if v, ok := numberFromMap(m, "channels"); ok && v > 0 {
					channels = int(v)
				}
			}
		}

		return rt.promiseFrom(vm, func() (any, error) {
			if err := requireFfmpeg(); err != nil {
				return nil, err
			}

			raw, err := decodeAudio(body, rate, channels)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"sampleRate": rate,
				"channels":   channels,
				"count":      len(raw) / 2,
				"samples":    vm.NewArrayBuffer(raw),
			}, nil
		})
	})

	// encode turns PCM samples back into a playable file.
	audio.Set("encode", func(samples goja.Value, options goja.Value) goja.Value {
		body, err := pluginBytes(samples.Export())
		if err != nil {
			throw("audio.encode: %v", err)
		}

		rate, channels, format := 8000, 1, "m4a"
		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if v, ok := numberFromMap(m, "sampleRate"); ok && v > 0 {
					rate = int(v)
				}
				if v, ok := numberFromMap(m, "channels"); ok && v > 0 {
					channels = int(v)
				}
				if v := stringFromMap(m, "format"); v != "" {
					format = v
				}
			}
		}

		return rt.promiseFrom(vm, func() (any, error) {
			if err := requireFfmpeg(); err != nil {
				return nil, err
			}

			encoded, err := encodeAudio(body, rate, channels, format)
			if err != nil {
				return nil, err
			}

			return vm.NewArrayBuffer(encoded), nil
		})
	})

	// convert re-encodes audio, optionally normalising loudness. This is the
	// one most plugins want: it never decodes into the runtime at all.
	audio.Set("convert", func(data goja.Value, options goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("audio.convert: %v", err)
		}

		settings := audioConvertOptions{format: "m4a", bitrate: "32k"}

		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if v := stringFromMap(m, "format"); v != "" {
					settings.format = v
				}
				if v := stringFromMap(m, "bitrate"); v != "" {
					settings.bitrate = v
				}
				if v, ok := numberFromMap(m, "sampleRate"); ok && v > 0 {
					settings.sampleRate = int(v)
				}
				if v, ok := m["normalize"].(bool); ok {
					settings.normalize = v
				}
				if v := stringFromMap(m, "filter"); v != "" {
					settings.filter = v
				}
			}
		}

		return rt.promiseFrom(vm, func() (any, error) {
			if err := requireFfmpeg(); err != nil {
				return nil, err
			}

			converted, err := convertAudio(body, settings)
			if err != nil {
				return nil, err
			}

			return vm.NewArrayBuffer(converted), nil
		})
	})

	// goertzel measures how much energy sits at particular frequencies, which
	// is what tone and alert detection needs.
	//
	// Native because it cannot be anything else: a pass over a few frequencies
	// across a thirty-second call is around a million operations, and goja is
	// roughly two orders of magnitude slower than a browser engine. In Go it is
	// a few dozen lines and finishes in milliseconds.
	audio.Set("goertzel", func(samples goja.Value, frequencies goja.Value, options goja.Value) goja.Value {
		body, err := pluginBytes(samples.Export())
		if err != nil {
			throw("audio.goertzel: %v", err)
		}

		targets := []float64{}
		if list, ok := frequencies.Export().([]any); ok {
			for _, entry := range list {
				switch v := entry.(type) {
				case int64:
					targets = append(targets, float64(v))
				case float64:
					targets = append(targets, v)
				}
			}
		}
		if len(targets) == 0 {
			throw("audio.goertzel: at least one frequency is required")
		}

		rate, window := 8000, 0
		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if v, ok := numberFromMap(m, "sampleRate"); ok && v > 0 {
					rate = int(v)
				}
				if v, ok := numberFromMap(m, "windowSize"); ok && v > 0 {
					window = int(v)
				}
			}
		}

		return vm.ToValue(goertzelScan(pcmFromBytes(body), targets, rate, window))
	})

	rdio.Set("audio", audio)
}

// --- ffmpeg ---------------------------------------------------------------

func probeAudio(body []byte) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginAudioTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", "-i", "pipe:0")
	cmd.Stdin = bytes.NewReader(body)

	out := &bytes.Buffer{}
	cmd.Stdout = out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe: %v", err)
	}

	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
			Name     string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecName  string `json:"codec_name"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("ffprobe returned something unreadable: %v", err)
	}

	result := map[string]any{
		"format": parsed.Format.Name,
		"size":   len(body),
	}

	if v, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		result["duration"] = v
	}
	if v, err := strconv.Atoi(parsed.Format.BitRate); err == nil {
		result["bitrate"] = v
	}

	if len(parsed.Streams) > 0 {
		stream := parsed.Streams[0]
		result["codec"] = stream.CodecName
		result["channels"] = stream.Channels
		if v, err := strconv.Atoi(stream.SampleRate); err == nil {
			result["sampleRate"] = v
		}
	}

	return result, nil
}

func decodeAudio(body []byte, rate int, channels int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginAudioTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-f", "s16le", "-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(channels),
		"pipe:1",
	)
	cmd.Stdin = bytes.NewReader(body)

	out := &boundedBuffer{limit: pluginAudioMaxBytes}
	stderr := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode: %v: %s", err, strings.TrimSpace(stderr.String()))
	}

	return []byte(out.String()), nil
}

func encodeAudio(pcm []byte, rate int, channels int, format string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginAudioTimeout)
	defer cancel()

	// A seekable destination, for the same reason Convert uses one: MP4 writes
	// its sample index near the front but only knows it at the end, so piping
	// forces a fragmented file that Safari cannot decode in one shot.
	tmp, err := os.CreateTemp("", "rdio-plugin-encode-*."+format)
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "s16le", "-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(channels),
		"-i", "pipe:0",
		path,
	)
	cmd.Stdin = bytes.NewReader(pcm)

	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg encode: %v: %s", err, strings.TrimSpace(stderr.String()))
	}

	return os.ReadFile(path)
}

type audioConvertOptions struct {
	format     string
	bitrate    string
	sampleRate int
	normalize  bool
	filter     string
}

func convertAudio(body []byte, options audioConvertOptions) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginAudioTimeout)
	defer cancel()

	tmp, err := os.CreateTemp("", "rdio-plugin-convert-*."+options.format)
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", "pipe:0"}

	filters := []string{}
	if options.normalize {
		// The same loudness filter the server's own normalising conversion
		// modes use, so a plugin and core produce comparable results.
		filters = append(filters, "loudnorm")
	}
	if options.filter != "" {
		filters = append(filters, options.filter)
	}
	if len(filters) > 0 {
		args = append(args, "-af", strings.Join(filters, ","))
	}

	if options.sampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(options.sampleRate))
	}
	if options.bitrate != "" {
		args = append(args, "-b:a", options.bitrate)
	}

	args = append(args, path)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(body)

	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg convert: %v: %s", err, strings.TrimSpace(stderr.String()))
	}

	return os.ReadFile(path)
}

// --- signal processing ----------------------------------------------------

// pcmFromBytes reinterprets little-endian signed 16-bit samples.
func pcmFromBytes(body []byte) []int16 {
	samples := make([]int16, len(body)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(body[i*2:]))
	}
	return samples
}

// goertzelScan measures energy at each frequency over successive windows.
//
// Returned per window rather than as one number for the whole clip, because a
// tone is defined by being present for a period — a single figure across a
// thirty-second call would average a two-second tone into nothing.
func goertzelScan(samples []int16, frequencies []float64, rate int, window int) []any {
	if window <= 0 {
		// A fifth of a second: long enough to resolve paging tones, short
		// enough that a two-tone sequence lands in separate windows.
		window = rate / 5
	}
	if window <= 0 || len(samples) == 0 {
		return []any{}
	}

	results := []any{}

	for start := 0; start+window <= len(samples); start += window {
		chunk := samples[start : start+window]

		magnitudes := make([]any, len(frequencies))
		for i, frequency := range frequencies {
			magnitudes[i] = goertzel(chunk, frequency, rate)
		}

		results = append(results, map[string]any{
			"offset":     float64(start) / float64(rate),
			"magnitudes": magnitudes,
		})
	}

	return results
}

// goertzel is the standard single-frequency DFT bin: cheaper than a full FFT
// when only a handful of frequencies matter, which is exactly the tone case.
func goertzel(samples []int16, frequency float64, rate int) float64 {
	if rate <= 0 || len(samples) == 0 {
		return 0
	}

	k := math.Round(float64(len(samples)) * frequency / float64(rate))
	omega := 2 * math.Pi * k / float64(len(samples))
	coefficient := 2 * math.Cos(omega)

	var s0, s1, s2 float64

	for _, sample := range samples {
		s0 = float64(sample)/32768.0 + coefficient*s1 - s2
		s2 = s1
		s1 = s0
	}

	power := s1*s1 + s2*s2 - coefficient*s1*s2

	if power <= 0 {
		return 0
	}

	// Normalised by window length so windows of different sizes compare.
	return math.Sqrt(power) / float64(len(samples))
}
