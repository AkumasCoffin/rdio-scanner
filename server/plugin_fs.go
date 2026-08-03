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
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/google/uuid"
)

// The filesystem, other programs, and the handful of primitives JavaScript
// does not have.
//
// None of it is restricted by path or by program. A plugin is code the operator
// chose to install and run, and gating it here would only refuse things the
// plugin already announced it would do — the decision belongs at install, where
// a person is present. What is bounded is resource use: a runaway program is
// killed, and a huge file is refused rather than pulled into memory whole.

const (
	// pluginFsMaxRead bounds a single readFile. Larger files must be read in
	// pieces, so a plugin cannot exhaust memory with one call.
	pluginFsMaxRead = 256 << 20 // 256 MiB

	// pluginExecTimeout is how long a program may run before it is killed, and
	// pluginExecTimeoutMax is as far as a plugin may raise it. A process meant
	// to outlive a single call belongs behind a scheduled job or its own
	// service, reached over HTTP — not in a call the server is waiting on.
	pluginExecTimeout    = 5 * time.Minute
	pluginExecTimeoutMax = 30 * time.Minute

	// pluginExecMaxOutput bounds what is captured from a program's output.
	pluginExecMaxOutput = 32 << 20 // 32 MiB
)

func (rt *PluginRuntime) bindFs(vm *goja.Runtime, rdio *goja.Object, throw func(string, ...any)) {
	fs := vm.NewObject()

	// Paths are resolved relative to the plugin's data directory, so the common
	// case — a plugin keeping its own files — needs no absolute paths and lands
	// somewhere that survives an update.
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(rt.dataDir, path)
	}

	fs.Set("resolve", func(path string) goja.Value {
		return vm.ToValue(resolve(path))
	})

	// readPart is the shared body of readFile and readText.
	//
	// The size guard belongs to both. readText had none at all, which made the
	// unbounded call the more natural one to reach for: a log file is exactly
	// what someone reads as text, and exactly what is large enough to exhaust
	// the server's memory.
	readPart := func(name string, path string, options goja.Value) []byte {
		var offset, length int64

		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if v, ok := numberFromMap(m, "offset"); ok && v > 0 {
					offset = int64(v)
				}
				if v, ok := numberFromMap(m, "length"); ok && v > 0 {
					length = int64(v)
				}
			}
		}

		body, err := readFileRange(resolve(path), offset, length, pluginFsMaxRead)
		if err != nil {
			throw("fs.%s: %v", name, err)
		}

		return body
	}

	fs.Set("readFile", func(path string, options goja.Value) goja.Value {
		return vm.ToValue(vm.NewArrayBuffer(readPart("readFile", path, options)))
	})

	fs.Set("readText", func(path string, options goja.Value) goja.Value {
		return vm.ToValue(string(readPart("readText", path, options)))
	})

	fs.Set("writeFile", func(path string, data goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("fs.writeFile: %v", err)
		}

		full := resolve(path)

		if err := os.MkdirAll(filepath.Dir(full), 0o770); err != nil {
			throw("fs.writeFile: %v", err)
		}
		if err := os.WriteFile(full, body, 0o660); err != nil {
			throw("fs.writeFile: %v", err)
		}

		return goja.Undefined()
	})

	fs.Set("appendFile", func(path string, data goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("fs.appendFile: %v", err)
		}

		full := resolve(path)

		if err := os.MkdirAll(filepath.Dir(full), 0o770); err != nil {
			throw("fs.appendFile: %v", err)
		}

		file, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o660)
		if err != nil {
			throw("fs.appendFile: %v", err)
		}
		defer file.Close()

		if _, err := file.Write(body); err != nil {
			throw("fs.appendFile: %v", err)
		}

		return goja.Undefined()
	})

	fs.Set("exists", func(path string) goja.Value {
		_, err := os.Stat(resolve(path))
		return vm.ToValue(err == nil)
	})

	fs.Set("stat", func(path string) goja.Value {
		info, err := os.Stat(resolve(path))
		if err != nil {
			return goja.Null()
		}
		return vm.ToValue(map[string]any{
			"name":     info.Name(),
			"size":     info.Size(),
			"isDir":    info.IsDir(),
			"modified": info.ModTime().UTC().Format(time.RFC3339),
		})
	})

	fs.Set("list", func(path string) goja.Value {
		entries, err := os.ReadDir(resolve(path))
		if err != nil {
			throw("fs.list: %v", err)
		}

		out := []any{}
		for _, entry := range entries {
			item := map[string]any{"name": entry.Name(), "isDir": entry.IsDir()}
			if info, err := entry.Info(); err == nil {
				item["size"] = info.Size()
				item["modified"] = info.ModTime().UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}

		return vm.ToValue(out)
	})

	fs.Set("mkdir", func(path string) goja.Value {
		if err := os.MkdirAll(resolve(path), 0o770); err != nil {
			throw("fs.mkdir: %v", err)
		}
		return goja.Undefined()
	})

	fs.Set("remove", func(path string) goja.Value {
		if err := os.RemoveAll(resolve(path)); err != nil && !os.IsNotExist(err) {
			throw("fs.remove: %v", err)
		}
		return goja.Undefined()
	})

	fs.Set("rename", func(from string, to string) goja.Value {
		if err := os.Rename(resolve(from), resolve(to)); err != nil {
			throw("fs.rename: %v", err)
		}
		return goja.Undefined()
	})

	fs.Set("tempDir", func() goja.Value {
		return vm.ToValue(os.TempDir())
	})

	rdio.Set("fs", fs)
}

