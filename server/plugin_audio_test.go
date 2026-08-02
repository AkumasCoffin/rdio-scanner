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
	"encoding/binary"
	"math"
	"testing"
)

// tone renders a sine wave as signed 16-bit samples.
func tone(frequency float64, rate int, seconds float64, amplitude float64) []int16 {
	count := int(float64(rate) * seconds)
	samples := make([]int16, count)

	for i := range samples {
		v := math.Sin(2 * math.Pi * frequency * float64(i) / float64(rate))
		samples[i] = int16(v * amplitude * 32767)
	}

	return samples
}

// TestGoertzelFindsATone is the check that matters for tone detection: energy
// at the frequency actually present should dominate energy at one that is not.
func TestGoertzelFindsATone(t *testing.T) {
	const rate = 8000

	samples := tone(1050, rate, 1, 0.8)

	present := goertzel(samples, 1050, rate)
	absent := goertzel(samples, 2400, rate)

	if present <= absent {
		t.Fatalf("energy at the tone (%f) should exceed energy at a frequency that is not there (%f)", present, absent)
	}

	// Not a marginal difference — a detector thresholding on this needs real
	// separation, not a few percent.
	if present < absent*10 {
		t.Fatalf("separation is too weak to threshold on: present %f, absent %f", present, absent)
	}
}

// TestGoertzelIgnoresSilence guards against a detector that fires on nothing.
func TestGoertzelIgnoresSilence(t *testing.T) {
	const rate = 8000

	silence := make([]int16, rate)

	if energy := goertzel(silence, 1050, rate); energy != 0 {
		t.Fatalf("silence produced energy %f", energy)
	}
}

// TestGoertzelScanLocatesToneInTime checks the windowing, which is why the scan
// returns a series rather than one number: a two-tone page is defined by when
// each tone appears, and an average across the whole clip would erase that.
func TestGoertzelScanLocatesToneInTime(t *testing.T) {
	const rate = 8000

	// One second of silence, then one second of 1050 Hz.
	samples := append(make([]int16, rate), tone(1050, rate, 1, 0.8)...)

	results := goertzelScan(samples, []float64{1050}, rate, rate/5)
	if len(results) == 0 {
		t.Fatal("scan returned nothing")
	}

	var quiet, loud float64
	for _, entry := range results {
		window := entry.(map[string]any)
		offset := window["offset"].(float64)
		magnitude := window["magnitudes"].([]any)[0].(float64)

		if offset < 0.9 {
			quiet += magnitude
		} else if offset >= 1.0 {
			loud += magnitude
		}
	}

	if loud <= quiet {
		t.Fatalf("the tone was not located in time: energy before %f, during %f", quiet, loud)
	}
}

// TestPcmFromBytes covers the reinterpretation, including negative samples —
// getting the sign wrong would silently halve every detector's accuracy.
func TestPcmFromBytes(t *testing.T) {
	want := []int16{0, 1, -1, 32767, -32768}

	raw := make([]byte, len(want)*2)
	for i, sample := range want {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(sample))
	}

	got := pcmFromBytes(raw)

	if len(got) != len(want) {
		t.Fatalf("got %d samples, expected %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d was %d, expected %d", i, got[i], want[i])
		}
	}
}
