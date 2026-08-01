// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
	"encoding/binary"
	"math"
	"testing"
)

// sineWav builds a mono 16-bit PCM wav in memory, so the conversion test needs
// no fixture file and no ffmpeg to produce its input.
func sineWav(seconds int, sampleRate int) []byte {
	samples := seconds * sampleRate
	data := new(bytes.Buffer)
	for i := 0; i < samples; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/float64(sampleRate)) * 8000)
		binary.Write(data, binary.LittleEndian, v)
	}
	pcm := data.Bytes()

	buf := new(bytes.Buffer)
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVEfmt ")
	binary.Write(buf, binary.LittleEndian, uint32(16))            // fmt chunk size
	binary.Write(buf, binary.LittleEndian, uint16(1))             // PCM
	binary.Write(buf, binary.LittleEndian, uint16(1))             // mono
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))    // sample rate
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate*2))  // byte rate
	binary.Write(buf, binary.LittleEndian, uint16(2))             // block align
	binary.Write(buf, binary.LittleEndian, uint16(16))            // bits
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)

	return buf.Bytes()
}

// findBox walks the top level of an MP4 looking for a four-character box type,
// descending into the containers on the path to the sample tables.
func findBox(buf []byte, want string) []byte {
	for off := 0; off+8 <= len(buf); {
		size := int(binary.BigEndian.Uint32(buf[off : off+4]))
		typ := string(buf[off+4 : off+8])

		if size == 0 {
			size = len(buf) - off
		}
		if size < 8 || off+size > len(buf) {
			return nil
		}
		if typ == want {
			return buf[off : off+size]
		}
		switch typ {
		case "moov", "trak", "mdia", "minf", "stbl":
			if found := findBox(buf[off+8:off+size], want); found != nil {
				return found
			}
		}
		off += size
	}
	return nil
}

// Converted audio must be a plain MP4, not a fragmented one.
//
// Writing the MP4 to a pipe used to force -movflags frag_keyframe+empty_moov,
// which emits a moov describing zero samples and puts the real index in a
// trailing moof. Chrome and ffprobe read that; Safari's decodeAudioData does
// not, so every converted call was silent on iOS. Guard the shape of the
// output rather than the ffmpeg arguments, so the property survives a rewrite
// of how the command is assembled.
func TestConvertProducesNonFragmentedMp4(t *testing.T) {
	ffmpeg := NewFFMpeg()
	if !ffmpeg.Available() {
		t.Skip("ffmpeg not installed")
	}

	call := &Call{
		Audio:     sineWav(2, 8000),
		AudioName: "test.wav",
		AudioType: "audio/wav",
		System:    1,
		Talkgroup: 100,
	}

	if err := ffmpeg.Convert(call, NewSystems(), NewTags(), AUDIO_CONVERSION_ENABLED); err != nil {
		t.Fatalf("convert: %v", err)
	}

	if call.AudioType != "audio/mp4" {
		t.Fatalf("expected audio/mp4, got %v — conversion did not run", call.AudioType)
	}
	if len(call.Audio) == 0 {
		t.Fatal("conversion produced no audio")
	}

	if findBox(call.Audio, "moof") != nil {
		t.Error("output is a fragmented MP4 (moof present) — Safari cannot decode this")
	}
	if findBox(call.Audio, "mvex") != nil {
		t.Error("output declares fragmentation (mvex present) — Safari cannot decode this")
	}

	// The real check: the sample table has to describe the audio. A fragmented
	// file has this at zero, which is exactly what Safari trips over.
	stsz := findBox(call.Audio, "stsz")
	if stsz == nil {
		t.Fatal("no stsz box — output has no sample table at all")
	}
	if len(stsz) < 20 {
		t.Fatalf("stsz too short: %d bytes", len(stsz))
	}
	if count := binary.BigEndian.Uint32(stsz[16:20]); count == 0 {
		t.Error("stsz reports zero samples — the moov describes no audio")
	}

	if name, ok := call.AudioName.(string); !ok || name != "test.m4a" {
		t.Errorf("expected the name to be rewritten to test.m4a, got %v", call.AudioName)
	}
}

// Conversion must be a no-op when disabled, leaving the upload untouched.
func TestConvertDisabledLeavesAudioAlone(t *testing.T) {
	original := sineWav(1, 8000)

	call := &Call{
		Audio:     append([]byte(nil), original...),
		AudioName: "test.wav",
		AudioType: "audio/wav",
	}

	if err := ffmpegForTest().Convert(call, NewSystems(), NewTags(), AUDIO_CONVERSION_DISABLED); err != nil {
		t.Fatalf("convert: %v", err)
	}

	if !bytes.Equal(call.Audio, original) {
		t.Error("audio was modified while conversion is disabled")
	}
	if call.AudioType != "audio/wav" {
		t.Errorf("audio type changed to %v while conversion is disabled", call.AudioType)
	}
}

func ffmpegForTest() *FFMpeg {
	return NewFFMpeg()
}