func (rt *PluginRuntime) bindExec(vm *goja.Runtime, rdio *goja.Object, throw func(string, ...any)) {
	// Runs a program and resolves with its exit code and output. Asynchronous
	// so a plugin's event loop keeps turning while the process runs — and
	// because a filter waiting on a subprocess would otherwise hold whatever
	// pipeline it was called from.
	run := func(name string, args goja.Value, options goja.Value) goja.Value {
		argv := []string{}
		if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
			if list, ok := args.Export().([]any); ok {
				for _, arg := range list {
					argv = append(argv, fmt.Sprintf("%v", arg))
				}
			}
		}

		timeout := pluginExecTimeout
		workDir := rt.dataDir
		var stdin []byte
		env := []string{}

		// Whether the program's output is data or text. A JavaScript string is
		// UTF-8, so returning arbitrary bytes through one replaces every
		// invalid sequence — roughly half of all byte values do not survive.
		// That is silent corruption for anything binary, audio above all, so a
		// plugin piping a call through its own program asks for bytes and gets
		// an ArrayBuffer instead.
		binary := false

		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if ms, ok := numberFromMap(m, "timeoutMs"); ok && ms > 0 {
					timeout = time.Duration(ms) * time.Millisecond
					// Capped, the way rdio.http already caps its own. Without
					// this the comment above claiming plugins can only lower it
					// was untrue, and {timeoutMs: 86400000} held a goroutine and
					// a child process for a day.
					if timeout > pluginExecTimeoutMax {
						timeout = pluginExecTimeoutMax
					}
				}
				if dir := stringFromMap(m, "cwd"); dir != "" {
					workDir = dir
				}
				if raw, ok := m["stdin"]; ok {
					if b, err := pluginBytes(raw); err == nil {
						stdin = b
					}
				}
				if vars, ok := m["env"].(map[string]any); ok {
					for key, value := range vars {
						env = append(env, fmt.Sprintf("%s=%v", key, value))
					}
				}
				if flag, ok := m["binary"].(bool); ok {
					binary = flag
				}
			}
		}

		return rt.promiseFrom(vm, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			command := exec.CommandContext(ctx, name, argv...)
			command.Dir = workDir

			if len(env) > 0 {
				command.Env = append(os.Environ(), env...)
			}
			if len(stdin) > 0 {
				command.Stdin = strings.NewReader(string(stdin))
			}

			stdout := &boundedBuffer{limit: pluginExecMaxOutput}
			stderr := &boundedBuffer{limit: pluginExecMaxOutput}
			command.Stdout = stdout
			command.Stderr = stderr

			err := command.Run()

			code := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else if ctx.Err() != nil {
					return nil, fmt.Errorf("%s timed out after %s", name, timeout)
				} else {
					return nil, err
				}
			}

			result := map[string]any{
				"code": code,
				// Reported rather than swallowed: a caller that got half a
				// file needs to know it got half a file.
				"truncated": stdout.truncated || stderr.truncated,
			}

			if binary {
				result["stdout"] = vm.NewArrayBuffer(stdout.Bytes())
				result["stderr"] = vm.NewArrayBuffer(stderr.Bytes())
			} else {
				result["stdout"] = stdout.String()
				result["stderr"] = stderr.String()
			}

			return result, nil
		})
	}

	rdio.Set("exec", func(name string, args goja.Value, options goja.Value) goja.Value {
		if strings.TrimSpace(name) == "" {
			throw("exec requires a program name")
		}
		return run(name, args, options)
	})
}

