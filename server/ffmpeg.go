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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ffmpegTimeout caps a single audio conversion. Convert runs synchronously on
// the one ingest goroutine, so an ffmpeg that never exits — a malformed or
// truncated upload is enough — stopped every subsequent call from being
// processed for as long as the process stayed up. Scanner clips are seconds
// long; a minute is far more than any legitimate conversion needs.
const ffmpegTimeout = time.Minute

// ffmpegVersionTimeout caps the capability probe at startup, which otherwise
// blocks the boot path.
const ffmpegVersionTimeout = 10 * time.Second

type FFMpeg struct {
	available bool
	version43 bool
	warned    bool
}

func NewFFMpeg() *FFMpeg {
	ffmpeg := &FFMpeg{}

	stdout := bytes.NewBuffer([]byte(nil))

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegVersionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", "-version")
	cmd.Stdout = stdout

	if err := cmd.Run(); err == nil {
		ffmpeg.available = true

		if l, err := stdout.ReadString('\n'); err == nil {
			s := regexp.MustCompile(`.*ffmpeg version .{0,1}([0-9])\.([0-9])\.[0-9].*`).ReplaceAllString(strings.TrimSuffix(l, "\n"), "$1.$2")
			v := strings.Split(s, ".")
			if len(v) > 1 {
				if major, err := strconv.Atoi(v[0]); err == nil {
					if minor, err := strconv.Atoi(v[1]); err == nil {
						if major > 4 || (major == 4 && minor >= 3) {
							ffmpeg.version43 = true
						}
					}
				}
			}
		}
	}

	return ffmpeg
}

// Available reports whether an ffmpeg binary was found at startup. Surfaced in
// the admin config payload so the Audio Conversion setting can say when it has
// nothing to run.
func (ffmpeg *FFMpeg) Available() bool {
	return ffmpeg.available
}

// UnavailableMessage explains that ffmpeg is missing and gives the command to
// install it on this host. Kept short enough to survive the log table's column
// width (see logMessageMaxLen).
func (ffmpeg *FFMpeg) UnavailableMessage() string {
	return ffmpegUnavailableMessage(ffmpegInstallCommand())
}

// ffmpegUnavailableMessage is split out from UnavailableMessage so the wording
// can be length-checked against every platform's install command, not just the
// one this binary happens to be built for.
func ffmpegUnavailableMessage(installCommand string) string {
	return fmt.Sprintf("ffmpeg not found — audio conversion is disabled and calls are stored as uploaded. Install it with: %s", installCommand)
}

// ffmpegInstallCommand is the best guess at how to install ffmpeg here. On
// Linux the package manager is inferred from /etc/os-release; anything
// unrecognised falls back to naming the package rather than guessing a command
// that won't exist.
func ffmpegInstallCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install ffmpeg"

	case "freebsd":
		return "sudo pkg install ffmpeg"

	case "windows":
		return `winget install Gyan.FFmpeg  (then reopen your terminal so PATH is picked up)`

	case "linux":
		return linuxFFmpegInstallCommand(readOsReleaseIds())
	}

	return "your platform's ffmpeg package"
}

// linuxFFmpegInstallCommand maps os-release ID / ID_LIKE values to a package
// manager. ids is checked in order, so the distro's own ID wins over the
// family it declares compatibility with.
func linuxFFmpegInstallCommand(ids []string) string {
	for _, id := range ids {
		switch id {
		case "debian", "ubuntu", "raspbian", "linuxmint", "pop":
			return "sudo apt install ffmpeg"

		case "fedora":
			// Fedora's own repo carries ffmpeg-free; the full build needs
			// RPM Fusion, which most scanner setups will want.
			return "sudo dnf install ffmpeg-free  (or enable RPM Fusion for the full build)"

		case "rhel", "centos", "rocky", "almalinux":
			return "sudo dnf install ffmpeg  (needs EPEL + RPM Fusion)"

		case "arch", "manjaro", "endeavouros":
			return "sudo pacman -S ffmpeg"

		case "alpine":
			return "apk add ffmpeg"

		case "opensuse", "opensuse-leap", "opensuse-tumbleweed", "suse", "sles":
			return "sudo zypper install ffmpeg"

		case "void":
			return "sudo xbps-install -S ffmpeg"

		case "gentoo":
			return "sudo emerge media-video/ffmpeg"
		}
	}

	return "your distribution's ffmpeg package"
}

// readOsReleaseIds returns the ID followed by each ID_LIKE value from
// /etc/os-release, lowercased. Empty when the file is absent or unreadable.
func readOsReleaseIds() []string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return nil
	}

	var id string
	var like []string

	for _, line := range strings.Split(string(b), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}

		value = strings.ToLower(strings.Trim(strings.TrimSpace(value), `"'`))

		switch key {
		case "ID":
			id = value
		case "ID_LIKE":
			like = strings.Fields(value)
		}
	}

	ids := []string{}
	if id != "" {
		ids = append(ids, id)
	}

	return append(ids, like...)
}

