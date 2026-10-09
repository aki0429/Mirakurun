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
	"encoding/json"
	"net/http"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// decodeJSONBody はリクエストボディを JSON として読み込む。失敗時は 400 を書いて false。
func (s *Server) decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		s.responseError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// handleGetServerConfig は GET /api/config/server。
func (s *Server) handleGetServerConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loader.LoadServer()
	if err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.responseJSON(w, cfg)
}

// handlePutServerConfig は PUT /api/config/server。
func (s *Server) handlePutServerConfig(w http.ResponseWriter, r *http.Request) {
	var cfg apid.ConfigServer
	if !s.decodeJSONBody(w, r, &cfg) {
		return
	}
	if err := s.loader.SaveServer(&cfg); err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.responseJSON(w, &cfg)
}

// handleGetTunersConfig は GET /api/config/tuners。
func (s *Server) handleGetTunersConfig(w http.ResponseWriter, r *http.Request) {
	tuners, err := s.loader.LoadTuners()
	if err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tuners == nil {
		tuners = []apid.ConfigTunersItem{}
	}
	s.responseJSON(w, tuners)
}

// handlePutTunersConfig は PUT /api/config/tuners。
func (s *Server) handlePutTunersConfig(w http.ResponseWriter, r *http.Request) {
	var tuners []apid.ConfigTunersItem
	if !s.decodeJSONBody(w, r, &tuners) {
		return
	}
	if err := s.loader.SaveTuners(tuners); err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.responseJSON(w, tuners)
}

// handleGetChannelsConfig は GET /api/config/channels。
func (s *Server) handleGetChannelsConfig(w http.ResponseWriter, r *http.Request) {
	channels, err := s.loader.LoadChannels()
	if err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if channels == nil {
		channels = []apid.ConfigChannelsItem{}
	}
	s.responseJSON(w, channels)
}

// handlePutChannelsConfig は PUT /api/config/channels。
func (s *Server) handlePutChannelsConfig(w http.ResponseWriter, r *http.Request) {
	var channels []apid.ConfigChannelsItem
	if !s.decodeJSONBody(w, r, &channels) {
		return
	}
	if err := s.loader.SaveChannels(channels); err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.responseJSON(w, channels)
}