// readFileRange reads part of a file, refusing anything that would pull more
// than max into memory in one go.
//
// The guard has to cover text as well as bytes. readText had none, and a log
// file — the obvious thing to read as text — is exactly the case large enough
// to exhaust the server. A length of 0 means "the whole file, if it fits".
func readFileRange(path string, offset int64, length int64, max int64) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset %d is before the start of the file", offset)
	}
	if length > max {
		return nil, fmt.Errorf("asked for %d bytes, more than the %d byte limit for one read", length, max)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	remaining := info.Size() - offset
	if remaining < 0 {
		remaining = 0
	}

	// Only the unbounded form is refused for being too big. Someone who named a
	// length has already accepted reading part of the file.
	if length == 0 {
		if remaining > max {
			return nil, fmt.Errorf("%s is %d bytes from offset %d, larger than the %d byte limit; pass {offset, length} to read part of it",
				path, remaining, offset, max)
		}
		length = remaining
	}

	if length > remaining {
		length = remaining
	}
	if length == 0 {
		return []byte{}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
	}

	body := make([]byte, length)

	read, err := io.ReadFull(file, body)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}

	return body[:read], nil
}

// boundedBuffer collects output up to a limit and then discards the rest, so a
// program that writes without end cannot exhaust memory. It remembers that it
// had to, because output that stops halfway is not the same as output that
// ended — for audio, a silent truncation is a corrupt file that still decodes.
type boundedBuffer struct {
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)

	if remaining > 0 {
		if len(p) <= remaining {
			b.data = append(b.data, p...)
		} else {
			b.data = append(b.data, p[:remaining]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}

	// Always report the full length: refusing bytes makes the program see a
	// write error, which is not what happened.
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

func (b *boundedBuffer) Bytes() []byte { return b.data }

func (rt *PluginRuntime) bindCrypto(vm *goja.Runtime, rdio *goja.Object, throw func(string, ...any)) {
	crypto := vm.NewObject()

	hasher := func(name string) (hash.Hash, error) {
		switch strings.ToLower(name) {
		case "md5":
			return md5.New(), nil
		case "sha1":
			return sha1.New(), nil
		case "sha256", "":
			return sha256.New(), nil
		case "sha512":
			return sha512.New(), nil
		default:
			return nil, fmt.Errorf("unknown algorithm %q", name)
		}
	}

	crypto.Set("hash", func(algorithm string, data goja.Value) goja.Value {
		h, err := hasher(algorithm)
		if err != nil {
			throw("crypto.hash: %v", err)
		}

		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("crypto.hash: %v", err)
		}

		h.Write(body)

		return vm.ToValue(hex.EncodeToString(h.Sum(nil)))
	})

	crypto.Set("hmac", func(algorithm string, key goja.Value, data goja.Value) goja.Value {
		keyBytes, err := pluginBytes(key.Export())
		if err != nil {
			throw("crypto.hmac: %v", err)
		}
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("crypto.hmac: %v", err)
		}

		var mac hash.Hash
		switch strings.ToLower(algorithm) {
		case "md5":
			mac = hmac.New(md5.New, keyBytes)
		case "sha1":
			mac = hmac.New(sha1.New, keyBytes)
		case "sha512":
			mac = hmac.New(sha512.New, keyBytes)
		default:
			mac = hmac.New(sha256.New, keyBytes)
		}

		mac.Write(body)

		return vm.ToValue(hex.EncodeToString(mac.Sum(nil)))
	})

	crypto.Set("base64Encode", func(data goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("crypto.base64Encode: %v", err)
		}
		return vm.ToValue(base64.StdEncoding.EncodeToString(body))
	})

	crypto.Set("base64Decode", func(encoded string) goja.Value {
		body, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			throw("crypto.base64Decode: %v", err)
		}
		return vm.ToValue(vm.NewArrayBuffer(body))
	})

	crypto.Set("hexEncode", func(data goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("crypto.hexEncode: %v", err)
		}
		return vm.ToValue(hex.EncodeToString(body))
	})

	crypto.Set("hexDecode", func(encoded string) goja.Value {
		body, err := hex.DecodeString(encoded)
		if err != nil {
			throw("crypto.hexDecode: %v", err)
		}
		return vm.ToValue(vm.NewArrayBuffer(body))
	})

	crypto.Set("randomBytes", func(n int) goja.Value {
		if n <= 0 || n > 1<<20 {
			throw("crypto.randomBytes: length must be between 1 and 1048576")
		}
		body := make([]byte, n)
		if _, err := rand.Read(body); err != nil {
			throw("crypto.randomBytes: %v", err)
		}
		return vm.ToValue(vm.NewArrayBuffer(body))
	})

	crypto.Set("uuid", func() goja.Value {
		return vm.ToValue(uuid.New().String())
	})

	rdio.Set("crypto", crypto)
}
