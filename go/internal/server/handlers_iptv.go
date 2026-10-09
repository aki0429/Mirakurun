/*
   Copyright 2016 kanreisa
   Copyright 2026 aki0429 (Go port)

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/aki0429/Mirakurun/go/internal/core"
)

// apiRoot は Node 版 `${req.protocol}://${req.headers.host}/api` を再現する。
//
//	Express は既定で trust proxy 無効のため req.protocol は常に "http"。
func apiRoot(r *http.Request) string {
	return "http://" + r.Host + "/api"
}

// handleIPTVDiscover は GET /api/iptv/discover.json (src/Mirakurun/api/iptv/discover.json.ts)。
func (s *Server) handleIPTVDiscover(w http.ResponseWriter, r *http.Request) {
	root := apiRoot(r)
	st := s.app.GetStatus()

	type discover struct {
		FriendlyName    string `json:"FriendlyName"`
		ModelNumber     string `json:"ModelNumber"`
		FirmwareName    string `json:"FirmwareName"`
		FirmwareVersion string `json:"FirmwareVersion"`
		Manufacturer    string `json:"Manufacturer"`
		DeviceID        string `json:"DeviceID"`
		DeviceAuth      string `json:"DeviceAuth"`
		TunerCount      int    `json:"TunerCount"`
		BaseURL         string `json:"BaseURL"`
		LineupURL       string `json:"LineupURL"`
	}
	body := discover{
		FriendlyName:    "Mirakurun",
		ModelNumber:     "MIRAKURUN",
		FirmwareName:    "mirakurun_" + st.Process.Arch + "_" + st.Process.Platform,
		FirmwareVersion: s.version,
		Manufacturer:    "Chinachu Project",
		DeviceID:        deviceID(r.Host),
		DeviceAuth:      "MIRAKURUN",
		TunerCount:      len(s.tuner.Devices()),
		BaseURL:         root + "/iptv",
		LineupURL:       root + "/iptv/lineup.json",
	}
	s.responseJSON(w, body)
}

var deviceIDRe = regexp.MustCompile(`[\[\].:]`)

func deviceID(host string) string { return deviceIDRe.ReplaceAllString(host, "") }

// handleIPTVLineup は GET /api/iptv/lineup.json。
func (s *Server) handleIPTVLineup(w http.ResponseWriter, r *http.Request) {
	root := apiRoot(r)

	type hdhrChannel struct {
		GuideNumber string `json:"GuideNumber"`
		GuideName   string `json:"GuideName"`
		HD          int    `json:"HD"`
		URL         string `json:"URL"`
	}

	out := []hdhrChannel{}
	countMap := map[int]int{}
	for _, svc := range s.sortedServiceItems() {
		t := svc.Type()
		if t == nil || (*t != 1 && *t != 173) {
			continue
		}
		mainNum := mainChannelNumber(svc)
		countMap[mainNum]++
		subNum := countMap[mainNum]

		out = append(out, hdhrChannel{
			GuideNumber: itoa(mainNum) + "." + itoa(subNum),
			GuideName:   svc.Name(),
			HD:          1,
			URL:         root + "/services/" + itoa64(svc.ID()) + "/stream",
		})
	}
	s.responseJSON(w, out)
}

// handleIPTVLineupStatus は GET /api/iptv/lineup_status.json。
func (s *Server) handleIPTVLineupStatus(w http.ResponseWriter, r *http.Request) {
	type lineupStatus struct {
		ScanInProgress int      `json:"ScanInProgress"`
		ScanPossible   int      `json:"ScanPossible"`
		Source         string   `json:"Source"`
		SourceList     []string `json:"SourceList"`
	}
	s.responseJSON(w, lineupStatus{ScanInProgress: 0, ScanPossible: 0, Source: "Antenna", SourceList: []string{"Antenna"}})
}

// handleIPTVPlaylist は GET /api/iptv/playlist (src/Mirakurun/api/iptv/playlist.ts)。
func (s *Server) handleIPTVPlaylist(w http.ResponseWriter, r *http.Request) {
	root := apiRoot(r)

	var b strings.Builder
	b.WriteString("#EXTM3U url-tvg=\"" + root + "/iptv/xmltv\"\n")
	for _, svc := range s.sortedServiceItems() {
		t := svc.Type()
		if t == nil || (*t != 1 && *t != 173) {
			continue
		}
		b.WriteString("#KODIPROP:mimetype=video/mp2t\n")
		b.WriteString("#EXTINF:-1 tvg-id=\"" + itoa64(svc.ID()) + "\"")
		if s.hasLogoData(svc) {
			b.WriteString(" tvg-logo=\"" + root + "/services/" + itoa64(svc.ID()) + "/logo\"")
		}
		channelType := ""
		if ch := svc.Channel(); ch != nil {
			channelType = string(ch.Type)
		}
		b.WriteString(" group-title=\"" + channelType + "\"," + svc.Name() + "\n")
		b.WriteString(root + "/services/" + itoa64(svc.ID()) + "/stream\n")
	}

	w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// mainChannelNumber は Node 版 `service.remoteControlKeyId || service.serviceId` を再現する。
func mainChannelNumber(svc *core.ServiceItem) int {
	if rc := svc.RemoteControlKeyID(); rc != nil && *rc != 0 {
		return *rc
	}
	return svc.ServiceID()
}