func (ffmpeg *FFMpeg) Convert(call *Call, systems *Systems, tags *Tags, mode uint) error {
	var (
		// -y because the temp output path already exists (CreateTemp makes it).
		// Without it ffmpeg asks whether to overwrite, on a stdin that is
		// carrying the audio.
		args = []string{"-y", "-i", "-"}
		err  error
	)

	if mode == AUDIO_CONVERSION_DISABLED {
		return nil
	}

	if !ffmpeg.available {
		if !ffmpeg.warned {
			ffmpeg.warned = true

			return errors.New(ffmpeg.UnavailableMessage())
		}
		return nil
	}

	if system, ok := systems.GetSystem(call.System); ok {
		if talkgroup, ok := system.Talkgroups.GetTalkgroup(call.Talkgroup); ok {
			if tag, ok := tags.GetTag(talkgroup.TagId); ok {
				args = append(args,
					"-metadata", fmt.Sprintf("album=%v", talkgroup.Label),
					"-metadata", fmt.Sprintf("artist=%v", system.Label),
					"-metadata", fmt.Sprintf("date=%v", call.DateTime),
					"-metadata", fmt.Sprintf("genre=%v", tag),
					"-metadata", fmt.Sprintf("title=%v", talkgroup.Name),
				)
			}
		}
	}

	if ffmpeg.version43 {
		if mode == AUDIO_CONVERSION_ENABLED_NORM {
			args = append(args, "-af", "apad=whole_dur=3s,loudnorm")
		} else if mode == AUDIO_CONVERSION_ENABLED_LOUD_NORM {
			args = append(args, "-af", "apad=whole_dur=3s,loudnorm=I=-16:TP=-1.5:LRA=11")
		}
	}

	// Output goes to a temp file rather than stdout, and that is load-bearing.
	//
	// MP4 keeps its sample index (the moov atom) at the end of muxing but must
	// write it near the front, so ffmpeg has to seek backwards — impossible on
	// a pipe. Piping therefore forced -movflags frag_keyframe+empty_moov,
	// producing a FRAGMENTED MP4: a moov declaring zero samples, with the real
	// index in a trailing moof box.
	//
	// Chrome and ffprobe read that happily. WebKit does not: Safari's
	// decodeAudioData() resolves samples from the moov sample tables, finds
	// none, and fails — so every converted call was silent on iOS while
	// working everywhere else. Safari handles fragmented MP4 only through
	// Media Source Extensions, not the one-shot decode the webapp uses.
	//
	// A seekable output lets ffmpeg write a normal moov with a populated
	// sample table, which decodes everywhere. The file is small and
	// short-lived; ingest is serialized, so at most one exists at a time.
	tmp, err := os.CreateTemp("", "rdio-convert-*.m4a")
	if err != nil {
		return fmt.Errorf("ffmpeg: cannot create temp file: %v", err)
	}
	tmpPath := tmp.Name()
	// Close immediately — ffmpeg opens the path itself, and on Windows an open
	// handle would block it.
	tmp.Close()
	defer os.Remove(tmpPath)

	// -ar is what makes the result playable on iOS.
	//
	// ffmpeg keeps the source sample rate unless told otherwise, and scanner
	// audio arrives at 8 or 16 kHz, so every converted call used to be low-rate
	// AAC. iOS plays that happily in its media player — the same file opens in
	// Files — but WebKit's Web Audio decoder is stricter than its media stack
	// and rejects it outright with "EncodingError: Decoding failed". Since the
	// webapp plays calls through decodeAudioData(), every call was silent on
	// every iOS browser (all WebKit) while working in Chrome and on Android.
	//
	// 48 kHz is what iOS hardware runs at, so the browser does not resample
	// again on playback. The source is bandwidth-limited speech, so upsampling
	// invents no detail and loses none. It does cost roughly 18% more bytes per
	// call — the bitrate is unchanged but there are six times as many AAC
	// frames, each with its own overhead — which is the price of the audio
	// being playable at all on an entire class of device.
	args = append(args, "-c:a", "aac", "-b:a", "32k", "-ar", "48000", "-f", "ipod", tmpPath)

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(call.Audio)

	stderr := bytes.NewBuffer([]byte(nil))
	cmd.Stderr = stderr

	if err = cmd.Run(); err == nil {
		converted, readErr := os.ReadFile(tmpPath)

		if readErr != nil || len(converted) == 0 {
			// Keep the original audio rather than storing an empty call.
			fmt.Printf("ffmpeg produced no output converting %v, keeping original audio\n", call.AudioName)
			return nil
		}

		call.Audio = converted
		call.AudioType = "audio/mp4"

		switch v := call.AudioName.(type) {
		case string:
			call.AudioName = fmt.Sprintf("%v.m4a", strings.TrimSuffix(v, path.Ext((v))))
		}

	} else if ctx.Err() == context.DeadlineExceeded {
		// Killed by the deadline. The call keeps its original audio and still
		// gets ingested — losing the conversion beats losing the call, and
		// beats stalling every call behind it.
		fmt.Printf("ffmpeg timed out after %v converting %v, keeping original audio\n", ffmpegTimeout, call.AudioName)

	} else {
		fmt.Println(stderr.String())
	}

	return nil
}
