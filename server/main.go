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
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	const defaultAddr = "0.0.0.0"

	var (
		addr     string
		port     string
		hostname string
		sslAddr  string
		sslPort  string
	)

	config := NewConfig()

	controller := NewController(config)

	if config.newAdminPassword != "" {
		if hash, err := bcrypt.GenerateFromPassword([]byte(config.newAdminPassword), bcrypt.DefaultCost); err == nil {
			if err := controller.Options.Read(controller.Database); err != nil {
				log.Fatal(err)
			}

			controller.Options.adminPassword = string(hash)
			controller.Options.adminPasswordNeedChange = config.newAdminPassword == defaults.adminPassword

			if err := controller.Options.Write(controller.Database); err != nil {
				log.Fatal(err)
			}

			controller.Logs.LogEvent(LogLevelInfo, "admin password changed.")

			os.Exit(0)

		} else {
			log.Fatal(err)
		}
	}

	fmt.Printf("\nRdio Scanner v%s\n", Version)
	fmt.Printf("----------------------------------\n")

	if err := controller.Start(); err != nil {
		log.Fatal(err)
	}

	hostname = defaultAddr

	if s := strings.Split(config.Listen, ":"); len(s) > 1 {
		addr = s[0]
		port = s[1]
	} else {
		addr = s[0]
		port = "3000"
	}
	if len(addr) == 0 {
		addr = defaultAddr
	}

	if s := strings.Split(config.SslListen, ":"); len(s) > 1 {
		sslAddr = s[0]
		sslPort = s[1]
	} else {
		sslAddr = s[0]
		sslPort = "3000"
	}
	if len(sslAddr) == 0 {
		sslAddr = defaultAddr
	}

	http.HandleFunc("/api/admin/config", gzipHandler(controller.Admin.ConfigHandler))

	http.HandleFunc("/api/admin/login", controller.Admin.LoginHandler)

	http.HandleFunc("/api/admin/logout", controller.Admin.LogoutHandler)

	http.HandleFunc("/api/admin/logs", gzipHandler(controller.Admin.LogsHandler))

	http.HandleFunc("/api/admin/password", controller.Admin.PasswordHandler)

	http.HandleFunc("/api/admin/user-add", controller.Admin.UserAddHandler)

	http.HandleFunc("/api/admin/user-remove", controller.Admin.UserRemoveHandler)

	http.HandleFunc("/api/admin/stats", gzipHandler(controller.Stats.Handler))

	http.HandleFunc("/api/admin/transcribe", controller.Admin.TranscribeHandler)

	http.HandleFunc("/api/admin/stats/talkgroup-units", gzipHandler(controller.Stats.TalkgroupUnitsHandler))

	http.HandleFunc("/api/stats", gzipHandler(controller.Stats.PublicHandler))

	http.HandleFunc("/api/stats/talkgroup-units", gzipHandler(controller.Stats.PublicTalkgroupUnitsHandler))

	http.HandleFunc("/api/call-upload", controller.Api.CallUploadHandler)

	http.HandleFunc("/api/trunk-recorder-call-upload", controller.Api.TrunkRecorderCallUploadHandler)

	http.HandleFunc("/api/v1/calls", gzipHandler(controller.PublicApi.CallsRouter))
	http.HandleFunc("/api/v1/calls/", gzipHandler(controller.PublicApi.CallsRouter))

	http.HandleFunc("/", gzipHandler(func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Path[1:]

		if strings.EqualFold(r.Header.Get("upgrade"), "websocket") {
			upgrader := websocket.Upgrader{
				CheckOrigin: func(r *http.Request) bool {
					return true
				},
				ReadBufferSize:    4096,
				WriteBufferSize:   4096,
				EnableCompression: true,
			}

			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				log.Println(err)
			}

			// permessage-deflate: tell the other side we want compressed
			// frames on this connection. Call/list payloads (which can
			// carry many transcripts) shrink ~3x.
			conn.EnableWriteCompression(true)

			client := &Client{}
			if err = client.Init(controller, r, conn); err != nil {
				log.Println(err)
			}

		} else {
			if url == "" {
				url = "index.html"
			}

			if b, err := webapp.ReadFile(path.Join("webapp", url)); err == nil {
				var t string
				switch path.Ext(url) {
				case ".js":
					t = "text/javascript" // see https://github.com/golang/go/issues/32350
				default:
					t = mime.TypeByExtension(path.Ext(url))
				}
				w.Header().Set("Content-Type", t)
				// Fingerprinted bundle files (main.<hash>.js, styles.<hash>.css,
				// chunk.<hash>.js, font.<hash>.woff2 …) can be cached forever.
				// The hash changes on every build, so browsers pick up new
				// versions automatically. index.html is left uncached so the
				// new hashes are always discovered.
				if hasHashedName(url) {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else if url == "index.html" {
					w.Header().Set("Cache-Control", "no-cache")
				}
				w.Write(b)

			} else if url[:len(url)-1] != "/" {
				if b, err := webapp.ReadFile("webapp/index.html"); err == nil {
					w.Header().Set("Cache-Control", "no-cache")
					w.Write(b)

				} else {
					w.WriteHeader(http.StatusNotFound)
				}

			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}))

	if port == "80" {
		log.Printf("main interface at http://%s", hostname)
	} else {
		log.Printf("main interface at http://%s:%s", hostname, port)
	}

	sslPrintInfo := func() {
		if sslPort == "443" {
			log.Printf("main interface at https://%s", hostname)
			log.Printf("admin interface at https://%s/admin", hostname)

		} else {
			log.Printf("main interface at https://%s:%s", hostname, sslPort)
			log.Printf("admin interface at https://%s:%s/admin", hostname, sslPort)
		}
	}

	newServer := func(addr string, tlsConfig *tls.Config) *http.Server {
		s := &http.Server{
			Addr:         addr,
			TLSConfig:    tlsConfig,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			ErrorLog:     log.New(io.Discard, "", 0),
		}

		s.SetKeepAlivesEnabled(true)

		return s
	}

	if len(config.SslCertFile) > 0 && len(config.SslKeyFile) > 0 {
		go func() {
			sslPrintInfo()

			sslCert := config.GetSslCertFilePath()
			sslKey := config.GetSslKeyFilePath()

			server := newServer(fmt.Sprintf("%s:%s", sslAddr, sslPort), nil)

			if err := server.ListenAndServeTLS(sslCert, sslKey); err != nil {
				log.Fatal(err)
			}
		}()

	} else if config.SslAutoCert != "" {
		go func() {
			sslPrintInfo()

			manager := &autocert.Manager{
				Cache:      autocert.DirCache("autocert"),
				Prompt:     autocert.AcceptTOS,
				HostPolicy: autocert.HostWhitelist(config.SslAutoCert),
			}

			server := newServer(fmt.Sprintf("%s:%s", sslAddr, sslPort), manager.TLSConfig())

			if err := server.ListenAndServeTLS("", ""); err != nil {
				log.Fatal(err)
			}
		}()

	} else if port == "80" {
		log.Printf("admin interface at http://%s/admin", hostname)

	} else {
		log.Printf("admin interface at http://%s:%s/admin", hostname, port)
	}

	server := newServer(fmt.Sprintf("%s:%s", addr, port), nil)

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// hasHashedName matches Angular production bundle filenames that include
// a content hash (e.g. main.a32cfe303cdeeb4a.js, 981.f6de34de1b41840e.js,
// styles.218f114274faba1c.css, roboto-latin-w400.7b8d7718ba08bc7d.woff2).
// These are safe to cache forever because their names change on every
// build.
func hasHashedName(name string) bool {
	// Look for a "." followed by 12+ hex chars followed by "." + ext
	// anywhere in the name. Cheap & robust without a regex.
	for i := 0; i < len(name)-13; i++ {
		if name[i] != '.' {
			continue
		}
		j := i + 1
		hexCount := 0
		for j < len(name) && isHexChar(name[j]) {
			hexCount++
			j++
		}
		if hexCount >= 12 && j < len(name) && name[j] == '.' {
			return true
		}
	}
	return false
}

func isHexChar(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func GetRemoteAddr(r *http.Request) string {
	re := regexp.MustCompile(`(.+):.*$`)

	for _, addr := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if ip := re.ReplaceAllString(addr, "$1"); len(ip) > 0 {
			return ip
		}
	}

	if ip := re.ReplaceAllString(r.RemoteAddr, "$1"); len(ip) > 0 {
		return ip
	}

	return r.RemoteAddr
}
