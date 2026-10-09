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
	"errors"
	"io"
	"net/http"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// handleServices は GET /api/services (src/Mirakurun/api/services.ts)。
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	q := queryMap(r)
	if rejectWhereQuery(q) {
		s.responseError(w, http.StatusBadRequest)
		return
	}

	out := []apid.Service{}
	for _, svc := range s.sortedServiceItems() {
		if !matchServiceQuery(q, svc) {
			continue
		}
		out = append(out, s.exportServiceWithLogoData(svc))
	}
	s.responseJSON(w, out)
}

// handleService は GET /api/services/{id} (src/Mirakurun/api/services/{id}.ts)。
func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	svc := s.app.Service.GetByID(atoi64(r.PathValue("id")))
	if svc == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.responseJSON(w, s.exportServiceWithLogoData(svc))
}

// handleServiceLogo は GET /api/services/{id}/logo (src/Mirakurun/api/services/{id}/logo.ts)。
func (s *Server) handleServiceLogo(w http.ResponseWriter, r *http.Request) {
	svc := s.app.Service.GetByID(atoi64(r.PathValue("id")))
	if svc == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	logoID := svc.LogoID()
	if logoID == nil || *logoID < 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	data := core.LoadLogoData(s.app.Config.LogoDataDirPath, svc.NetworkID(), *logoID)
	if data != nil {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

// handleServiceStream は GET/HEAD /api/services/{id}/stream。
func (s *Server) handleServiceStream(w http.ResponseWriter, r *http.Request) {
	svc := s.app.Service.GetByID(atoi64(r.PathValue("id")))
	if svc == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.serveStream(w, r, func(req UserRequest, out io.Writer) (TSFilter, error) {
		return s.tuner.InitServiceStream(svc, req, out)
	})
}

// handleChannelStream は GET/HEAD /api/channels/{type}/{channel}/stream。
func (s *Server) handleChannelStream(w http.ResponseWriter, r *http.Request) {
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
	s.serveStream(w, r, func(req UserRequest, out io.Writer) (TSFilter, error) {
		return s.tuner.InitChannelStream(channel, req, out)
	})
}

// handleChannelServiceStream は GET/HEAD /api/channels/{type}/{channel}/services/{id}/stream。
func (s *Server) handleChannelServiceStream(w http.ResponseWriter, r *http.Request) {
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
	s.serveStream(w, r, func(req UserRequest, out io.Writer) (TSFilter, error) {
		return s.tuner.InitServiceStream(found, req, out)
	})
}

// handleProgramStream は GET/HEAD /api/programs/{id}/stream。
func (s *Server) handleProgramStream(w http.ResponseWriter, r *http.Request) {
	prog := s.app.Program.Get(atoi64(r.PathValue("id")))
	if prog == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.serveStream(w, r, func(req UserRequest, out io.Writer) (TSFilter, error) {
		return s.tuner.InitProgramStream(prog, req, out)
	})
}

// isWhereQueryError は core.WhereQueryError か判定する。
func isWhereQueryError(err error) bool {
	var w *core.WhereQueryError
	return errors.As(err, &w)
}
