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

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// handleChannels は GET /api/channels (src/Mirakurun/api/channels.ts)。
func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	q := queryMap(r)
	if rejectWhereQuery(q) {
		s.responseError(w, http.StatusBadRequest)
		return
	}

	out := []channelJSON{}
	for _, item := range s.app.Channel.Items() {
		ch := s.toChannelJSON(item)
		if matchChannelQuery(q, ch) {
			out = append(out, ch)
		}
	}
	s.responseJSON(w, out)
}

// handleChannelsByType は GET /api/channels/{type} (src/Mirakurun/api/channels/{type}.ts)。
func (s *Server) handleChannelsByType(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if !validChannelType(typ) {
		s.responseError(w, http.StatusBadRequest)
		return
	}
	q := queryMap(r)
	if rejectWhereQuery(q) {
		s.responseError(w, http.StatusBadRequest)
		return
	}

	out := []channelJSON{}
	for _, item := range s.app.Channel.FindByType(apid.ChannelType(typ)) {
		ch := s.toChannelJSON(item)
		if matchChannelQuery(q, ch) {
			out = append(out, ch)
		}
	}
	s.responseJSON(w, out)
}

// handleChannel は GET /api/channels/{type}/{channel} (src/Mirakurun/api/channels/{type}/{channel}.ts)。
func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if !validChannelType(typ) {
		s.responseError(w, http.StatusBadRequest)
		return
	}
	channel := s.app.Channel.Get(apid.ChannelType(typ), r.PathValue("channel"))
	if channel == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.responseJSON(w, s.toChannelJSON(channel))
}

// handleChannelServices は GET /api/channels/{type}/{channel}/services。
// Node 版は 307 リダイレクトで /api/services に誘導する。
func (s *Server) handleChannelServices(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if !validChannelType(typ) {
		s.responseError(w, http.StatusBadRequest)
		return
	}
	channel := s.app.Channel.Get(apid.ChannelType(typ), r.PathValue("channel"))
	if channel == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/api/services?channel.type="+string(channel.Type)+"&channel.channel="+channel.Channel, http.StatusTemporaryRedirect)
}

// handleChannelService は GET /api/channels/{type}/{channel}/services/{id}。
// Node 版は 307 リダイレクトで /api/services/{id} に誘導する。
func (s *Server) handleChannelService(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if !validChannelType(typ) {
		s.responseError(w, http.StatusBadRequest)
		return
	}
	channel := s.app.Channel.Get(apid.ChannelType(typ), r.PathValue("channel"))
	if channel == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	reqID := atoi64(r.PathValue("id"))
	var found *core.ServiceItem
	for _, svc := range s.app.Service.FindByChannel(channel) {
		if svc.ID() == reqID || int64(svc.ServiceID()) == reqID {
			found = svc
			break
		}
	}
	if found == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/api/services/"+itoa64(found.ID()), http.StatusTemporaryRedirect)
}
